package org

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"token-gateway/internal/httpx"
	"token-gateway/internal/auth"
	"token-gateway/internal/middleware"
	"token-gateway/internal/model"
	"token-gateway/internal/service"
)

type Handler struct {
	DB *gorm.DB
}

func NewHandler(db *gorm.DB) *Handler {
	return &Handler{DB: db}
}

// orgID 当前公司管理员所属组织（org 隔离的基础）
func orgID(c *gin.Context) (int64, bool) {
	if oid := middleware.GetOrgID(c); oid != nil && *oid > 0 {
		return *oid, true
	}
	httpx.Fail(c, http.StatusForbidden, "缺少组织归属")
	return 0, false
}

// ---------------- 员工管理 ----------------

// ListMembers GET /api/org/members
func (h *Handler) ListMembers(c *gin.Context) {
	oid, ok := orgID(c)
	if !ok {
		return
	}
	page, size, offset := httpx.PageParams(c)
	cond, args := "u.org_id = ? AND u.role = 'member'", []any{oid}
	if q := c.Query("query"); q != "" {
		cond += " AND (u.username LIKE ? OR u.display_name LIKE ?)"
		args = append(args, "%"+q+"%", "%"+q+"%")
	}
	var total int64
	_ = h.DB.Raw(fmt.Sprintf("SELECT COUNT(*) FROM users u WHERE %s", cond), args...).Scan(&total).Error
	type memberRow struct {
		model.User
		GrantCount int64 `json:"grant_count"`
		KeyCount   int64 `json:"key_count"`
	}
	var rows []memberRow
	_ = h.DB.Raw(fmt.Sprintf(`
		SELECT u.*,
		       (SELECT COUNT(*) FROM user_model_grants g WHERE g.user_id = u.id) AS grant_count,
		       (SELECT COUNT(*) FROM api_keys k WHERE k.user_id = u.id) AS key_count
		FROM users u WHERE %s ORDER BY u.id DESC LIMIT ? OFFSET ?`, cond),
		append(args, size, offset)...).Scan(&rows).Error
	if rows == nil {
		rows = []memberRow{}
	}
	httpx.PageResult(c, rows, total, page, size)
}

// CreateMember POST /api/org/members
func (h *Handler) CreateMember(c *gin.Context) {
	oid, ok := orgID(c)
	if !ok {
		return
	}
	var req struct {
		Username    string `json:"username" binding:"required,min=3"`
		Password    string `json:"password" binding:"required,min=6"`
		DisplayName string `json:"display_name"`
		QuotaAmount int64  `json:"quota_amount"`
	}
	if !httpx.BindJSON(c, &req) {
		return
	}
	var cnt int64
	_ = h.DB.Model(&model.User{}).Where("username = ?", req.Username).Count(&cnt).Error
	if cnt > 0 {
		httpx.Fail(c, http.StatusBadRequest, "用户名已存在")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "密码加密失败")
		return
	}
	now := time.Now().Unix()
	m := model.User{
		OrgID: &oid, Username: req.Username, PasswordHash: hash,
		DisplayName: req.DisplayName, Role: model.RoleMember, Status: 1,
	}
	err = h.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&m).Error; err != nil {
			return err
		}
		if req.QuotaAmount != 0 {
			if err := tx.Exec("UPDATE users SET quota_limit = ? WHERE id = ?", req.QuotaAmount, m.ID).Error; err != nil {
				return err
			}
			return tx.Create(&model.QuotaGrant{
				SubjectType: "user", SubjectID: m.ID, Amount: req.QuotaAmount,
				Remark: "创建员工初始额度", OperatorID: opID(middleware.GetUID(c)), CreatedAt: now,
			}).Error
		}
		return nil
	})
	if err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "创建员工失败: "+err.Error())
		return
	}
	_ = h.DB.Where("id = ?", m.ID).First(&m).Error // 回读，带上事务内设置的额度
	httpx.OK(c, m)
}

// GetMember GET /api/org/members/:id
func (h *Handler) GetMember(c *gin.Context) {
	oid, ok := orgID(c)
	if !ok {
		return
	}
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	var m model.User
	if err := h.DB.Where("id = ? AND org_id = ?", id, oid).First(&m).Error; err != nil {
		httpx.Fail(c, http.StatusNotFound, "员工不存在")
		return
	}
	httpx.OK(c, m)
}

