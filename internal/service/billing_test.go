package service

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"token-gateway/internal/config"
	"token-gateway/internal/database"
)

// ---------- 口径：账期边界与时区 ----------

func TestPeriodBoundsTimezone(t *testing.T) {
	utc := time.UTC
	// UTC 服务器（或 UTC 账期）：8 月边界 = 2026-08-01T00:00Z ~ 2026-09-01T00:00Z
	s, e, err := PeriodBounds(utc, "2026-08")
	if err != nil {
		t.Fatal(err)
	}
	if s != time.Date(2026, 8, 1, 0, 0, 0, 0, utc).Unix() {
		t.Fatalf("UTC 期初错: %d", s)
	}
	if e != time.Date(2026, 9, 1, 0, 0, 0, 0, utc).Unix() {
		t.Fatalf("UTC 期末错: %d", e)
	}

	// UTC 服务器 + Asia/Shanghai 账期：8 月边界 = UTC 2026-07-31T16:00Z 起
	sh := BillingLocation("Asia/Shanghai")
	s, e, err = PeriodBounds(sh, "2026-08")
	if err != nil {
		t.Fatal(err)
	}
	if s != time.Date(2026, 7, 31, 16, 0, 0, 0, utc).Unix() {
		t.Fatalf("SH 期初应为 UTC 07-31T16:00Z，得 %d", s)
	}
	if e != time.Date(2026, 8, 31, 16, 0, 0, 0, utc).Unix() {
		t.Fatalf("SH 期末应为 UTC 08-31T16:00Z，得 %d", e)
	}

	// 非法 period
	if _, _, err := PeriodBounds(sh, "2026-13"); err == nil {
		t.Fatal("非法 period 应报错")
	}
	if _, _, err := PeriodBounds(sh, "2026/08"); err == nil {
		t.Fatal("非法格式应报错")
	}
}

// 归期 = 结算完成时刻：SH 9 月 1 日 00:00:00+08:00（= UTC 8-31T16:00Z）的记录归 9 月账期
func TestCrossMonthAttribution(t *testing.T) {
	sh := BillingLocation("Asia/Shanghai")
	ts := time.Date(2026, 9, 1, 0, 0, 0, 0, sh).Unix()
	if got := PeriodOf(sh, ts); got != "2026-09" {
		t.Fatalf("SH 09-01 00:00 应归 2026-09，得 %s", got)
	}
	// 一秒之前仍属 8 月
	if got := PeriodOf(sh, ts-1); got != "2026-08" {
		t.Fatalf("SH 08-31 23:59:59 应归 2026-08，得 %s", got)
	}
}

// ---------- 快照与勾稽 ----------

func newBillingDB(t *testing.T) *dbFixture2 {
	t.Helper()
	gdb, err := database.Open(config.Database{Driver: "sqlite", Path: t.TempDir() + "/test.db"})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := database.Migrate(gdb); err != nil {
		t.Fatalf("迁移测试库失败: %v", err)
	}
	t.Cleanup(func() { sqlDB, _ := gdb.DB(); _ = sqlDB.Close() })
	return &dbFixture2{t: t, db: gdb}
}

type dbFixture2 struct {
	t  *testing.T
	db *gorm.DB
}

func (f *dbFixture2) insertOrg(id int64, limit, used int64) {
	f.t.Helper()
	now := time.Now().Unix()
	if err := f.db.Exec(`INSERT INTO orgs (id, name, quota_limit, quota_used, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, 1, ?, ?)`, id, "bo"+time.Now().Format("150405.000000000"), limit, used, now, now).Error; err != nil {
		f.t.Fatal(err)
	}
}

func (f *dbFixture2) insertUsage(orgID int64, createdAt, cost, vendorCost int64, noUsage int, center *int64) {
	f.t.Helper()
	var cid any
	if center != nil {
		cid = *center
	}
	if err := f.db.Exec(`INSERT INTO usage_logs (org_id, user_id, api_key_id, channel_id, cost_center_id,
		model_name, prompt_tokens, completion_tokens, cost, vendor_cost, no_usage, status, created_at)
		VALUES (?, 1, 1, 1, ?, 'm', 10, 10, ?, ?, ?, 200, ?)`,
		orgID, cid, cost, vendorCost, noUsage, createdAt).Error; err != nil {
		f.t.Fatal(err)
	}
}

