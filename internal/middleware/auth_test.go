package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"token-gateway/internal/auth"
	"token-gateway/internal/config"
	"token-gateway/internal/database"
)

// JWT 回库校验：禁用/删除账号、停用组织后，存量 token 必须即时失效（状态即时生效）
func newAuthEnv(t *testing.T) (*gin.Engine, *gorm.DB, string) {
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
	if err := db.Exec(`INSERT INTO orgs (id, name, quota_limit, status, created_at, updated_at)
		VALUES (1, 'o', 1000000, 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatalf("造 org 失败: %v", err)
	}
	if err := db.Exec(`INSERT INTO users (id, org_id, username, password_hash, role, status, created_at, updated_at)
		VALUES (10, 1, 'm', 'x', 'member', 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatalf("造 user 失败: %v", err)
	}

	const secret = "test-secret"
	oid := int64(1)
	token, err := auth.GenerateToken(secret, time.Hour, 10, "member", &oid)
	if err != nil {
		t.Fatalf("生成 token 失败: %v", err)
	}

	engine := gin.New()
	grp := engine.Group("/api", JWTAuth(secret, db))
	grp.GET("/ping", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	return engine, db, token
}

func getWithToken(engine *gin.Engine, token string) int {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/ping", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	engine.ServeHTTP(w, req)
	return w.Code
}

func TestJWTUserDisabledImmediate(t *testing.T) {
	engine, db, token := newAuthEnv(t)
	if code := getWithToken(engine, token); code != http.StatusOK {
		t.Fatalf("正常账号应 200，got %d", code)
	}
	if err := db.Exec("UPDATE users SET status = 0 WHERE id = 10").Error; err != nil {
		t.Fatal(err)
	}
	if code := getWithToken(engine, token); code != http.StatusUnauthorized {
		t.Fatalf("禁用后存量 token 应 401，got %d", code)
	}
}

func TestJWTUserDeletedImmediate(t *testing.T) {
	engine, db, token := newAuthEnv(t)
	if err := db.Exec("DELETE FROM users WHERE id = 10").Error; err != nil {
		t.Fatal(err)
	}
	if code := getWithToken(engine, token); code != http.StatusUnauthorized {
		t.Fatalf("删除账号后存量 token 应 401，got %d", code)
	}
}

func TestJWTOrgDisabledImmediate(t *testing.T) {
	engine, db, token := newAuthEnv(t)
	if err := db.Exec("UPDATE orgs SET status = 0 WHERE id = 1").Error; err != nil {
		t.Fatal(err)
	}
	if code := getWithToken(engine, token); code != http.StatusUnauthorized {
		t.Fatalf("组织停用后存量 token 应 401，got %d", code)
	}
}

// 欠费停服（status=2）不拦管理台：只拦数据面
func TestJWTOrgArrearsStillAllowed(t *testing.T) {
	engine, db, token := newAuthEnv(t)
	if err := db.Exec("UPDATE orgs SET status = 2 WHERE id = 1").Error; err != nil {
		t.Fatal(err)
	}
	if code := getWithToken(engine, token); code != http.StatusOK {
		t.Fatalf("欠费停服组织的管理台 token 应仍有效，got %d", code)
	}
}

// 库内角色以库为准：token 里是 member，库内升为 org_admin 后按新角色鉴权
func TestJWTRoleFromDB(t *testing.T) {
	engine, db, token := newAuthEnv(t)
	if err := db.Exec("UPDATE users SET role = 'org_admin' WHERE id = 10").Error; err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/ping", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("应 200，got %d", w.Code)
	}
	// 角色 contexts 由中间件注入，这里仅验证请求通过；RequireRole 的分支由各角色 handler 测试覆盖
}
