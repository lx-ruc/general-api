package org

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"token-gateway/internal/auth"
	"token-gateway/internal/crypto"
	"token-gateway/internal/database"
	"token-gateway/internal/httpx"
	"token-gateway/internal/middleware"
	"token-gateway/internal/model"
	"token-gateway/internal/service"
)

// errRequestHandled 审批事务内条件更新未命中（申请已被并发处理）的哨兵错误
var errRequestHandled = errors.New("request already handled")

// errMemberUnlimited 审批额度申请时申请人为不限额子账号的哨兵错误：
// NULL 基线上加额会把「不限」悄悄顶成「限额=追加额」，必须显式拒绝
var errMemberUnlimited = errors.New("member quota unlimited")

type Handler struct {
	DB     *gorm.DB
	Cipher *crypto.Cipher // 完整密钥 AES-GCM 存档（「密钥一览」复制完整密钥用）
}

func NewHandler(db *gorm.DB, cipher *crypto.Cipher) *Handler {
	return &Handler{DB: db, Cipher: cipher}
}

// orgID 当前客户管理员所属组织（org 隔离的基础）
func orgID(c *gin.Context) (int64, bool) {
	if oid := middleware.GetOrgID(c); oid != nil && *oid > 0 {
		return *oid, true
	}
	httpx.Fail(c, http.StatusForbidden, "缺少组织归属")
	return 0, false
}

