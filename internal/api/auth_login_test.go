package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"token-gateway/internal/auth"
	"token-gateway/internal/config"
	"token-gateway/internal/database"
	"token-gateway/internal/service"
)

// 登录与组织状态：手动停用（0）拒绝；欠费停服（2）允许登录管理台（数据面另行拦截）
func TestLoginOrgArrearsAllowed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := database.Open(config.Database{Driver: "sqlite", Path: t.TempDir() + "/test.db"})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := database.Migrate(db); err != nil {
		t.Fatalf("迁移测试库失败: %v", err)
	}
	t.Cleanup(func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() })

	hash, _ := auth.HashPassword("pass123456")
	now := time.Now().Unix()
	if err := db.Exec(`INSERT INTO orgs (id, name, quota_limit, status, created_at, updated_at)
		VALUES (1, 'o', 1000000, 2, ?, ?)`, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO users (id, org_id, username, password_hash, role, status, created_at, updated_at)
		VALUES (5, 1, 'oa', ?, 'org_admin', 1, ?, ?)`, hash, now, now).Error; err != nil {
		t.Fatal(err)
	}

	h := &AuthHandler{DB: db, Secret: "test-secret", TTL: time.Hour,
		Verif: service.NewVerification(db, &config.Smtp{})}
	engine := gin.New()
	engine.POST("/api/auth/login", h.Login)

	post := func() int {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login",
			strings.NewReader(`{"username":"oa","password":"pass123456"}`))
		req.Header.Set("Content-Type", "application/json")
		engine.ServeHTTP(w, req)
		return w.Code
	}

	if code := post(); code != http.StatusOK {
		t.Fatalf("欠费停服（status=2）组织管理员应能登录管理台，got %d", code)
	}
	if err := db.Exec("UPDATE orgs SET status = 0 WHERE id = 1").Error; err != nil {
		t.Fatal(err)
	}
	if code := post(); code != http.StatusForbidden {
		t.Fatalf("手动停用（status=0）组织应拒绝登录，got %d", code)
	}
}
