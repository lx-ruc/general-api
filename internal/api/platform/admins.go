package platform

// 平台层子管理员管理（超级管理员专属，路由挂在 RequireSuperAdmin 组内）。
// 子管理员拥有 platform_admin 全部业务权限，仅本文件的账号管理与操作审计
// 查看为超管专属；管理员的一切写操作由 audit 中间件自动留痕。
//
// 防自锁约定：/admins/:id 的一切写操作禁止目标为自己（改名/停用走个人资料页，
// 改密走「修改密码」——顺带闭合「超管自重置密码绕过旧密码校验」的口子）。
// 由此可推出：停用/降级后必然至少剩操作者自己这个在岗超管；降级 UPDATE 再带
// 「启用超管数 > 1」守卫，把两超管互降的理论竞态窗口收窄到可忽略。
// 无 DELETE 端点：audit_logs.actor_id / quota_grants.operator_id /
// notifications.user_id 裸引用用户 ID，删号会让历史痕迹悬空——停用替代。

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"token-gateway/internal/auth"
	"token-gateway/internal/database"
	"token-gateway/internal/httpx"
	"token-gateway/internal/middleware"
	"token-gateway/internal/model"
	"token-gateway/internal/service"
)

const selfLockMsg = "不能对自己执行该操作：改名请用个人资料页，改密请用「修改密码」"

// ListAdmins GET /api/platform/admins：平台账号列表（超管在前）
func (h *Handler) ListAdmins(c *gin.Context) {
	page, size, offset := httpx.PageParams(c)
	cond, args := "role = 'platform_admin'", []any{}
	if q := c.Query("query"); q != "" {
		cond += " AND (username LIKE ? OR display_name LIKE ?)"
		args = append(args, "%"+q+"%", "%"+q+"%")
	}
	var total int64
	_ = h.DB.Raw(fmt.Sprintf("SELECT COUNT(*) FROM users WHERE %s", cond), args...).Scan(&total).Error
	var rows []model.User
	_ = h.DB.Raw(fmt.Sprintf(
		"SELECT * FROM users WHERE %s ORDER BY is_super DESC, id ASC LIMIT ? OFFSET ?", cond),
		append(args, size, offset)...).Scan(&rows).Error
	if rows == nil {
		rows = []model.User{}
	}
	httpx.PageResult(c, rows, total, page, size)
}

// CreateAdmin POST /api/platform/admins：创建子管理员（建号恒为子管理员，
// 超管位只能由 PUT 提升——避免「建号即超管」的误操作面）
func (h *Handler) CreateAdmin(c *gin.Context) {
	var req struct {
		Username    string `json:"username" binding:"required,min=3"`
		Password    string `json:"password" binding:"required,min=6"`
		DisplayName string `json:"display_name"`
		Email       string `json:"email"` // 邮箱（选填）：绑定后可自助找回密码、接收平台告警
	}
	if !httpx.BindJSON(c, &req) {
		return
	}
	// 邮箱三段校验（归一 + 格式 + 占用预查），与建子账号同口径
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email != "" && !service.ValidEmail(email) {
		httpx.Fail(c, http.StatusBadRequest, "邮箱格式不正确")
		return
	}
	var cnt int64
	_ = h.DB.Model(&model.User{}).Where("username = ?", req.Username).Count(&cnt).Error
	if cnt > 0 {
		httpx.Fail(c, http.StatusBadRequest, "用户名已存在")
		return
	}
	if email != "" {
		_ = h.DB.Model(&model.User{}).Where("email != '' AND email = ?", email).Count(&cnt).Error
		if cnt > 0 {
			httpx.Fail(c, http.StatusBadRequest, "用户名或邮箱已存在")
			return
		}
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "密码加密失败")
		return
	}
	m := model.User{
		Username: req.Username, PasswordHash: hash,
		DisplayName: req.DisplayName, Email: email,
		Role: model.RolePlatformAdmin, IsSuper: 0, Status: 1,
	}
	if err := h.DB.Create(&m).Error; err != nil {
		// 并发同用户名/同邮箱建号：预检查拦不住（PG READ COMMITTED 快照窗口），
		// 唯一索引兜底，映射回同款文案
		if database.IsDuplicateKey(err) {
			httpx.Fail(c, http.StatusBadRequest, "用户名或邮箱已存在")
			return
		}
		httpx.Fail(c, http.StatusInternalServerError, "创建子管理员失败: "+err.Error())
		return
	}
	httpx.OK(c, m)
}