func (f *dbFixture2) insertGrant(orgID, amount int64, at int64, remark string) {
	f.t.Helper()
	if err := f.db.Exec(`INSERT INTO quota_grants (subject_type, subject_id, amount, remark, created_at)
		VALUES ('org', ?, ?, ?, ?)`, orgID, amount, remark, at).Error; err != nil {
		f.t.Fatal(err)
	}
}

func TestStatementChain(t *testing.T) {
	f := newBillingDB(t)
	sh := BillingLocation("Asia/Shanghai")
	oid := int64(1)
	f.insertOrg(oid, 1_000_000, 0)

	augS, _, _ := PeriodBounds(sh, "2026-08")
	sepS, _, _ := PeriodBounds(sh, "2026-09")

	// 7 月末快照：期初 (1M, 0)
	if err := SnapshotBalances(f.db, "2026-07"); err != nil {
		t.Fatal(err)
	}
	// 幂等：重复写不报错不翻倍
	if err := SnapshotBalances(f.db, "2026-07"); err != nil {
		t.Fatal(err)
	}
	var snapCnt int64
	_ = f.db.Raw("SELECT COUNT(*) FROM period_balances WHERE org_id = 1 AND period = '2026-07'").Scan(&snapCnt).Error
	if snapCnt != 1 {
		t.Fatalf("快照应幂等 1 行，得 %d", snapCnt)
	}

	// 8 月：授权 +500k、冲减 −100k、消耗 300k（两笔：200k + 100k，其中一笔 no_usage）
	f.insertGrant(oid, 500_000, augS+3600, "充值")
	f.insertGrant(oid, -100_000, augS+7200, "退款冲减")
	f.insertUsage(oid, augS+100, 200_000, 120_000, 0, nil)
	f.insertUsage(oid, augS+200, 100_000, 0, 1, nil)
	if err := f.db.Exec("UPDATE orgs SET quota_limit = 1400000, quota_used = 300000 WHERE id = ?", oid).Error; err != nil {
		f.t.Fatal(err)
	}

	// 8 月末快照（当前值即近似月末值）
	if err := SnapshotBalances(f.db, "2026-08"); err != nil {
		t.Fatal(err)
	}

	st, err := BuildBillStatement(f.db, sh, oid, "2026-08", false, 100)
	if err != nil {
		t.Fatal(err)
	}
	if st.OpeningLimit == nil || *st.OpeningLimit != 1_000_000 || *st.OpeningUsed != 0 {
		t.Fatalf("期初应 (1M, 0)，得 %v %v", st.OpeningLimit, st.OpeningUsed)
	}
	if st.TotalGranted != 500_000 || st.TotalRevoked != -100_000 {
		t.Fatalf("授权/冲减 Σ 错: %d %d", st.TotalGranted, st.TotalRevoked)
	}
	if st.Consumption != 300_000 {
		t.Fatalf("消耗 Σ 应 300k，得 %d", st.Consumption)
	}
	if st.ClosingLimit != 1_400_000 || st.ClosingUsed != 300_000 {
		t.Fatalf("期末错: %d %d", st.ClosingLimit, st.ClosingUsed)
	}
	if st.ClosingIsLive {
		t.Fatal("有快照的月份期末不应是实时值")
	}
	if st.ChainOK == nil || !*st.ChainOK {
		t.Fatal("勾稽链应成立：期末 300k − 期初 0 == 消耗 300k")
	}
	if st.NoUsageCount != 1 {
		t.Fatalf("no_usage 应 1 笔，得 %d", st.NoUsageCount)
	}
	if len(st.Revokes) != 1 || st.Revokes[0].Amount != -100_000 || st.Revokes[0].Remark != "退款冲减" {
		t.Fatalf("冲减段错: %+v", st.Revokes)
	}
	if len(st.Grants) != 1 || st.Grants[0].Amount != 500_000 {
		t.Fatalf("授权段错: %+v", st.Grants)
	}

	// 断链：篡改 8 月快照 used → 链式校验标 ✗（不静默）
	if err := f.db.Exec("UPDATE period_balances SET quota_used = 299999 WHERE org_id = 1 AND period = '2026-08'").Error; err != nil {
		f.t.Fatal(err)
	}
	st2, _ := BuildBillStatement(f.db, sh, oid, "2026-08", false, 100)
	if st2.ChainOK == nil || *st2.ChainOK {
		t.Fatal("断链应显式标 ✗")
	}

	// 跨月归期：9 月账期不含 8 月末边界旁的记录
	f.insertUsage(oid, sepS+10, 50_000, 0, 0, nil)
	st3, _ := BuildBillStatement(f.db, sh, oid, "2026-09", false, 100)
	if st3.Consumption != 50_000 {
		t.Fatalf("9 月账期应只含 9 月记录（50k），得 %d", st3.Consumption)
	}
	// 9 月（当月）期末 = 实时值
	if !st3.ClosingIsLive {
		t.Fatal("当月无快照，期末应为实时值")
	}

	// 首个快照月之前的账单：期初缺失显示"—"，无法勾稽
	st4, _ := BuildBillStatement(f.db, sh, oid, "2026-06", false, 100)
	if !st4.OpeningMissing || st4.OpeningLimit != nil || st4.ChainOK != nil {
		t.Fatalf("无期初快照应 OpeningMissing 且 ChainOK=nil，得 %+v", st4)
	}
}

