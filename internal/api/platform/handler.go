package platform

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"token-gateway/internal/httpx"
	"token-gateway/internal/auth"
	"token-gateway/internal/crypto"
	"token-gateway/internal/middleware"
	"token-gateway/internal/model"
	"token-gateway/internal/service"
)

type Handler struct {
	DB     *gorm.DB
	Cipher *crypto.Cipher
	Client *http.Client
}

func NewHandler(db *gorm.DB, cipher *crypto.Cipher, client *http.Client) *Handler {
	return &Handler{DB: db, Cipher: cipher, Client: client}
}

// ---------------- 公司管理 ----------------

// ListOrgs GET /api/platform/orgs
func (h *Handler) ListOrgs(c *gin.Context) {
	page, size, offset := httpx.PageParams(c)
	q := strings.TrimSpace(c.Query("query"))
	cond, args := "1=1", []any{}
	if q != "" {
		cond += " AND name LIKE ?"
		args = append(args, "%"+q+"%")
	}
	var total int64
	_ = h.DB.Raw(fmt.Sprintf("SELECT COUNT(*) FROM orgs WHERE %s", cond), args...).Scan(&total).Error
	type orgRow struct {
		model.Org
		MemberCount int64 `json:"member_count"`
	}
	var rows []orgRow
	_ = h.DB.Raw(fmt.Sprintf(`
		SELECT o.*, (SELECT COUNT(*) FROM users u WHERE u.org_id = o.id) AS member_count
		FROM orgs o WHERE %s ORDER BY o.id DESC LIMIT ? OFFSET ?`, cond),
		append(args, size, offset)...).Scan(&rows).Error
	if rows == nil {
		rows = []orgRow{}
	}
	httpx.PageResult(c, rows, total, page, size)
}

type createOrgReq struct {
	Name             string `json:"name" binding:"required"`
	Remark           string `json:"remark"`
	QuotaAmount      int64  `json:"quota_amount"` // 初始额度（点），可为 0
	AdminUsername    string `json:"admin_username" binding:"required,min=3"`
	AdminPassword    string `json:"admin_password" binding:"required,min=6"`
	AdminDisplayName string `json:"admin_display_name"`
}

// CreateOrg POST /api/platform/orgs：创建公司 + 首任公司管理员
func (h *Handler) CreateOrg(c *gin.Context) {
	var req createOrgReq
	if !httpx.BindJSON(c, &req) {
		return
	}
	var cnt int64
	_ = h.DB.Model(&model.Org{}).Where("name = ?", req.Name).Count(&cnt).Error
	if cnt > 0 {
		httpx.Fail(c, http.StatusBadRequest, "公司名已存在")
		return
	}
	_ = h.DB.Model(&model.User{}).Where("username = ?", req.AdminUsername).Count(&cnt).Error
	if cnt > 0 {
		httpx.Fail(c, http.StatusBadRequest, "管理员用户名已存在")
		return
	}
	hash, err := auth.HashPassword(req.AdminPassword)
	if err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "密码加密失败")
		return
	}
	now := time.Now().Unix()
	var org model.Org
	err = h.DB.Transaction(func(tx *gorm.DB) error {
		org = model.Org{Name: req.Name, Remark: req.Remark, QuotaLimit: 0, Status: 1}
		if err := tx.Create(&org).Error; err != nil {
			return err
		}
		if req.QuotaAmount != 0 {
			if err := tx.Exec("UPDATE orgs SET quota_limit = ? WHERE id = ?", req.QuotaAmount, org.ID).Error; err != nil {
				return err
			}
			if err := tx.Create(&model.QuotaGrant{
				SubjectType: "org", SubjectID: org.ID, Amount: req.QuotaAmount,
				Remark: "创建公司初始额度", OperatorID: opID(middleware.GetUID(c)), CreatedAt: now,
			}).Error; err != nil {
				return err
			}
		}
		return tx.Create(&model.User{
			OrgID: &org.ID, Username: req.AdminUsername, PasswordHash: hash,
			DisplayName: req.AdminDisplayName, Role: model.RoleOrgAdmin, Status: 1,
		}).Error
	})
	if err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "创建公司失败: "+err.Error())
		return
	}
	httpx.OK(c, gin.H{
		"org": gin.H{"id": org.ID, "name": org.Name, "quota_limit": req.QuotaAmount},
		"admin_username": req.AdminUsername,
	})
}

