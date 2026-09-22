package platform

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"token-gateway/internal/auth"
	"token-gateway/internal/middleware"
)

// newDemoEnv 脚手架：临时库 + 系统管理员 + 挂 demo-key 三条路由
func newDemoEnv(t *testing.T) (*gin.Engine, *gorm.DB, string) {
	t.Helper()
	engine, db, token := newPlatformEnv(t)
	h := NewHandler(db, nil, nil, nil)
	g := engine.Group("/api/platform", middleware.JWTAuth("test-secret", db))
	g.GET("/demo-key", h.GetDemoKey)
	g.PUT("/demo-key", h.ConfigureDemoKey)
	g.POST("/demo-key/rotate", h.RotateDemoKey)
	return engine, db, token
}

// demoCall 发请求拿 JSON body
func demoCall(t *testing.T, engine *gin.Engine, token, method, url, body string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("[%s %s] 响应非 JSON（%d）: %s", method, url, w.Code, w.Body.String())
	}
	if w.Code != http.StatusOK {
		t.Fatalf("[%s %s] 应 200，得 %d: %v", method, url, w.Code, out)
	}
	return out
}

func qInt64(t *testing.T, db *gorm.DB, sql string, args ...any) int64 {
	t.Helper()
	var v int64
	if err := db.Raw(sql, args...).Scan(&v).Error; err != nil {
		t.Fatal(err)
	}
	return v
}

// 首次 GET 即幂等开通体验账号；重复 GET 不重复建
func TestDemoKeyProvisionIdempotent(t *testing.T) {
	engine, db, token := newDemoEnv(t)

	r1 := demoCall(t, engine, token, http.MethodGet, "/api/platform/demo-key", "")
	if r1["provisioned"] != false {
		t.Fatalf("初始应未开通，得 %v", r1["provisioned"])
	}

	var orgID, userID int64
	_ = db.Raw("SELECT id FROM orgs WHERE name = '在线体验'").Scan(&orgID).Error
	_ = db.Raw("SELECT id FROM users WHERE username = 'online_demo'").Scan(&userID).Error
	if orgID == 0 || userID == 0 {
		t.Fatalf("应自动开通体验客户/账号，org=%d user=%d", orgID, userID)
	}
	// 子账号个人不限额（NULL）+ 挂对客户 + role=member
	var lim *int64
	var owner int64
	var role string
	_ = db.Raw("SELECT quota_limit FROM users WHERE id = ?", userID).Scan(&lim).Error
	_ = db.Raw("SELECT COALESCE(org_id,0) FROM users WHERE id = ?", userID).Scan(&owner).Error
	_ = db.Raw("SELECT role FROM users WHERE id = ?", userID).Scan(&role).Error
	if lim != nil {
		t.Fatalf("体验子账号应个人不限额（NULL），得 %v", *lim)
	}
	if owner != orgID || role != "member" {
		t.Fatalf("体验子账号归属/角色错误: owner=%d role=%s", owner, role)
	}
	// 默认告警阈值列默认生效（不因 GORM 空串压掉）
	var levels string
	_ = db.Raw("SELECT alert_levels FROM users WHERE id = ?", userID).Scan(&levels).Error
	if levels != "[80]" {
		t.Fatalf("alert_levels 应走列默认 [80]，得 %q", levels)
	}

	// 再 GET：锚点复用，不重复建
	_ = demoCall(t, engine, token, http.MethodGet, "/api/platform/demo-key", "")
	if n := qInt64(t, db, "SELECT COUNT(*) FROM orgs WHERE name = '在线体验'"); n != 1 {
		t.Fatalf("客户应恰 1 个，得 %d", n)
	}
	if n := qInt64(t, db, "SELECT COUNT(*) FROM users WHERE username = 'online_demo'"); n != 1 {
		t.Fatalf("子账号应恰 1 个，得 %d", n)
	}
}

