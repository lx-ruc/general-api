package org

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"token-gateway/internal/middleware"
)

// 模型开通申请（org 端）：T8 白名单收窄生效、T9 回收窗口审批拦截、T10 申请提交

func newModelRequestEnv(t *testing.T) *orgEnv {
	t.Helper()
	e := newOrgEnv(t)
	h := NewHandler(e.db)
	g := e.engine.Group("/api/org", middleware.JWTAuth("test-secret", e.db))
	g.GET("/members/:id/models", h.GetMemberModels)
	g.PUT("/members/:id/models", h.SetMemberModels)
	g.PUT("/requests/:id", h.HandleRequest)
	g.GET("/model-requests/candidates", h.ListModelRequestCandidates)
	g.GET("/model-requests", h.ListModelRequests)
	g.POST("/model-requests", h.CreateModelRequest)
	return e
}

func seedModelsForOrg(t *testing.T, db *gorm.DB, names ...string) {
	t.Helper()
	now := time.Now().Unix()
	for _, n := range names {
		if err := db.Exec(`INSERT INTO models (name, input_price, output_price, status, created_at, updated_at)
			VALUES (?, 1000000, 1000000, 1, ?, ?)`, n, now, now).Error; err != nil {
			t.Fatal(err)
		}
	}
}

// waitForOrgModelNotification 轮询等待异步站内通知落库（最多 1s）
func waitForOrgModelNotification(t *testing.T, db *gorm.DB, userID int64, typ string) bool {
	t.Helper()
	for i := 0; i < 40; i++ {
		var cnt int64
		_ = db.Raw(`SELECT COUNT(*) FROM notifications WHERE user_id = ? AND type = ?`, userID, typ).Scan(&cnt).Error
		if cnt > 0 {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	return false
}

// T8 白名单收窄：available 只含客户已开通模型；给子账号授权未开通模型 → 400
func TestMemberModelsNarrowedByOrgGrants(t *testing.T) {
	e := newModelRequestEnv(t)
	now := time.Now().Unix()
	seedModelsForOrg(t, e.db, "m1", "m2")
	// 平台管理员（id=1，无 org）与客户管理员（id=2）；mem10 待授权
	if err := e.db.Exec(`INSERT INTO users (id, org_id, username, password_hash, role, status, created_at, updated_at)
		VALUES (10, 1, 'mem10', 'x', 'member', 1, ?, ?), (1, NULL, 'root', 'x', 'platform_admin', 1, ?, ?)`,
		now, now, now, now).Error; err != nil {
		t.Fatal(err)
	}
	// 客户仅开通 m1
	if err := e.db.Exec(`INSERT INTO org_model_grants (org_id, model_name, created_at) VALUES (1, 'm1', ?)`, now).Error; err != nil {
		t.Fatal(err)
	}

	// available 收窄
	w := e.do(http.MethodGet, "/api/org/members/10/models", "")
	if w.Code != http.StatusOK {
		t.Fatalf("应 200，得 %d", w.Code)
	}
	if strings.Contains(w.Body.String(), `"name":"m2"`) {
		t.Fatalf("available 不应含未开通的 m2: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"name":"m1"`) {
		t.Fatalf("available 应含已开通的 m1: %s", w.Body.String())
	}

	// 授权未开通模型 → 400
	w = e.do(http.MethodPut, "/api/org/members/10/models", `{"models":["m2"]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("授权未开通模型应 400，得 %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "模型申请") {
		t.Fatalf("400 文案应指引到模型申请: %s", w.Body.String())
	}
	// 授权已开通模型 → 200
	w = e.do(http.MethodPut, "/api/org/members/10/models", `{"models":["m1"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("授权已开通模型应 200，得 %d: %s", w.Code, w.Body.String())
	}
}

// T9 子账号申请在途时平台回收了该模型 → 审批 400（不得绕过白名单）
func TestHandleRequestModelRecycledByPlatform(t *testing.T) {
	e := newModelRequestEnv(t)
	now := time.Now().Unix()
	seedModelsForOrg(t, e.db, "m1")
	if err := e.db.Exec(`INSERT INTO users (id, org_id, username, password_hash, role, status, created_at, updated_at)
		VALUES (10, 1, 'mem10', 'x', 'member', 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := e.db.Exec(`INSERT INTO quota_requests (id, org_id, user_id, kind, model_names, status, created_at)
		VALUES (81, 1, 10, 'model', 'm1', 'pending', ?)`, now).Error; err != nil {
		t.Fatal(err)
	}
	// 模型启用但客户未开通（提交后被平台回收的情形）
	w := e.do(http.MethodPut, "/api/org/requests/81", `{"action":"approve"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("未开通模型审批应 400，得 %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "回收") {
		t.Fatalf("文案应说明已被平台回收: %s", w.Body.String())
	}
}

// T10 提交开通申请：剔除已开通、空申请 400、成功后平台管理员收到站内通知
func TestCreateModelRequest(t *testing.T) {
	e := newModelRequestEnv(t)
	now := time.Now().Unix()
	seedModelsForOrg(t, e.db, "m1", "m2", "m3")
	if err := e.db.Exec(`INSERT INTO users (id, org_id, username, password_hash, role, status, created_at, updated_at)
		VALUES (1, NULL, 'root', 'x', 'platform_admin', 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatal(err)
	}
	// m1 已开通
	if err := e.db.Exec(`INSERT INTO org_model_grants (org_id, model_name, created_at) VALUES (1, 'm1', ?)`, now).Error; err != nil {
		t.Fatal(err)
	}

	// 全部已开通 → 400
	w := e.do(http.MethodPost, "/api/org/model-requests", `{"models":["m1"],"reason":"x"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("全部已开通应 400，得 %d: %s", w.Code, w.Body.String())
	}
	// 空 → 400
	if w := e.do(http.MethodPost, "/api/org/model-requests", `{"reason":"x"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("空申请应 400，得 %d", w.Code)
	}
	// 含已开通的 m1 + 未开通的 m2 → 剔除 m1 只落 m2
	w = e.do(http.MethodPost, "/api/org/model-requests", `{"models":["m1","m2"],"reason":"新项目"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("申请应 200，得 %d: %s", w.Code, w.Body.String())
	}
	var names string
	_ = e.db.Raw(`SELECT model_names FROM org_model_requests ORDER BY id DESC LIMIT 1`).Scan(&names).Error
	if names != "m2" {
		t.Fatalf("应剔除已开通的 m1 只落 m2，得 %q", names)
	}
	// 平台管理员收到站内通知（异步）
	if !waitForOrgModelNotification(t, e.db, 1, "model_request") {
		t.Fatal("平台管理员应收到待审批通知")
	}

	// candidates：m1 granted、m2 pending、m3 可申请
	w = e.do(http.MethodGet, "/api/org/model-requests/candidates", "")
	if w.Code != http.StatusOK {
		t.Fatalf("candidates 应 200，得 %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"granted":true`) || !strings.Contains(w.Body.String(), `"pending":true`) {
		t.Fatalf("候选应带 granted/pending 标记: %s", w.Body.String())
	}
}
