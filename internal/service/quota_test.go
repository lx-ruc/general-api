package service

import (
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"token-gateway/internal/config"
	"token-gateway/internal/database"
	"token-gateway/internal/model"
)

func TestQuotaLedgerInvariant(t *testing.T) {
	gdb, err := database.Open(config.Database{Driver: "sqlite", Path: t.TempDir() + "/test.db"})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := database.Migrate(gdb); err != nil {
		t.Fatalf("迁移测试库失败: %v", err)
	}
	t.Cleanup(func() { sqlDB, _ := gdb.DB(); _ = sqlDB.Close() })

	now := time.Now().Unix()
	oid := int64(1)
	uid := int64(1)
	if err := gdb.Exec(`INSERT INTO orgs (id, name, quota_limit, status, created_at, updated_at)
		VALUES (1, 'o', 100000000, 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`INSERT INTO users (id, org_id, username, password_hash, role, status, created_at, updated_at)
		VALUES (1, 1, 'u', 'x', 'member', 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatal(err)
	}

	// 断言器：Σgrants(user) == COALESCE(quota_limit, 0)
	sumGrants := func() int64 {
		var s int64
		_ = gdb.Raw(`SELECT COALESCE(SUM(amount), 0) FROM quota_grants WHERE subject_type='user' AND subject_id = ?`, uid).Scan(&s).Error
		return s
	}
	limitBase := func() int64 {
		var v int64
		_ = gdb.Raw(`SELECT COALESCE(quota_limit, 0) FROM users WHERE id = ?`, uid).Scan(&v).Error
		return v
	}
	assertInvariant := func(step string) {
		t.Helper()
		if g, l := sumGrants(), limitBase(); g != l {
			t.Fatalf("[%s] Σgrants=%d 但 COALESCE(limit,0)=%d，审计链断裂", step, g, l)
		}
	}

	// 不限 → 限额（消耗 0 起点）：差值 0，无流水但不变量成立
	if err := SetUserQuotaUnlimited(gdb, oid, uid, false, 9); err != nil {
		t.Fatalf("转限额失败: %v", err)
	}
	assertInvariant("初始转限额(0)")
	var cnt int64
	_ = gdb.Raw(`SELECT COUNT(*) FROM quota_grants WHERE subject_type='user'`).Scan(&cnt).Error
	if cnt != 0 {
		t.Fatalf("差值 0 不应产生流水，得 %d 条", cnt)
	}

	// 追加 ×2
	for _, amt := range []int64{1_000_000, 500_000} {
		if err := AddUserQuota(gdb, oid, uid, amt, 9, "追加"); err != nil {
			t.Fatalf("追加失败: %v", err)
		}
		assertInvariant("追加")
	}
	if g, l := sumGrants(), limitBase(); g != 1_500_000 || l != 1_500_000 {
		t.Fatalf("追加后应 1,500,000，得 Σ=%d limit=%d", g, l)
	}

	// 同态重复调用（已限额再转限额）：无操作、无流水
	before := sumGrants()
	if err := SetUserQuotaUnlimited(gdb, oid, uid, false, 9); err != nil {
		t.Fatalf("同态调用失败: %v", err)
	}
	if after := sumGrants(); after != before {
		t.Fatalf("同态调用不应产生流水，%d → %d", before, after)
	}

	// 限额 → 不限：差值 −1,500,000
	if err := SetUserQuotaUnlimited(gdb, oid, uid, true, 9); err != nil {
		t.Fatalf("转不限失败: %v", err)
	}
	if g := sumGrants(); g != 0 {
		t.Fatalf("转不限后 Σgrants 应 0，得 %d", g)
	}
	var nullLimit *int64
	_ = gdb.Raw(`SELECT quota_limit FROM users WHERE id = ?`, uid).Scan(&nullLimit).Error
	if nullLimit != nil {
		t.Fatalf("转不限后 limit 应为 NULL，得 %v", *nullLimit)
	}

	// 制造消耗后 不限 → 限额：以当前消耗为起点
	if err := gdb.Exec(`UPDATE users SET quota_used = 800000 WHERE id = ?`, uid).Error; err != nil {
		t.Fatal(err)
	}
	if err := SetUserQuotaUnlimited(gdb, oid, uid, false, 9); err != nil {
		t.Fatalf("转限额失败: %v", err)
	}
	if g, l := sumGrants(), limitBase(); g != 800_000 || l != 800_000 {
		t.Fatalf("转限额应以消耗 800,000 为起点，得 Σ=%d limit=%d", g, l)
	}
	assertInvariant("消耗起点转限额")
}

// 额度追加溢出防护：amount 接近 MaxInt64 时 quota_limit + amount 不得回绕，
// 必须显式报错且不留半截事务（limit 不变、无流水）
func TestAddQuotaOverflowGuard(t *testing.T) {
	gdb, err := database.Open(config.Database{Driver: "sqlite", Path: t.TempDir() + "/ov.db"})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := database.Migrate(gdb); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() { sqlDB, _ := gdb.DB(); _ = sqlDB.Close() })
	now := time.Now().Unix()
	_ = gdb.Exec(`INSERT INTO orgs (id, name, quota_limit, status, created_at, updated_at)
		VALUES (7, 'o', ?, 1, ?, ?)`, int64(1)<<62, now, now).Error
	_ = gdb.Exec(`INSERT INTO users (id, org_id, username, password_hash, role, status, quota_limit, created_at, updated_at)
		VALUES (7, 7, 'u7', 'x', 'member', 1, ?, ?, ?)`, int64(1)<<62, now, now).Error

	if err := AddOrgQuota(gdb, 7, math.MaxInt64-(1<<60), 1, "溢出充值"); err == nil {
		t.Fatal("org 追加溢出应报错")
	} else {
		var lim int64
		_ = gdb.Raw("SELECT quota_limit FROM orgs WHERE id = 7").Scan(&lim).Error
		if lim != int64(1)<<62 {
			t.Fatalf("失败事务不得改动 limit，got %d", lim)
		}
		var cnt int64
		_ = gdb.Raw("SELECT COUNT(*) FROM quota_grants WHERE subject_type='org' AND subject_id=7").Scan(&cnt).Error
		if cnt != 0 {
			t.Fatalf("失败不得留流水，got %d 条", cnt)
		}
	}
	if err := AddUserQuota(gdb, 7, 7, math.MaxInt64-(1<<60), 1, "溢出追加"); err == nil {
		t.Fatal("user 追加溢出应报错")
	}
	// 正常路径不受影响
	if err := AddOrgQuota(gdb, 7, 1000, 1, "正常"); err != nil {
		t.Fatalf("正常追加失败: %v", err)
	}
	if err := AddUserQuota(gdb, 7, 7, 1000, 1, "正常"); err != nil {
		t.Fatalf("正常追加失败: %v", err)
	}
}