// GetOrg GET /api/platform/orgs/:id：公司详情 + 额度流水 + 成员一览
func (h *Handler) GetOrg(c *gin.Context) {
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	var org model.Org
	if err := h.DB.Where("id = ?", id).First(&org).Error; err != nil {
		httpx.Fail(c, http.StatusNotFound, "公司不存在")
		return
	}
	var grants []model.QuotaGrant
	_ = h.DB.Where("subject_type = 'org' AND subject_id = ?", id).
		Order("id DESC").Limit(50).Find(&grants).Error
	var users []model.User
	_ = h.DB.Where("org_id = ?", id).Order("id").Find(&users).Error
	if grants == nil {
		grants = []model.QuotaGrant{}
	}
	if users == nil {
		users = []model.User{}
	}
	httpx.OK(c, gin.H{"org": org, "quota_grants": grants, "users": users})
}

// UpdateOrg PUT /api/platform/orgs/:id
func (h *Handler) UpdateOrg(c *gin.Context) {
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	var req struct {
		Name   *string `json:"name"`
		Remark *string `json:"remark"`
		Status *int    `json:"status"`
	}
	if !httpx.BindJSON(c, &req) {
		return
	}
	var org model.Org
	if err := h.DB.Where("id = ?", id).First(&org).Error; err != nil {
		httpx.Fail(c, http.StatusNotFound, "公司不存在")
		return
	}
	updates := map[string]any{"updated_at": time.Now().Unix()}
	if req.Name != nil && *req.Name != "" {
		if *req.Name != org.Name {
			var cnt int64
			_ = h.DB.Model(&model.Org{}).Where("name = ? AND id != ?", *req.Name, id).Count(&cnt).Error
			if cnt > 0 {
				httpx.Fail(c, http.StatusBadRequest, "公司名已存在")
				return
			}
		}
		updates["name"] = *req.Name
	}
	if req.Remark != nil {
		updates["remark"] = *req.Remark
	}
	if req.Status != nil {
		if *req.Status != 0 && *req.Status != 1 {
			httpx.Fail(c, http.StatusBadRequest, "status 只能为 0 或 1")
			return
		}
		updates["status"] = *req.Status
	}
	if err := h.DB.Model(&model.Org{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "更新失败")
		return
	}
	httpx.OK(c, gin.H{"message": "已更新"})
}

// DeleteOrg DELETE /api/platform/orgs/:id（级联删除公司账号与密钥；日志保留）
func (h *Handler) DeleteOrg(c *gin.Context) {
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	res := h.DB.Where("id = ?", id).Delete(&model.Org{})
	if res.Error != nil {
		httpx.Fail(c, http.StatusInternalServerError, "删除失败")
		return
	}
	if res.RowsAffected == 0 {
		httpx.Fail(c, http.StatusNotFound, "公司不存在")
		return
	}
	httpx.OK(c, gin.H{"message": "已删除"})
}

// AddOrgQuota POST /api/platform/orgs/:id/quota
func (h *Handler) AddOrgQuota(c *gin.Context) {
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
	if err := service.AddOrgQuota(h.DB, id, req.Amount, middleware.GetUID(c), req.Remark); err != nil {
		if err == service.ErrNotFound {
			httpx.Fail(c, http.StatusNotFound, "公司不存在")
			return
		}
		httpx.Fail(c, http.StatusInternalServerError, "追加额度失败")
		return
	}
	httpx.OK(c, gin.H{"message": fmt.Sprintf("已追加 %d 点", req.Amount)})
}