// UpdateMember PUT /api/org/members/:id（display_name/status/quota_unlimited）
func (h *Handler) UpdateMember(c *gin.Context) {
	oid, ok := orgID(c)
	if !ok {
		return
	}
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	var req struct {
		DisplayName    *string `json:"display_name"`
		Status         *int    `json:"status"`
		QuotaUnlimited *bool   `json:"quota_unlimited"`
	}
	if !httpx.BindJSON(c, &req) {
		return
	}
	// 员工必须属于本公司（防越权），且不能动公司管理员自己
	var m model.User
	if err := h.DB.Where("id = ? AND org_id = ? AND role = 'member'", id, oid).First(&m).Error; err != nil {
		httpx.Fail(c, http.StatusNotFound, "员工不存在")
		return
	}
	updates := map[string]any{"updated_at": time.Now().Unix()}
	if req.DisplayName != nil {
		updates["display_name"] = *req.DisplayName
	}
	if req.Status != nil {
		if *req.Status != 0 && *req.Status != 1 {
			httpx.Fail(c, http.StatusBadRequest, "status 只能为 0 或 1")
			return
		}
		updates["status"] = *req.Status
	}
	if req.QuotaUnlimited != nil {
		if *req.QuotaUnlimited {
			updates["quota_limit"] = nil
		} else if m.QuotaLimit == nil {
			updates["quota_limit"] = m.QuotaUsed // 从不限转限额：以当前消耗为起点
		}
	}
	if err := h.DB.Model(&model.User{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "更新失败")
		return
	}
	httpx.OK(c, gin.H{"message": "已更新"})
}

// DeleteMember DELETE /api/org/members/:id
func (h *Handler) DeleteMember(c *gin.Context) {
	oid, ok := orgID(c)
	if !ok {
		return
	}
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	res := h.DB.Where("id = ? AND org_id = ? AND role = 'member'", id, oid).Delete(&model.User{})
	if res.Error != nil {
		httpx.Fail(c, http.StatusInternalServerError, "删除失败")
		return
	}
	if res.RowsAffected == 0 {
		httpx.Fail(c, http.StatusNotFound, "员工不存在")
		return
	}
	httpx.OK(c, gin.H{"message": "已删除"})
}

// ResetMemberPassword POST /api/org/members/:id/reset-password
func (h *Handler) ResetMemberPassword(c *gin.Context) {
	oid, ok := orgID(c)
	if !ok {
		return
	}
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	var req struct {
		NewPassword string `json:"new_password" binding:"required,min=6"`
	}
	if !httpx.BindJSON(c, &req) {
		return
	}
	var cnt int64
	_ = h.DB.Model(&model.User{}).Where("id = ? AND org_id = ? AND role = 'member'", id, oid).Count(&cnt).Error
	if cnt == 0 {
		httpx.Fail(c, http.StatusNotFound, "员工不存在")
		return
	}
	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "密码加密失败")
		return
	}
	if err := h.DB.Exec("UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?",
		hash, time.Now().Unix(), id).Error; err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "重置失败")
		return
	}
	httpx.OK(c, gin.H{"message": "密码已重置"})
}

// AddMemberQuota POST /api/org/members/:id/quota
func (h *Handler) AddMemberQuota(c *gin.Context) {
	oid, ok := orgID(c)
	if !ok {
		return
	}
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	var req struct {
		Amount int64  `json:"amount" binding:"required"`
		Remark string `json:"remark"`
	}
	if !httpx.BindJSON(c, &req) {
		return
	}
	if err := service.AddUserQuota(h.DB, oid, id, req.Amount, middleware.GetUID(c), req.Remark); err != nil {
		if err == service.ErrNotFound {
			httpx.Fail(c, http.StatusNotFound, "员工不存在")
			return
		}
		httpx.Fail(c, http.StatusInternalServerError, "追加额度失败")
		return
	}
	httpx.OK(c, gin.H{"message": fmt.Sprintf("已追加 %d token", req.Amount)})
}

// ---------------- 模型授权 ----------------

// GetMemberModels GET /api/org/members/:id/models
func (h *Handler) GetMemberModels(c *gin.Context) {
	oid, ok := orgID(c)
	if !ok {
		return
	}
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	var cnt int64
	_ = h.DB.Model(&model.User{}).Where("id = ? AND org_id = ?", id, oid).Count(&cnt).Error
	if cnt == 0 {
		httpx.Fail(c, http.StatusNotFound, "员工不存在")
		return
	}
	var granted []string
	_ = h.DB.Model(&model.UserModelGrant{}).Where("user_id = ?", id).
		Order("model_name").Pluck("model_name", &granted).Error
	var available []model.Model
	_ = h.DB.Where("status = 1").Order("name").Find(&available).Error
	if granted == nil {
		granted = []string{}
	}
	httpx.OK(c, gin.H{"granted": granted, "available": available})
}

