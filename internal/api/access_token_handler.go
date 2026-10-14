package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"token-gateway/internal/auth"
	"token-gateway/internal/crypto"
	"token-gateway/internal/httpx"
	"token-gateway/internal/middleware"
	"token-gateway/internal/model"
	"token-gateway/internal/service"
)

// AccessTokenHandler 管理面访问令牌 CRUD（/api/me/tokens）。
// 仅平台/客户管理员可建（供程序化对接管理 API：开户/授权/查用量等）；
// 令牌权限 = 属主权限（RBAC / org 隔离与网页登录完全一致），吊销即时生效
type AccessTokenHandler struct {
	DB     *gorm.DB
	Cipher *crypto.Cipher // 完整令牌 AES-GCM 存档（复制完整令牌用；鉴权仍走哈希）
}

// List GET /api/me/tokens：当前用户自己的令牌列表（含已吊销，便于审计）
func (h *AccessTokenHandler) List(c *gin.Context) {
	var rows []model.AccessToken
	if err := h.DB.Where("user_id = ?", middleware.GetUID(c)).
		Order("id DESC").Find(&rows).Error; err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "查询令牌失败")
		return
	}
	list := make([]gin.H, 0, len(rows))
	for _, t := range rows {
		list = append(list, gin.H{
			"id": t.ID, "name": t.Name, "prefix": t.Prefix,
			"token_masked": service.KeyMaskedForList(h.Cipher, t.TokenEnc, t.Prefix),
			"copyable":     t.TokenEnc != "",
			"status":       t.Status, "expires_at": t.ExpiresAt,
			"last_used_at": t.LastUsedAt, "created_at": t.CreatedAt,
		})
	}
	httpx.OK(c, list)
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
	enc, err := h.Cipher.Encrypt(plain)
	if err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "加密令牌失败")
		return
	}
	now := time.Now().Unix()
	tok := model.AccessToken{
		UserID: middleware.GetUID(c), Name: req.Name,
		TokenHash: hash, TokenEnc: enc, Prefix: prefix, Status: 1,
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

// Copy POST /api/me/tokens/:id/copy —— 复制完整令牌（仅属主）：
// 明文只进剪贴板不回显界面；存量无存档的旧令牌 400 引导重建
func (h *AccessTokenHandler) Copy(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		httpx.Fail(c, http.StatusBadRequest, "无效的令牌 ID")
		return
	}
	service.CopyKeyPayload(c, h.DB, h.Cipher, "令牌", middleware.GetUID(c),
		"SELECT id, token_enc AS enc FROM access_tokens WHERE id = ? AND user_id = ?",
		id, middleware.GetUID(c))
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