// ListOrgUsers GET /api/platform/orgs/:id/users
func (h *Handler) ListOrgUsers(c *gin.Context) {
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	var users []model.User
	_ = h.DB.Where("org_id = ?", id).Order("id").Find(&users).Error
	if users == nil {
		users = []model.User{}
	}
	httpx.OK(c, users)
}

// ---------------- 渠道管理 ----------------

type abilityReq struct {
	ModelName         string  `json:"model_name" binding:"required"`
	UpstreamModelName *string `json:"upstream_model_name"`
}

type channelReq struct {
	Name        string        `json:"name" binding:"required"`
	Vendor      string        `json:"vendor"`
	BaseURL     string        `json:"base_url" binding:"required"`
	Path        string        `json:"path"`
	UpstreamKey string        `json:"upstream_key"`
	Weight      int           `json:"weight"`
	Priority    int           `json:"priority"`
	Status      *int          `json:"status"`
	Remark      string        `json:"remark"`
	Models      []abilityReq  `json:"models" binding:"required,min=1"`
}

func (r *channelReq) pathOrDefault() string {
	if r.Path == "" {
		return "/v1/chat/completions"
	}
	return r.Path
}

func abilityModels(models []abilityReq) []model.ChannelAbility {
	out := make([]model.ChannelAbility, 0, len(models))
	for _, m := range models {
		out = append(out, model.ChannelAbility{ModelName: m.ModelName, UpstreamModelName: m.UpstreamModelName})
	}
	return out
}

// ListChannels GET /api/platform/channels
func (h *Handler) ListChannels(c *gin.Context) {
	var channels []model.Channel
	_ = h.DB.Order("id").Find(&channels).Error
	if channels == nil {
		channels = []model.Channel{}
	}
	type abilityRow struct {
		ChannelID         int64   `json:"channel_id"`
		ModelName         string  `json:"model_name"`
		UpstreamModelName *string `json:"upstream_model_name"`
	}
	var abilities []abilityRow
	_ = h.DB.Raw("SELECT channel_id, model_name, upstream_model_name FROM channel_abilities ORDER BY model_name").Scan(&abilities).Error
	byChannel := map[int64][]abilityRow{}
	for _, a := range abilities {
		byChannel[a.ChannelID] = append(byChannel[a.ChannelID], a)
	}
	list := make([]gin.H, 0, len(channels))
	for _, ch := range channels {
		list = append(list, gin.H{
			"id": ch.ID, "name": ch.Name, "vendor": ch.Vendor,
			"base_url": ch.BaseURL, "path": ch.Path,
			"has_key": ch.UpstreamKeyEnc != "",
			"weight": ch.Weight, "priority": ch.Priority, "status": ch.Status,
			"last_test_at": ch.LastTestAt, "last_test_ok": ch.LastTestOk,
			"remark": ch.Remark, "created_at": ch.CreatedAt,
			"models": byChannel[ch.ID],
		})
	}
	httpx.OK(c, list)
}

// CreateChannel POST /api/platform/channels
func (h *Handler) CreateChannel(c *gin.Context) {
	var req channelReq
	if !httpx.BindJSON(c, &req) {
		return
	}
	var cnt int64
	_ = h.DB.Model(&model.Channel{}).Where("name = ?", req.Name).Count(&cnt).Error
	if cnt > 0 {
		httpx.Fail(c, http.StatusBadRequest, "渠道名已存在")
		return
	}
	status := 1
	if req.Status != nil {
		status = *req.Status
	}
	enc, err := h.Cipher.Encrypt(req.UpstreamKey)
	if err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "密钥加密失败")
		return
	}
	ch := model.Channel{
		Name: req.Name, Vendor: req.Vendor, BaseURL: req.BaseURL,
		Path: req.pathOrDefault(), UpstreamKeyEnc: enc,
		Weight: maxInt(req.Weight, 1), Priority: req.Priority,
		Status: status, Remark: req.Remark,
	}
	err = h.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&ch).Error; err != nil {
			return err
		}
		for i := range req.Models {
			ab := abilityModels(req.Models)[i]
			ab.ChannelID = ch.ID
			if err := tx.Create(&ab).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "创建渠道失败: "+err.Error())
		return
	}
	httpx.OK(c, gin.H{"id": ch.ID, "message": "渠道已创建"})
}