// org 视角 CSV：BOM 存在、无厂商列；platform 视角有厂商列
func TestStatementCSVisolation(t *testing.T) {
	f := newBillingDB(t)
	sh := BillingLocation("Asia/Shanghai")
	oid := int64(2)
	f.insertOrg(oid, 1_000_000, 0)
	augS, _, _ := PeriodBounds(sh, "2026-08")
	f.insertUsage(oid, augS+100, 200_000, 120_000, 0, nil)

	// org 视角
	stOrg, err := BuildBillStatement(f.db, sh, oid, "2026-08", false, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range stOrg.Rows {
		if r.VendorCost != 0 || r.Margin != 0 {
			t.Fatal("org 视角明细不得携带厂商成本/毛利")
		}
	}
	bOrg := WriteStatementCSV(stOrg, "测试客户", false)
	if !bytes.HasPrefix(bOrg, []byte("\xEF\xBB\xBF")) {
		t.Fatal("CSV 必须带 UTF-8 BOM")
	}
	if strings.Contains(string(bOrg), "厂商成本") {
		t.Fatal("org CSV 不得含厂商成本列")
	}

	// JSON 序列化同样防泄漏（omitempty：0 值不出现）
	jOrg, _ := json.Marshal(stOrg)
	if strings.Contains(string(jOrg), "vendor_cost") || strings.Contains(string(jOrg), "margin") {
		t.Fatal("org 账单 JSON 不得含 vendor_cost/margin 字段")
	}

	// platform 视角：有厂商列与毛利
	stPlat, _ := BuildBillStatement(f.db, sh, oid, "2026-08", true, 100)
	if len(stPlat.Rows) != 1 || stPlat.Rows[0].VendorCost != 120_000 || stPlat.Rows[0].Margin != 80_000 {
		t.Fatalf("platform 明细应含厂商成本/毛利: %+v", stPlat.Rows)
	}
	bPlat := WriteStatementCSV(stPlat, "测试客户", true)
	if !strings.Contains(string(bPlat), "厂商成本") || !strings.Contains(string(bPlat), "毛利") {
		t.Fatal("platform CSV 应含厂商成本与毛利列")
	}
	if !bytes.HasPrefix(bPlat, []byte("\xEF\xBB\xBF")) {
		t.Fatal("platform CSV 也应带 BOM")
	}
}

// 明细段：未归集置底 + 成本中心名 join
func TestStatementDetailOrdering(t *testing.T) {
	f := newBillingDB(t)
	sh := BillingLocation("Asia/Shanghai")
	oid := int64(3)
	f.insertOrg(oid, 10_000_000, 0)
	augS, _, _ := PeriodBounds(sh, "2026-08")
	now := time.Now().Unix()
	_ = f.db.Exec(`INSERT INTO cost_centers (org_id, name, status, created_at, updated_at)
		VALUES (3, 'AI客服', 1, ?, ?)`, now, now).Error
	center := int64(1)
	f.insertUsage(oid, augS+100, 10_000, 0, 0, nil)     // 未归集
	f.insertUsage(oid, augS+200, 5_000, 0, 0, &center) // 已归集

	st, _ := BuildBillStatement(f.db, sh, oid, "2026-08", false, 100)
	if len(st.Rows) != 2 {
		t.Fatalf("应 2 行，得 %d", len(st.Rows))
	}
	// ORDER BY (cost_center_id IS NULL)：false(0) 在前 → 已归集行第一、未归集置底
	if st.Rows[0].CostCenterID == nil || st.Rows[0].CostCenterName != "AI客服" {
		t.Fatalf("首行应为已归集行: %+v", st.Rows[0])
	}
	if st.Rows[1].CostCenterID != nil || st.Rows[1].CostCenterName != "" {
		t.Fatalf("未归集应排在最后: %+v", st.Rows[1])
	}
}