// ---------------- 子账号管理 ----------------

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
		Email       string `json:"email"`     // 邮箱（选填）：绑定后子账号可自助找回密码
		QuotaAmount int64  `json:"quota_amount"`
	}
	if !httpx.BindJSON(c, &req) {
		return
	}
	// 负初始额度 = 子账号一出生即欠费态（precheck quota_used>=limit 恒真），
	// 且会写入负数 grant；与更新路径的 monthly_quota>=0 校验口径一致
	if req.QuotaAmount < 0 {
		httpx.Fail(c, http.StatusBadRequest, "初始额度不能为负（0=不限）")
		return
	}
	// 邮箱（选填）：归一 + 格式校验 + 占用预检查（找回邮件按 users.email 发，一号一邮箱）
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
	now := time.Now().Unix()
	m := model.User{
		OrgID: &oid, Username: req.Username, PasswordHash: hash,
		DisplayName: req.DisplayName, Email: email, Role: model.RoleMember, Status: 1,
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
				Remark: "创建子账号初始额度", OperatorID: opID(middleware.GetUID(c)), CreatedAt: now,
			}).Error
		}
		return nil
	})
	if err != nil {
		// 并发同用户名/同邮箱建号：预检查拦不住，映射回同款文案（原缺陷：500 + 驱动错误原文泄漏）
		if database.IsDuplicateKey(err) {
			httpx.Fail(c, http.StatusBadRequest, "用户名或邮箱已存在")
			return
		}
		httpx.Fail(c, http.StatusInternalServerError, "创建子账号失败: "+err.Error())
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
		httpx.Fail(c, http.StatusNotFound, "子账号不存在")
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
		MonthlyQuota   *int64  `json:"monthly_quota"` // 单月消费上限（点）；0=不限，次月自动清零
	}
	if !httpx.BindJSON(c, &req) {
		return
	}
	// 子账号必须属于本客户（防越权），且不能动客户管理员自己
	var m model.User
	if err := h.DB.Where("id = ? AND org_id = ? AND role = 'member'", id, oid).First(&m).Error; err != nil {
		httpx.Fail(c, http.StatusNotFound, "子账号不存在")
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
	if req.MonthlyQuota != nil {
		if *req.MonthlyQuota < 0 {
			httpx.Fail(c, http.StatusBadRequest, "monthly_quota 不能为负（0=不限）")
			return
		}
		updates["monthly_quota"] = *req.MonthlyQuota
	}
	if err := h.DB.Model(&model.User{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "更新失败")
		return
	}
	if req.QuotaUnlimited != nil {
		// 设值路径走额度唯一入口：同事务差值入流水（转不限记 −旧值；转限额以当前消耗为起点）
		if err := service.SetUserQuotaUnlimited(h.DB, oid, id, *req.QuotaUnlimited, middleware.GetUID(c)); err != nil {
			httpx.Fail(c, http.StatusInternalServerError, "更新失败")
			return
		}
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
	// 级联删除子账号的 API key 与管理面访问令牌（历史 usage_logs 保留）
	err := h.DB.Transaction(func(tx *gorm.DB) error {
		res := tx.Where("id = ? AND org_id = ? AND role = 'member'", id, oid).Delete(&model.User{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		if err := tx.Where("user_id = ?", id).Delete(&model.APIKey{}).Error; err != nil {
			return err
		}
		return tx.Where("user_id = ?", id).Delete(&model.AccessToken{}).Error
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httpx.Fail(c, http.StatusNotFound, "子账号不存在")
			return
		}
		httpx.Fail(c, http.StatusInternalServerError, "删除失败")
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
		httpx.Fail(c, http.StatusNotFound, "子账号不存在")
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
			httpx.Fail(c, http.StatusNotFound, "子账号不存在")
			return
		}
		if err == service.ErrQuotaOverflow {
			httpx.Fail(c, http.StatusBadRequest, "追加后额度超出可表示范围，请调整数值")
			return
		}
		if err == service.ErrQuotaUnlimited {
			httpx.Fail(c, http.StatusBadRequest, "该子账号为不限额，无需追加额度；如需恢复上限请把额度模式切回「限额」")
			return
		}
		httpx.Fail(c, http.StatusInternalServerError, "追加额度失败")
		return
	}
	httpx.OK(c, gin.H{"message": fmt.Sprintf("已追加 %s", service.FormatYuan(h.DB, req.Amount))})
}

// ---------------- 模型授权 ----------------

// GetMemberModels GET /api/org/members/:id/models
// 厂商口径：granted.vendors = 已授权厂商 id 列表；granted.models = 模型级例外名单；
// granted.models_all = 厂商展开 ∪ 例外的完整模型名单（旧前端兼容字段）
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
		httpx.Fail(c, http.StatusNotFound, "子账号不存在")
		return
	}
	var vendorIDs []int64
	_ = h.DB.Raw("SELECT vendor_id FROM user_vendor_grants WHERE user_id = ? ORDER BY vendor_id", id).Scan(&vendorIDs).Error
	var modelExcept []string
	_ = h.DB.Raw(`SELECT model_name FROM user_model_grants WHERE user_id = ? ORDER BY model_name`, id).Scan(&modelExcept).Error
	// 候选收窄为客户已开通厂商/模型（平台级白名单）且当前可路由——给成员授权一个
	// 上游已死的模型只会换来调用 503；附归属厂商名（授权弹窗厂商分组/标签用）
	var available []struct {
		model.Model
		Vendor string `json:"vendor"`
	}
	_ = h.DB.Raw(`SELECT m.*, COALESCE(v.name, '') AS vendor FROM models m
		LEFT JOIN vendors v ON v.id = m.vendor_id
		WHERE m.status = 1
		  AND (m.vendor_id IN (SELECT vendor_id FROM org_vendor_grants WHERE org_id = ?)
		    OR m.name IN (SELECT model_name FROM org_model_grants WHERE org_id = ?))
		  AND `+service.ModelHasLiveUpstream+`
		ORDER BY m.name`, oid, oid).Scan(&available).Error
	if vendorIDs == nil {
		vendorIDs = []int64{}
	}
	if modelExcept == nil {
		modelExcept = []string{}
	}
	granted := map[string]any{"vendors": vendorIDs, "models": modelExcept}
	httpx.OK(c, gin.H{"granted": granted, "available": available})
}

