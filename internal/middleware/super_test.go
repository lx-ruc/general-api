package middleware

import (
	"encoding/json"
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

// 超管分层测试环境：user 1=超级管理员 / user 2=子管理员（同 role=platform_admin，
// 仅 is_super 不同）；/super 路由挂 RequireSuperAdmin，/whoami 回显 IsSuperAdmin
func newSuperEnv(t *testing.T) (*gin.Engine, *gorm.DB, string, string) {
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
	for _, u := range []struct {
		id    int64
		name  string
		super int
	}{{1, "boss", 1}, {2, "deputy", 0}} {
		if err := db.Exec(`INSERT INTO users (id, username, password_hash, role, is_super, status, created_at, updated_at)
			VALUES (?, ?, 'x', 'platform_admin', ?, 1, ?, ?)`, u.id, u.name, u.super, now, now).Error; err != nil {
			t.Fatalf("造管理员失败: %v", err)
		}
	}

	const secret = "test-secret"
	superTok, err := auth.GenerateToken(secret, time.Hour, 1, "platform_admin", nil, "")
	if err != nil {
		t.Fatalf("生成超管 token 失败: %v", err)
	}
	subTok, err := auth.GenerateToken(secret, time.Hour, 2, "platform_admin", nil, "")
	if err != nil {
		t.Fatalf("生成子管 token 失败: %v", err)
	}

	engine := gin.New()
	grp := engine.Group("/api", JWTAuth(secret, db))
	grp.GET("/whoami", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"is_super": IsSuperAdmin(c)})
	})
	grp.GET("/super", RequireSuperAdmin(), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	return engine, db, superTok, subTok
}

func superGet(engine *gin.Engine, path, token string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	engine.ServeHTTP(w, req)
	return w
}

func whoamiIsSuper(engine *gin.Engine, token string) bool {
	w := superGet(engine, "/api/whoami", token)
	if w.Code != http.StatusOK {
		return false
	}
	var resp struct {
		IsSuper bool `json:"is_super"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return resp.IsSuper
}

// 超管放行、子管理员 403（whoami 佐证 JWT 轨 SELECT 均带出 is_super）
func TestRequireSuperAdminGate(t *testing.T) {
	engine, _, superTok, subTok := newSuperEnv(t)
	if !whoamiIsSuper(engine, superTok) {
		t.Fatal("超管 whoami 应回显 is_super=true")
	}
	if whoamiIsSuper(engine, subTok) {
		t.Fatal("子管理员 whoami 应回显 is_super=false")
	}
	if w := superGet(engine, "/api/super", superTok); w.Code != http.StatusOK {
		t.Fatalf("超管应放行超管专属路由，got %d: %s", w.Code, w.Body.String())
	}
	if w := superGet(engine, "/api/super", subTok); w.Code != http.StatusForbidden {
		t.Fatalf("子管理员应 403，got %d", w.Code)
	}
}

// 库内 is_super 即时生效：降级后同一 token 立刻 403（与角色/状态同机制）
func TestSuperFlagTakesEffectImmediately(t *testing.T) {
	engine, db, superTok, _ := newSuperEnv(t)
	if w := superGet(engine, "/api/super", superTok); w.Code != http.StatusOK {
		t.Fatalf("降级前应放行，got %d", w.Code)
	}
	if err := db.Exec("UPDATE users SET is_super = 0 WHERE id = 1").Error; err != nil {
		t.Fatal(err)
	}
	if w := superGet(engine, "/api/super", superTok); w.Code != http.StatusForbidden {
		t.Fatalf("降级后同一 token 应立即 403，got %d", w.Code)
	}
}

// tgp_ 访问令牌轨同样受限：子管理员的令牌进不了超管专属路由；改挂超管属主即放行
func TestAccessTokenSuperGate(t *testing.T) {
	engine, db, _, _ := newSuperEnv(t)
	plain, _, hash, err := auth.GenerateAccessToken()
	if err != nil {
		t.Fatalf("生成令牌失败: %v", err)
	}
	now := time.Now().Unix()
	if err := db.Exec(`INSERT INTO access_tokens (user_id, name, token_hash, prefix, status, expires_at, created_at, updated_at)
		VALUES (2, 'CI', ?, 'tgp_test…', 1, 0, ?, ?)`, hash, now, now).Error; err != nil {
		t.Fatalf("造令牌失败: %v", err)
	}
	if w := superGet(engine, "/api/super", plain); w.Code != http.StatusForbidden {
		t.Fatalf("子管理员的 tgp_ 令牌应 403，got %d", w.Code)
	}
	if err := db.Exec("UPDATE access_tokens SET user_id = 1").Error; err != nil {
		t.Fatal(err)
	}
	if w := superGet(engine, "/api/super", plain); w.Code != http.StatusOK {
		t.Fatalf("改挂超管属主后应放行，got %d", w.Code)
	}
}
