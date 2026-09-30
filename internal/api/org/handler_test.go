package org

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"token-gateway/internal/auth"
	"token-gateway/internal/config"
	"token-gateway/internal/database"
	"token-gateway/internal/middleware"
)

type orgEnv struct {
	engine *gin.Engine
	db     *gorm.DB
	token  string
}

// newOrgEnv 临时库 + org + org_admin 身份，挂成员管理路由
func newOrgEnv(t *testing.T) *orgEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := database.Open(config.Database{Driver: "sqlite", Path: t.TempDir() + "/test.db"})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := database.Migrate(db); err != nil {
		t.Fatalf("迁移测试库失败: %v", err)
	}
	t.Cleanup(func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() })

	now := time.Now().Unix()
	oid := int64(1)
	if err := db.Exec(`INSERT INTO orgs (id, name, quota_limit, status, created_at, updated_at)
		VALUES (1, 'o', 100000000, 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatalf("造 org 失败: %v", err)
	}
	if err := db.Exec(`INSERT INTO users (id, org_id, username, password_hash, role, status, created_at, updated_at)
		VALUES (2, 1, 'admin', 'x', 'org_admin', 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatalf("造管理员失败: %v", err)
	}

	const secret = "test-secret"
	token, err := auth.GenerateToken(secret, time.Hour, 2, "org_admin", &oid, "")
	if err != nil {
		t.Fatalf("生成 token 失败: %v", err)
	}

	engine := gin.New()
	og := engine.Group("/api/org", middleware.JWTAuth(secret, db))
	h := NewHandler(db)
	og.POST("/members", h.CreateMember)
	og.PUT("/members/:id", h.UpdateMember)
	og.GET("/cost-centers", h.ListCostCenters)
	og.POST("/cost-centers", h.CreateCostCenter)
	og.PUT("/cost-centers/:id", h.UpdateCostCenter)
	og.GET("/reports/cost-centers", h.CostCenterReport)
	og.GET("/billing", h.Billing)
	og.GET("/quota-grants", h.ListQuotaGrants)
	og.PUT("/keys/:id/cost-center", h.ReassignKeyCenter)
	return &orgEnv{engine: engine, db: db, token: token}
}

