package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"token-gateway/internal/auth"
	"token-gateway/internal/config"
	"token-gateway/internal/database"
	"token-gateway/internal/middleware"
)

// ---------- 登录加固：按用户名限流 + 会话指纹签发 ----------

// newLoginEnv 临时库 + 真实 bcrypt 密码账号，挂 /api/auth/login 与 JWT 保护路由
func newLoginEnv(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := database.Open(config.Database{Driver: "sqlite", Path: t.TempDir() + "/login.db"})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := database.Migrate(db); err != nil {
		t.Fatalf("迁移测试库失败: %v", err)
	}
	t.Cleanup(func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() })

	hash, err := auth.HashPassword("right-pass-123")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	if err := db.Exec(`INSERT INTO users (id, org_id, username, password_hash, role, status, created_at, updated_at)
		VALUES (10, NULL, 'victim', ?, 'platform_admin', 1, ?, ?)`, hash, now, now).Error; err != nil {
		t.Fatal(err)
	}

	const secret = "test-secret"
	h := &AuthHandler{DB: db, Secret: secret, TTL: time.Hour, UserLimiter: middleware.NewRateLimiter(10, 10)}
	engine := gin.New()
	engine.POST("/api/auth/login", h.Login)
	authed := engine.Group("/api", middleware.JWTAuth(secret, db))
	authed.GET("/ping", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	return engine, db
}

func postLogin(engine *gin.Engine, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	return w
}

// 按用户名限流：不管请求来自哪个 IP，同一账号连续错密码必须被掐断
// （原缺陷：仅按 IP 限流，伪造 X-Forwarded-For 即可每请求换 IP 绕过，无限暴力猜密码）
func TestLoginPerUsernameRateLimit(t *testing.T) {
	engine, _ := newLoginEnv(t)
	rateLimited := 0
	for i := 0; i < 12; i++ {
		w := postLogin(engine, `{"username":"victim","password":"wrong-pass!"}`)
		if w.Code == http.StatusTooManyRequests {
			rateLimited++
		}
	}
	if rateLimited == 0 {
		t.Fatal("同一账号连续错密码 12 次应触发按用户名限流 429")
	}
	// 限流后正确密码也被挡（掐断的是账号维度的尝试，不是响应内容）
	if w := postLogin(engine, `{"username":"victim","password":"right-pass-123"}`); w.Code != http.StatusTooManyRequests {
		t.Fatalf("限流生效期间正确密码也应 429，got %d", w.Code)
	}
	// 其他账号不受牵连
	w := postLogin(engine, `{"username":"other","password":"whatever!"}`)
	if w.Code == http.StatusTooManyRequests {
		t.Fatal("按用户名限流不应牵连其他账号")
	}
}

// 登录签发的 JWT 带会话指纹：改密码后该会话即时失效
func TestLoginIssuesSessionPinnedToken(t *testing.T) {
	engine, db := newLoginEnv(t)
	w := postLogin(engine, `{"username":"victim","password":"right-pass-123"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("正确密码应 200，got %d: %s", w.Code, w.Body.String())
	}
	token := w.Body.String()
	// 提取 token 字段（简单子串即可避免引入 JSON 解析样板）
	const marker = `"token":"`
	i := strings.Index(token, marker)
	if i < 0 {
		t.Fatal("登录响应缺 token")
	}
	token = token[i+len(marker):]
	if j := strings.Index(token, `"`); j > 0 {
		token = token[:j]
	}

	ping := func(tok string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/ping", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		pw := httptest.NewRecorder()
		engine.ServeHTTP(pw, req)
		return pw.Code
	}
	if code := ping(token); code != http.StatusOK {
		t.Fatalf("登录 token 应可用，got %d", code)
	}
	// 改密码（模拟重置/自助修改后的哈希变更）→ 旧会话即时 401
	newHash, err := auth.HashPassword("rotated-pass-456")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("UPDATE users SET password_hash = ? WHERE id = 10", newHash).Error; err != nil {
		t.Fatal(err)
	}
	if code := ping(token); code != http.StatusUnauthorized {
		t.Fatalf("改密码后旧登录会话应 401，got %d", code)
	}
}
