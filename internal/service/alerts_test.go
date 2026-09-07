package service

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"token-gateway/internal/config"
	"token-gateway/internal/database"
	"token-gateway/internal/metrics"
)

// 测试夹具：临时库 + 重置节流表（alertLastCheck 为包级状态，用例间必须清）
// + 注入发信观察器（sendAlertMailFn 包级替换，计数即"已发信次数"）
func newAlertTestDB(t *testing.T) (*dbFixture, *metrics.Metrics, *int64) {
	t.Helper()
	gdb, err := database.Open(config.Database{Driver: "sqlite", Path: t.TempDir() + "/test.db"})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := database.Migrate(gdb); err != nil {
		t.Fatalf("迁移测试库失败: %v", err)
	}
	t.Cleanup(func() { sqlDB, _ := gdb.DB(); _ = sqlDB.Close() })

	m := metrics.New()
	var sent int64
	sentinel := &sent
	sendAlertMailFn = func(db *gorm.DB, kind string, id, used, limit, threshold int64) {
		atomic.AddInt64(sentinel, 1)
	}
	t.Cleanup(func() { sendAlertMailFn = sendAlertMail })

	alertLastCheck = sync.Map{}
	return &dbFixture{t: t, db: gdb}, m, sentinel
}

type dbFixture struct {
	t  *testing.T
	db *gorm.DB
}

func (f *dbFixture) resetThrottle() { alertLastCheck = sync.Map{} }

func (f *dbFixture) insertOrg(id int64, limit, used int64, levels string) {
	f.t.Helper()
	now := time.Now().Unix()
	if err := f.db.Exec(`INSERT INTO orgs (id, name, quota_limit, quota_used, alert_levels, alert_level, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 0, 1, ?, ?)`, id, "org"+time.Now().Format("150405.000000000")+string(rune('a'+id%26)), limit, used, levels, now, now).Error; err != nil {
		f.t.Fatal(err)
	}
}

func (f *dbFixture) insertUser(id, orgID int64, limit *int64, used int64, levels string) {
	f.t.Helper()
	now := time.Now().Unix()
	if err := f.db.Exec(`INSERT INTO users (id, org_id, username, password_hash, role, quota_limit, quota_used, alert_levels, alert_level, status, created_at, updated_at)
		VALUES (?, ?, ?, 'x', 'member', ?, ?, ?, 0, 1, ?, ?)`,
		id, orgID, "u"+time.Now().Format("150405.000000000"), limit, used, levels, now, now).Error; err != nil {
		f.t.Fatal(err)
	}
}

func (f *dbFixture) orgLevel(id int64) int {
	f.t.Helper()
	var lv int
	_ = f.db.Raw("SELECT alert_level FROM orgs WHERE id = ?", id).Scan(&lv).Error
	return lv
}

func (f *dbFixture) orgSince(id int64) int64 {
	f.t.Helper()
	var s int64
	_ = f.db.Raw("SELECT alert_since FROM orgs WHERE id = ?", id).Scan(&s).Error
	return s
}

func (f *dbFixture) setOrg(id, limit, used int64, levels string) {
	f.t.Helper()
	if err := f.db.Exec("UPDATE orgs SET quota_limit = ?, quota_used = ?, alert_levels = ? WHERE id = ?",
		limit, used, levels, id).Error; err != nil {
		f.t.Fatal(err)
	}
}

// ---------- 纯函数 ----------

func TestAlertBracket(t *testing.T) {
	levels := []int64{80, 100}
	cases := []struct {
		used, limit int64
		want        int
	}{
		{0, 1000, 0},
		{799, 1000, 0},
		{800, 1000, 1},
		{999, 1000, 1},
		{1000, 1000, 2},
		{1500, 1000, 2}, // 超扣仍在最高档
		{7999, 10000, 0},
	}
	for _, c := range cases {
		if got := bracket(c.used, c.limit, levels); got != c.want {
			t.Fatalf("bracket(%d/%d) = %d, want %d", c.used, c.limit, got, c.want)
		}
	}
}

func TestAlertParseLevels(t *testing.T) {
	cases := []struct {
		raw  string
		want []int64
	}{
		{"[80]", []int64{80}},
		{"[]", nil},
		{"", nil},
		{"garbage", nil},
		{"[100, 80]", []int64{80, 100}},   // 排序
		{"[80, 80]", []int64{80}},         // 去重
		{"[0, -5, 50]", []int64{50}},      // 剔除非法
		{"[50, 0, 80, 80]", []int64{50, 80}},
	}
	for _, c := range cases {
		got := parseLevels(c.raw)
		if len(got) != len(c.want) {
			t.Fatalf("parseLevels(%q) = %v, want %v", c.raw, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("parseLevels(%q) = %v, want %v", c.raw, got, c.want)
			}
		}
	}
}