// SetMemberModels PUT /api/org/members/:id/models：整体替换白名单（双段：
// vendors = 厂商授权 id 列表；models = 模型级例外名单。兼容旧前端只传 models——
// 按模型名归并到所属厂商后整体替换）
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
		Vendors []int64  `json:"vendors"`
		Models  []string `json:"models"`
	}
	if !httpx.BindJSON(c, &req) {
		return
	}
	var cnt int64
	_ = h.DB.Model(&model.User{}).Where("id = ? AND org_id = ? AND role = 'member'", id, oid).Count(&cnt).Error
	if cnt == 0 {
		httpx.Fail(c, http.StatusNotFound, "子账号不存在")
		return
	}
	// 校验厂商存在且客户已开通（org_vendor_grants）；模型例外须存在/启用且在客户
	// 开通范围（厂商展开 ∪ 模型例外），且未被所属厂商整体覆盖（重复授权无意义但无害）
	var orgVids []int64
	_ = h.DB.Raw("SELECT vendor_id FROM org_vendor_grants WHERE org_id = ?", oid).Scan(&orgVids).Error
	orgOpenVendor := map[int64]bool{}
	for _, v := range orgVids {
		orgOpenVendor[v] = true
	}
	seenV := map[int64]bool{}
	vendors := make([]int64, 0, len(req.Vendors))
	for _, vid := range req.Vendors {
		if vid <= 0 || seenV[vid] {
			continue
		}
		var exist int64
		_ = h.DB.Raw("SELECT COUNT(*) FROM vendors WHERE id = ?", vid).Scan(&exist).Error
		if exist == 0 {
			httpx.Fail(c, http.StatusBadRequest, "厂商不存在或未启用")
			return
		}
		if !orgOpenVendor[vid] {
			httpx.Fail(c, http.StatusBadRequest,
				"该厂商未在本客户开通，请先在【模型申请】向平台申请开通")
			return
		}
		seenV[vid] = true
		vendors = append(vendors, vid)
	}
	grantedOrg := service.OrgGrantedSet(h.DB, oid)
	seenM := map[string]bool{}
	models := make([]string, 0, len(req.Models))
	for _, name := range req.Models {
		if name == "" || seenM[name] {
			continue
		}
		var mc int64
		_ = h.DB.Model(&model.Model{}).Where("name = ? AND status = 1", name).Count(&mc).Error
		if mc == 0 {
			httpx.Fail(c, http.StatusBadRequest, "模型不存在或未启用: "+name)
			return
		}
		if _, open := grantedOrg[name]; !open {
			httpx.Fail(c, http.StatusBadRequest,
				"模型 "+name+" 未在本客户开通，请先在【模型申请】向平台申请开通")
			return
		}
		seenM[name] = true
		models = append(models, name)
	}
	operator := middleware.GetUID(c)
	err := h.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ?", id).Delete(&model.UserVendorGrant{}).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", id).Delete(&model.UserModelGrant{}).Error; err != nil {
			return err
		}
		for _, vid := range vendors {
			if err := tx.Create(&model.UserVendorGrant{
				UserID: id, VendorID: vid, GrantedBy: opID(operator),
			}).Error; err != nil {
				return err
			}
		}
		for _, name := range models {
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
	httpx.OK(c, gin.H{"message": fmt.Sprintf("已授权 %d 个厂商、%d 个模型例外", len(vendors), len(models)),
		"vendors": vendors, "models": models})
}

// ---------------- 模型开通申请（向平台） ----------------

// ListModelRequestCandidates GET /api/org/model-requests/candidates
// 全部启用模型 + 本客户是否已开通/在途申请中，供申请页勾选。
// 注意：候选目录必须是全量启用模型（申请的目的就是开通还没有的），
// 白名单收窄只作用于给子账号授权、子账号申请等上下文
func (h *Handler) ListModelRequestCandidates(c *gin.Context) {
	oid, ok := orgID(c)
	if !ok {
		return
	}
	type row struct {
		Name        string   `json:"name"`
		DisplayName string   `json:"display_name"`
		Vendor      string   `json:"vendor"`
		Vendors     []string `json:"vendors"` // 归属厂商标签；空=回退 vendor
		Granted     bool     `json:"granted"`
		Pending     bool     `json:"pending"`
		// Routable=false 表示该模型当前没有可用上游 Key（如渠道 Key 因上游 401
		// 被自动禁用）——仍可申请（平台可能换 Key/换渠道），但前端要提示开通后
		// 未必立即可用
		Routable bool `json:"routable"`
	}
	var rows []row
	_ = h.DB.Raw(`
		SELECT m.name, m.display_name, COALESCE(v.name, '') AS vendor,
		       CASE WHEN (EXISTS (SELECT 1 FROM org_vendor_grants vg
		                             WHERE vg.org_id = ? AND vg.vendor_id = m.vendor_id)
		              OR EXISTS (SELECT 1 FROM org_model_grants og
		                          WHERE og.org_id = ? AND og.model_name = m.name))
		         THEN 1 ELSE 0 END AS granted,
		       CASE WHEN EXISTS (SELECT 1 FROM org_model_requests r
		                           WHERE r.org_id = ? AND r.status = 'pending'
		                             AND (',' || r.model_names || ',') LIKE ('%,' || COALESCE(v.name,'') || ',%'))
		         THEN 1 ELSE 0 END AS pending,
		       CASE WHEN `+service.ModelHasLiveUpstream+` THEN 1 ELSE 0 END AS routable
		FROM models m LEFT JOIN vendors v ON v.id = m.vendor_id
		WHERE m.status = 1 ORDER BY m.name`, oid, oid, oid).Scan(&rows).Error
	if rows == nil {
		rows = []row{}
	}
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		names = append(names, r.Name)
	}
	for i := range rows {
		rows[i].Vendors = service.VendorSingle(rows[i].Vendor)
	}
	httpx.OK(c, rows)
}

