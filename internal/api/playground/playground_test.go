package playground

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"token-gateway/internal/auth"
	"token-gateway/internal/config"
	"token-gateway/internal/coord"
	"token-gateway/internal/crypto"
	"token-gateway/internal/database"
	"token-gateway/internal/gateway"
	"token-gateway/internal/metrics"
	"token-gateway/internal/middleware"
)

// ---------------- 测试底座 ----------------

const jwtSecret = "test-secret"

type pgEnv struct {
	db     *gorm.DB
	gw     *gateway.Handler
	engine *gin.Engine
	up     *httptest.Server
}

const pgOKBody = `{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`

func mustExec(t *testing.T, db *gorm.DB, sql string, args ...any) {
	t.Helper()
	if err := db.Exec(sql, args...).Error; err != nil {
		t.Fatalf("exec 失败: %v\n%s", err, sql)
	}
}

// newPGEnv：org1（org_admin=u1，member=u2 只授权 m1，member=u4 只授权 m2）+ 系统管理员 u3；
// 模型 m1/m2 可路由（渠道 ch1→mock 上游），m3 启用但无渠道，m4 已授权但无渠道。
func newPGEnv(t *testing.T) *pgEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := database.Open(config.Database{Driver: "sqlite", Path: t.TempDir() + "/pg.db"})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := database.Migrate(db); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() })

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(pgOKBody))
	}))
	t.Cleanup(up.Close)

	now := time.Now().Unix()
	mustExec(t, db, `INSERT INTO orgs (id, name, quota_limit, quota_used, status, created_at, updated_at)
		VALUES (1, 't-org', 100000000, 0, 1, ?, ?)`, now, now)
	mustExec(t, db, `INSERT INTO users (id, org_id, username, password_hash, role, status, created_at, updated_at) VALUES
		(1, 1,    'boss',  'x', 'org_admin',      1, ?, ?),
		(2, 1,    'staff', 'x', 'member',         1, ?, ?),
		(3, NULL, 'root',  'x', 'platform_admin', 1, ?, ?),
		(4, 1,    'staff2','x', 'member',         1, ?, ?)`, now, now, now, now, now, now, now, now)
	mustExec(t, db, `INSERT INTO models (name, input_price, output_price, status, created_at, updated_at) VALUES
		('m1', 2000000, 8000000, 1, ?, ?),
		('m2', 2000000, 8000000, 1, ?, ?),
		('m3', 2000000, 8000000, 1, ?, ?),
		('m4', 2000000, 8000000, 1, ?, ?)`, now, now, now, now, now, now, now, now)
	mustExec(t, db, `INSERT INTO channels (id, name, base_url, path, upstream_key_enc, weight, priority, status, created_at, updated_at)
		VALUES (1, 'ch1', ?, '/v1/chat/completions', 'legacy-key', 1, 1, 1, ?, ?)`, up.URL, now, now)
	mustExec(t, db, `INSERT INTO channel_abilities (channel_id, model_name) VALUES (1, 'm1'), (1, 'm2')`)
	mustExec(t, db, `INSERT INTO user_model_grants (user_id, model_name, created_at) VALUES
		(2, 'm1', ?), (2, 'm4', ?), (4, 'm2', ?)`, now, now, now)

	cfg := &config.Config{}
	cfg.Gateway = config.Gateway{
		MaxBodyMB:                10,
		PerKeyRPM:                10000,
		UpstreamFirstByteTimeout: config.Duration{Duration: 10 * time.Second},
		ChannelBreakerThreshold:  0,
		QueueWaitTimeout:         config.Duration{Duration: 2 * time.Second},
	}
	cipher, _ := crypto.NewCipher("")
	gw := gateway.NewHandler(db, cipher, cfg, middleware.NewRateLimiter(10000, 20), metrics.New(), coord.NewMem(1000))

	engine := gin.New()
	grp := engine.Group("/api", middleware.JWTAuth(jwtSecret, db))
	pg := NewHandler(db, gw)
	grp.GET("/playground/models", pg.Models)
	grp.POST("/playground/chat", pg.Chat)
	return &pgEnv{db: db, gw: gw, engine: engine, up: up}
}