// SetMemberModels PUT /api/org/members/:id/models：整体替换白名单
func (h *Handler) SetMemberModels(c *gin.Context) {
	oid, ok := orgID(c)
	if !ok {
		return
	}
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	var req struct {
		Models []string `json:"models"`
	}
	if !httpx.BindJSON(c, &req) {
		return
	}
	var cnt int64
	_ = h.DB.Model(&model.User{}).Where("id = ? AND org_id = ? AND role = 'member'", id, oid).Count(&cnt).Error
	if cnt == 0 {
		httpx.Fail(c, http.StatusNotFound, "员工不存在")
		return
	}
	// 校验模型都存在且启用
	dedup := make([]string, 0, len(req.Models))
	seen := map[string]bool{}
	for _, name := range req.Models {
		if name == "" || seen[name] {
			continue
		}
		var mc int64
		_ = h.DB.Model(&model.Model{}).Where("name = ? AND status = 1", name).Count(&mc).Error
		if mc == 0 {
			httpx.Fail(c, http.StatusBadRequest, "模型不存在或未启用: "+name)
			return
		}
		seen[name] = true
		dedup = append(dedup, name)
	}
	operator := middleware.GetUID(c)
	err := h.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ?", id).Delete(&model.UserModelGrant{}).Error; err != nil {
			return err
		}
		for _, name := range dedup {
			if err := tx.Create(&model.UserModelGrant{
				UserID: id, ModelName: name, GrantedBy: opID(operator),
			}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "授权失败: "+err.Error())
		return
	}
	httpx.OK(c, gin.H{"message": fmt.Sprintf("已授权 %d 个模型", len(dedup)), "models": dedup})
}

// ---------------- 统计 / 日志 / 密钥 ----------------

// StatsOverview GET /api/org/stats/overview
func (h *Handler) StatsOverview(c *gin.Context) {
	oid, ok := orgID(c)
	if !ok {
		return
	}
	ov, err := service.StatsOverview(h.DB, service.Scope{OrgID: &oid})
	if err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "统计查询失败")
		return
	}
	var org model.Org
	_ = h.DB.Where("id = ?", oid).First(&org).Error
	httpx.OK(c, gin.H{"overview": ov, "org": org})
}

// ListUsage GET /api/org/usage
func (h *Handler) ListUsage(c *gin.Context) {
	oid, ok := orgID(c)
	if !ok {
		return
	}
	page, size, offset := httpx.PageParams(c)
	cond, args := "l.org_id = ?", []any{oid}
	if v := httpx.QueryInt64(c, "user_id", 0); v > 0 {
		cond += " AND l.user_id = ?"
		args = append(args, v)
	}
	if v := c.Query("model"); v != "" {
		cond += " AND l.model_name = ?"
		args = append(args, v)
	}
	if v := httpx.QueryInt64(c, "status", 0); v > 0 {
		cond += " AND l.status = ?"
		args = append(args, v)
	}
	if v := httpx.QueryInt64(c, "start", 0); v > 0 {
		cond += " AND l.created_at >= ?"
		args = append(args, v)
	}
	if v := httpx.QueryInt64(c, "end", 0); v > 0 {
		cond += " AND l.created_at < ?"
		args = append(args, v)
	}
	var total int64
	_ = h.DB.Raw(fmt.Sprintf("SELECT COUNT(*) FROM usage_logs l WHERE %s", cond), args...).Scan(&total).Error
	type usageRow struct {
		model.UsageLog
		Username string `json:"username"`
	}
	var rows []usageRow
	_ = h.DB.Raw(fmt.Sprintf(`
		SELECT l.*, u.username FROM usage_logs l
		LEFT JOIN users u ON u.id = l.user_id
		WHERE %s ORDER BY l.id DESC LIMIT ? OFFSET ?`, cond),
		append(args, size, offset)...).Scan(&rows).Error
	if rows == nil {
		rows = []usageRow{}
	}
	httpx.PageResult(c, rows, total, page, size)
}

// ListKeys GET /api/org/keys：公司内全部密钥
func (h *Handler) ListKeys(c *gin.Context) {
	oid, ok := orgID(c)
	if !ok {
		return
	}
	page, size, offset := httpx.PageParams(c)
	var total int64
	_ = h.DB.Raw("SELECT COUNT(*) FROM api_keys k WHERE k.org_id = ?", oid).Scan(&total).Error
	type keyRow struct {
		model.APIKey
		Username string `json:"username"`
	}
	var rows []keyRow
	_ = h.DB.Raw(`
		SELECT k.*, u.username FROM api_keys k
		LEFT JOIN users u ON u.id = k.user_id
		WHERE k.org_id = ? ORDER BY k.id DESC LIMIT ? OFFSET ?`, oid, size, offset).Scan(&rows).Error
	if rows == nil {
		rows = []keyRow{}
	}
	httpx.PageResult(c, rows, total, page, size)
}