// CreateModelRequest POST /api/org/model-requests：向平台申请开通厂商（组内全部模型）。
// 校验厂商存在 → 去重 → 静默剔除本客户已开通厂商（重复申请无意义），剩空 → 400。
// 请求体兼容旧字段 models（存量前端）：按模型名归并到所属厂商
func (h *Handler) CreateModelRequest(c *gin.Context) {
	oid, ok := orgID(c)
	if !ok {
		return
	}
	var req struct {
		Vendors []string `json:"vendors"`
		Models  []string `json:"models"` // 兼容旧前端：模型名归并到所属厂商
		Reason  string   `json:"reason"`
	}
	if !httpx.BindJSON(c, &req) {
		return
	}
	// 模型名归并厂商（厂商化前的旧前端提交口径）
	if len(req.Vendors) == 0 && len(req.Models) > 0 {
		var rows []struct{ Name string }
		_ = h.DB.Raw(`SELECT DISTINCT v.name FROM models m JOIN vendors v ON v.id = m.vendor_id
			WHERE m.name IN ? AND m.vendor_id > 0`, req.Models).Scan(&rows).Error
		for _, r := range rows {
			req.Vendors = append(req.Vendors, r.Name)
		}
	}
	// 去重去空
	seen := map[string]bool{}
	names := make([]string, 0, len(req.Vendors))
	for _, n := range req.Vendors {
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		names = append(names, n)
	}
	if len(names) == 0 {
		httpx.Fail(c, http.StatusBadRequest, "请选择要申请的厂商")
		return
	}
	var vendorRows []struct{ ID int64; Name string }
	_ = h.DB.Raw("SELECT id, name FROM vendors").Scan(&vendorRows).Error
	byName := map[string]int64{}
	for _, v := range vendorRows {
		byName[v.Name] = v.ID
	}
	var grantedVids []int64
	_ = h.DB.Raw("SELECT vendor_id FROM org_vendor_grants WHERE org_id = ?", oid).Scan(&grantedVids).Error
	granted := map[int64]bool{}
	for _, id := range grantedVids {
		granted[id] = true
	}
	var valid []string
	for _, n := range names {
		vid, ok := byName[n]
		if !ok {
			httpx.Fail(c, http.StatusBadRequest, "厂商不存在："+n)
			return
		}
		if !granted[vid] {
			valid = append(valid, n)
		}
	}
	if len(valid) == 0 {
		httpx.Fail(c, http.StatusBadRequest, "申请的厂商均已开通，无需重复申请")
		return
	}
	r := model.OrgModelRequest{
		OrgID: oid, ModelNames: strings.Join(valid, ","), Reason: req.Reason, Status: "pending",
	}
	if err := h.DB.Create(&r).Error; err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "提交申请失败")
		return
	}
	// 站内通知平台管理员审批（顶栏铃铛）
	go func() {
		var orgName string
		_ = h.DB.Raw("SELECT name FROM orgs WHERE id = ?", oid).Scan(&orgName).Error
		service.NotifyPlatformAdminsInsite(h.DB, service.NotifyTypeModelRequest,
			fmt.Sprintf("厂商开通申请待审批：%s", orgName),
			fmt.Sprintf("客户「%s」申请开通厂商（组内全部模型）：%s。\n事由：%s\n到【模型开通】处理。",
				orgName, r.ModelNames, req.Reason))
	}()
	httpx.OK(c, gin.H{"id": r.ID, "vendors": valid, "message": "申请已提交，平台审批通过后即可在子账号管理中授权该厂商"})
}

