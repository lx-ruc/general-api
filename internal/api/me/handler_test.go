package me

import (
	"encoding/json"
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

type meEnv struct {
	engine *gin.Engine
	db     *gorm.DB
	token  string // uid=1（org_admin）
}

// newMeEnv 临时库 + 两个账号（1=org_admin、2=member），挂三角色通用的通知路由
func newMeEnv(t *testing.T) *meEnv {
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
		VALUES (1, 1, 'admin', 'x', 'org_admin', 1, ?, ?), (2, 1, 'm1', 'x', 'member', 1, ?, ?)`,
		now, now, now, now).Error; err != nil {
		t.Fatalf("造用户失败: %v", err)
	}

	const secret = "test-secret"
	orgID := int64(1)
	token, err := auth.GenerateToken(secret, time.Hour, 1, "org_admin", &orgID, "")
	if err != nil {
		t.Fatalf("生成 token 失败: %v", err)
	}

	engine := gin.New()
	g := engine.Group("/api/me", middleware.JWTAuth(secret, db))
	h := NewHandler(db)
	g.GET("/notifications", h.ListNotifications)
	g.PUT("/notifications/:id/read", h.ReadNotification)
	g.PUT("/notifications/read-all", h.ReadAllNotifications)
	return &meEnv{engine: engine, db: db, token: token}
}

func (e *meEnv) do(method, path, body string) *httptest.ResponseRecorder {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	req.Header.Set("Authorization", "Bearer "+e.token)
	w := httptest.NewRecorder()
	e.engine.ServeHTTP(w, req)
	return w
}

// TestNotificationsUserIsolation 通知按 JWT uid 严格隔离：只看到自己的，
// 且不能把别人的通知标已读（越权行回 404、对方行保持未读）
func TestNotificationsUserIsolation(t *testing.T) {
	e := newMeEnv(t)
	now := time.Now().Unix()
	if err := e.db.Exec(`INSERT INTO notifications (user_id, type, title, body, read_at, created_at)
		VALUES (1, 'quota_alert', '给管理员', 'a', 0, ?), (2, 'request_handled', '给子账号', 'b', 0, ?)`,
		now, now).Error; err != nil {
		t.Fatalf("造通知失败: %v", err)
	}

	// uid=1 只看到自己的 1 条，未读 1
	w := e.do(http.MethodGet, "/api/me/notifications", "")
	if w.Code != http.StatusOK {
		t.Fatalf("列表状态码 %d", w.Code)
	}
	var resp struct {
		List []struct {
			ID    int64  `json:"id"`
			Title string `json:"title"`
		} `json:"list"`
		Unread int64 `json:"unread"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(resp.List) != 1 || resp.List[0].Title != "给管理员" {
		t.Fatalf("应只看到自己的通知，实际 %+v", resp.List)
	}
	if resp.Unread != 1 {
		t.Fatalf("未读数应为 1，实际 %d", resp.Unread)
	}

	// uid=1 不能读掉 uid=2 的通知（id=2）
	if w := e.do(http.MethodPut, "/api/me/notifications/2/read", ""); w.Code != http.StatusNotFound {
		t.Fatalf("读他人通知应 404，实际 %d", w.Code)
	}
	var readAt int64
	_ = e.db.Raw("SELECT read_at FROM notifications WHERE id = 2").Scan(&readAt).Error
	if readAt != 0 {
		t.Fatalf("他人通知不应被标已读")
	}

	// 全部已读只影响自己
	if w := e.do(http.MethodPut, "/api/me/notifications/read-all", ""); w.Code != http.StatusOK {
		t.Fatalf("read-all 状态码 %d", w.Code)
	}
	var cnt int64
	_ = e.db.Raw("SELECT COUNT(*) FROM notifications WHERE user_id = 2 AND read_at = 0").Scan(&cnt).Error
	if cnt != 1 {
		t.Fatalf("read-all 不应影响他人未读数，剩余 %d", cnt)
	}
}