// UpdateKeyStatus PUT /api/org/keys/:id/status
func (h *Handler) UpdateKeyStatus(c *gin.Context) {
	oid, ok := orgID(c)
	if !ok {
		return
	}
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	// 注意：整型 status 用指针接收 —— gin 的 required 会把字面 0 当空值拒绝，
	// 导致「禁用(0)」永远 400；*int 只区分「没传(nil)」和「传了(含 0)」
	var req struct {
		Status *int `json:"status" binding:"required"`
	}
	if !httpx.BindJSON(c, &req) {
		return
	}
	if *req.Status != 0 && *req.Status != 1 {
		httpx.Fail(c, http.StatusBadRequest, "status 只能为 0 或 1")
		return
	}
	res := h.DB.Exec("UPDATE api_keys SET status = ? WHERE id = ? AND org_id = ?", *req.Status, id, oid)
	if res.Error != nil || res.RowsAffected == 0 {
		httpx.Fail(c, http.StatusNotFound, "密钥不存在")
		return
	}
	httpx.OK(c, gin.H{"message": "已更新"})
}

// ---------------- 额度申请审批 ----------------

// ListRequests GET /api/org/requests
func (h *Handler) ListRequests(c *gin.Context) {
	oid, ok := orgID(c)
	if !ok {
		return
	}
	page, size, offset := httpx.PageParams(c)
	cond, args := "r.org_id = ?", []any{oid}
	if st := c.Query("status"); st != "" {
		cond += " AND r.status = ?"
		args = append(args, st)
	}
	var total int64
	_ = h.DB.Raw(fmt.Sprintf("SELECT COUNT(*) FROM quota_requests r WHERE %s", cond), args...).Scan(&total).Error
	type reqRow struct {
		model.QuotaRequest
		Username string `json:"username"`
	}
	var rows []reqRow
	_ = h.DB.Raw(fmt.Sprintf(`
		SELECT r.*, u.username FROM quota_requests r
		LEFT JOIN users u ON u.id = r.user_id
		WHERE %s ORDER BY r.id DESC LIMIT ? OFFSET ?`, cond),
		append(args, size, offset)...).Scan(&rows).Error
	if rows == nil {
		rows = []reqRow{}
	}
	httpx.PageResult(c, rows, total, page, size)
}

// HandleRequest PUT /api/org/requests/:id：审批（通过即同事务追加额度）
func (h *Handler) HandleRequest(c *gin.Context) {
	oid, ok := orgID(c)
	if !ok {
		return
	}
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	var req struct {
		Action string `json:"action" binding:"required,oneof=approve reject"`
		Reply  string `json:"reply"`
	}
	if !httpx.BindJSON(c, &req) {
		return
	}
	var qr model.QuotaRequest
	if err := h.DB.Where("id = ? AND org_id = ? AND status = 'pending'", id, oid).First(&qr).Error; err != nil {
		httpx.Fail(c, http.StatusNotFound, "申请不存在或已处理")
		return
	}
	now := time.Now().Unix()
	operator := middleware.GetUID(c)
	err := h.DB.Transaction(func(tx *gorm.DB) error {
		newStatus := "approved"
		if req.Action == "reject" {
			newStatus = "rejected"
		}
		if err := tx.Exec(`UPDATE quota_requests SET status = ?, handled_by = ?, handled_at = ?, reply = ? WHERE id = ?`,
			newStatus, operator, now, req.Reply, id).Error; err != nil {
			return err
		}
		if req.Action == "approve" {
			return tx.Exec("UPDATE users SET quota_limit = COALESCE(quota_limit, 0) + ?, updated_at = ? WHERE id = ? AND org_id = ?",
				qr.Amount, now, qr.UserID, oid).Error
		}
		return nil
	})
	if err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "审批失败")
		return
	}
	if req.Action == "approve" {
		_ = h.DB.Create(&model.QuotaGrant{
			SubjectType: "user", SubjectID: qr.UserID, Amount: qr.Amount,
			Remark:      fmt.Sprintf("额度申请 #%d 审批通过", id),
			OperatorID:  opID(operator), CreatedAt: now,
		}).Error
	}
	httpx.OK(c, gin.H{"message": "已处理"})
}

func opID(id int64) *int64 {
	if id == 0 {
		return nil
	}
	return &id
}
