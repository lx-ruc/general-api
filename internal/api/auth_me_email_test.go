package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"token-gateway/internal/auth"
	"token-gateway/internal/config"
	"token-gateway/internal/database"
	"token-gateway/internal/middleware"
)

// 邮箱绑定端点（/api/me/email/*）：手动挂路由 + JWTAuth，SMTP 保持未配置走开发模式
func TestMeEmailBindEndpoints(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := database.Open(config.Database{Driver: "sqlite", Path: t.TempDir() + "/me.db"})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := database.Migrate(db); err != nil {
		t.Fatalf("迁移测试库失败: %v", err)
	}
	t.Cleanup(func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() })

	now := time.Now().Unix()
	if err := db.Exec(`INSERT INTO users (id, org_id, username, password_hash, role, status, created_at, updated_at)
		VALUES (10, NULL, 'plat', 'x', 'platform_admin', 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO users (id, org_id, username, password_hash, email, role, status, created_at, updated_at)
		VALUES (11, NULL, 'other', 'x', 'taken@x.com', 'org_admin', 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatal(err)
	}

	const secret = "test-secret"
	h := &AuthHandler{DB: db, Secret: secret, TTL: time.Hour}
	engine := gin.New()
	authed := engine.Group("/api", middleware.JWTAuth(secret, db))
	authed.GET("/me", h.Me)
	authed.POST("/me/email/code", h.SendEmailCode)
	authed.PUT("/me/email", h.BindEmail)

	token, err := auth.GenerateToken(secret, time.Hour, 10, "platform_admin", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body string, withJWT bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if withJWT {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		return w
	}

	// 无 JWT：401
	if w := call(http.MethodPost, "/api/me/email/code", `{"email":"a@x.com"}`, false); w.Code != http.StatusUnauthorized {
		t.Fatalf("无 JWT 应 401，得 %d", w.Code)
	}

	// /api/me 回显 email（未绑定为空串）
	w := call(http.MethodGet, "/api/me", "", true)
	if w.Code != http.StatusOK {
		t.Fatalf("me 应 200，得 %d", w.Code)
	}
	var me map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &me)
	if _, ok := me["email"]; !ok {
		t.Fatal("me 响应应含 email 字段")
	}

	// 发码（开发模式返回 dev_code）→ 绑定 → me 已更新
	w = call(http.MethodPost, "/api/me/email/code", `{"email":"New@X.com"}`, true)
	if w.Code != http.StatusOK {
		t.Fatalf("发码应 200，得 %d body=%s", w.Code, w.Body.String())
	}
	var sent map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &sent)
	devCode, _ := sent["dev_code"].(string)
	if devCode == "" {
		t.Fatal("开发模式应返回 dev_code")
	}
	w = call(http.MethodPut, "/api/me/email",
		`{"email":"new@x.com","code":"`+devCode+`"}`, true)
	if w.Code != http.StatusOK {
		t.Fatalf("绑定应 200，得 %d body=%s", w.Code, w.Body.String())
	}
	w = call(http.MethodGet, "/api/me", "", true)
	_ = json.Unmarshal(w.Body.Bytes(), &me)
	if me["email"] != "new@x.com" {
		t.Fatalf("绑定后 me.email 应为 new@x.com，得 %v", me["email"])
	}

	// 占用邮箱：发码即拒
	if w := call(http.MethodPost, "/api/me/email/code", `{"email":"taken@x.com"}`, true); w.Code != http.StatusBadRequest {
		t.Fatalf("占用邮箱发码应 400，得 %d", w.Code)
	}
}