// ---------- 转移表逐行（设计 D1 边沿状态机） ----------

func TestAlertTransitionTable(t *testing.T) {
	f, m, sent := newAlertTestDB(t)
	const oid = int64(1)
	f.insertOrg(oid, 1000, 0, "[80, 100]")

	check := func(step string) { f.t.Helper(); f.resetThrottle(); checkAlert(f.db, m, "org", oid) }
	mail := func(step string, want int64) {
		t.Helper()
		if got := atomic.LoadInt64(sent); got != want {
			t.Fatalf("[%s] 告警发信 %d 次, want %d", step, got, want)
		}
	}

	// 未达档：level 0，不发
	check("0%"); mail("0%", 0)
	if f.orgLevel(oid) != 0 {
		t.Fatalf("0%% 后 level 应 0，得 %d", f.orgLevel(oid))
	}

	// 79%：仍未达
	f.setOrg(oid, 1000, 790, "[80, 100]")
	check("79%"); mail("79%", 0)

	// 80%：升档 → 发一次（阈值 80）
	f.setOrg(oid, 1000, 800, "[80, 100]")
	check("80%"); mail("80%", 1)
	if lv := f.orgLevel(oid); lv != 1 {
		t.Fatalf("80%% 后 level 应 1，得 %d", lv)
	}
	if s := f.orgSince(oid); s == 0 {
		t.Fatal("升档应写入 alert_since")
	}

	// 维持 85%：不重发
	f.setOrg(oid, 1000, 850, "[80, 100]")
	check("85%"); mail("85%", 1)

	// 100%：再升档 → 再发（阈值 100）
	f.setOrg(oid, 1000, 1000, "[80, 100]")
	check("100%"); mail("100%", 2)
	if lv := f.orgLevel(oid); lv != 2 {
		t.Fatalf("100%% 后 level 应 2，得 %d", lv)
	}

	// 拨备回落（limit 1000→3000，水位 33%）：静默降级，不发
	f.setOrg(oid, 3000, 1000, "[80, 100]")
	check("回落"); mail("回落", 2)
	if lv := f.orgLevel(oid); lv != 0 {
		t.Fatalf("拨备回落后 level 应 0，得 %d", lv)
	}

	// 再耗尽（3000 的 100%）：追加后再告警
	f.setOrg(oid, 3000, 3000, "[80, 100]")
	check("再耗尽"); mail("再耗尽", 3)
	if lv := f.orgLevel(oid); lv != 2 {
		t.Fatalf("再耗尽后 level 应 2，得 %d", lv)
	}

	// 拨到中间档（limit 6000，水位 50% = 档 0）后继续消耗到 80% 档：
	// 中间档（6000×50%=3000 → 档 0）不重发，80% 档（4800）重发
	f.setOrg(oid, 6000, 3000, "[80, 100]")
	check("拨中间"); mail("拨中间", 3)
	if lv := f.orgLevel(oid); lv != 0 {
		t.Fatalf("拨中间档后 level 应 0，得 %d", lv)
	}
	f.setOrg(oid, 6000, 4000, "[80, 100]") // 66%：档 0 维持
	check("66%"); mail("66%", 3)
	f.setOrg(oid, 6000, 4800, "[80, 100]") // 80%：档 1 → 发
	check("再达80"); mail("再达80", 4)
}

// 跳档收敛：水位一次跨多档，只发最高档一封
func TestAlertSkipLevelsSingleMail(t *testing.T) {
	f, m, sent := newAlertTestDB(t)
	const oid = int64(2)
	f.insertOrg(oid, 1000, 0, "[50, 80, 100]")

	f.setOrg(oid, 1000, 1000, "[50, 80, 100]") // 一步到 100%
	f.resetThrottle()
	checkAlert(f.db, m, "org", oid)

	if got := atomic.LoadInt64(sent); got != 1 {
		t.Fatalf("跳档应只发 1 封最高档，得 %d", got)
	}
	if lv := f.orgLevel(oid); lv != 3 {
		t.Fatalf("level 应 3，得 %d", lv)
	}
}

// 并发 CAS：N 个检查同时升档，仅一个胜者发信
func TestAlertConcurrentSingleWinner(t *testing.T) {
	f, m, sent := newAlertTestDB(t)
	const oid = int64(3)
	f.insertOrg(oid, 1000, 0, "[80]")

	f.setOrg(oid, 1000, 900, "[80]")
	f.resetThrottle()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			checkAlert(f.db, m, "org", oid)
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt64(sent); got != 1 {
		t.Fatalf("并发 20 检查应只发 1 封，得 %d", got)
	}
	if lv := f.orgLevel(oid); lv != 1 {
		t.Fatalf("level 应 1，得 %d", lv)
	}
}

