package api

import (
	"log/slog"
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
	Verif  *service.Verification
}

// SendCode POST /api/auth/send-code：向邮箱发送注册验证码
func (h *AuthHandler) SendCode(c *gin.Context) {
	var req struct {
		Email string `json:"email" binding:"required"`
	}
	if !httpx.BindJSON(c, &req) {
		return
	}
	devCode, retryAfter, err := h.Verif.SendCode(req.Email)
	if err != nil {
		status := http.StatusBadRequest
		if retryAfter > 0 {
			status = http.StatusTooManyRequests
		}
		c.JSON(status, gin.H{"error": gin.H{"message": err.Error(), "retry_after": retryAfter}})
		return
	}
	resp := gin.H{"message": "验证码已发送，请查收邮箱（注意垃圾邮件箱）"}
	if devCode != "" {
		// SMTP 未配置（开发模式）：直接返回验证码，方便本地联调
		resp["dev_code"] = devCode
		resp["message"] = "SMTP 未配置：验证码以开发模式返回"
	}
	httpx.OK(c, resp)
}

// Register POST /api/auth/register：公司自助注册（公司名+邮箱验证码+账号+密码）
func (h *AuthHandler) Register(c *gin.Context) {
	var req struct {
		OrgName  string `json:"org_name" binding:"required"`
		Email    string `json:"email" binding:"required"`
		Code     string `json:"code" binding:"required"`
		Username string `json:"username" binding:"required,min=3"`
		Password string `json:"password" binding:"required,min=6"`
	}
	if !httpx.BindJSON(c, &req) {
		return
	}
	if err := h.Verif.RegisterCompany(h.DB, req.OrgName, req.Email, req.Code, req.Username, req.Password); err != nil {
		httpx.Fail(c, http.StatusBadRequest, err.Error())
		return
	}
	slog.Info("公司自助注册", "org", req.OrgName, "email", req.Email, "admin", req.Username)
	httpx.OK(c, gin.H{
		"message": "注册成功，请登录。公司初始额度为 0，请联系平台管理员分配额度后再调用 API",
		"org_name": req.OrgName, "username": req.Username,
	})
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