// ListModelRequests GET /api/org/model-requests：本客户的开通申请记录
func (h *Handler) ListModelRequests(c *gin.Context) {
	oid, ok := orgID(c)
	if !ok {
		return
	}
	var rows []model.OrgModelRequest
	_ = h.DB.Where("org_id = ?", oid).Order("id DESC").Limit(100).Find(&rows).Error
	if rows == nil {
		rows = []model.OrgModelRequest{}
	}
	httpx.OK(c, rows)
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
	// 子账号上限聚合（配额下发页超发提示用）：限额账号的 quota_limit 合计；
	// 存在不限账号时合计无意义，前端据 unlimited_count 跳过提示
	var pool struct {
		LimitsSum      int64 `json:"limits_sum"`
		MemberCount    int64 `json:"member_count"`
		UnlimitedCount int64 `json:"unlimited_count"`
	}
	_ = h.DB.Raw(`SELECT COALESCE(SUM(quota_limit), 0) AS limits_sum,
		COUNT(*) AS member_count,
		SUM(CASE WHEN quota_limit IS NULL THEN 1 ELSE 0 END) AS unlimited_count
		FROM users WHERE org_id = ? AND role = 'member'`, oid).Scan(&pool).Error
	httpx.OK(c, gin.H{"overview": ov, "org": org, "quota_pool": pool})
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

// ListKeys GET /api/org/keys：客户内全部密钥
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
		Username       string `json:"username"`
		CostCenterName string `json:"cost_center_name"` // "" = 未归集
		KeyMasked      string `json:"key_masked"`       // 打码展示（复制走 /copy 端点）
		KeyCopyable    bool   `json:"key_copyable"`     // false=上线前存量（无加密存档），复制按钮置灰
	}
	var rows []keyRow
	_ = h.DB.Raw(`
		SELECT k.*, u.username, COALESCE(cc.name,'') AS cost_center_name FROM api_keys k
		LEFT JOIN users u ON u.id = k.user_id
		LEFT JOIN cost_centers cc ON cc.id = k.cost_center_id
		WHERE k.org_id = ? ORDER BY k.id DESC LIMIT ? OFFSET ?`, oid, size, offset).Scan(&rows).Error
	if rows == nil {
		rows = []keyRow{}
	}
	for i := range rows {
		rows[i].KeyMasked = service.KeyMaskedForList(h.Cipher, rows[i].KeyEnc, rows[i].KeyPrefix)
		rows[i].KeyCopyable = rows[i].KeyEnc != ""
	}
	httpx.PageResult(c, rows, total, page, size)
}

