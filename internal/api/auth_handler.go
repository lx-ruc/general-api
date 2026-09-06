package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"token-gateway/internal/auth"
	"token-gateway/internal/httpx"
	"token-gateway/internal/middleware"
	"token-gateway/internal/model"
	"token-gateway/internal/service"
)

type AuthHandler struct {
	DB     *gorm.DB
	Secret string
	TTL    time.Duration
}

// Login POST /api/auth/login
func (h *AuthHandler) Login(c *gin.Context) {
	var req struct {
		Username string `json:"username" binding:"required"`
		Password string `json:"password" binding:"required"`
	}
	if !httpx.BindJSON(c, &req) {
		return
	}
	var u model.User
	if err := h.DB.Where("username = ?", req.Username).First(&u).Error; err != nil || u.ID == 0 {
		httpx.Fail(c, http.StatusUnauthorized, "用户名或密码错误")
		return
	}
	if !auth.CheckPassword(u.PasswordHash, req.Password) {
		httpx.Fail(c, http.StatusUnauthorized, "用户名或密码错误")
		return
	}
	if u.Status != 1 {
		httpx.Fail(c, http.StatusForbidden, "账号已被停用")
		return
	}
	if u.OrgID != nil {
		var orgStatus int
		if err := h.DB.Raw("SELECT status FROM orgs WHERE id = ?", *u.OrgID).Scan(&orgStatus).Error; err != nil || orgStatus != 1 {
			httpx.Fail(c, http.StatusForbidden, "所属组织已被停用")
			return
		}
	}
	token, err := auth.GenerateToken(h.Secret, h.TTL, u.ID, u.Role, u.OrgID)
	if err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "签发令牌失败")
		return
	}
	now := time.Now().Unix()
	_ = h.DB.Exec("UPDATE users SET last_login_at = ? WHERE id = ?", now, u.ID).Error
	httpx.OK(c, gin.H{
		"token":      token,
		"expires_in": int(h.TTL.Seconds()),
		"user":       userInfo(h.DB, &u),
	})
}

// Me GET /api/me
func (h *AuthHandler) Me(c *gin.Context) {
	var u model.User
	if err := h.DB.Where("id = ?", middleware.GetUID(c)).First(&u).Error; err != nil || u.ID == 0 {
		httpx.Fail(c, http.StatusUnauthorized, "用户不存在")
		return
	}
	resp := userInfo(h.DB, &u)
	resp["points_per_yuan"] = service.PointsPerYuan(h.DB)
	httpx.OK(c, resp)
}

// ChangePassword PUT /api/me/password
func (h *AuthHandler) ChangePassword(c *gin.Context) {
	var req struct {
		OldPassword string `json:"old_password" binding:"required"`
		NewPassword string `json:"new_password" binding:"required,min=6"`
	}
	if !httpx.BindJSON(c, &req) {
		return
	}
	uid := middleware.GetUID(c)
	var u model.User
	if err := h.DB.Where("id = ?", uid).First(&u).Error; err != nil || u.ID == 0 {
		httpx.Fail(c, http.StatusUnauthorized, "用户不存在")
		return
	}
	if !auth.CheckPassword(u.PasswordHash, req.OldPassword) {
		httpx.Fail(c, http.StatusBadRequest, "原密码错误")
		return
	}
	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "密码加密失败")
		return
	}
	if err := h.DB.Exec("UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?",
		hash, time.Now().Unix(), uid).Error; err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "更新密码失败")
		return
	}
	httpx.OK(c, gin.H{"message": "密码已更新"})
}

func userInfo(db *gorm.DB, u *model.User) gin.H {
	resp := gin.H{
		"id": u.ID, "username": u.Username, "display_name": u.DisplayName,
		"role": u.Role, "org_id": u.OrgID, "status": u.Status,
		"quota_limit": u.QuotaLimit, "quota_used": u.QuotaUsed,
		"last_login_at": u.LastLoginAt,
	}
	if u.OrgID != nil {
		var name string
		_ = db.Raw("SELECT name FROM orgs WHERE id = ?", *u.OrgID).Scan(&name).Error
		resp["org_name"] = name
	}
	return resp
}
