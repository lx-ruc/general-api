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
)

// 复用 newAuthEnv 的库（user 10 = member / org 1），注册一个回显身份的路由，
// 造一把 tgp_ 访问令牌 → 验证双轨鉴权与 JWT 完全等价（RBAC / org 隔离）
func newTokenEnv(t *testing.T) (*gin.Engine, *gorm.DB, string) {
	t.Helper()
	engine, db, _ := newAuthEnv(t)

	plain, _, hash, err := auth.GenerateAccessToken()
	if err != nil {
		t.Fatalf("生成令牌失败: %v", err)
	}
	now := time.Now().Unix()
	if err := db.Exec(`INSERT INTO access_tokens (user_id, name, token_hash, prefix, status, expires_at, created_at, updated_at)
		VALUES (10, 'CI', ?, 'tgp_test…', 1, 0, ?, ?)`, hash, now, now).Error; err != nil {
		t.Fatalf("造令牌失败: %v", err)
	}

	engine.GET("/api/whoami", JWTAuth("test-secret", db), func(c *gin.Context) {
		orgID := int64(0)
		if org := GetOrgID(c); org != nil {
			orgID = *org
		}
		c.JSON(http.StatusOK, gin.H{"uid": GetUID(c), "role": GetRole(c), "org_id": orgID})
	})
	return engine, db, plain
}

func getWithTokenRaw(engine *gin.Engine, path, token string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	engine.ServeHTTP(w, req)
	return w
}

// 令牌轨身份 = 属主身份：uid/role/org 与 JWT 轨一致
func TestAccessTokenAuthEquivalence(t *testing.T) {
	engine, _, token := newTokenEnv(t)
	w := getWithTokenRaw(engine, "/api/whoami", token)
	if w.Code != http.StatusOK {
		t.Fatalf("有效令牌应 200，got %d", w.Code)
	}
	var resp struct {
		UID   int64  `json:"uid"`
		Role  string `json:"role"`
		OrgID int64  `json:"org_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.UID != 10 || resp.Role != "member" || resp.OrgID != 1 {
		t.Fatalf("令牌身份应等于属主身份，got %+v", resp)
	}
}

// 吊销（status=0）→ 即时 401
func TestAccessTokenRevoked(t *testing.T) {
	engine, db, token := newTokenEnv(t)
	if err := db.Exec("UPDATE access_tokens SET status = 0").Error; err != nil {
		t.Fatal(err)
	}
	if w := getWithTokenRaw(engine, "/api/whoami", token); w.Code != http.StatusUnauthorized {
		t.Fatalf("吊销后应 401，got %d", w.Code)
	}
}

// 过期（expires_at < now）→ 401
func TestAccessTokenExpired(t *testing.T) {
	engine, db, token := newTokenEnv(t)
	if err := db.Exec("UPDATE access_tokens SET expires_at = ?", time.Now().Unix()-1).Error; err != nil {
		t.Fatal(err)
	}
	if w := getWithTokenRaw(engine, "/api/whoami", token); w.Code != http.StatusUnauthorized {
		t.Fatalf("过期后应 401，got %d", w.Code)
	}
}

// last_used_at 节流更新：首次请求写入，60s 内重复请求不写
func TestAccessTokenLastUsedThrottle(t *testing.T) {
	engine, db, token := newTokenEnv(t)
	_ = getWithTokenRaw(engine, "/api/whoami", token)

	var first int64
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_ = db.Raw("SELECT last_used_at FROM access_tokens").Scan(&first).Error
		if first > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if first == 0 {
		t.Fatal("首次使用后 last_used_at 应被写入")
	}
	// 60s 窗口内第二笔：不应更新（仍是 first）
	_ = getWithTokenRaw(engine, "/api/whoami", token)
	time.Sleep(200 * time.Millisecond)
	var second int64
	_ = db.Raw("SELECT last_used_at FROM access_tokens").Scan(&second).Error
	if second != first {
		t.Fatalf("60s 内重复请求不应更新 last_used_at：%d → %d", first, second)
	}
}

// 令牌轨同样受 RequireRole 管控：member 令牌访问管理员路由 403；
// 库内升角色后同令牌立即放行（角色以库为准）
func TestAccessTokenRequireRoleGate(t *testing.T) {
	engine, db, token := newTokenEnv(t)
	adminOnly := engine.Group("/api/admin", JWTAuth("test-secret", db), RequireRole("org_admin"))
	adminOnly.GET("/x", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })

	if w := getWithTokenRaw(engine, "/api/admin/x", token); w.Code != http.StatusForbidden {
		t.Fatalf("member 令牌访问管理员路由应 403，got %d", w.Code)
	}
	if err := db.Exec("UPDATE users SET role = 'org_admin' WHERE id = 10").Error; err != nil {
		t.Fatal(err)
	}
	if w := getWithTokenRaw(engine, "/api/admin/x", token); w.Code != http.StatusOK {
		t.Fatalf("库内升级后同令牌应放行，got %d", w.Code)
	}
}