// CopyKey POST /api/org/keys/:id/copy —— 复制完整密钥（org 隔离：仅本客户成员的密钥）：
// 明文只进剪贴板不回显界面；存量无存档的旧密钥 400 引导重建
func (h *Handler) CopyKey(c *gin.Context) {
	oid, ok := orgID(c)
	if !ok {
		return
	}
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	service.CopyKeyPayload(c, h.DB, h.Cipher, "密钥", middleware.GetUID(c),
		"SELECT id, key_enc AS enc FROM api_keys WHERE id = ? AND org_id = ?", id, oid)
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

// HandleRequest PUT /api/org/requests/:id：审批。额度申请通过即同事务追加额度；
// 模型申请通过即同事务逐模型写 user_model_grants（UNIQUE + ON CONFLICT DO NOTHING 天然幂等）
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
	// 模型申请前置校验（申请单 model_names 现为厂商名）：厂商须存在且客户已开通
	if qr.Kind == "model" && req.Action == "approve" {
		for _, n := range modelNamesOf(qr.ModelNames) {
			var vid int64
			if err := h.DB.Raw("SELECT id FROM vendors WHERE name = ?", n).Scan(&vid).Error; err != nil || vid == 0 {
				httpx.Fail(c, http.StatusBadRequest, "厂商 "+n+" 不存在，请拒绝本次申请")
				return
			}
			// 平台已回收该厂商（子账号提交申请后客户开通被撤销的窗口）
			var open int64
			_ = h.DB.Raw("SELECT COUNT(*) FROM org_vendor_grants WHERE org_id = ? AND vendor_id = ?", oid, vid).Scan(&open).Error
			if open == 0 {
				// 兼容：客户仅按模型级例外开通（未经厂商授权）时同样放行
				var byModel int64
				_ = h.DB.Raw(`SELECT COUNT(*) FROM org_model_grants og JOIN models m ON m.name = og.model_name
					WHERE og.org_id = ? AND m.vendor_id = ?`, oid, vid).Scan(&byModel).Error
				if byModel == 0 {
					httpx.Fail(c, http.StatusBadRequest, "厂商 "+n+" 已被平台回收，无法授权，请拒绝本次申请")
					return
				}
			}
		}
	}
	now := time.Now().Unix()
	operator := middleware.GetUID(c)
	err := h.DB.Transaction(func(tx *gorm.DB) error {
		newStatus := "approved"
		if req.Action == "reject" {
			newStatus = "rejected"
		}
		// pending 判定并入事务内的条件更新：并发双审批（双击/双管理员）只有一个事务能改到行，
		// 杜绝"两个事务都读到 pending、各自加额入账"的双重记账
		res := tx.Exec(`UPDATE quota_requests SET status = ?, handled_by = ?, handled_at = ?, reply = ?
			WHERE id = ? AND org_id = ? AND status = 'pending'`,
			newStatus, operator, now, req.Reply, id, oid)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errRequestHandled
		}
		if req.Action == "approve" {
			switch qr.Kind {
			case "model":
				// 逐厂商写授权（厂商授权 = 组内全部模型可用，新模型自动继承）：
				// 已授权的静默跳过（幂等），granted_by 记审批人
				for _, n := range modelNamesOf(qr.ModelNames) {
					if err := tx.Exec(`INSERT INTO user_vendor_grants (user_id, vendor_id, granted_by, created_at)
						SELECT ?, v.id, ?, ? FROM vendors v WHERE v.name = ?
						ON CONFLICT (user_id, vendor_id) DO NOTHING`,
						qr.UserID, operator, now, n).Error; err != nil {
						return err
					}
				}
				return nil
			default: // quota（存量行 kind 为空串时同样走额度分支）
				// 不限额子账号拒绝加额（NULL 基线上加额会顶掉「不限」），整单拒绝留待改限额后再批
				q := tx.Where("id = ? AND org_id = ?", qr.UserID, oid)
				if tx.Dialector.Name() == "postgres" {
					q = q.Clauses(clause.Locking{Strength: "UPDATE"}) // PG 行锁防丢失更新；SQLite 由 _txlock=immediate 串行化护住窗口
				}
				var mu model.User
				if err := q.First(&mu).Error; err != nil {
					return err
				}
				if mu.QuotaLimit == nil {
					return errMemberUnlimited
				}
				// 与 AddMemberQuota 同口径的溢出预检：SQL 加法溢出只会以 500 收场且文案难辨，
				// 提前以 400 拒绝并保持 pending（申请可改小后重批）
				if *mu.QuotaLimit > math.MaxInt64-qr.Amount {
					return service.ErrQuotaOverflow
				}
				if err := tx.Exec("UPDATE users SET quota_limit = COALESCE(quota_limit, 0) + ?, updated_at = ? WHERE id = ? AND org_id = ?",
					qr.Amount, now, qr.UserID, oid).Error; err != nil {
					return err
				}
				// 流水与加额度同事务，杜绝"额度已动、流水缺失"的审计断裂
				return tx.Create(&model.QuotaGrant{
					SubjectType: "user", SubjectID: qr.UserID, Amount: qr.Amount,
					Remark:     fmt.Sprintf("额度申请 #%d 审批通过", id),
					OperatorID: opID(operator), CreatedAt: now,
				}).Error
			}
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, errRequestHandled) {
			httpx.Fail(c, http.StatusNotFound, "申请不存在或已处理")
			return
		}
		if errors.Is(err, errMemberUnlimited) {
			httpx.Fail(c, http.StatusBadRequest, "该子账号当前为不限额，无法追加额度；请先把额度模式切回「限额」再审批")
			return
		}
		if errors.Is(err, service.ErrQuotaOverflow) {
			httpx.Fail(c, http.StatusBadRequest, "额度超出可表示范围，请调低申请额度后重批")
			return
		}
		httpx.Fail(c, http.StatusInternalServerError, "审批失败")
		return
	}
	// 站内通知申请人审批结果（顶栏铃铛；邮件扇出另行按需配置）
	go func() {
		var title, body string
		if req.Action == "approve" {
			switch qr.Kind {
			case "model":
				title = "模型申请已通过"
				body = fmt.Sprintf("管理员已为你开通厂商（组内全部模型可用）：%s。", qr.ModelNames)
			default:
				title = "额度申请已通过"
				body = fmt.Sprintf("额度 +%s 已到账生效。", service.FormatYuan(h.DB, qr.Amount))
			}
		} else {
			title = "申请已被拒绝"
			body = "管理员拒绝了你的申请。"
			if req.Reply != "" {
				body += "回复：" + req.Reply
			}
		}
		service.NotifyUserInsite(h.DB, qr.UserID, service.NotifyTypeRequestHandled, title, body)
	}()
	httpx.OK(c, gin.H{"message": "已处理"})
}