func (e *orgEnv) do(method, path, body string) *httptest.ResponseRecorder {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	req.Header.Set("Authorization", "Bearer "+e.token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.engine.ServeHTTP(w, req)
	return w
}

// sumUserGrants 某 user 的流水合计
func (e *orgEnv) sumUserGrants(userID int64) int64 {
	var s int64
	_ = e.db.Raw(`SELECT COALESCE(SUM(amount),0) FROM quota_grants WHERE subject_type='user' AND subject_id=?`, userID).Scan(&s).Error
	return s
}

// 建号初始额度入流水；quota_unlimited 切换走差值流水；Σgrants == COALESCE(limit,0) 恒成立
func TestMemberQuotaLedger(t *testing.T) {
	e := newOrgEnv(t)

	// 建号初始额度 1,000,000 → 一条"创建子账号初始额度"流水
	w := e.do(http.MethodPost, "/api/org/members", `{"username":"mem1","password":"pass123","quota_amount":1000000}`)
	if w.Code != http.StatusOK {
		t.Fatalf("建号应 200，得 %d: %s", w.Code, w.Body.String())
	}
	var mid int64 // 新成员 id（自增，不假设具体值）
	_ = e.db.Raw(`SELECT id FROM users WHERE username='mem1'`).Scan(&mid).Error
	if mid == 0 {
		t.Fatal("未找到新成员")
	}
	var cnt int64
	_ = e.db.Raw(`SELECT COUNT(*) FROM quota_grants WHERE subject_type='user' AND subject_id=? AND amount=1000000 AND remark='创建子账号初始额度'`, mid).Scan(&cnt).Error
	if cnt != 1 {
		t.Fatalf("初始额度应入流水，cnt=%d", cnt)
	}
	if s := e.sumUserGrants(mid); s != 1_000_000 {
		t.Fatalf("建号后 Σgrants 应 1,000,000，得 %d", s)
	}

	// 转不限 → 差值流水 −1,000,000，Σ 归零
	if w := e.do(http.MethodPut, "/api/org/members/"+fmt.Sprint(mid), `{"quota_unlimited":true}`); w.Code != http.StatusOK {
		t.Fatalf("转不限应 200，得 %d: %s", w.Code, w.Body.String())
	}
	if s := e.sumUserGrants(mid); s != 0 {
		t.Fatalf("转不限后 Σgrants 应 0，得 %d", s)
	}
	var nullLimit *int64
	_ = e.db.Raw(`SELECT quota_limit FROM users WHERE id=?`, mid).Scan(&nullLimit).Error
	if nullLimit != nil {
		t.Fatalf("转不限后 limit 应 NULL，得 %v", *nullLimit)
	}

	// 制造消耗后转回限额 → 以消耗为起点
	_ = e.db.Exec(`UPDATE users SET quota_used=300000 WHERE id=?`, mid).Error
	if w := e.do(http.MethodPut, "/api/org/members/"+fmt.Sprint(mid), `{"quota_unlimited":false}`); w.Code != http.StatusOK {
		t.Fatalf("转限额应 200，得 %d: %s", w.Code, w.Body.String())
	}
	if s := e.sumUserGrants(mid); s != 300_000 {
		t.Fatalf("转限额应以消耗 300,000 为起点，得 Σ=%d", s)
	}
	var limit int64
	_ = e.db.Raw(`SELECT COALESCE(quota_limit,0) FROM users WHERE id=?`, mid).Scan(&limit).Error
	if limit != 300_000 {
		t.Fatalf("limit 应 300,000，得 %d", limit)
	}

	// 同态重复调用：无新流水
	before := e.sumUserGrants(mid)
	if w := e.do(http.MethodPut, "/api/org/members/"+fmt.Sprint(mid), `{"quota_unlimited":false}`); w.Code != http.StatusOK {
		t.Fatalf("同态调用应 200，得 %d: %s", w.Code, w.Body.String())
	}
	if after := e.sumUserGrants(mid); after != before {
		t.Fatalf("同态调用不应产生流水，%d → %d", before, after)
	}
}

// 并发审批同一份额度申请：pending 判定必须在事务内以条件 UPDATE 完成，
// 双击/双管理员的并发 approve 只允许加额一次（Σgrants 与 quota_limit 均不得双记）
func TestHandleRequestConcurrentApproveOnce(t *testing.T) {
	e := newOrgEnv(t)
	e.engine.Group("/api/org") // 确保 group 存在（newOrgEnv 已建）
	now := time.Now().Unix()
	// 子账号 + 一份 pending 申请（amount=5000）
	if err := e.db.Exec(`INSERT INTO users (id, org_id, username, password_hash, role, quota_limit, status, created_at, updated_at)
		VALUES (10, 1, 'm1', 'x', 'member', 1000, 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := e.db.Exec(`INSERT INTO quota_requests (id, org_id, user_id, amount, reason, status, created_at)
		VALUES (77, 1, 10, 5000, 'need more', 'pending', ?)`, now).Error; err != nil {
		t.Fatal(err)
	}
	h := NewHandler(e.db)
	// 挂到带 JWTAuth 的 /api/org 组上（与 newOrgEnv 同 secret）
	g := e.engine.Group("/api/org", middleware.JWTAuth("test-secret", e.db))
	g.PUT("/requests/:id", h.HandleRequest)

	var wg sync.WaitGroup
	codes := make([]int, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w := e.do(http.MethodPut, "/api/org/requests/77", `{"action":"approve"}`)
			codes[i] = w.Code
		}(i)
	}
	wg.Wait()

	var limit int64
	if err := e.db.Raw("SELECT quota_limit FROM users WHERE id = 10").Scan(&limit).Error; err != nil {
		t.Fatal(err)
	}
	if limit != 1000+5000 {
		t.Errorf("并发审批后 quota_limit = %d, want 6000（加额只能生效一次）", limit)
	}
	if got := e.sumUserGrants(10); got != 5000+0 { // 建号 1000 未走流水（直插），批准 5000 必须恰好一条
		t.Errorf("Σgrants(user) = %d, want 5000", got)
	}
	approved := 0
	for _, c := range codes {
		if c == http.StatusOK {
			approved++
		}
	}
	if approved != 1 {
		t.Errorf("成功响应数 = %d, want 1（其余应为 404 已处理），codes=%v", approved, codes)
	}
}

// 审批不限额子账号的额度申请必须整体拒绝：NULL 基线上加额会把「不限」悄悄顶成
// 「限额=追加额」（真实事故：下发弹窗默认 +1M 被顺手提交，超限拦截全客户调用）。
// 拒绝要干净——申请保持 pending（切回限额后可重批）、无流水、limit 保持 NULL
// 审批额度溢出预检：limit 已近 MaxInt64 时批准申请应 400 拒绝（而非 SQL 溢出 500），申请保持 pending
func TestHandleRequestApproveQuotaOverflow(t *testing.T) {
	e := newOrgEnv(t)
	now := time.Now().Unix()
	if err := e.db.Exec(`INSERT INTO users (id, org_id, username, password_hash, role, quota_limit, status, created_at, updated_at)
		VALUES (13, 1, 'm3', 'x', 'member', 9223372036854774807, 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := e.db.Exec(`INSERT INTO quota_requests (id, org_id, user_id, amount, reason, status, created_at)
		VALUES (79, 1, 13, 5000, 'big', 'pending', ?)`, now).Error; err != nil {
		t.Fatal(err)
	}
	h := NewHandler(e.db)
	g := e.engine.Group("/api/org", middleware.JWTAuth("test-secret", e.db))
	g.PUT("/requests/:id", h.HandleRequest)

	w := e.do(http.MethodPut, "/api/org/requests/79", `{"action":"approve"}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400（溢出必须显式拒绝而非 500）", w.Code)
	}
	var status string
	_ = e.db.Raw("SELECT status FROM quota_requests WHERE id = 79").Scan(&status).Error
	if status != "pending" {
		t.Errorf("申请应保持 pending（可调低后重批），得 %q", status)
	}
	if got := e.sumUserGrants(13); got != 0 {
		t.Errorf("Σgrants(user) = %d, want 0（不得留半截流水）", got)
	}
}

func TestHandleRequestApproveUnlimitedMember(t *testing.T) {
	e := newOrgEnv(t)
	now := time.Now().Unix()
	// 不限额子账号（quota_limit NULL）+ 一份 pending 额度申请
	if err := e.db.Exec(`INSERT INTO users (id, org_id, username, password_hash, role, quota_limit, status, created_at, updated_at)
		VALUES (12, 1, 'm2', 'x', 'member', NULL, 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := e.db.Exec(`INSERT INTO quota_requests (id, org_id, user_id, amount, reason, status, created_at)
		VALUES (78, 1, 12, 5000, 'need more', 'pending', ?)`, now).Error; err != nil {
		t.Fatal(err)
	}
	h := NewHandler(e.db)
	g := e.engine.Group("/api/org", middleware.JWTAuth("test-secret", e.db))
	g.PUT("/requests/:id", h.HandleRequest)

	w := e.do(http.MethodPut, "/api/org/requests/78", `{"action":"approve"}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400（不限额账号加额必须显式拒绝）", w.Code)
	}

	var nullLimit *int64
	if err := e.db.Raw("SELECT quota_limit FROM users WHERE id = 12").Scan(&nullLimit).Error; err != nil {
		t.Fatal(err)
	}
	if nullLimit != nil {
		t.Errorf("被拒后 limit 应保持 NULL，得 %v", *nullLimit)
	}
	if got := e.sumUserGrants(12); got != 0 {
		t.Errorf("Σgrants(user) = %d, want 0（不得留半截流水）", got)
	}
	var status string
	_ = e.db.Raw("SELECT status FROM quota_requests WHERE id = 78").Scan(&status).Error
	if status != "pending" {
		t.Errorf("申请应保持 pending 以便切回限额后重批，得 %q", status)
	}
}

// 删除子账号必须连带清理其管理面访问令牌（tgp_），否则令牌在账号删除后仍可调用管理 API
func TestDeleteMemberCleansAccessTokens(t *testing.T) {
	e := newOrgEnv(t)
	now := time.Now().Unix()
	if err := e.db.Exec(`INSERT INTO users (id, org_id, username, password_hash, role, status, created_at, updated_at)
		VALUES (11, 1, 'doomed', 'x', 'member', 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := e.db.Exec(`INSERT INTO access_tokens (user_id, name, token_hash, prefix, status, created_at, updated_at)
		VALUES (11, 'ci', 'hash-11', 'tgp_x', 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatal(err)
	}
	h := NewHandler(e.db)
	g := e.engine.Group("/api/org", middleware.JWTAuth("test-secret", e.db))
	g.DELETE("/members/:id", h.DeleteMember)

	w := e.do(http.MethodDelete, "/api/org/members/11", "")
	if w.Code != http.StatusOK {
		t.Fatalf("删除应 200，got %d: %s", w.Code, w.Body.String())
	}
	var cnt int64
	_ = e.db.Raw(`SELECT COUNT(*) FROM access_tokens WHERE user_id = 11`).Scan(&cnt).Error
	if cnt != 0 {
		t.Fatalf("子账号删除后其访问令牌应一并清理，剩 %d", cnt)
	}
}

// 并发同用户名建子账号：预检查拦不住竞态，败者撞 users.username 唯一索引。
// 原缺陷：败者拿 500 + 驱动错误原文「constraint failed: UNIQUE constraint failed:
// users.username (2067)」；现应 400 + 与预检查一致的文案
func TestCreateMemberConcurrentDuplicateFriendly(t *testing.T) {
	e := newOrgEnv(t)
	const body = `{"username":"dupuser","password":"Pass123456"}`
	const n = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	codes := make([]int, n)
	bodies := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			w := e.do(http.MethodPost, "/api/org/members", body)
			codes[i], bodies[i] = w.Code, w.Body.String()
		}(i)
	}
	close(start)
	wg.Wait()

	ok := 0
	for i := 0; i < n; i++ {
		if codes[i] == http.StatusOK {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("并发建号应恰 1 成功，得 %d：%v %v", ok, codes, bodies)
	}
	for i := 0; i < n; i++ {
		if codes[i] == http.StatusOK {
			continue
		}
		if codes[i] != http.StatusBadRequest || !strings.Contains(bodies[i], "用户名已存在") {
			t.Fatalf("败者应 400 + 友好文案，得 %d: %s", codes[i], bodies[i])
		}
		if strings.Contains(bodies[i], "constraint") {
			t.Fatalf("驱动错误原文泄漏: %s", bodies[i])
		}
	}
	var cnt int64
	_ = e.db.Raw(`SELECT COUNT(*) FROM users WHERE username='dupuser'`).Scan(&cnt).Error
	if cnt != 1 {
		t.Fatalf("库中应恰 1 行，得 %d", cnt)
	}
}

// 负初始额度必须 400 拒绝：负值 = 子账号一出生即欠费态（precheck 恒拒），
// 且会写入负数 grant；与 UpdateMember 的 monthly_quota>=0 校验口径一致
func TestCreateMemberRejectNegativeQuota(t *testing.T) {
	e := newOrgEnv(t)
	w := e.do(http.MethodPost, "/api/org/members", `{"username":"negmem","password":"pass123","quota_amount":-1}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("负初始额度应 400，得 %d: %s", w.Code, w.Body.String())
	}
	var users, negGrants int64
	_ = e.db.Raw(`SELECT COUNT(*) FROM users WHERE username='negmem'`).Scan(&users).Error
	_ = e.db.Raw(`SELECT COUNT(*) FROM quota_grants WHERE amount < 0`).Scan(&negGrants).Error
	if users != 0 || negGrants != 0 {
		t.Fatalf("拒绝后不应落库：users=%d neg_grants=%d", users, negGrants)
	}
}

// 模型申请审批：approve 逐模型写 user_model_grants（granted_by=审批人），已授权的
// 幂等跳过；reject 不落授权；审批时模型已下架 → 400 整单拒绝（不留半截授权）
func TestHandleRequestModelKind(t *testing.T) {
	e := newOrgEnv(t)
	now := time.Now().Unix()
	// 启用模型 m1/m2，m3 下架
	for _, m := range []struct {
		name   string
		status int
	}{{"m1", 1}, {"m2", 1}, {"m3", 0}} {
		if err := e.db.Exec(`INSERT INTO models (name, input_price, output_price, status, created_at, updated_at)
			VALUES (?, 1000000, 1000000, ?, ?, ?)`, m.name, m.status, now, now).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := e.db.Exec(`INSERT INTO users (id, org_id, username, password_hash, role, status, created_at, updated_at)
		VALUES (10, 1, 'mem10', 'x', 'member', 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatal(err)
	}
	// mem10 已被授权 m1（申请里重复出现应幂等跳过）
	if err := e.db.Exec(`INSERT INTO user_model_grants (user_id, model_name, created_at) VALUES (10, 'm1', ?)`, now).Error; err != nil {
		t.Fatal(err)
	}
	// pending 模型申请：m1（已授权，重复）+ m2（新）
	if err := e.db.Exec(`INSERT INTO quota_requests (id, org_id, user_id, kind, model_names, reason, status, created_at)
		VALUES (81, 1, 10, 'model', 'm1,m2', '项目需要', 'pending', ?)`, now).Error; err != nil {
		t.Fatal(err)
	}

	h := NewHandler(e.db)
	g := e.engine.Group("/api/org", middleware.JWTAuth("test-secret", e.db))
	g.PUT("/requests/:id", h.HandleRequest)

	if w := e.do(http.MethodPut, "/api/org/requests/81", `{"action":"approve"}`); w.Code != http.StatusOK {
		t.Fatalf("审批模型申请应 200，得 %d: %s", w.Code, w.Body.String())
	}
	var cnt int64
	_ = e.db.Raw(`SELECT COUNT(*) FROM user_model_grants WHERE user_id = 10`).Scan(&cnt).Error
	if cnt != 2 {
		t.Fatalf("授权应恰为 m1/m2 两行（重复的幂等跳过），得 %d", cnt)
	}
	var grantedBy *int64
	_ = e.db.Raw(`SELECT granted_by FROM user_model_grants WHERE user_id = 10 AND model_name = 'm2'`).Scan(&grantedBy).Error
	if grantedBy == nil || *grantedBy != 2 {
		t.Fatalf("新授权 granted_by 应为审批人（org_admin id=2），得 %v", grantedBy)
	}
	// 额度 untouched：模型申请不得动 quota_limit / 流水
	var limit *int64
	_ = e.db.Raw(`SELECT quota_limit FROM users WHERE id = 10`).Scan(&limit).Error
	if limit != nil {
		t.Fatalf("模型申请不应动 quota_limit，得 %v", *limit)
	}
	if got := e.sumUserGrants(10); got != 0 {
		t.Fatalf("模型申请不应产生额度流水，得 %d", got)
	}
	// 重复审批 → 404（pending 已消费），授权行数不变
	if w := e.do(http.MethodPut, "/api/org/requests/81", `{"action":"approve"}`); w.Code != http.StatusNotFound {
		t.Fatalf("重复审批应 404，得 %d", w.Code)
	}
	_ = e.db.Raw(`SELECT COUNT(*) FROM user_model_grants WHERE user_id = 10`).Scan(&cnt).Error
	if cnt != 2 {
		t.Fatalf("重复审批不得再落授权，得 %d", cnt)
	}

	// reject 分支：不落任何授权
	if err := e.db.Exec(`INSERT INTO quota_requests (id, org_id, user_id, kind, model_names, status, created_at)
		VALUES (82, 1, 10, 'model', 'm2', 'pending', ?)`, now).Error; err != nil {
		t.Fatal(err)
	}
	// 先删 m2 授权模拟「未授权状态」再拒绝
	if err := e.db.Exec(`DELETE FROM user_model_grants WHERE user_id = 10 AND model_name = 'm2'`).Error; err != nil {
		t.Fatal(err)
	}
	if w := e.do(http.MethodPut, "/api/org/requests/82", `{"action":"reject","reply":"暂不开放"}`); w.Code != http.StatusOK {
		t.Fatalf("拒绝应 200，得 %d: %s", w.Code, w.Body.String())
	}
	_ = e.db.Raw(`SELECT COUNT(*) FROM user_model_grants WHERE user_id = 10 AND model_name = 'm2'`).Scan(&cnt).Error
	if cnt != 0 {
		t.Fatalf("拒绝不得落授权，得 %d", cnt)
	}

	// 审批时模型已下架 → 400，整单拒绝不落授权
	if err := e.db.Exec(`INSERT INTO quota_requests (id, org_id, user_id, kind, model_names, status, created_at)
		VALUES (83, 1, 10, 'model', 'm3', 'pending', ?)`, now).Error; err != nil {
		t.Fatal(err)
	}
	if w := e.do(http.MethodPut, "/api/org/requests/83", `{"action":"approve"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("下架模型审批应 400，得 %d: %s", w.Code, w.Body.String())
	}
	_ = e.db.Raw(`SELECT COUNT(*) FROM user_model_grants WHERE user_id = 10 AND model_name = 'm3'`).Scan(&cnt).Error
	if cnt != 0 {
		t.Fatalf("下架模型不得落授权，得 %d", cnt)
	}
	var status string
	_ = e.db.Raw(`SELECT status FROM quota_requests WHERE id = 83`).Scan(&status).Error
	if status != "pending" {
		t.Fatalf("整单拒绝后申请应保持 pending（由管理员显式拒绝），得 %q", status)
	}
}

// 统计端点的子账号上限聚合（配额下发页超发提示的数据源）：
// 限额子账号的 quota_limit 合计 + 不限账号数；org_admin 不计入
func TestStatsOverviewQuotaPool(t *testing.T) {
	e := newOrgEnv(t)
	g := e.engine.Group("/api/org", middleware.JWTAuth("test-secret", e.db))
	h := NewHandler(e.db)
	g.GET("/stats/overview", h.StatsOverview)

	now := time.Now().Unix()
	seed := func(id int64, name, role string, limit *int64) {
		var lim any
		if limit != nil {
			lim = *limit
		}
		if err := e.db.Exec(`INSERT INTO users (id, org_id, username, password_hash, role, quota_limit, status, created_at, updated_at)
			VALUES (?, 1, ?, 'x', ?, ?, 1, ?, ?)`, id, name, role, lim, now, now).Error; err != nil {
			t.Fatal(err)
		}
	}
	l30, l20 := int64(30_000_000), int64(20_000_000)
	seed(10, "m1", "member", &l30)
	seed(11, "m2", "member", &l20)
	seed(12, "m3", "member", nil) // 不限账号：不计入合计，单列计数
	seed(13, "boss", "org_admin", &l30)

	w := e.do(http.MethodGet, "/api/org/stats/overview", "")
	if w.Code != http.StatusOK {
		t.Fatalf("统计应 200，得 %d: %s", w.Code, w.Body.String())
	}
	var out struct {
		QuotaPool struct {
			LimitsSum      int64 `json:"limits_sum"`
			MemberCount    int64 `json:"member_count"`
			UnlimitedCount int64 `json:"unlimited_count"`
		} `json:"quota_pool"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.QuotaPool.LimitsSum != 50_000_000 || out.QuotaPool.MemberCount != 3 || out.QuotaPool.UnlimitedCount != 1 {
		t.Fatalf("quota_pool 聚合不符：got %+v（want sum=50M count=3 unlimited=1）", out.QuotaPool)
	}
}

// waitForNotification 轮询等异步站内通知落库（审批结果在 go func 里写，需短暂等待）
func waitForNotification(t *testing.T, db *gorm.DB, userID int64, typ string) bool {
	t.Helper()
	for i := 0; i < 40; i++ {
		var cnt int64
		_ = db.Raw("SELECT COUNT(*) FROM notifications WHERE user_id = ? AND type = ?", userID, typ).Scan(&cnt).Error
		if cnt > 0 {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	return false
}

// 审批结果站内通知申请人（顶栏铃铛）：approve 额度申请 → 子账号收到 request_handled，
// 标题与正文含到账额度；org_admin 不收到（结果只发给申请人本人）
func TestHandleRequestNotifiesMember(t *testing.T) {
	e := newOrgEnv(t)
	now := time.Now().Unix()
	if err := e.db.Exec(`INSERT INTO users (id, org_id, username, password_hash, role, quota_limit, status, created_at, updated_at)
		VALUES (10, 1, 'm1', 'x', 'member', 1000, 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := e.db.Exec(`INSERT INTO quota_requests (id, org_id, user_id, amount, reason, status, created_at)
		VALUES (88, 1, 10, 5000, 'need more', 'pending', ?)`, now).Error; err != nil {
		t.Fatal(err)
	}
	h := NewHandler(e.db)
	g := e.engine.Group("/api/org", middleware.JWTAuth("test-secret", e.db))
	g.PUT("/requests/:id", h.HandleRequest)

	if w := e.do(http.MethodPut, "/api/org/requests/88", `{"action":"approve"}`); w.Code != http.StatusOK {
		t.Fatalf("审批应 200，得 %d: %s", w.Code, w.Body.String())
	}
	if !waitForNotification(t, e.db, 10, "request_handled") {
		t.Fatal("子账号应收到审批结果站内通知")
	}
	var row struct {
		Title string
		Body  string
	}
	_ = e.db.Raw("SELECT title, body FROM notifications WHERE user_id = 10 AND type = 'request_handled'").Scan(&row).Error
	if !strings.Contains(row.Title, "额度申请已通过") || !strings.Contains(row.Body, "5000") {
		t.Fatalf("通知内容应含到账额度：title=%q body=%q", row.Title, row.Body)
	}
	// 管理员自己不应收到审批结果（扇出只给申请人）
	var adminCnt int64
	_ = e.db.Raw("SELECT COUNT(*) FROM notifications WHERE user_id = 2 AND type = 'request_handled'").Scan(&adminCnt).Error
	if adminCnt != 0 {
		t.Fatalf("审批人不应收到 request_handled，实际 %d 条", adminCnt)
	}
}
