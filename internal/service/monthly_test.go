package service

import (
	"errors"
	"testing"
	"time"

	"token-gateway/internal/config"
	"token-gateway/internal/database"
	"token-gateway/internal/model"
)

// 需求规格 4.7 / 1.5：月度上限（当月拦截、次月清零）+ 欠费停服状态机
func newMonthlyDB(t *testing.T) *dbFixture2 {
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

// insertMonthlyUser 建 org + member（member 月限 mQuota，org 总限 oLimit、月限 oQuota）
func (f *dbFixture2) insertMonthlyUser(oLimit, oQuota, mQuota int64) (orgID, userID int64) {
	f.t.Helper()
	orgID = 1
	now := time.Now().Unix()
	if err := f.db.Exec(`INSERT INTO orgs (id, name, quota_limit, quota_used, monthly_quota, status, created_at, updated_at)
		VALUES (1, 'm-org', ?, 0, ?, 1, ?, ?)`, oLimit, oQuota, now, now).Error; err != nil {
		f.t.Fatal(err)
	}
	if err := f.db.Exec(`INSERT INTO users (id, org_id, username, password_hash, role, quota_limit, quota_used,
		monthly_quota, status, created_at, updated_at)
		VALUES (1, 1, 'm-user', 'x', 'member', NULL, 0, ?, 1, ?, ?)`, mQuota, now, now).Error; err != nil {
		f.t.Fatal(err)
	}
	return orgID, 1
}

func settle(f *dbFixture2, orgID, userID, cost int64) {
	f.t.Helper()
	ch := int64(1)
	if err := Settle(f.db, &model.UsageLog{
		OrgID: orgID, UserID: userID, APIKeyID: 1, ChannelID: &ch,
		ModelName: "m", Cost: cost, Status: 200, CreatedAt: time.Now().Unix(),
	}); err != nil {
		f.t.Fatal(err)
	}
}

func getOrgCols(f *dbFixture2) (status int, monthlyCost int64, monthlyPeriod string) {
	f.t.Helper()
	var row struct {
		Status        int
		MonthlyCost   int64
		MonthlyPeriod string
	}
	if err := f.db.Raw("SELECT status, monthly_cost, monthly_period FROM orgs WHERE id = 1").
		Scan(&row).Error; err != nil {
		f.t.Fatal(err)
	}
	return row.Status, row.MonthlyCost, row.MonthlyPeriod
}

// 同月累计 → 达限拦截 → 跨月首笔原子清零
func TestMonthlyCapAndRollover(t *testing.T) {
	f := newMonthlyDB(t)
	orgID, userID := f.insertMonthlyUser(100_000_000, 30_000, 10_000) // org 月限 30k，子账号月限 10k

	// 同月两笔累计：4k + 4k = 8k，未达子账号月限 10k
	settle(f, orgID, userID, 4_000)
	settle(f, orgID, userID, 4_000)
	if err := Precheck(f.db, userID); err != nil {
		t.Fatalf("8k/10k 不应拦截: %v", err)
	}

	// 再结 2k → 月累计 10k 达子账号月限 → Precheck 拦截
	settle(f, orgID, userID, 2_000)
	if !errors.Is(Precheck(f.db, userID), ErrUserMonthly) {
		t.Fatalf("子账号月限应拦截，得 %v", Precheck(f.db, userID))
	}

	// 跨月清零：把存储账期伪造成上月 → 读侧视为 0，下一笔结算原子重置
	prev := PeriodOf(BillingLoc(), time.Now().AddDate(0, -1, 0).Unix())
	if err := f.db.Exec("UPDATE users SET monthly_period = ?, monthly_cost = 5000 WHERE id = 1", prev).Error; err != nil {
		t.Fatal(err)
	}
	if err := Precheck(f.db, userID); err != nil {
		t.Fatalf("跨月后月累计应清零: %v", err)
	}
	settle(f, orgID, userID, 3_000) // 新月首笔
	var urow struct {
		MonthlyCost   int64
		MonthlyPeriod string
	}
	_ = f.db.Raw("SELECT monthly_cost, monthly_period FROM users WHERE id = 1").Scan(&urow).Error
	if urow.MonthlyCost != 3_000 || urow.MonthlyPeriod != PeriodOf(BillingLoc(), time.Now().Unix()) {
		t.Fatalf("新月首笔应重置为 3k，得 cost=%d period=%s", urow.MonthlyCost, urow.MonthlyPeriod)
	}
}

// org 月限独立拦截（子账号不限时）
func TestOrgMonthlyCap(t *testing.T) {
	f := newMonthlyDB(t)
	orgID, userID := f.insertMonthlyUser(100_000_000, 5_000, 0)
	settle(f, orgID, userID, 5_000)
	if !errors.Is(Precheck(f.db, userID), ErrOrgMonthly) {
		t.Fatal("客户月限应拦截")
	}
}

// 0 = 不限：月限关闭时不拦截
func TestMonthlyZeroUnlimited(t *testing.T) {
	f := newMonthlyDB(t)
	orgID, userID := f.insertMonthlyUser(100_000_000, 0, 0)
	settle(f, orgID, userID, 999_999)
	if err := Precheck(f.db, userID); err != nil {
		t.Fatalf("月限 0=不限不应拦截: %v", err)
	}
}

// 欠费停服状态机：额度耗尽自动置 2；充值自动恢复；手动停用 0 不被自动恢复
func TestArrearsLifecycle(t *testing.T) {
	f := newMonthlyDB(t)
	orgID, userID := f.insertMonthlyUser(10_000, 0, 0)

	// 未耗尽：状态仍 1
	settle(f, orgID, userID, 9_000)
	if s, _, _ := getOrgCols(f); s != 1 {
		t.Fatalf("未耗尽不应停服，status=%d", s)
	}

	// 耗尽（超扣封顶在单请求内）：自动置 2
	settle(f, orgID, userID, 1_500)
	if s, _, _ := getOrgCols(f); s != 2 {
		t.Fatalf("额度耗尽应自动欠费停服 status=2，得 %d", s)
	}
	if !errors.Is(Precheck(f.db, userID), ErrOrgQuota) {
		t.Fatal("耗尽后 Precheck 应返回总额不足")
	}

	// 追加额度：自动恢复 1
	if err := AddOrgQuota(f.db, orgID, 100_000, 0, "充值恢复"); err != nil {
		t.Fatal(err)
	}
	if s, _, _ := getOrgCols(f); s != 1 {
		t.Fatalf("充值后应自动恢复 status=1，得 %d", s)
	}

	// 手动停用 0：追加额度不会误恢复
	if err := f.db.Exec("UPDATE orgs SET status = 0 WHERE id = 1").Error; err != nil {
		t.Fatal(err)
	}
	if err := AddOrgQuota(f.db, orgID, 50_000, 0, "手动停用期间追加"); err != nil {
		t.Fatal(err)
	}
	if s, _, _ := getOrgCols(f); s != 0 {
		t.Fatalf("手动停用不应被自动恢复，得 %d", s)
	}
}

// org 月累计同样记账（对账口径）
func TestOrgMonthlyAccumulates(t *testing.T) {
	f := newMonthlyDB(t)
	orgID, userID := f.insertMonthlyUser(100_000_000, 0, 0)
	settle(f, orgID, userID, 7_000)
	_, cost, period := getOrgCols(f)
	if cost != 7_000 || period == "" {
		t.Fatalf("org 月累计应 7000，得 cost=%d period=%s", cost, period)
	}
}