// modelNamesOf 申请单里的模型名拆分（逗号分隔，去空）
func modelNamesOf(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func opID(id int64) *int64 {
	if id == 0 {
		return nil
	}
	return &id
}

// ---------------- 充值与账单 ----------------

// GetBankInfo GET /api/org/bank-info：平台收款信息
func (h *Handler) GetBankInfo(c *gin.Context) {
	var v string
	_ = h.DB.Raw("SELECT value FROM settings WHERE key = 'bank_info'").Scan(&v).Error
	httpx.OK(c, gin.H{"bank_info": v})
}

// ListRecharges GET /api/org/recharges：本客户充值记录
func (h *Handler) ListRecharges(c *gin.Context) {
	oid, ok := orgID(c)
	if !ok {
		return
	}
	var rows []model.RechargeRequest
	_ = h.DB.Where("org_id = ?", oid).Order("id DESC").Limit(100).Find(&rows).Error
	if rows == nil {
		rows = []model.RechargeRequest{}
	}
	httpx.OK(c, rows)
}

// CreateRecharge POST /api/org/recharges：发起充值申请（对公转账后提交凭证）
func (h *Handler) CreateRecharge(c *gin.Context) {
	oid, ok := orgID(c)
	if !ok {
		return
	}
	var req struct {
		Amount  int64  `json:"amount" binding:"required,gt=0,lte=1000000000000000"` // 上限 1e15 点（默认汇率合 10 亿元），防天价申请
		Voucher string `json:"voucher" binding:"required"`
	}
	if !httpx.BindJSON(c, &req) {
		return
	}
	r := model.RechargeRequest{OrgID: oid, Amount: req.Amount, Voucher: req.Voucher, Status: "pending"}
	if err := h.DB.Create(&r).Error; err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "提交失败")
		return
	}
	// 站内通知平台管理员审批（顶栏铃铛）
	go func() {
		var orgName string
		_ = h.DB.Raw("SELECT name FROM orgs WHERE id = ?", oid).Scan(&orgName).Error
		service.NotifyPlatformAdminsInsite(h.DB, service.NotifyTypeRecharge,
			fmt.Sprintf("充值申请待审批：%s", orgName),
			fmt.Sprintf("客户「%s」提交了充值申请 #%d：+%s（凭证已上传）。到【客户管理 → 充值审批】处理。",
				orgName, r.ID, service.FormatYuan(h.DB, req.Amount)))
	}()
	httpx.OK(c, gin.H{"id": r.ID, "message": "充值申请已提交，平台确认到账后额度自动增加并邮件通知你"})
}