// 60s 节流：同主体连查第二次被跳过（即使水位已变）
func TestAlertThrottle(t *testing.T) {
	f, m, sent := newAlertTestDB(t)
	const oid = int64(4)
	f.insertOrg(oid, 1000, 0, "[80]")

	f.setOrg(oid, 1000, 850, "[80]")
	f.resetThrottle()
	checkAlert(f.db, m, "org", oid) // 首查：发
	if got := atomic.LoadInt64(sent); got != 1 {
		t.Fatalf("首查应发 1 封，得 %d", got)
	}

	f.setOrg(oid, 1000, 1000, "[80]") // 升到 100%（仍是档 1）——节流期内不检查
	checkAlert(f.db, m, "org", oid)
	if got := atomic.LoadInt64(sent); got != 1 {
		t.Fatalf("节流期内不应重复发信，得 %d", got)
	}
	if m.AlertThrottled.Value() == 0 {
		t.Fatal("节流指标应计数")
	}
}

// 空数组 = 关闭；org 无限额（limit 0）= 不参与
func TestAlertDisabledCases(t *testing.T) {
	f, m, sent := newAlertTestDB(t)

	f.insertOrg(5, 1000, 1000, "[]") // 空数组
	f.insertOrg(6, 0, 500, "[80]")   // 无限额（平台未分配）
	f.resetThrottle()
	checkAlert(f.db, m, "org", 5)
	checkAlert(f.db, m, "org", 6)
	if got := atomic.LoadInt64(sent); got != 0 {
		t.Fatalf("关闭/无限额主体不应发信，得 %d", got)
	}
	if lv := f.orgLevel(5); lv != 0 {
		t.Fatalf("空数组 level 应保持 0，得 %d", lv)
	}
}

// user 主体：不限额（NULL）跳过；限额正常告警
func TestAlertUserSubject(t *testing.T) {
	f, m, sent := newAlertTestDB(t)
	f.insertOrg(7, 100000, 0, "[]")

	var lim int64 = 1000
	f.insertUser(7, 7, &lim, 900, "[80]") // 限额 90%
	f.insertUser(8, 7, nil, 900, "[80]")  // 不限额：跳过
	f.resetThrottle()
	checkAlert(f.db, m, "user", 7)
	checkAlert(f.db, m, "user", 8)

	if got := atomic.LoadInt64(sent); got != 1 {
		t.Fatalf("user 应发 1 封，得 %d", got)
	}
	var lv int
	_ = f.db.Raw("SELECT alert_level FROM users WHERE id = 8").Scan(&lv).Error
	if lv != 0 {
		t.Fatalf("不限额 user level 应 0，得 %d", lv)
	}
}

// 阈值编辑 → 静默重算（不发信），水位已过线则档位立即就位等待下次水位事件
func TestAlertRecomputeSilent(t *testing.T) {
	f, m, sent := newAlertTestDB(t)
	const oid = int64(9)
	f.insertOrg(oid, 1000, 850, "[80]") // 已在档 0（未检查过）

	RecomputeAlertLevel(f.db, "org", oid)
	if got := atomic.LoadInt64(sent); got != 0 {
		t.Fatalf("重算不应发信，得 %d", got)
	}
	if lv := f.orgLevel(oid); lv != 1 {
		t.Fatalf("重算后 level 应 1（85%% 已达 80%%），得 %d", lv)
	}

	// 阈值改 90（水位 85% < 90%）→ 重算降为 0；之后水位到 90% 再发
	_ = f.db.Exec("UPDATE orgs SET alert_levels = '[90]' WHERE id = ?", oid).Error
	RecomputeAlertLevel(f.db, "org", oid)
	if lv := f.orgLevel(oid); lv != 0 {
		t.Fatalf("阈值上调后 level 应 0，得 %d", lv)
	}
	f.setOrg(oid, 1000, 900, "[90]")
	f.resetThrottle()
	checkAlert(f.db, m, "org", oid)
	if got := atomic.LoadInt64(sent); got != 1 {
		t.Fatalf("新阈值首次达线应发 1 封，得 %d", got)
	}
}

// CheckBudgetAlerts 入口自带 panic 恢复（坏库不炸数据面）
func TestAlertPanicRecovered(t *testing.T) {
	f, m, _ := newAlertTestDB(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		CheckBudgetAlerts(f.db, m, 999, 999) // 不存在的主体：安全返回
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("CheckBudgetAlerts 未返回")
	}
}