// 并发额度竞态的两条既定语义（可用性优先的 advisory 预检 + 无条件结算，见 docs/需求验收报告.md）：
//  1. 预检不锁：N 路并发预检在余额未尽时全部放行（并发齐射的超扣上界是 N×单次成本，
//     「封顶在单请求成本内」仅对串行到达成立）
//  2. 结算不丢：N 路并发 Settle 每一笔都必须落账——quota_used 与 Σusage_logs.cost
//     精确相等（SQLite WAL _txlock=immediate 单写者串行化下不得出现丢失更新），
//     且额度耗尽后 org 自动置欠费停服
func TestQuotaConcurrentAdmissionAndSettle(t *testing.T) {
	gdb, err := database.Open(config.Database{Driver: "sqlite", Path: t.TempDir() + "/race.db"})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := database.Migrate(gdb); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() { sqlDB, _ := gdb.DB(); _ = sqlDB.Close() })
	now := time.Now().Unix()
	const uid, oid, limit, cost = int64(11), int64(11), int64(300), int64(200)
	_ = gdb.Exec(`INSERT INTO orgs (id, name, quota_limit, status, created_at, updated_at)
		VALUES (11, 'race-org', ?, 1, ?, ?)`, limit, now, now).Error
	_ = gdb.Exec(`INSERT INTO users (id, org_id, username, password_hash, role, status, quota_limit, created_at, updated_at)
		VALUES (11, 11, 'race-u', 'x', 'member', 1, ?, ?, ?)`, limit, now, now).Error

	const n = 20
	// 阶段一：N 路并发预检（used=0 < limit）——必须全部放行（advisory，不加锁）
	start := make(chan struct{})
	pre := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			pre[i] = Precheck(gdb, uid)
		}(i)
	}
	close(start)
	wg.Wait()
	for i, e := range pre {
		if e != nil {
			t.Fatalf("预检 #%d 不应拒绝（advisory 语义）: %v", i, e)
		}
	}

	// 阶段二：N 路并发结算——全部精确落账
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := &model.UsageLog{RequestID: fmt.Sprintf("race-%d", i), OrgID: oid, UserID: uid,
				ModelName: "race-m", Cost: cost, Status: 200, CreatedAt: now}
			if err := Settle(gdb, rec); err != nil {
				t.Errorf("结算 #%d 失败: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	var userUsed, orgUsed, sumCost, cnt, orgStatus int64
	_ = gdb.Raw("SELECT quota_used FROM users WHERE id = ?", uid).Scan(&userUsed).Error
	_ = gdb.Raw("SELECT quota_used FROM orgs WHERE id = ?", oid).Scan(&orgUsed).Error
	_ = gdb.Raw("SELECT COALESCE(SUM(cost),0), COUNT(*) FROM usage_logs WHERE user_id = ?", uid).Row().Scan(&sumCost, &cnt)
	_ = gdb.Raw("SELECT status FROM orgs WHERE id = ?", oid).Scan(&orgStatus).Error

	want := int64(n) * cost
	if userUsed != want || orgUsed != want || sumCost != want || cnt != n {
		t.Fatalf("并发结算丢失更新：user=%d org=%d Σcost=%d rows=%d，应全为 %d/%d 行",
			userUsed, orgUsed, sumCost, cnt, want, n)
	}
	if userUsed <= limit {
		t.Fatalf("并发齐射应真实超扣（used=%d > limit=%d），否则测试未覆盖竞态窗口", userUsed, limit)
	}
	if userUsed > want {
		t.Fatalf("超扣不得超出 N×单次成本：used=%d > %d", userUsed, want)
	}
	if orgStatus != 2 {
		t.Fatalf("额度耗尽后 org 应欠费停服 status=2，got %d", orgStatus)
	}
	// 竞态之后的串行请求必须被预检拦下（超扣止步于本批在飞请求）
	if err := Precheck(gdb, uid); err != ErrUserQuota {
		t.Fatalf("耗尽后串行预检应拒绝（ErrUserQuota），got %v", err)
	}
}

// 欠费停服状态机的全部边界迁移（并发主路径 1→2 已由 TestQuotaConcurrentAdmissionAndSettle 覆盖）：
//   - 首次超扣结算：1 → 2（used 达 limit）
//   - 充值有余量：2 → 1
//   - 负回收后仍超限：status 停在 1（迁移只发生在结算点；停服效果由 Precheck 拦截兜底）
//   - 手动停用（0）：结算不迁移、充值不误恢复（管理员意图优先）
//   - quota_limit=0：结算不置 2（Precheck 已拦；防御性验证）
//   - cost=0 结算（缓存命中/无 usage）：不做欠费迁移
func TestArrearsStateMachine(t *testing.T) {
	gdb, err := database.Open(config.Database{Driver: "sqlite", Path: t.TempDir() + "/arrears.db"})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := database.Migrate(gdb); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() { sqlDB, _ := gdb.DB(); _ = sqlDB.Close() })
	now := time.Now().Unix()

	// org 21 正常额度；22 手动停用；23 limit=0；24 供 cost=0 结算
	seed := func(id int64, limit int64, status int) {
		_ = gdb.Exec(`INSERT INTO orgs (id, name, quota_limit, status, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)`, id, fmt.Sprintf("arr-%d", id), limit, status, now, now).Error
		_ = gdb.Exec(`INSERT INTO users (id, org_id, username, password_hash, role, status, created_at, updated_at)
			VALUES (?, ?, ?, 'x', 'member', 1, ?, ?)`, id, id, fmt.Sprintf("arru-%d", id), now, now).Error
	}
	seed(21, 100, 1)
	seed(22, 100, 0)
	seed(23, 0, 1)
	seed(24, 100, 1)

	status := func(id int64) int {
		var s int
		_ = gdb.Raw("SELECT status FROM orgs WHERE id = ?", id).Scan(&s).Error
		return s
	}
	settle := func(id, cost int64) {
		t.Helper()
		rec := &model.UsageLog{RequestID: fmt.Sprintf("ar-%d-%d", id, cost), OrgID: id, UserID: id,
			ModelName: "ar-m", Cost: cost, Status: 200, CreatedAt: now}
		if err := Settle(gdb, rec); err != nil {
			t.Fatalf("org %d 结算失败: %v", id, err)
		}
	}

	// 21：首次超扣（used 99 < 100 放行，结算后 199 >= 100）→ 欠费停服
	_ = gdb.Exec("UPDATE orgs SET quota_used = 99 WHERE id = 21").Error
	_ = gdb.Exec("UPDATE users SET quota_used = 99 WHERE id = 21").Error
	settle(21, 100)
	if s := status(21); s != 2 {
		t.Fatalf("[21] 首次超扣结算应置 status=2，got %d", s)
	}
	// 充值 +200 → limit 300 > used 199 → 自动恢复 1
	if err := AddOrgQuota(gdb, 21, 200, 1, "充值恢复"); err != nil {
		t.Fatalf("[21] 充值失败: %v", err)
	}
	if s := status(21); s != 1 {
		t.Fatalf("[21] 充值有余量应恢复 status=1，got %d", s)
	}
	// 负回收 −250 → limit 50 < used 199 → status 停在 1（迁移只在结算点；Precheck 拦截兜底）
	if err := AddOrgQuota(gdb, 21, -250, 1, "回收仍超限"); err != nil {
		t.Fatalf("[21] 负回收失败: %v", err)
	}
	if s := status(21); s != 1 {
		t.Fatalf("[21] 负回收后 status 应保持 1（不在此处迁移），got %d", s)
	}
	if err := Precheck(gdb, 21); err != ErrOrgQuota {
		t.Fatalf("[21] 回收超限后 Precheck 应拒（ErrOrgQuota），got %v", err)
	}

	// 22：手动停用下结算不迁移、充值不误恢复
	_ = gdb.Exec("UPDATE orgs SET quota_used = 99 WHERE id = 22").Error
	settle(22, 100)
	if s := status(22); s != 0 {
		t.Fatalf("[22] 手动停用结算不得迁移（保持 0），got %d", s)
	}
	if err := AddOrgQuota(gdb, 22, 1000, 1, "充值"); err != nil {
		t.Fatalf("[22] 充值失败: %v", err)
	}
	if s := status(22); s != 0 {
		t.Fatalf("[22] 手动停用充值不得误恢复（保持 0），got %d", s)
	}

	// 23：limit=0 结算不置 2（quota_limit > 0 条件；Precheck 已在入口拦截）
	settle(23, 50)
	if s := status(23); s != 1 {
		t.Fatalf("[23] limit=0 结算不应置 2，got %d", s)
	}

	// 24：cost=0（缓存命中/无 usage）结算不做欠费评估——即使已超限也不迁移
	_ = gdb.Exec("UPDATE orgs SET quota_used = 150 WHERE id = 24").Error
	settle(24, 0)
	if s := status(24); s != 1 {
		t.Fatalf("[24] cost=0 结算不应迁移 status，got %d", s)
	}
}

