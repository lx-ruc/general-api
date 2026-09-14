package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"token-gateway/internal/auth"
	"token-gateway/internal/httpx"
	"token-gateway/internal/middleware"
	"token-gateway/internal/model"
)

// AccessTokenHandler 管理面访问令牌 CRUD（/api/me/tokens）。
// 仅平台/客户管理员可建（供程序化对接管理 API：开户/授权/查用量等）；
// 令牌权限 = 属主权限（RBAC / org 隔离与网页登录完全一致），吊销即时生效
type AccessTokenHandler struct {
	DB *gorm.DB
}

// List GET /api/me/tokens：当前用户自己的令牌列表（含已吊销，便于审计）
func (h *AccessTokenHandler) List(c *gin.Context) {
	var rows []model.AccessToken
	if err := h.DB.Where("user_id = ?", middleware.GetUID(c)).
		Order("id DESC").Find(&rows).Error; err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "查询令牌失败")
		return
	}
	httpx.OK(c, rows)
}

// Create POST /api/me/tokens：body {name, expires_days?}（0/缺省=永不过期）
// 明文令牌仅本次响应返回一次
func (h *AccessTokenHandler) Create(c *gin.Context) {
	var req struct {
		Name        string `json:"name" binding:"required,max=64"`
		ExpiresDays int64  `json:"expires_days"`
	}
	if !httpx.BindJSON(c, &req) {
		return
	}
	plain, prefix, hash, err := auth.GenerateAccessToken()
	if err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "生成令牌失败")
		return
	}
	now := time.Now().Unix()
	tok := model.AccessToken{
		UserID: middleware.GetUID(c), Name: req.Name,
		TokenHash: hash, Prefix: prefix, Status: 1,
	}
	if req.ExpiresDays > 0 {
		tok.ExpiresAt = now + req.ExpiresDays*86400
	}
	if err := h.DB.Create(&tok).Error; err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "保存令牌失败")
		return
	}
	httpx.OK(c, gin.H{"token": plain, "id": tok.ID, "prefix": prefix,
		"expires_at": tok.ExpiresAt})
}

// Revoke DELETE /api/me/tokens/:id：软删除（status=0），鉴权立即 401
func (h *AccessTokenHandler) Revoke(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		httpx.Fail(c, http.StatusBadRequest, "无效的令牌 ID")
		return
	}
	res := h.DB.Exec(
		"UPDATE access_tokens SET status = 0, updated_at = ? WHERE id = ? AND user_id = ?",
		time.Now().Unix(), id, middleware.GetUID(c))
	if res.Error != nil || res.RowsAffected == 0 {
		httpx.Fail(c, http.StatusNotFound, "令牌不存在")
		return
	}
	httpx.OK(c, gin.H{"message": "令牌已吊销"})
}