// 轮换：建真实 key（sk- 前缀 + 哈希落库 + 明文进 settings 可反复查看）；旧 key 全部吊销
func TestDemoKeyRotateAndRevoke(t *testing.T) {
	engine, db, token := newDemoEnv(t)

	r1 := demoCall(t, engine, token, http.MethodPost, "/api/platform/demo-key/rotate", "")
	oldKey, _ := r1["key"].(string)
	if !strings.HasPrefix(oldKey, "sk-") {
		t.Fatalf("key 应 sk- 前缀，得 %q", oldKey)
	}
	oldHash := auth.HashAPIKey(oldKey)
	var oldStatus int
	_ = db.Raw("SELECT status FROM api_keys WHERE key_hash = ?", oldHash).Scan(&oldStatus).Error
	if oldStatus != 1 {
		t.Fatalf("新 key 应启用，得 %d", oldStatus)
	}
	var uid int64
	_ = db.Raw("SELECT CAST(value AS INTEGER) FROM settings WHERE key = 'demo.user_id'").Scan(&uid).Error
	if uid == 0 {
		t.Fatal("settings 缺 demo.user_id 锚点")
	}

	// GET 可见明文（演示密钥可反复查看）
	r2 := demoCall(t, engine, token, http.MethodGet, "/api/platform/demo-key", "")
	if r2["provisioned"] != true || r2["key"] != oldKey {
		t.Fatalf("GET 应回显密钥，得 provisioned=%v key=%v", r2["provisioned"], r2["key"])
	}

	// 再轮换：旧 key 即时吊销（status=0），库里共 2 行
	r3 := demoCall(t, engine, token, http.MethodPost, "/api/platform/demo-key/rotate", "")
	newKey, _ := r3["key"].(string)
	if newKey == oldKey {
		t.Fatal("两次轮换不应产生相同 key")
	}
	_ = db.Raw("SELECT status FROM api_keys WHERE key_hash = ?", oldHash).Scan(&oldStatus).Error
	if oldStatus != 0 {
		t.Fatalf("旧 key 应已吊销，得 %d", oldStatus)
	}
	if n := qInt64(t, db, "SELECT COUNT(*) FROM api_keys WHERE user_id = ?", uid); n != 2 {
		t.Fatalf("体验账号应有 2 把 key，得 %d", n)
	}
	if n := qInt64(t, db, "SELECT COUNT(*) FROM api_keys WHERE user_id = ? AND status = 1", uid); n != 1 {
		t.Fatalf("应恰 1 把启用，得 %d", n)
	}
}

// 配置：额度走 AddOrgQuota（Σgrants 不变量）、模型授权全量替换、有效期/启停生效
func TestDemoKeyConfigure(t *testing.T) {
	engine, db, token := newDemoEnv(t)
	now := time.Now().Unix()
	for _, m := range []string{"glm-4", "deepseek-chat"} {
		if err := db.Exec(`INSERT INTO models (name, status, created_at, updated_at)
			VALUES (?, 1, ?, ?)`, m, now, now).Error; err != nil {
			t.Fatal(err)
		}
	}
	_ = demoCall(t, engine, token, http.MethodPost, "/api/platform/demo-key/rotate", "")

	expire := now + 86400
	r := demoCall(t, engine, token, http.MethodPut, "/api/platform/demo-key",
		`{"quota_points":1000000,"models":["glm-4","deepseek-chat"],"expires_at":`+
			strconv.FormatInt(expire, 10)+`,"enabled":true}`)
	if r["provisioned"] != true {
		t.Fatalf("保存后回显应 provisioned=true，得 %v", r["provisioned"])
	}

	var orgID int64
	_ = db.Raw("SELECT id FROM orgs WHERE name = '在线体验'").Scan(&orgID).Error
	var uid int64
	_ = db.Raw("SELECT CAST(value AS INTEGER) FROM settings WHERE key = 'demo.user_id'").Scan(&uid).Error

	// 额度：limit=1M 且 Σgrants == quota_limit（差值入流水）
	if lim := qInt64(t, db, "SELECT quota_limit FROM orgs WHERE id = ?", orgID); lim != 1_000_000 {
		t.Fatalf("体验额度应 1,000,000，得 %d", lim)
	}
	if sum := qInt64(t, db, "SELECT COALESCE(SUM(amount),0) FROM quota_grants WHERE subject_type='org' AND subject_id=?",
		orgID); sum != 1_000_000 {
		t.Fatalf("Σgrants 应 == quota_limit，得 %d", sum)
	}

	// 追加到 1.5M：差值 500k 入流水，不变量保持
	_ = demoCall(t, engine, token, http.MethodPut, "/api/platform/demo-key", `{"quota_points":1500000}`)
	if lim := qInt64(t, db, "SELECT quota_limit FROM orgs WHERE id = ?", orgID); lim != 1_500_000 {
		t.Fatalf("体验额度应 1,500,000，得 %d", lim)
	}
	if sum := qInt64(t, db, "SELECT COALESCE(SUM(amount),0) FROM quota_grants WHERE subject_type='org' AND subject_id=?",
		orgID); sum != 1_500_000 {
		t.Fatalf("追加后 Σgrants 应 == quota_limit，得 %d", sum)
	}

	// 模型授权：全量替换 + 授权人落 operator
	if n := qInt64(t, db, "SELECT COUNT(*) FROM user_model_grants WHERE user_id = ?", uid); n != 2 {
		t.Fatalf("应授权 2 个模型，得 %d", n)
	}
	var grantor *int64
	_ = db.Raw("SELECT granted_by FROM user_model_grants WHERE user_id = ? AND model_name='glm-4'", uid).Scan(&grantor).Error
	if grantor == nil || *grantor != 1 {
		t.Fatalf("授权人应为系统管理员(1)，得 %v", grantor)
	}

	// 有效期落库
	var exp *int64
	var keyID int64
	_ = db.Raw("SELECT CAST(value AS INTEGER) FROM settings WHERE key = 'demo.key_id'").Scan(&keyID).Error
	_ = db.Raw("SELECT expired_at FROM api_keys WHERE id = ?", keyID).Scan(&exp).Error
	if exp == nil || *exp != expire {
		t.Fatalf("expires_at 应 %d，得 %v", expire, exp)
	}

	// expires_at=0 → 永久（NULL）
	_ = demoCall(t, engine, token, http.MethodPut, "/api/platform/demo-key", `{"expires_at":0}`)
	_ = db.Raw("SELECT expired_at FROM api_keys WHERE id = ?", keyID).Scan(&exp).Error
	if exp != nil {
		t.Fatalf("expires_at=0 应清空为永久，得 %v", *exp)
	}

	// 停用 → status=0；再启用 → 1
	_ = demoCall(t, engine, token, http.MethodPut, "/api/platform/demo-key", `{"enabled":false}`)
	if st := qInt64(t, db, "SELECT status FROM api_keys WHERE id = ?", keyID); st != 0 {
		t.Fatalf("停用后 status 应 0，得 %d", st)
	}
	_ = demoCall(t, engine, token, http.MethodPut, "/api/platform/demo-key", `{"enabled":true}`)
	if st := qInt64(t, db, "SELECT status FROM api_keys WHERE id = ?", keyID); st != 1 {
		t.Fatalf("启用后 status 应 1，得 %d", st)
	}
}

