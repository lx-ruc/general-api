package platform

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
	"token-gateway/internal/crypto"
	"token-gateway/internal/database"
	"token-gateway/internal/middleware"
)

// newNotifEnv 挂三个站内通知端点 + 两个系统管理员（uid 1/2），返回双份 token
func newNotifEnv(t *testing.T) (*gin.Engine, *gorm.DB, string, string) {
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
	for _, uid := range []int64{1, 2} {
		if err := db.Exec(`INSERT INTO users (id, org_id, username, password_hash, role, status, created_at, updated_at)
			VALUES (?, NULL, ?, 'x', 'platform_admin', 1, ?, ?)`, uid, "root"+string(rune('0'+uid)), now, now).Error; err != nil {
			t.Fatalf("造系统管理员 %d 失败: %v", uid, err)
		}
	}
	const secret = "test-secret"
	tok := func(uid int64) string {
		token, err := auth.GenerateToken(secret, time.Hour, uid, "platform_admin", nil, "")
		if err != nil {
			t.Fatalf("生成 token 失败: %v", err)
		}
		return token
	}
	engine := gin.New()
	g := engine.Group("/api/platform", middleware.JWTAuth(secret, db))
	cipher, _ := crypto.NewCipher("")
	h := NewHandler(db, cipher, &http.Client{}, nil, nil)
	g.GET("/notifications", h.ListNotifications)
	g.PUT("/notifications/:id/read", h.ReadNotification)
	g.PUT("/notifications/read-all", h.ReadAllNotifications)
	return engine, db, tok(1), tok(2)
}

func notifReq(engine *gin.Engine, token, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	return w
}

// 列表按人隔离、未读数正确；单条已读与全部已读生效；他人通知不可读
func TestNotificationsListReadFlow(t *testing.T) {
	engine, db, t1, t2 := newNotifEnv(t)
	now := time.Now().Unix()
	seed := []struct {
		id     int64
		userID int64
		readAt int64
	}{
		{1, 1, 0}, {2, 1, 0}, {3, 1, now}, {4, 2, 0},
	}
	for _, s := range seed {
		if err := db.Exec(`INSERT INTO notifications (id, user_id, type, title, body, payload, read_at, created_at)
			VALUES (?, ?, 'key_quota_cooling', 't', 'b', '{}', ?, ?)`, s.id, s.userID, s.readAt, now).Error; err != nil {
			t.Fatal(err)
		}
	}

	// 管理员 1：只看到自己的 3 条（倒序）、未读 2
	w := notifReq(engine, t1, http.MethodGet, "/api/platform/notifications")
	if w.Code != http.StatusOK {
		t.Fatalf("列表应 200，得 %d: %s", w.Code, w.Body.String())
	}
	var out struct {
		List []struct {
			ID     int64 `json:"id"`
			ReadAt int64 `json:"read_at"`
		} `json:"list"`
		Unread int64 `json:"unread"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.List) != 3 || out.Unread != 2 || out.List[0].ID != 3 {
		t.Fatalf("应 3 条倒序 + 未读 2，got len=%d unread=%d first=%d", len(out.List), out.Unread, out.List[0].ID)
	}

	// 管理员 2 读管理员 1 的通知 → 404（跨人不可操作）
	if w := notifReq(engine, t2, http.MethodPut, "/api/platform/notifications/1/read"); w.Code != http.StatusNotFound {
		t.Fatalf("他人通知应 404，得 %d", w.Code)
	}
	// 本人单条已读 → 未读减一；重复读 → 404
	if w := notifReq(engine, t1, http.MethodPut, "/api/platform/notifications/1/read"); w.Code != http.StatusOK {
		t.Fatalf("标已读应 200，得 %d: %s", w.Code, w.Body.String())
	}
	if w := notifReq(engine, t1, http.MethodPut, "/api/platform/notifications/1/read"); w.Code != http.StatusNotFound {
		t.Fatalf("重复标已读应 404，得 %d", w.Code)
	}
	var unread int64
	_ = db.Raw("SELECT COUNT(*) FROM notifications WHERE user_id = 1 AND read_at = 0").Scan(&unread).Error
	if unread != 1 {
		t.Fatalf("单条已读后应剩 1 未读，got %d", unread)
	}
	// 全部已读只影响本人：管理员 1 清零，管理员 2 仍 1 条未读
	if w := notifReq(engine, t1, http.MethodPut, "/api/platform/notifications/read-all"); w.Code != http.StatusOK {
		t.Fatalf("全部已读应 200，得 %d", w.Code)
	}
	_ = db.Raw("SELECT COUNT(*) FROM notifications WHERE user_id = 1 AND read_at = 0").Scan(&unread).Error
	if unread != 0 {
		t.Fatalf("管理员 1 应全部已读，got %d", unread)
	}
	_ = db.Raw("SELECT COUNT(*) FROM notifications WHERE user_id = 2 AND read_at = 0").Scan(&unread).Error
	if unread != 1 {
		t.Fatalf("全部已读不应影响管理员 2，got %d", unread)
	}
}

// 超过保留期（30 天）的通知在列表查询时被顺手清理
func TestNotificationsPruneExpired(t *testing.T) {
	engine, db, t1, _ := newNotifEnv(t)
	now := time.Now().Unix()
	if err := db.Exec(`INSERT INTO notifications (id, user_id, type, title, body, payload, read_at, created_at)
		VALUES (1, 1, 'key_quota_cooling', 'old', 'b', '{}', 0, ?), (2, 1, 'key_quota_cooling', 'new', 'b', '{}', 0, ?)`,
		now-31*86400, now).Error; err != nil {
		t.Fatal(err)
	}
	w := notifReq(engine, t1, http.MethodGet, "/api/platform/notifications")
	if w.Code != http.StatusOK {
		t.Fatalf("列表应 200，得 %d", w.Code)
	}
	var cnt int64
	_ = db.Raw("SELECT COUNT(*) FROM notifications WHERE user_id = 1").Scan(&cnt).Error
	if cnt != 1 {
		t.Fatalf("31 天前的通知应被清理，剩 1 条，got %d", cnt)
	}
}