// GetChannel GET /api/platform/channels/:id
func (h *Handler) GetChannel(c *gin.Context) {
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	var ch model.Channel
	if err := h.DB.Where("id = ?", id).First(&ch).Error; err != nil {
		httpx.Fail(c, http.StatusNotFound, "渠道不存在")
		return
	}
	var abilities []model.ChannelAbility
	_ = h.DB.Where("channel_id = ?", id).Order("model_name").Find(&abilities).Error
	if abilities == nil {
		abilities = []model.ChannelAbility{}
	}
	httpx.OK(c, gin.H{
		"id": ch.ID, "name": ch.Name, "vendor": ch.Vendor,
		"base_url": ch.BaseURL, "path": ch.Path,
		"has_key": ch.UpstreamKeyEnc != "",
		"weight": ch.Weight, "priority": ch.Priority, "status": ch.Status,
		"remark": ch.Remark, "models": abilities,
	})
}

// UpdateChannel PUT /api/platform/channels/:id（upstream_key 留空 = 不修改）
func (h *Handler) UpdateChannel(c *gin.Context) {
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	var req channelReq
	if !httpx.BindJSON(c, &req) {
		return
	}
	var ch model.Channel
	if err := h.DB.Where("id = ?", id).First(&ch).Error; err != nil {
		httpx.Fail(c, http.StatusNotFound, "渠道不存在")
		return
	}
	if req.Name != ch.Name {
		var cnt int64
		_ = h.DB.Model(&model.Channel{}).Where("name = ? AND id != ?", req.Name, id).Count(&cnt).Error
		if cnt > 0 {
			httpx.Fail(c, http.StatusBadRequest, "渠道名已存在")
			return
		}
	}
	updates := map[string]any{
		"name": req.Name, "vendor": req.Vendor, "base_url": req.BaseURL,
		"path": req.pathOrDefault(), "weight": maxInt(req.Weight, 1),
		"priority": req.Priority, "remark": req.Remark, "updated_at": time.Now().Unix(),
	}
	if req.Status != nil {
		updates["status"] = *req.Status
	}
	if req.UpstreamKey != "" {
		enc, err := h.Cipher.Encrypt(req.UpstreamKey)
		if err != nil {
			httpx.Fail(c, http.StatusInternalServerError, "密钥加密失败")
			return
		}
		updates["upstream_key_enc"] = enc
	}
	err := h.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.Channel{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			return err
		}
		// 整体替换 abilities
		if err := tx.Where("channel_id = ?", id).Delete(&model.ChannelAbility{}).Error; err != nil {
			return err
		}
		for _, ab := range abilityModels(req.Models) {
			ab.ChannelID = id
			if err := tx.Create(&ab).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "更新渠道失败: "+err.Error())
		return
	}
	httpx.OK(c, gin.H{"message": "渠道已更新"})
}

// UpdateChannelStatus PUT /api/platform/channels/:id/status
func (h *Handler) UpdateChannelStatus(c *gin.Context) {
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	// 整型 status 用指针接收：gin 的 required 会把字面 0 当空值拒绝（同 org keys）
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
	res := h.DB.Exec("UPDATE channels SET status = ?, updated_at = ? WHERE id = ?",
		*req.Status, time.Now().Unix(), id)
	if res.Error != nil || res.RowsAffected == 0 {
		httpx.Fail(c, http.StatusNotFound, "渠道不存在")
		return
	}
	httpx.OK(c, gin.H{"message": "已更新"})
}