// 轮换继承有效期：有效期是持久配置，不应随轮换静默丢失变永久（应急轮换恰恰最需要保留期限）
func TestDemoKeyRotatePreservesExpiry(t *testing.T) {
	engine, db, token := newDemoEnv(t)
	_ = demoCall(t, engine, token, http.MethodPost, "/api/platform/demo-key/rotate", "")

	expire := time.Now().Unix() + 86400
	r := demoCall(t, engine, token, http.MethodPut, "/api/platform/demo-key",
		`{"expires_at":`+strconv.FormatInt(expire, 10)+`}`)
	if got, _ := r["expires_at"].(float64); got != float64(expire) {
		t.Fatalf("保存后回显 expires_at 应 %d，得 %v", expire, got)
	}

	// 轮换后新 key 必须继承同一有效期
	_ = demoCall(t, engine, token, http.MethodPost, "/api/platform/demo-key/rotate", "")
	r2 := demoCall(t, engine, token, http.MethodGet, "/api/platform/demo-key", "")
	if got, _ := r2["expires_at"].(float64); got != float64(expire) {
		t.Fatalf("轮换后 expires_at 应保持 %d，得 %v（有效期被重置为永久）", expire, got)
	}
	var keyID int64
	_ = db.Raw("SELECT CAST(value AS INTEGER) FROM settings WHERE key = 'demo.key_id'").Scan(&keyID).Error
	var exp *int64
	_ = db.Raw("SELECT expired_at FROM api_keys WHERE id = ?", keyID).Scan(&exp).Error
	if exp == nil || *exp != expire {
		t.Fatalf("新 key 落库 expired_at 应 %d，得 %v", expire, exp)
	}
}

// 非法配置：负额度 / 额度低于已耗 / 模型不存在——400 且不落库
func TestDemoKeyConfigureInvalid(t *testing.T) {
	engine, db, token := newDemoEnv(t)
	_ = demoCall(t, engine, token, http.MethodPost, "/api/platform/demo-key/rotate", "")
	var orgID int64
	_ = db.Raw("SELECT id FROM orgs WHERE name = '在线体验'").Scan(&orgID).Error
	var keyID int64
	_ = db.Raw("SELECT CAST(value AS INTEGER) FROM settings WHERE key = 'demo.key_id'").Scan(&keyID).Error

	bad := func(body, why string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPut, "/api/platform/demo-key", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("[%s] 应 400，得 %d: %s", why, w.Code, w.Body.String())
		}
	}
	bad(`{"quota_points":-5}`, "负额度")
	// 已耗 600 时收到 500 以下
	_ = db.Exec("UPDATE orgs SET quota_limit = 1000, quota_used = 600 WHERE id = ?", orgID).Error
	bad(`{"quota_points":500}`, "额度低于已耗")
	bad(`{"models":["no-such-model"]}`, "模型不存在")
	bad(`{"expires_at":-1}`, "负有效期")

	// 拒绝后无副作用：limit 未被改写、无授权、有效期未动
	if lim := qInt64(t, db, "SELECT quota_limit FROM orgs WHERE id = ?", orgID); lim != 1000 {
		t.Fatalf("拒绝后 limit 不应变，得 %d", lim)
	}
	var exp *int64
	_ = db.Raw("SELECT expired_at FROM api_keys WHERE id = ?", keyID).Scan(&exp).Error
	if exp != nil {
		t.Fatalf("拒绝后有效期不应被设置，得 %v", *exp)
	}
}

// 未登录不可访问（管理面 RBAC 兜底）
func TestDemoKeyRequiresAuth(t *testing.T) {
	engine, _, _ := newDemoEnv(t)
	req := httptest.NewRequest(http.MethodGet, "/api/platform/demo-key", nil)
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("未登录应 401，得 %d", w.Code)
	}
}