// UpdateAdmin PUT /api/platform/admins/:id：改名/邮箱/停启用/超管位（防自锁）
func (h *Handler) UpdateAdmin(c *gin.Context) {
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	if id == middleware.GetUID(c) {
		httpx.Fail(c, http.StatusBadRequest, selfLockMsg)
		return
	}
	var req struct {
		DisplayName *string `json:"display_name"`
		Email       *string `json:"email"`
		Status      *int    `json:"status"`
		IsSuper     *bool   `json:"is_super"`
	}
	if !httpx.BindJSON(c, &req) {
		return
	}
	var target model.User
	if err := h.DB.Where("id = ? AND role = 'platform_admin'", id).First(&target).Error; err != nil || target.ID == 0 {
		httpx.Fail(c, http.StatusNotFound, "管理员不存在")
		return
	}
	if req.Status != nil && *req.Status != 0 && *req.Status != 1 {
		httpx.Fail(c, http.StatusBadRequest, "状态仅支持 0=停用 / 1=启用")
		return
	}
	email := ""
	if req.Email != nil {
		email = strings.ToLower(strings.TrimSpace(*req.Email))
		if email != "" && !service.ValidEmail(email) {
			httpx.Fail(c, http.StatusBadRequest, "邮箱格式不正确")
			return
		}
		if email != "" {
			var cnt int64
			_ = h.DB.Model(&model.User{}).
				Where("email != '' AND email = ? AND id != ?", email, id).Count(&cnt).Error
			if cnt > 0 {
				httpx.Fail(c, http.StatusBadRequest, "该邮箱已被其他账号绑定")
				return
			}
		}
	}

	now := time.Now().Unix()
	err := h.DB.Transaction(func(tx *gorm.DB) error {
		// 超管位变更：提升直接放行；降级带「启用超管数 > 1」守卫（RowsAffected==0
		// 即守卫拦截或目标非超管，统一拒绝防误判）
		if req.IsSuper != nil && *req.IsSuper != (target.IsSuper == 1) {
			if *req.IsSuper {
				if err := tx.Exec("UPDATE users SET is_super = 1, updated_at = ? WHERE id = ?", now, id).Error; err != nil {
					return err
				}
			} else {
				res := tx.Exec(`UPDATE users SET is_super = 0, updated_at = ? WHERE id = ?
					AND (SELECT COUNT(*) FROM users WHERE is_super = 1 AND status = 1) > 1`, now, id)
				if res.Error != nil {
					return res.Error
				}
				if res.RowsAffected == 0 {
					return errLastSuper
				}
			}
		}
		updates := map[string]any{}
		if req.DisplayName != nil {
			updates["display_name"] = *req.DisplayName
		}
		if req.Email != nil {
			updates["email"] = email
		}
		if req.Status != nil {
			updates["status"] = *req.Status
		}
		if len(updates) > 0 {
			updates["updated_at"] = now
			return tx.Model(&model.User{}).Where("id = ?", id).Updates(updates).Error
		}
		return nil
	})
	if err != nil {
		if err == errLastSuper {
			httpx.Fail(c, http.StatusBadRequest, "至少保留一名启用的超级管理员")
			return
		}
		if database.IsDuplicateKey(err) {
			httpx.Fail(c, http.StatusBadRequest, "该邮箱已被其他账号绑定")
			return
		}
		httpx.Fail(c, http.StatusInternalServerError, "更新失败: "+err.Error())
		return
	}
	_ = h.DB.Where("id = ?", id).First(&target).Error
	httpx.OK(c, target)
}

// errLastSuper 降级守卫拦截的哨兵错误（事务内回滚 + 外层映射 400）
var errLastSuper = fmt.Errorf("last enabled super admin")

// ResetAdminPassword POST /api/platform/admins/:id/reset-password
func (h *Handler) ResetAdminPassword(c *gin.Context) {
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	if id == middleware.GetUID(c) {
		httpx.Fail(c, http.StatusBadRequest, selfLockMsg)
		return
	}
	var req struct {
		NewPassword string `json:"new_password" binding:"required,min=6"`
	}
	if !httpx.BindJSON(c, &req) {
		return
	}
	var cnt int64
	_ = h.DB.Model(&model.User{}).Where("id = ? AND role = 'platform_admin'", id).Count(&cnt).Error
	if cnt == 0 {
		httpx.Fail(c, http.StatusNotFound, "管理员不存在")
		return
	}
	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "密码加密失败")
		return
	}
	if err := h.DB.Exec("UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?",
		hash, time.Now().Unix(), id).Error; err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "重置密码失败: "+err.Error())
		return
	}
	// 改 hash 即改会话指纹：该管理员存量 JWT 下一次请求即 401（期望行为）
	httpx.OK(c, gin.H{"message": "密码已重置，该管理员的旧登录会话已全部失效"})
}
