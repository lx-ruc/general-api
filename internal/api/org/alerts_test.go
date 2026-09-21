package org

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
	"token-gateway/internal/model"
)

type alertEnv struct {
	engine *gin.Engine
	db     *gorm.DB
	token  string
}

// newAlertEnv 只挂预警路由的最小环境（复用 newOrgEnv 的造数方式）
func newAlertEnv(t *testing.T) *alertEnv {
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
	oid := int64(1)
	if err := db.Exec(`INSERT INTO orgs (id, name, quota_limit, status, created_at, updated_at)
		VALUES (1, 'o', 100000000, 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatalf("造 org 失败: %v", err)
	}
	if err := db.Exec(`INSERT INTO users (id, org_id, username, password_hash, role, status, created_at, updated_at)
		VALUES (2, 1, 'admin', 'x', 'org_admin', 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatalf("造管理员失败: %v", err)
	}
	token, err := auth.GenerateToken("test-secret", time.Hour, 2, "org_admin", &oid, "")
	if err != nil {
		t.Fatalf("生成 token 失败: %v", err)
	}
	engine := gin.New()
	og := engine.Group("/api/org", middleware.JWTAuth("test-secret", db))
	h := NewHandler(db)
	og.GET("/alert-levels", h.GetAlertLevels)
	og.PUT("/alert-levels", h.UpdateAlertLevels)
	return &alertEnv{engine: engine, db: db, token: token}
}

func (e *alertEnv) do(method, body string) *httptest.ResponseRecorder {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, "/api/org/alert-levels", nil)
	} else {
		req = httptest.NewRequest(method, "/api/org/alert-levels", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+e.token)
	w := httptest.NewRecorder()
	e.engine.ServeHTTP(w, req)
	return w
}

// 新客户默认 80% 预警（schema 列默认 '[80]'）；老数据 alert_levels 为空串时
// 不是错误：应 200 + threshold 0（关闭），而不是 404"客户不存在"——
// 对账单页一打开就打这个接口
func TestGetAlertLevelsUnconfigured(t *testing.T) {
	e := newAlertEnv(t)
	w := e.do(http.MethodGet, "")
	if w.Code != http.StatusOK {
		t.Fatalf("查询预警应 200，got %d body=%s", w.Code, w.Body)
	}
	for _, want := range []string{`"threshold":80`, `"quota_limit":100000000`} {
		if !strings.Contains(w.Body.String(), want) {
			t.Fatalf("新客户响应应含 %s（列默认 [80]），got %s", want, w.Body)
		}
	}
	// 遗留空串（曾走 GORM 零值写入压掉列默认）：200 + 0，绝不 404
	if err := e.db.Exec("UPDATE orgs SET alert_levels = '' WHERE id = 1").Error; err != nil {
		t.Fatalf("改空串失败: %v", err)
	}
	w = e.do(http.MethodGet, "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"threshold":0`) {
		t.Fatalf("空串应 200 + threshold 0，got %d %s", w.Code, w.Body)
	}
}

// 配置回程：PUT 70 → GET 回 70；PUT 0（关闭）→ GET 回 0；非法值 400
func TestAlertLevelsRoundTrip(t *testing.T) {
	e := newAlertEnv(t)
	for _, step := range []struct{ put, want string }{
		{`{"threshold":70}`, `"threshold":70`},
		{`{"threshold":0}`, `"threshold":0`},
	} {
		if w := e.do(http.MethodPut, step.put); w.Code != http.StatusOK {
			t.Fatalf("PUT %s 应 200，got %d body=%s", step.put, w.Code, w.Body)
		}
		g := e.do(http.MethodGet, "")
		if g.Code != http.StatusOK || !strings.Contains(g.Body.String(), step.want) {
			t.Fatalf("PUT %s 后 GET 应含 %s，got %d %s", step.put, step.want, g.Code, g.Body)
		}
	}
	if w := e.do(http.MethodPut, `{"threshold":101}`); w.Code != http.StatusBadRequest {
		t.Fatalf("threshold 101 应 400，got %d", w.Code)
	}
}

// GORM Create 零值不得压掉列默认 '[80]'（org/user 同理）——曾因无 default tag
// 写入空串，导致新建客户/子账号默认 80% 预警静默失效
func TestAlertLevelsColumnDefault(t *testing.T) {
	e := newAlertEnv(t)
	if err := e.db.Create(&model.Org{Name: "xo", Status: 1}).Error; err != nil {
		t.Fatalf("建 org 失败: %v", err)
	}
	if err := e.db.Create(&model.User{Username: "u1", Role: model.RoleMember, Status: 1}).Error; err != nil {
		t.Fatalf("建 user 失败: %v", err)
	}
	var orgAL, userAL string
	_ = e.db.Raw("SELECT alert_levels FROM orgs ORDER BY id DESC LIMIT 1").Scan(&orgAL).Error
	_ = e.db.Raw("SELECT alert_levels FROM users ORDER BY id DESC LIMIT 1").Scan(&userAL).Error
	if orgAL != "[80]" || userAL != "[80]" {
		t.Fatalf("列默认应 '[80]'，got org=%q user=%q", orgAL, userAL)
	}
}