// DeleteChannel DELETE /api/platform/channels/:id
func (h *Handler) DeleteChannel(c *gin.Context) {
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	res := h.DB.Where("id = ?", id).Delete(&model.Channel{})
	if res.Error != nil {
		httpx.Fail(c, http.StatusInternalServerError, "删除失败")
		return
	}
	if res.RowsAffected == 0 {
		httpx.Fail(c, http.StatusNotFound, "渠道不存在")
		return
	}
	httpx.OK(c, gin.H{"message": "已删除"})
}

// TestChannel POST /api/platform/channels/:id/test：实发一次 max_tokens=1 请求测连通
func (h *Handler) TestChannel(c *gin.Context) {
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	var ch model.Channel
	if err := h.DB.Where("id = ?", id).First(&ch).Error; err != nil {
		httpx.Fail(c, http.StatusNotFound, "渠道不存在")
		return
	}
	var ab model.ChannelAbility
	if err := h.DB.Where("channel_id = ?", id).Order("model_name").First(&ab).Error; err != nil || ab.ModelName == "" {
		httpx.Fail(c, http.StatusBadRequest, "渠道未配置模型，无法测试")
		return
	}
	key, err := h.Cipher.Decrypt(ch.UpstreamKeyEnc)
	if err != nil || key == "" {
		httpx.Fail(c, http.StatusBadRequest, "渠道未配置上游密钥")
		return
	}
	upModel := ab.ModelName
	if ab.UpstreamModelName != nil && *ab.UpstreamModelName != "" {
		upModel = *ab.UpstreamModelName
	}
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"ping"}],"max_tokens":1,"stream":false}`, upModel)
	url := strings.TrimRight(ch.BaseURL, "/") + ch.Path
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)

	start := time.Now()
	resp, derr := h.Client.Do(req)
	latency := time.Since(start).Milliseconds()
	okFlag, errMsg := false, ""
	statusCode := 0
	if derr != nil {
		errMsg = derr.Error()
	} else {
		statusCode = resp.StatusCode
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		_ = resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			okFlag = true
		} else {
			errMsg = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, truncateStr(string(data), 300))
		}
	}
	_ = h.DB.Exec("UPDATE channels SET last_test_at = ?, last_test_ok = ?, updated_at = ? WHERE id = ?",
		time.Now().Unix(), b2i(okFlag), time.Now().Unix(), id).Error
	httpx.OK(c, gin.H{"ok": okFlag, "status": statusCode, "latency_ms": latency, "error": errMsg})
}

// ---------------- 模型与定价 ----------------

type modelReq struct {
	Name        string `json:"name" binding:"required"`
	DisplayName string `json:"display_name"`
	Vendor      string `json:"vendor"`
	InputPrice  int64  `json:"input_price"`
	OutputPrice int64  `json:"output_price"`
	Status      *int   `json:"status"`
	Remark      string `json:"remark"`
}

// ListModels GET /api/platform/models
func (h *Handler) ListModels(c *gin.Context) {
	var models []model.Model
	_ = h.DB.Order("name").Find(&models).Error
	if models == nil {
		models = []model.Model{}
	}
	httpx.OK(c, models)
}

// CreateModel POST /api/platform/models
func (h *Handler) CreateModel(c *gin.Context) {
	var req modelReq
	if !httpx.BindJSON(c, &req) {
		return
	}
	if req.InputPrice < 0 || req.OutputPrice < 0 {
		httpx.Fail(c, http.StatusBadRequest, "价格不能为负")
		return
	}
	var cnt int64
	_ = h.DB.Model(&model.Model{}).Where("name = ?", req.Name).Count(&cnt).Error
	if cnt > 0 {
		httpx.Fail(c, http.StatusBadRequest, "模型名已存在")
		return
	}
	status := 1
	if req.Status != nil {
		status = *req.Status
	}
	m := model.Model{
		Name: req.Name, DisplayName: req.DisplayName, Vendor: req.Vendor,
		InputPrice: req.InputPrice, OutputPrice: req.OutputPrice,
		Status: status, Remark: req.Remark,
	}
	if err := h.DB.Create(&m).Error; err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "创建模型失败")
		return
	}
	httpx.OK(c, m)
}

// UpdateModel PUT /api/platform/models/:id（name 是路由键，不允许修改）
func (h *Handler) UpdateModel(c *gin.Context) {
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	var req struct {
		DisplayName string `json:"display_name"`
		Vendor      string `json:"vendor"`
		InputPrice  *int64 `json:"input_price" binding:"required,min=0"`
		OutputPrice *int64 `json:"output_price" binding:"required,min=0"`
		Status      *int   `json:"status"`
		Remark      string `json:"remark"`
	}
	if !httpx.BindJSON(c, &req) {
		return
	}
	updates := map[string]any{
		"display_name": req.DisplayName, "vendor": req.Vendor,
		"input_price": *req.InputPrice, "output_price": *req.OutputPrice,
		"remark": req.Remark, "updated_at": time.Now().Unix(),
	}
	if req.Status != nil {
		updates["status"] = *req.Status
	}
	res := h.DB.Model(&model.Model{}).Where("id = ?", id).Updates(updates)
	if res.Error != nil || res.RowsAffected == 0 {
		httpx.Fail(c, http.StatusNotFound, "模型不存在")
		return
	}
	httpx.OK(c, gin.H{"message": "已更新"})
}

// DeleteModel DELETE /api/platform/models/:id（同时清理渠道能力与用户授权）
func (h *Handler) DeleteModel(c *gin.Context) {
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	var m model.Model
	if err := h.DB.Where("id = ?", id).First(&m).Error; err != nil {
		httpx.Fail(c, http.StatusNotFound, "模型不存在")
		return
	}
	err := h.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("model_name = ?", m.Name).Delete(&model.ChannelAbility{}).Error; err != nil {
			return err
		}
		if err := tx.Where("model_name = ?", m.Name).Delete(&model.UserModelGrant{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", id).Delete(&model.Model{}).Error
	})
	if err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "删除失败")
		return
	}
	httpx.OK(c, gin.H{"message": "已删除"})
}

// ---------------- 统计与日志 ----------------

// StatsOverview GET /api/platform/stats/overview
func (h *Handler) StatsOverview(c *gin.Context) {
	ov, err := service.StatsOverview(h.DB, service.Scope{})
	if err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "统计查询失败")
		return
	}
	httpx.OK(c, ov)
}

// ListUsage GET /api/platform/usage：全站调用日志（多维筛选分页）
func (h *Handler) ListUsage(c *gin.Context) {
	page, size, offset := httpx.PageParams(c)
	cond, args := "1=1", []any{}
	if v := httpx.QueryInt64(c, "org_id", 0); v > 0 {
		cond += " AND l.org_id = ?"
		args = append(args, v)
	}
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
		OrgName  string `json:"org_name"`
	}
	var rows []usageRow
	_ = h.DB.Raw(fmt.Sprintf(`
		SELECT l.*, u.username, o.name AS org_name
		FROM usage_logs l
		LEFT JOIN users u ON u.id = l.user_id
		LEFT JOIN orgs o ON o.id = l.org_id
		WHERE %s ORDER BY l.id DESC LIMIT ? OFFSET ?`, cond),
		append(args, size, offset)...).Scan(&rows).Error
	if rows == nil {
		rows = []usageRow{}
	}
	httpx.PageResult(c, rows, total, page, size)
}

func opID(id int64) *int64 {
	if id == 0 {
		return nil
	}
	return &id
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