func (e *pgEnv) token(t *testing.T, uid int64, role string, orgID *int64) string {
	t.Helper()
	tok, err := auth.GenerateToken(jwtSecret, time.Hour, uid, role, orgID)
	if err != nil {
		t.Fatalf("签发 JWT 失败: %v", err)
	}
	return tok
}

func i64(v int64) *int64 { return &v }

func (e *pgEnv) get(t *testing.T, token, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	e.engine.ServeHTTP(w, req)
	return w
}

func (e *pgEnv) chat(t *testing.T, token, model string, extra string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/playground/chat",
		strings.NewReader(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}]%s}`, model, extra)))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.engine.ServeHTTP(w, req)
	return w
}

func quotaUsed(t *testing.T, db *gorm.DB, table string, id int64) int64 {
	t.Helper()
	var v int64
	if err := db.Raw("SELECT quota_used FROM "+table+" WHERE id = ?", id).Scan(&v).Error; err != nil {
		t.Fatalf("读 %s.quota_used 失败: %v", table, err)
	}
	return v
}

// ---------------- 模型列表按角色收敛 ----------------

func TestModelsByRole(t *testing.T) {
	e := newPGEnv(t)

	// 子账号：个人白名单（m1、m4）∩ 可路由（m1、m2）= m1
	w := e.get(t, e.token(t, 2, "member", i64(1)), "/api/playground/models")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"m1"`) || strings.Contains(w.Body.String(), `"m2"`) {
		t.Fatalf("member 列表应仅 m1，得 %d: %s", w.Code, w.Body.String())
	}
	// 客户管理员：客户授权并集 {m1,m2,m4} ∩ 可路由 = m1、m2
	w = e.get(t, e.token(t, 1, "org_admin", i64(1)), "/api/playground/models")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"m1"`) || !strings.Contains(w.Body.String(), `"m2"`) {
		t.Fatalf("org_admin 列表应含 m1、m2，得 %d: %s", w.Code, w.Body.String())
	}
	// 系统管理员：全部可路由模型（含没人授权的），不含无渠道的 m3/m4
	w = e.get(t, e.token(t, 3, "platform_admin", nil), "/api/playground/models")
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, `"m1"`) || !strings.Contains(body, `"m2"`) ||
		strings.Contains(body, `"m3"`) || strings.Contains(body, `"m4"`) {
		t.Fatalf("platform 列表应为 m1、m2，得 %d: %s", w.Code, body)
	}
}

// ---------------- 授权与计费语义 ----------------

// 子账号体验 = 真实计费：额度双记账（子账号 + 客户），usage_logs 归属 KeyID=0（在线体验）
func TestMemberChatBilled(t *testing.T) {
	e := newPGEnv(t)
	w := e.chat(t, e.token(t, 2, "member", i64(1)), "m1", "")
	if w.Code != http.StatusOK {
		t.Fatalf("member 对话应 200，得 %d: %s", w.Code, w.Body.String())
	}
	// m1 单价 入2/出8 点每 token：cost = 7×2 + 3×8 = 38
	if got := quotaUsed(t, e.db, "users", 2); got != 38 {
		t.Fatalf("子账号 quota_used 应 38，得 %d", got)
	}
	if got := quotaUsed(t, e.db, "orgs", 1); got != 38 {
		t.Fatalf("客户 quota_used 应 38，得 %d", got)
	}
	var rec struct {
		Cost     int64
		UserID   int64
		OrgID    int64
		APIKeyID int64
	}
	if err := e.db.Raw("SELECT cost, user_id, org_id, api_key_id FROM usage_logs ORDER BY id DESC LIMIT 1").Scan(&rec).Error; err != nil {
		t.Fatalf("读 usage_logs 失败: %v", err)
	}
	if rec.Cost != 38 || rec.UserID != 2 || rec.OrgID != 1 || rec.APIKeyID != 0 {
		t.Fatalf("usage_logs 应 cost=38 user=2 org=1 key=0，得 %+v", rec)
	}
}

// 系统管理员体验不产生计费（与渠道测试一致），usage_logs 记 org=0、cost=0
func TestPlatformAdminNotBilled(t *testing.T) {
	e := newPGEnv(t)
	w := e.chat(t, e.token(t, 3, "platform_admin", nil), "m1", "")
	if w.Code != http.StatusOK {
		t.Fatalf("platform_admin 对话应 200，得 %d: %s", w.Code, w.Body.String())
	}
	if got := quotaUsed(t, e.db, "users", 3); got != 0 {
		t.Fatalf("platform_admin 不应扣额度，得 %d", got)
	}
	if got := quotaUsed(t, e.db, "orgs", 1); got != 0 {
		t.Fatalf("客户不应被扣，得 %d", got)
	}
	var rec struct{ Cost, UserID, OrgID int64 }
	if err := e.db.Raw("SELECT cost, user_id, org_id FROM usage_logs ORDER BY id DESC LIMIT 1").Scan(&rec).Error; err != nil {
		t.Fatalf("读 usage_logs 失败: %v", err)
	}
	if rec.Cost != 0 || rec.UserID != 0 || rec.OrgID != 0 {
		t.Fatalf("usage_logs 应 cost=0 user=0 org=0，得 %+v", rec)
	}
}

// 客户管理员按客户授权并集体验（m2 授权给子账号 u4，不属于管理员本人）
func TestOrgAdminUnionAuth(t *testing.T) {
	e := newPGEnv(t)
	w := e.chat(t, e.token(t, 1, "org_admin", i64(1)), "m2", "")
	if w.Code != http.StatusOK {
		t.Fatalf("org_admin 应可用客户并集内的 m2，得 %d: %s", w.Code, w.Body.String())
	}
	if got := quotaUsed(t, e.db, "users", 1); got != 38 {
		t.Fatalf("org_admin 应真实计费 38，得 %d", got)
	}
}

// 越权与不可路由模型被拒：未授权的 m2、已授权但无渠道的 m4 都 403
func TestUnauthorizedModels(t *testing.T) {
	e := newPGEnv(t)
	tok := e.token(t, 2, "member", i64(1))
	for _, m := range []string{"m2", "m3", "m4"} {
		if w := e.chat(t, tok, m, ""); w.Code != http.StatusForbidden {
			t.Fatalf("member 调 %s 应 403，得 %d: %s", m, w.Code, w.Body.String())
		}
	}
}

// 欠费停服（org status=2）在管理面入口即被拦，消息与数据面口径一致
func TestArrearsOrgBlocked(t *testing.T) {
	e := newPGEnv(t)
	mustExec(t, e.db, `UPDATE orgs SET status = 2 WHERE id = 1`)
	w := e.chat(t, e.token(t, 2, "member", i64(1)), "m1", "")
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "arrears") {
		t.Fatalf("欠费客户应 403 arrears，得 %d: %s", w.Code, w.Body.String())
	}
}

// 子账号额度耗尽：复用数据面预检语义（429 insufficient_balance）
func TestMemberQuotaExhausted(t *testing.T) {
	e := newPGEnv(t)
	mustExec(t, e.db, `UPDATE users SET quota_limit = 10, quota_used = 10 WHERE id = 2`)
	w := e.chat(t, e.token(t, 2, "member", i64(1)), "m1", "")
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "insufficient_balance") {
		t.Fatalf("额度耗尽应 429 insufficient_balance，得 %d: %s", w.Code, w.Body.String())
	}
}

// 流式请求经 /api/playground/chat 原样 SSE 透传，末块 usage 参与结算
func TestStreamingPassthrough(t *testing.T) {
	e := newPGEnv(t)
	e.up.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"he\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"llo\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	})

	w := e.chat(t, e.token(t, 2, "member", i64(1)), "m1", `,"stream":true`)
	if w.Code != http.StatusOK {
		t.Fatalf("流式对话应 200，得 %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"he"`) || !strings.Contains(w.Body.String(), "[DONE]") {
		t.Fatalf("SSE 应逐块透传，得 %s", w.Body.String())
	}
	// usage 5×2 + 2×8 = 26
	if got := quotaUsed(t, e.db, "users", 2); got != 26 {
		t.Fatalf("流式结算应 26，得 %d", got)
	}
}
