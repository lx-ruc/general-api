package service

import (
	"testing"
	"time"
)

// ---------- 月末快照：时点正确性 + 标记自举（B2/C4） ----------

// 快照期末已用必须是时点值：当前水位扣除快照期之后发生的结算，
// 无论快照何时触发/补跑都收敛到同一数字（不受新月消耗污染）。
func TestSnapshotAsOfClosing(t *testing.T) {
	f := newBillingDB(t)
	sh := BillingLocation("Asia/Shanghai")
	oid := int64(1)
	f.insertOrg(oid, 1_000_000, 0)

	augS, _, _ := PeriodBounds(sh, "2026-08")
	sepS, _, _ := PeriodBounds(sh, "2026-09")
	octS, _, _ := PeriodBounds(sh, "2026-10")

	// 7 月末快照（期初 0）
	if err := SnapshotBalances(f.db, "2026-07"); err != nil {
		t.Fatal(err)
	}

	// 8 月消耗 300k，9 月消耗 60k；org 水位已被 9 月结算推到 360k
	f.insertUsage(oid, augS+100, 200_000, 0, 0, nil)
	f.insertUsage(oid, augS+200, 100_000, 0, 0, nil)
	f.insertUsage(oid, sepS+10, 60_000, 0, 0, nil)
	if err := f.db.Exec("UPDATE orgs SET quota_used = 360000 WHERE id = ?", oid).Error; err != nil {
		t.Fatal(err)
	}

	// 8 月末快照：必须是 360k − 60k(9月) = 300k，而非被污染的实时 360k
	if err := SnapshotBalances(f.db, "2026-08"); err != nil {
		t.Fatal(err)
	}
	var closing int64
	if err := f.db.Raw(`SELECT quota_used FROM period_balances WHERE org_id = 1 AND period = '2026-08'`).
		Scan(&closing).Error; err != nil {
		t.Fatal(err)
	}
	if closing != 300_000 {
		t.Fatalf("8 月末快照应 300k（扣除 9 月消耗），得 %d", closing)
	}

	// 勾稽链：期末 300k − 期初 0 == 8 月消耗 300k
	st, err := BuildBillStatement(f.db, sh, oid, "2026-08", false, 100)
	if err != nil {
		t.Fatal(err)
	}
	if st.ChainOK == nil || !*st.ChainOK {
		t.Fatalf("时点快照后勾稽链应成立: closing=%d consumption=%d", st.ClosingUsed, st.Consumption)
	}

	// 更晚补跑（10 月又消耗 30k、水位 390k）：重复写 8 月快照仍收敛到 300k
	f.insertUsage(oid, octS+10, 30_000, 0, 0, nil)
	if err := f.db.Exec("UPDATE orgs SET quota_used = 390000 WHERE id = ?", oid).Error; err != nil {
		t.Fatal(err)
	}
	if err := SnapshotBalances(f.db, "2026-08"); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Raw(`SELECT quota_used FROM period_balances WHERE org_id = 1 AND period = '2026-08'`).
		Scan(&closing).Error; err != nil {
		t.Fatal(err)
	}
	if closing != 300_000 {
		t.Fatalf("晚补跑应收敛回 300k，得 %d", closing)
	}
}

// 新库 settings 无标记行时，快照协程的一次执行必须真的写快照
// （原缺陷：CAS 的 UPDATE 在缺失行上恒 0 行 → 被当成"已写过" → 永不执行）。
func TestSnapshotPrevOnceBootstrapsMarker(t *testing.T) {
	f := newBillingDB(t)
	sh := BillingLocation("Asia/Shanghai")
	oid := int64(1)
	f.insertOrg(oid, 1_000_000, 0)

	prev := PeriodOf(sh, time.Now().AddDate(0, -1, 0).Unix())
	prevS, _, _ := PeriodBounds(sh, prev)

	// 上月消耗 120k、本月（当前时刻所在月）消耗 25k，水位 145k
	f.insertUsage(oid, prevS+100, 120_000, 0, 0, nil)
	f.insertUsage(oid, time.Now().Unix(), 25_000, 0, 0, nil)
	if err := f.db.Exec("UPDATE orgs SET quota_used = 145000 WHERE id = ?", oid).Error; err != nil {
		t.Fatal(err)
	}

	snapshotPrevOnce(f.db, sh)

	// 标记行已初始化并指向上一期
	var marker string
	if err := f.db.Raw(`SELECT value FROM settings WHERE key = 'balance_snapshot_period'`).Scan(&marker).Error; err != nil {
		t.Fatal(err)
	}
	if marker != prev {
		t.Fatalf("标记应 %s，得 %q", prev, marker)
	}
	// 快照真的写了，且是时点值 145k − 25k = 120k
	var snapUsed int64
	if err := f.db.Raw(`SELECT quota_used FROM period_balances WHERE org_id = 1 AND period = ?`, prev).
		Scan(&snapUsed).Error; err != nil {
		t.Fatal(err)
	}
	if snapUsed != 120_000 {
		t.Fatalf("上月期末快照应 120k，得 %d", snapUsed)
	}

	// 第二次执行：CAS 落空，不重写（snapshot_at 不变）
	var snapAt int64
	_ = f.db.Raw(`SELECT snapshot_at FROM period_balances WHERE org_id = 1 AND period = ?`, prev).Scan(&snapAt).Error
	time.Sleep(1100 * time.Millisecond) // snapshot_at 秒级精度，确保可区分
	snapshotPrevOnce(f.db, sh)
	var snapAt2 int64
	_ = f.db.Raw(`SELECT snapshot_at FROM period_balances WHERE org_id = 1 AND period = ?`, prev).Scan(&snapAt2).Error
	if snapAt2 != snapAt {
		t.Fatalf("重复执行不应重写快照（%d → %d）", snapAt, snapAt2)
	}
}
