package org

import (
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
	og.PUT("/cost-centers/config", h.UpdateCostCenterConfig)
	og.PUT("/cost-centers/:id", h.UpdateCostCenter)
	og.GET("/reports/cost-centers", h.CostCenterReport)
	og.GET("/billing", h.Billing)
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
