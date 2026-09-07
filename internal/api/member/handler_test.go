package member

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
	"token-gateway/internal/config"
	"token-gateway/internal/database"
	"token-gateway/internal/middleware"
)

type memberEnv struct {
	engine *gin.Engine
	db     *gorm.DB
	token  string
}

// newMemberEnv 临时库 + org/member 账号 + 挂 JWT 鉴权的 CreateKey 路由
func newMemberEnv(t *testing.T) *memberEnv {
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
	orgID := int64(1)
	if err := db.Exec(`INSERT INTO orgs (id, name, quota_limit, status, created_at, updated_at)
		VALUES (1, 'o', 1000000, 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatalf("造 org 失败: %v", err)
	}
	if err := db.Exec(`INSERT INTO users (id, org_id, username, password_hash, role, status, created_at, updated_at)
		VALUES (1, 1, 'm1', 'x', 'member', 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatalf("造 user 失败: %v", err)
	}

	const secret = "test-secret"
	token, err := auth.GenerateToken(secret, time.Hour, 1, "member", &orgID)
	if err != nil {
		t.Fatalf("生成 token 失败: %v", err)
	}

	engine := gin.New()
	api := engine.Group("/api/member", middleware.JWTAuth(secret))
	h := NewHandler(db)
	api.POST("/keys", h.CreateKey)
	api.PUT("/keys/:id/cost-center", h.AssignKeyCenter)
	api.GET("/cost-centers", h.ListCostCenters)
	return &memberEnv{engine: engine, db: db, token: token}
}

func (e *memberEnv) postCreate(body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/member/keys", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+e.token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.engine.ServeHTTP(w, req)
	return w
}

func TestCreateKeyExpiresAt(t *testing.T) {
	e := newMemberEnv(t)

	// 过去时刻 → 400
	past := time.Now().Add(-time.Hour).Unix()
	if w := e.postCreate(`{"name":"a","expires_at":` + strconv.FormatInt(past, 10) + `}`); w.Code != http.StatusBadRequest {
		t.Fatalf("过去时刻应 400，得 %d: %s", w.Code, w.Body.String())
	}

	// 缺省 → 永久（expired_at NULL），响应回显 null
	w := e.postCreate(`{"name":"b"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("缺省应 200，得 %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		ExpiresAt *int64 `json:"expires_at"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.ExpiresAt != nil {
		t.Fatalf("缺省应永久，得 %v", *resp.ExpiresAt)
	}
	var cnt int64
	_ = e.db.Raw(`SELECT COUNT(*) FROM api_keys WHERE name='b' AND expired_at IS NULL`).Scan(&cnt).Error
	if cnt != 1 {
		t.Fatalf("永久密钥应落库 expired_at NULL，cnt=%d", cnt)
	}

	// 合法未来时刻 → 落库且响应回显
	future := time.Now().Add(48 * time.Hour).Unix()
	w = e.postCreate(`{"name":"c","expires_at":` + strconv.FormatInt(future, 10) + `}`)
	if w.Code != http.StatusOK {
		t.Fatalf("未来时刻应 200，得 %d: %s", w.Code, w.Body.String())
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.ExpiresAt == nil || *resp.ExpiresAt != future {
		t.Fatalf("响应应回显 expires_at=%d，得 %v", future, resp.ExpiresAt)
	}
	_ = e.db.Raw(`SELECT COUNT(*) FROM api_keys WHERE name='c' AND expired_at = ?`, future).Scan(&cnt).Error
	if cnt != 1 {
		t.Fatalf("过期时刻应落库，cnt=%d", cnt)
	}
}

// 归集：require 开关拒绝未选中心；无效中心拒绝；合法中心写入 + 自助改派
func TestCreateKeyCostCenter(t *testing.T) {
	e := newMemberEnv(t)
	now := time.Now().Unix()
	// 本 org 启用中心 1、归档中心 2；他 org 中心 99
	if err := e.db.Exec(`INSERT INTO cost_centers (id, org_id, name, status, created_at, updated_at)
		VALUES (1, 1, 'AI客服', 1, ?, ?), (2, 1, '旧项目', 0, ?, ?)`, now, now, now, now).Error; err != nil {
		t.Fatal(err)
	}

	// 默认（未开 require）：不选中心可建
	if w := e.postCreate(`{"name":"free"}`); w.Code != http.StatusOK {
		t.Fatalf("未开开关不选中心应 200，得 %d: %s", w.Code, w.Body.String())
	}

	// 开启 require：不选 → 400
	if err := e.db.Exec(`UPDATE orgs SET require_cost_center = 1`).Error; err != nil {
		t.Fatal(err)
	}
	if w := e.postCreate(`{"name":"must"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("开启开关不选中心应 400，得 %d", w.Code)
	}
	// 选归档中心 → 400
	if w := e.postCreate(`{"name":"must2","cost_center_id":2}`); w.Code != http.StatusBadRequest {
		t.Fatalf("归档中心应 400，得 %d", w.Code)
	}
	// 选他 org 中心 → 400
	if w := e.postCreate(`{"name":"must3","cost_center_id":99}`); w.Code != http.StatusBadRequest {
		t.Fatalf("他 org 中心应 400，得 %d", w.Code)
	}
	// 选合法中心 → 200 且落库
	if w := e.postCreate(`{"name":"ok","cost_center_id":1}`); w.Code != http.StatusOK {
		t.Fatalf("合法中心应 200，得 %d: %s", w.Code, w.Body.String())
	}
	var cnt int64
	_ = e.db.Raw(`SELECT COUNT(*) FROM api_keys WHERE name='ok' AND cost_center_id=1`).Scan(&cnt).Error
	if cnt != 1 {
		t.Fatalf("中心应落库，cnt=%d", cnt)
	}

	// 自助改派：自己的 key 从中心1 改到未归集（null）
	var kid int64
	_ = e.db.Raw(`SELECT id FROM api_keys WHERE name='ok'`).Scan(&kid).Error
	req := httptest.NewRequest(http.MethodPut, "/api/member/keys/"+strconv.FormatInt(kid, 10)+"/cost-center",
		strings.NewReader(`{"cost_center_id":null}`))
	req.Header.Set("Authorization", "Bearer "+e.token)
	req.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	e.engine.ServeHTTP(w2, req)
	if w2.Code != http.StatusOK {
		t.Fatalf("自助改派应 200，得 %d: %s", w2.Code, w2.Body.String())
	}
	_ = e.db.Raw(`SELECT COUNT(*) FROM api_keys WHERE id=? AND cost_center_id IS NULL`, kid).Scan(&cnt).Error
	if cnt != 1 {
		t.Fatalf("改派后应为未归集，cnt=%d", cnt)
	}
}