// PointsPerYuan 超长数字串不得回绕出任意汇率
func TestPointsPerYuanOverflow(t *testing.T) {
	gdb, err := database.Open(config.Database{Driver: "sqlite", Path: t.TempDir() + "/pp.db"})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := database.Migrate(gdb); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() { sqlDB, _ := gdb.DB(); _ = sqlDB.Close() })
	_ = gdb.Exec("INSERT INTO settings (key, value) VALUES ('points_per_yuan', '99999999999999999999999999')").Error
	if got := PointsPerYuan(gdb); got != 1_000_000 {
		t.Fatalf("超长数字应回退默认 1e6，got %d", got)
	}
}

// SetOrgQuota 设值调整：目标值直接落 quota_limit，差值入流水，Σgrants 不变量恒成立；
// 设高解除欠费停服、设低不主动停服（与追加同口径）、同值重设无流水、负值拒绝
func TestSetOrgQuota(t *testing.T) {
	gdb, err := database.Open(config.Database{Driver: "sqlite", Path: t.TempDir() + "/setq.db"})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := database.Migrate(gdb); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() { sqlDB, _ := gdb.DB(); _ = sqlDB.Close() })
	now := time.Now().Unix()

	// 31 正常额度；32 手动停用（不得被设值误恢复）。
	// 初始额度同笔入流水（与生产 CreateOrg 走 AddOrgQuotaTx 一致），Σgrants 从成立起步
	seed := func(id, limit int64, status int) {
		_ = gdb.Exec(`INSERT INTO orgs (id, name, quota_limit, quota_used, status, created_at, updated_at)
			VALUES (?, ?, ?, 400000, ?, ?, ?)`, id, fmt.Sprintf("setq-%d", id), limit, status, now, now).Error
		_ = gdb.Exec(`INSERT INTO quota_grants (subject_type, subject_id, amount, remark, created_at)
			VALUES ('org', ?, ?, '初始额度', ?)`, id, limit, now).Error
	}
	seed(31, 1_000_000, 1)
	seed(32, 1_000_000, 0)

	sumGrants := func(id int64) int64 {
		var s int64
		_ = gdb.Raw(`SELECT COALESCE(SUM(amount), 0) FROM quota_grants WHERE subject_type='org' AND subject_id = ?`, id).Scan(&s).Error
		return s
	}
	limitOf := func(id int64) int64 {
		var v int64
		_ = gdb.Raw("SELECT quota_limit FROM orgs WHERE id = ?", id).Scan(&v).Error
		return v
	}
	statusOf := func(id int64) int {
		var s int
		_ = gdb.Raw("SELECT status FROM orgs WHERE id = ?", id).Scan(&s).Error
		return s
	}
	assertInvariant := func(id int64, step string) {
		t.Helper()
		if g, l := sumGrants(id), limitOf(id); g != l {
			t.Fatalf("[%s org %d] Σgrants=%d 但 limit=%d，审计链断裂", step, id, g, l)
		}
	}

	// 设高：limit 1M→2M，差值 +1M 入流水
	if d, err := SetOrgQuota(gdb, 31, 2_000_000, 1, "设高"); err != nil || d != 1_000_000 {
		t.Fatalf("设高失败: delta=%d err=%v", d, err)
	}
	assertInvariant(31, "设高")

	// 设低于消耗（300k < used 400k）：允许，不主动停服；Precheck 拦截兜底
	if _, err := SetOrgQuota(gdb, 31, 300_000, 1, "设低"); err != nil {
		t.Fatalf("设低失败: %v", err)
	}
	assertInvariant(31, "设低")
	if s := statusOf(31); s != 1 {
		t.Fatalf("[31] 设低不主动停服，got %d", s)
	}

	// 设高解除欠费停服：置 status=2 后设出富余 → 自动恢复 1
	_ = gdb.Exec("UPDATE orgs SET status = 2 WHERE id = 31").Error
	if _, err := SetOrgQuota(gdb, 31, 600_000, 1, "欠费后设富余"); err != nil {
		t.Fatalf("欠费后设值失败: %v", err)
	}
	if s := statusOf(31); s != 1 {
		t.Fatalf("[31] 设出富余应解除欠费停服，got %d", s)
	}

	// 同值重设：无新流水
	before := sumGrants(31)
	if d, err := SetOrgQuota(gdb, 31, 600_000, 1, "同值"); err != nil || d != 0 {
		t.Fatalf("同值重设应无操作: delta=%d err=%v", d, err)
	}
	if after := sumGrants(31); after != before {
		t.Fatalf("同值重设不应产生流水，%d → %d", before, after)
	}

	// 手动停用（0）不得被设值误恢复
	if _, err := SetOrgQuota(gdb, 32, 9_000_000, 1, "手动停用下设值"); err != nil {
		t.Fatalf("[32] 设值失败: %v", err)
	}
	if s := statusOf(32); s != 0 {
		t.Fatalf("[32] 手动停用不得被误恢复，got %d", s)
	}

	// 负值拒绝：limit 不变、无流水
	if _, err := SetOrgQuota(gdb, 31, -1, 1, "负值"); err != ErrQuotaOverflow {
		t.Fatalf("负值应拒绝，got %v", err)
	}
	if l := limitOf(31); l != 600_000 {
		t.Fatalf("负值拒绝后 limit 不应变，got %d", l)
	}
	assertInvariant(31, "负值拒绝后")

	// 不存在的客户
	if _, err := SetOrgQuota(gdb, 999, 100, 1, ""); err != ErrNotFound {
		t.Fatalf("不存在客户应 ErrNotFound，got %v", err)
	}
}
