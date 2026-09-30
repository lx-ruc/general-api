package me

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"token-gateway/internal/httpx"
	"token-gateway/internal/middleware"
	"token-gateway/internal/model"
)

// 三角色通用的「我的」端点：站内通知按 JWT uid 严格隔离（各自的角色能看到的
// 通知由写入侧的扇出决定，读取侧不需要任何角色判断）。

// notificationsKeepDays 已读/过期通知的保留期：列表查询时顺手清理，防表无限增长
const notificationsKeepDays = 30

type Handler struct {
	DB *gorm.DB
}

func NewHandler(db *gorm.DB) *Handler {
	return &Handler{DB: db}
}

// ListNotifications GET /api/me/notifications：当前登录账号的站内通知（最近 50 条）+ 未读数。
// 顶栏铃铛轮询用（三角色共用）；同时清理保留期外的旧通知（含未读——超期未看的告警已失效）
func (h *Handler) ListNotifications(c *gin.Context) {
	uid := middleware.GetUID(c)
	cutoff := time.Now().Unix() - notificationsKeepDays*86400
	// 旧通知清理属尽力而为（失败不阻塞列表）
	_ = h.DB.Exec("DELETE FROM notifications WHERE user_id = ? AND created_at < ?", uid, cutoff).Error
	var rows []model.Notification
	if err := h.DB.Where("user_id = ?", uid).Order("id DESC").Limit(50).Find(&rows).Error; err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "通知加载失败")
		return
	}
	if rows == nil {
		rows = []model.Notification{}
	}
	var unread int64
	if err := h.DB.Raw("SELECT COUNT(*) FROM notifications WHERE user_id = ? AND read_at = 0", uid).Scan(&unread).Error; err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "通知加载失败")
		return
	}
	httpx.OK(c, gin.H{"list": rows, "unread": unread})
}

// ReadNotification PUT /api/me/notifications/:id/read
func (h *Handler) ReadNotification(c *gin.Context) {
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	res := h.DB.Exec("UPDATE notifications SET read_at = ? WHERE id = ? AND user_id = ? AND read_at = 0",
		time.Now().Unix(), id, middleware.GetUID(c))
	if res.Error != nil || res.RowsAffected == 0 {
		httpx.Fail(c, http.StatusNotFound, "通知不存在或已读")
		return
	}
	httpx.OK(c, gin.H{"message": "已读"})
}

// ReadAllNotifications PUT /api/me/notifications/read-all
func (h *Handler) ReadAllNotifications(c *gin.Context) {
	_ = h.DB.Exec("UPDATE notifications SET read_at = ? WHERE user_id = ? AND read_at = 0",
		time.Now().Unix(), middleware.GetUID(c)).Error
	httpx.OK(c, gin.H{"message": "已全部标记已读"})
}