// Billing GET /api/org/billing?month=2026-09：月度对账单
func (h *Handler) Billing(c *gin.Context) {
	oid, ok := orgID(c)
	if !ok {
		return
	}
	// 月边界与对账单同一时区口径（billing.timezone），不随服务器系统时区漂移
	month := c.DefaultQuery("month", time.Now().In(service.BillingLoc()).Format("2006-01"))
	mStart, err := time.ParseInLocation("2006-01", month, service.BillingLoc())
	if err != nil {
		httpx.Fail(c, http.StatusBadRequest, "month 格式应为 YYYY-MM")
		return
	}
	mEnd := mStart.AddDate(0, 1, 0)
	s, e := mStart.Unix(), mEnd.Unix()

	// 汇总
	var sum struct {
		Requests int64 `json:"requests"`
		Tokens   int64 `json:"tokens"`
		Cost     int64 `json:"cost"`
	}
	_ = h.DB.Raw(`SELECT COUNT(*) AS requests,
		COALESCE(SUM(prompt_tokens + completion_tokens),0) AS tokens,
		COALESCE(SUM(cost),0) AS cost
		FROM usage_logs WHERE org_id = ? AND created_at >= ? AND created_at < ?`, oid, s, e).Scan(&sum).Error

	// 按模型明细
	type modelRow struct {
		Name     string `json:"name"`
		Requests int64  `json:"requests"`
		Tokens   int64  `json:"tokens"`
		Cost     int64  `json:"cost"`
	}
	var byModel []modelRow
	// 模型明细只统计真实消耗（status=200 或有计费）：被拒尝试（403 未授权/404 不存在等
	// 零成本行）不得以 0 成本行出现——与成本报表/账单同口径；上方汇总请求数仍计全量
	_ = h.DB.Raw(`SELECT model_name AS name, COUNT(*) AS requests,
		COALESCE(SUM(prompt_tokens + completion_tokens),0) AS tokens,
		COALESCE(SUM(cost),0) AS cost
		FROM usage_logs WHERE org_id = ? AND created_at >= ? AND created_at < ?
		  AND (status = 200 OR cost > 0)
		GROUP BY model_name ORDER BY cost DESC`, oid, s, e).Scan(&byModel).Error
	if byModel == nil {
		byModel = []modelRow{}
	}

	// 本月充值（approved）
	var recharged int64
	_ = h.DB.Raw(`SELECT COALESCE(SUM(amount),0) FROM recharge_requests
		WHERE org_id = ? AND status = 'approved' AND created_at >= ? AND created_at < ?`, oid, s, e).Scan(&recharged).Error
	var recharges []model.RechargeRequest
	_ = h.DB.Where("org_id = ? AND created_at >= ? AND created_at < ?", oid, s, e).
		Order("id DESC").Find(&recharges).Error
	if recharges == nil {
		recharges = []model.RechargeRequest{}
	}

	// 按日序列（曲线）
	type dayRow struct {
		Date     string `json:"date"`
		Requests int64  `json:"requests"`
		Cost     int64  `json:"cost"`
	}
	var daily []dayRow
	// 日分桶同样锚定账期时区：Go 侧算偏移，SQL 平移（与 billing.go 一致）
	_, off := time.Unix(s, 0).In(service.BillingLoc()).Zone()
	dateExpr := fmt.Sprintf("strftime('%%Y-%%m-%%d', created_at + %d, 'unixepoch')", off)
	if database.Dialect == "postgres" {
		dateExpr = fmt.Sprintf("to_char(to_timestamp(created_at + %d), 'YYYY-MM-DD')", off)
	}
	_ = h.DB.Raw(fmt.Sprintf(`SELECT %s AS date, COUNT(*) AS requests, COALESCE(SUM(cost),0) AS cost
		FROM usage_logs WHERE org_id = ? AND created_at >= ? AND created_at < ?
		GROUP BY date ORDER BY date`, dateExpr), oid, s, e).Scan(&daily).Error

	httpx.OK(c, gin.H{
		"month": month, "summary": sum, "by_model": byModel,
		"recharged": recharged, "recharges": recharges, "daily": daily,
	})
}
