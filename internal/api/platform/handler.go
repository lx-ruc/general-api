package platform

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"token-gateway/internal/auth"
	"token-gateway/internal/crypto"
	"token-gateway/internal/gateway"
	"token-gateway/internal/httpx"
	"token-gateway/internal/middleware"
	"token-gateway/internal/model"
	"token-gateway/internal/service"
)

type Handler struct {
	DB      *gorm.DB
	Cipher  *crypto.Cipher
	Client  *http.Client
	Breaker *gateway.Breaker // 手动启用渠道时清零熔断连续失败计数
}

func NewHandler(db *gorm.DB, cipher *crypto.Cipher, client *http.Client, breaker *gateway.Breaker) *Handler {
	return &Handler{DB: db, Cipher: cipher, Client: client, Breaker: breaker}
}

// ---------------- 客户管理 ----------------

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
	ContactName      string `json:"contact_name"`  // 客户联系人
	ContactPhone     string `json:"contact_phone"` // 联系电话
	QuotaAmount      int64  `json:"quota_amount"`  // 初始额度（token 预算），可为 0
	AdminUsername    string `json:"admin_username" binding:"required,min=3"`
	AdminPassword    string `json:"admin_password" binding:"required,min=6"`
	AdminDisplayName string `json:"admin_display_name"`
}

// CreateOrg POST /api/platform/orgs：创建客户 + 首任客户管理员
func (h *Handler) CreateOrg(c *gin.Context) {
	var req createOrgReq
	if !httpx.BindJSON(c, &req) {
		return
	}
	var cnt int64
	_ = h.DB.Model(&model.Org{}).Where("name = ?", req.Name).Count(&cnt).Error
	if cnt > 0 {
		httpx.Fail(c, http.StatusBadRequest, "客户名已存在")
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
		org = model.Org{Name: req.Name, Remark: req.Remark,
			ContactName: req.ContactName, ContactPhone: req.ContactPhone, QuotaLimit: 0, Status: 1}
		if err := tx.Create(&org).Error; err != nil {
			return err
		}
		if req.QuotaAmount != 0 {
			if err := tx.Exec("UPDATE orgs SET quota_limit = ? WHERE id = ?", req.QuotaAmount, org.ID).Error; err != nil {
				return err
			}
			if err := tx.Create(&model.QuotaGrant{
				SubjectType: "org", SubjectID: org.ID, Amount: req.QuotaAmount,
				Remark: "创建客户初始额度", OperatorID: opID(middleware.GetUID(c)), CreatedAt: now,
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
		httpx.Fail(c, http.StatusInternalServerError, "创建客户失败: "+err.Error())
		return
	}
	httpx.OK(c, gin.H{
		"org":            gin.H{"id": org.ID, "name": org.Name, "quota_limit": req.QuotaAmount},
		"admin_username": req.AdminUsername,
	})
}

// GetOrg GET /api/platform/orgs/:id：客户详情 + 额度流水 + 成员一览
func (h *Handler) GetOrg(c *gin.Context) {
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	var org model.Org
	if err := h.DB.Where("id = ?", id).First(&org).Error; err != nil {
		httpx.Fail(c, http.StatusNotFound, "客户不存在")
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
		Name         *string `json:"name"`
		Remark       *string `json:"remark"`
		ContactName  *string `json:"contact_name"`
		ContactPhone *string `json:"contact_phone"`
		MonthlyQuota *int64  `json:"monthly_quota"` // 单月消费上限（点）；0=不限
		Status       *int    `json:"status"`
	}
	if !httpx.BindJSON(c, &req) {
		return
	}
	var org model.Org
	if err := h.DB.Where("id = ?", id).First(&org).Error; err != nil {
		httpx.Fail(c, http.StatusNotFound, "客户不存在")
		return
	}
	updates := map[string]any{"updated_at": time.Now().Unix()}
	if req.Name != nil && *req.Name != "" {
		if *req.Name != org.Name {
			var cnt int64
			_ = h.DB.Model(&model.Org{}).Where("name = ? AND id != ?", *req.Name, id).Count(&cnt).Error
			if cnt > 0 {
				httpx.Fail(c, http.StatusBadRequest, "客户名已存在")
				return
			}
		}
		updates["name"] = *req.Name
	}
	if req.Remark != nil {
		updates["remark"] = *req.Remark
	}
	if req.ContactName != nil {
		updates["contact_name"] = *req.ContactName
	}
	if req.ContactPhone != nil {
		updates["contact_phone"] = *req.ContactPhone
	}
	if req.MonthlyQuota != nil {
		if *req.MonthlyQuota < 0 {
			httpx.Fail(c, http.StatusBadRequest, "monthly_quota 不能为负（0=不限）")
			return
		}
		updates["monthly_quota"] = *req.MonthlyQuota
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

// DeleteOrg DELETE /api/platform/orgs/:id（级联删除客户账号与密钥；日志保留）
func (h *Handler) DeleteOrg(c *gin.Context) {
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	// 级联清理：客户 + 其全部用户 + 用户的 API key 同事务删除；
	// usage_logs / quota_grants 为历史账单与审计流水，保留不动
	err := h.DB.Transaction(func(tx *gorm.DB) error {
		res := tx.Where("id = ?", id).Delete(&model.Org{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		if err := tx.Exec("DELETE FROM api_keys WHERE user_id IN (SELECT id FROM users WHERE org_id = ?)", id).Error; err != nil {
			return err
		}
		return tx.Where("org_id = ?", id).Delete(&model.User{}).Error
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httpx.Fail(c, http.StatusNotFound, "客户不存在")
			return
		}
		httpx.Fail(c, http.StatusInternalServerError, "删除失败")
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
			httpx.Fail(c, http.StatusNotFound, "客户不存在")
			return
		}
		httpx.Fail(c, http.StatusInternalServerError, "追加额度失败")
		return
	}
	// 邮件通知客户管理员具体的授权信息（异步；未配置 SMTP 走日志）
	notifyNote := notifyOrgQuota(h.DB, id, req.Amount, req.Remark)
	httpx.OK(c, gin.H{"message": fmt.Sprintf("已追加 %d token%s", req.Amount, notifyNote)})
}

// notifyOrgQuota 组装授权详情并通知；返回附在响应里的通知状态说明
func notifyOrgQuota(db *gorm.DB, orgID, amount int64, remark string) string {
	var org model.Org
	if err := db.Where("id = ?", orgID).First(&org).Error; err != nil {
		return ""
	}
	subject := "「token 中转站」你的客户额度已更新"
	body := service.QuotaGrantEmailBody(org.Name, org.Name+" 管理员",
		amount, org.QuotaLimit, org.QuotaUsed, remark)
	sent := service.NotifyOrgAdmins(db, orgID, subject, body)
	switch {
	case sent > 0:
		return fmt.Sprintf("，已邮件通知 %d 位客户管理员", sent)
	case service.MailerConfigured():
		return "（该客户管理员未留邮箱，未发送通知）"
	default:
		return "（SMTP 未配置，通知内容已写入服务日志）"
	}
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

// OrgStats GET /api/platform/orgs/:id/stats：该客户用量统计（含每个模型的用量明细）
func (h *Handler) OrgStats(c *gin.Context) {
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	var cnt int64
	_ = h.DB.Model(&model.Org{}).Where("id = ?", id).Count(&cnt).Error
	if cnt == 0 {
		httpx.Fail(c, http.StatusNotFound, "客户不存在")
		return
	}
	ov, err := service.StatsOverview(h.DB, service.Scope{OrgID: &id})
	if err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "统计查询失败")
		return
	}
	httpx.OK(c, ov)
}

// ResetOrgAdminPassword POST /api/platform/orgs/:id/reset-admin-password
// 系统管理员重置客户管理员密码（忘记密码时的唯一恢复路径）；user_id 为空时取首任管理员
func (h *Handler) ResetOrgAdminPassword(c *gin.Context) {
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	var req struct {
		UserID      int64  `json:"user_id"`
		NewPassword string `json:"new_password" binding:"required,min=6"`
	}
	if !httpx.BindJSON(c, &req) {
		return
	}
	q := h.DB.Where("org_id = ? AND role = ?", id, model.RoleOrgAdmin)
	if req.UserID > 0 {
		q = q.Where("id = ?", req.UserID)
	}
	var u model.User
	if err := q.Order("id").First(&u).Error; err != nil || u.ID == 0 {
		httpx.Fail(c, http.StatusNotFound, "该客户还没有管理员账号")
		return
	}
	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "密码加密失败")
		return
	}
	if err := h.DB.Exec("UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?",
		hash, time.Now().Unix(), u.ID).Error; err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "重置失败")
		return
	}
	httpx.OK(c, gin.H{"message": "密码已重置，请立即告知对方", "username": u.Username})
}

// ---------------- 渠道管理 ----------------

type abilityReq struct {
	ModelName         string  `json:"model_name" binding:"required"`
	UpstreamModelName *string `json:"upstream_model_name"`
}

type channelReq struct {
	Name        string       `json:"name" binding:"required"`
	Vendor      string       `json:"vendor"`
	BaseURL     string       `json:"base_url" binding:"required"`
	Path        string       `json:"path"`
	UpstreamKey string       `json:"upstream_key"` // 多行：每行一把 key，可后缀 :权重；留空=不修改
	Weight      int          `json:"weight"`
	Priority    int          `json:"priority"`
	Status      *int         `json:"status"`
	Remark      string       `json:"remark"`
	Models      []abilityReq `json:"models" binding:"required,min=1"`
}

func (r *channelReq) pathOrDefault() string {
	if r.Path == "" {
		return "/v1/chat/completions"
	}
	return r.Path
}

type keyLine struct {
	Key    string
	Weight int
}

// weightSuffix 行尾 :数字 才解析为权重（GLM 等 key 自身含 . 不受影响）
var weightSuffix = regexp.MustCompile(`:(\d+)$`)

// parseKeyLines 解析多行 upstream_key：split → trim 去空行 → 明文去重（AES-GCM nonce
// 随机、密文去重无效）→ 每行 key 或 key:权重（权重<=0 按 1）
func parseKeyLines(s string) []keyLine {
	seen := map[string]bool{}
	out := []keyLine{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		w := 1
		if m := weightSuffix.FindStringSubmatch(line); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
				w = n
				line = strings.TrimSpace(line[:len(line)-len(m[0])])
			}
		}
		if line == "" || seen[line] {
			continue
		}
		seen[line] = true
		out = append(out, keyLine{Key: line, Weight: w})
	}
	return out
}

// buildKeyPool 把多行明文 key 加密为待入库的 Key 池
func (h *Handler) buildKeyPool(channelID int64, s string) ([]model.ChannelKey, error) {
	lines := parseKeyLines(s)
	out := make([]model.ChannelKey, 0, len(lines))
	now := time.Now().Unix()
	for _, l := range lines {
		enc, err := h.Cipher.Encrypt(l.Key)
		if err != nil {
			return nil, err
		}
		out = append(out, model.ChannelKey{
			ChannelID: channelID, KeyEnc: enc, Weight: l.Weight, Status: 1, CreatedAt: now, UpdatedAt: now,
		})
	}
	return out, nil
}

// keyCounts 各渠道 Key 池统计（SQLite/PG 均可移植的 CASE 写法）
func (h *Handler) keyCounts() map[int64]struct{ Total, Active int64 } {
	var rows []struct {
		ChannelID int64
		Total     int64
		Active    int64
	}
	_ = h.DB.Raw(`SELECT channel_id, COUNT(*) AS total,
		SUM(CASE WHEN status = 1 THEN 1 ELSE 0 END) AS active
		FROM channel_keys GROUP BY channel_id`).Scan(&rows).Error
	out := make(map[int64]struct{ Total, Active int64 }, len(rows))
	for _, r := range rows {
		out[r.ChannelID] = struct{ Total, Active int64 }{r.Total, r.Active}
	}
	return out
}

// hasKey 渠道是否有可用密钥：池内有启用 Key，或 legacy 单 Key 密文存在
func hasKey(enc string, active int64) bool {
	return active > 0 || enc != ""
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
	counts := h.keyCounts()
	for _, ch := range channels {
		kc := counts[ch.ID]
		// legacy 单 Key 计入展示数量（池为空且存在密文时）
		keyCount, keyActive := kc.Total, kc.Active
		if keyCount == 0 && ch.UpstreamKeyEnc != "" {
			keyCount, keyActive = 1, 1
		}
		list = append(list, gin.H{
			"id": ch.ID, "name": ch.Name, "vendor": ch.Vendor,
			"base_url": ch.BaseURL, "path": ch.Path,
			"has_key":   hasKey(ch.UpstreamKeyEnc, kc.Active),
			"key_count": keyCount, "key_active_count": keyActive,
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
	pool, err := h.buildKeyPool(0, req.UpstreamKey)
	if err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "密钥加密失败")
		return
	}
	ch := model.Channel{
		Name: req.Name, Vendor: req.Vendor, BaseURL: req.BaseURL,
		Path:   req.pathOrDefault(),
		Weight: maxInt(req.Weight, 1), Priority: req.Priority,
		Status: status, Remark: req.Remark,
	}
	err = h.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&ch).Error; err != nil {
			return err
		}
		for i := range pool {
			pool[i].ChannelID = ch.ID
			if err := tx.Create(&pool[i]).Error; err != nil {
				return err
			}
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
	httpx.OK(c, gin.H{"id": ch.ID, "message": "渠道已创建", "key_count": len(pool)})
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
	kc := h.keyCounts()[ch.ID]
	keyCount, keyActive := kc.Total, kc.Active
	if keyCount == 0 && ch.UpstreamKeyEnc != "" {
		keyCount, keyActive = 1, 1
	}
	httpx.OK(c, gin.H{
		"id": ch.ID, "name": ch.Name, "vendor": ch.Vendor,
		"base_url": ch.BaseURL, "path": ch.Path,
		"has_key":   hasKey(ch.UpstreamKeyEnc, kc.Active),
		"key_count": keyCount, "key_active_count": keyActive,
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
	// upstream_key 非空 = 整池替换（多行 key）并同时置空 legacy 单 Key；留空 = 不修改
	replacePool := req.UpstreamKey != ""
	var pool []model.ChannelKey
	if replacePool {
		var perr error
		if pool, perr = h.buildKeyPool(id, req.UpstreamKey); perr != nil {
			httpx.Fail(c, http.StatusInternalServerError, "密钥加密失败")
			return
		}
		updates["upstream_key_enc"] = ""
	}
	err := h.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.Channel{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			return err
		}
		if replacePool {
			if err := tx.Where("channel_id = ?", id).Delete(&model.ChannelKey{}).Error; err != nil {
				return err
			}
			for i := range pool {
				if err := tx.Create(&pool[i]).Error; err != nil {
					return err
				}
			}
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
	msg := "渠道已更新"
	if replacePool {
		msg = fmt.Sprintf("渠道已更新（Key 池替换为 %d 把）", len(pool))
	}
	httpx.OK(c, gin.H{"message": msg})
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
	// 人工启停均清除"系统熔断"标记：手动禁用表达管理员意图，永不被自动探测重新启用；
	// 手动启用则清零熔断连续失败计数，避免恢复后一次失败即再次熔断
	res := h.DB.Exec("UPDATE channels SET status = ?, auto_disabled_at = 0, updated_at = ? WHERE id = ?",
		*req.Status, time.Now().Unix(), id)
	if res.Error == nil && res.RowsAffected > 0 {
		if *req.Status == 1 && h.Breaker != nil {
			h.Breaker.RecordSuccess(id)
		}
	}
	if res.Error != nil || res.RowsAffected == 0 {
		httpx.Fail(c, http.StatusNotFound, "渠道不存在")
		return
	}
	httpx.OK(c, gin.H{"message": "已更新"})
}

// DeleteChannel DELETE /api/platform/channels/:id（Key 池随渠道删除）
func (h *Handler) DeleteChannel(c *gin.Context) {
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	err := h.DB.Transaction(func(tx *gorm.DB) error {
		res := tx.Where("id = ?", id).Delete(&model.Channel{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return tx.Where("channel_id = ?", id).Delete(&model.ChannelKey{}).Error
	})
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			httpx.Fail(c, http.StatusNotFound, "渠道不存在")
			return
		}
		httpx.Fail(c, http.StatusInternalServerError, "删除失败")
		return
	}
	httpx.OK(c, gin.H{"message": "已删除"})
}

// ---------------- 渠道 Key 池管理 ----------------

// maskKey 打码展示：前 6 + … + 后 4
func maskKey(k string) string {
	if k == "" {
		return ""
	}
	if len(k) <= 12 {
		return k[:3] + "…"
	}
	return k[:6] + "…" + k[len(k)-4:]
}

// ListChannelKeys GET /api/platform/channels/:id/keys：Key 池列表（打码，不回明文/密文）
func (h *Handler) ListChannelKeys(c *gin.Context) {
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	var keys []model.ChannelKey
	if err := h.DB.Where("channel_id = ?", id).Order("id").Find(&keys).Error; err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "查询失败")
		return
	}
	list := make([]gin.H, 0, len(keys))
	for _, k := range keys {
		plain, derr := h.Cipher.Decrypt(k.KeyEnc)
		if derr != nil {
			plain = ""
		}
		list = append(list, gin.H{
			"id": k.ID, "key_masked": maskKey(plain), "weight": k.Weight,
			"status": k.Status, "remark": k.Remark,
			"created_at": k.CreatedAt, "updated_at": k.UpdatedAt,
		})
	}
	httpx.OK(c, list)
}

// UpdateChannelKeyStatus PUT /api/platform/channels/:id/keys/:kid/status
// 401 自动禁用后的恢复路径；重新启用时清掉自动禁用备注
func (h *Handler) UpdateChannelKeyStatus(c *gin.Context) {
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	kid, err := strconv.ParseInt(c.Param("kid"), 10, 64)
	if err != nil || kid <= 0 {
		httpx.Fail(c, http.StatusBadRequest, "invalid key id")
		return
	}
	// 整型 status 用指针接收：gin 的 required 会把字面 0 当空值拒绝（同渠道 status）
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
	res := h.DB.Exec(`UPDATE channel_keys SET status = ?,
		remark = CASE WHEN ? = 1 THEN '' ELSE remark END, updated_at = ?
		WHERE id = ? AND channel_id = ?`,
		*req.Status, *req.Status, time.Now().Unix(), kid, id)
	if res.Error != nil || res.RowsAffected == 0 {
		httpx.Fail(c, http.StatusNotFound, "Key 不存在")
		return
	}
	httpx.OK(c, gin.H{"message": "已更新"})
}

// AddChannelKeys POST /api/platform/channels/:id/keys：向 Key 池追加（单把或批量，不覆盖现有）
// weight 对整批生效（<=0 按 1）；追加入池即置空 legacy 单 Key（池成为唯一密钥来源）
func (h *Handler) AddChannelKeys(c *gin.Context) {
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	var req struct {
		Keys   []string `json:"keys" binding:"required,min=1"`
		Weight int      `json:"weight"`
	}
	if !httpx.BindJSON(c, &req) {
		return
	}
	weight := req.Weight
	if weight <= 0 {
		weight = 1
	}
	// 明文去重（仅限本次提交内；池内既有密文因 AES-GCM nonce 随机不可比对）
	seen := map[string]bool{}
	rows := make([]model.ChannelKey, 0, len(req.Keys))
	now := time.Now().Unix()
	for _, k := range req.Keys {
		k = strings.TrimSpace(k)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		enc, err := h.Cipher.Encrypt(k)
		if err != nil {
			httpx.Fail(c, http.StatusInternalServerError, "密钥加密失败")
			return
		}
		rows = append(rows, model.ChannelKey{
			ChannelID: id, KeyEnc: enc, Weight: weight, Status: 1, CreatedAt: now, UpdatedAt: now,
		})
	}
	if len(rows) == 0 {
		httpx.Fail(c, http.StatusBadRequest, "没有可新增的 Key")
		return
	}
	var cnt int64
	if err := h.DB.Model(&model.Channel{}).Where("id = ?", id).Count(&cnt).Error; err != nil || cnt == 0 {
		httpx.Fail(c, http.StatusNotFound, "渠道不存在")
		return
	}
	err := h.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&rows).Error; err != nil {
			return err
		}
		return tx.Exec("UPDATE channels SET upstream_key_enc = '', updated_at = ? WHERE id = ?", now, id).Error
	})
	if err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "保存失败")
		return
	}
	httpx.OK(c, gin.H{"added": len(rows)})
}

// DeleteChannelKey DELETE /api/platform/channels/:id/keys/:kid：从池中彻底移除（禁用可恢复，删除不可）
func (h *Handler) DeleteChannelKey(c *gin.Context) {
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	kid, err := strconv.ParseInt(c.Param("kid"), 10, 64)
	if err != nil || kid <= 0 {
		httpx.Fail(c, http.StatusBadRequest, "invalid key id")
		return
	}
	res := h.DB.Exec("DELETE FROM channel_keys WHERE id = ? AND channel_id = ?", kid, id)
	if res.Error != nil || res.RowsAffected == 0 {
		httpx.Fail(c, http.StatusNotFound, "Key 不存在")
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
	// Key 选择与数据面一致：池内第一把启用 Key → 回退 legacy 单 Key
	key, keyDesc := "", ""
	var poolKey model.ChannelKey
	if h.DB.Where("channel_id = ? AND status = 1", id).Order("id").First(&poolKey).Error == nil {
		key, _ = h.Cipher.Decrypt(poolKey.KeyEnc)
		keyDesc = fmt.Sprintf("池内 Key #%d", poolKey.ID)
	} else if ch.UpstreamKeyEnc != "" {
		key, _ = h.Cipher.Decrypt(ch.UpstreamKeyEnc)
		keyDesc = "legacy 单 Key"
	}
	if key == "" {
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
	httpx.OK(c, gin.H{"ok": okFlag, "status": statusCode, "latency_ms": latency, "error": errMsg, "key": keyDesc})
}

// UpstreamModels GET /api/platform/channels/:id/upstream-models：实时拉取上游模型列表，编辑渠道时供管理员挑选
func (h *Handler) UpstreamModels(c *gin.Context) {
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	var ch model.Channel
	if err := h.DB.Where("id = ?", id).First(&ch).Error; err != nil {
		httpx.Fail(c, http.StatusNotFound, "渠道不存在")
		return
	}
	// Key 选择与数据面/测试一致：池内第一把启用 Key → 回退 legacy 单 Key
	key := ""
	var poolKey model.ChannelKey
	if h.DB.Where("channel_id = ? AND status = 1", id).Order("id").First(&poolKey).Error == nil {
		key, _ = h.Cipher.Decrypt(poolKey.KeyEnc)
	} else if ch.UpstreamKeyEnc != "" {
		key, _ = h.Cipher.Decrypt(ch.UpstreamKeyEnc)
	}
	if key == "" {
		httpx.Fail(c, http.StatusBadRequest, "渠道未配置上游密钥，无法查询模型列表")
		return
	}
	// 模型列表路径由聊天路径推导（/v1/chat/completions → /v1/models）；非常规路径回退 /v1/models
	modelsPath := "/v1/models"
	if s, found := strings.CutSuffix(ch.Path, "/chat/completions"); found {
		modelsPath = s + "/models"
	}
	req, _ := http.NewRequest(http.MethodGet, strings.TrimRight(ch.BaseURL, "/")+modelsPath, nil)
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := h.Client.Do(req)
	if err != nil {
		httpx.Fail(c, http.StatusBadGateway, "请求上游失败："+err.Error())
		return
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		httpx.Fail(c, http.StatusBadGateway, fmt.Sprintf("上游返回 HTTP %d: %s", resp.StatusCode, truncateStr(string(data), 300)))
		return
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		httpx.Fail(c, http.StatusBadGateway, "解析上游响应失败："+truncateStr(string(data), 200))
		return
	}
	names := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		if m.ID != "" {
			names = append(names, m.ID)
		}
	}
	sort.Strings(names)
	httpx.OK(c, gin.H{"models": names, "count": len(names)})
}

// ---------------- 模型与定价 ----------------

type modelReq struct {
	Name            string `json:"name" binding:"required"`
	DisplayName     string `json:"display_name"`
	Vendor          string `json:"vendor"`
	InputPrice      int64  `json:"input_price"`
	OutputPrice     int64  `json:"output_price"`
	CostInputPrice  int64  `json:"cost_input_price"`
	CostOutputPrice int64  `json:"cost_output_price"`
	Status          *int   `json:"status"`
	Remark          string `json:"remark"`
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
		CostInputPrice: req.CostInputPrice, CostOutputPrice: req.CostOutputPrice,
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
		DisplayName     string `json:"display_name"`
		Vendor          string `json:"vendor"`
		InputPrice      *int64 `json:"input_price" binding:"required,min=0"`
		OutputPrice     *int64 `json:"output_price" binding:"required,min=0"`
		CostInputPrice  *int64 `json:"cost_input_price"`
		CostOutputPrice *int64 `json:"cost_output_price"`
		Status          *int   `json:"status"`
		Remark          string `json:"remark"`
	}
	if !httpx.BindJSON(c, &req) {
		return
	}
	updates := map[string]any{
		"display_name": req.DisplayName, "vendor": req.Vendor,
		"input_price": *req.InputPrice, "output_price": *req.OutputPrice,
		"remark": req.Remark, "updated_at": time.Now().Unix(),
	}
	if req.CostInputPrice != nil {
		updates["cost_input_price"] = *req.CostInputPrice
	}
	if req.CostOutputPrice != nil {
		updates["cost_output_price"] = *req.CostOutputPrice
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

// ---------------- 充值管理 ----------------

// GetBankInfo GET /api/platform/bank-info：收款信息（客户端展示用）
func (h *Handler) GetBankInfo(c *gin.Context) {
	var v string
	_ = h.DB.Raw("SELECT value FROM settings WHERE key = 'bank_info'").Scan(&v).Error
	httpx.OK(c, gin.H{"bank_info": v})
}

// UpdateBankInfo PUT /api/platform/bank-info
func (h *Handler) UpdateBankInfo(c *gin.Context) {
	var req struct {
		BankInfo string `json:"bank_info"`
	}
	if !httpx.BindJSON(c, &req) {
		return
	}
	if err := h.DB.Exec(`
		INSERT INTO settings (key, value) VALUES ('bank_info', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, req.BankInfo).Error; err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "保存失败")
		return
	}
	httpx.OK(c, gin.H{"message": "收款信息已更新"})
}

// ListRecharges GET /api/platform/recharges：充值申请列表
func (h *Handler) ListRecharges(c *gin.Context) {
	page, size, offset := httpx.PageParams(c)
	cond, args := "r.org_id > 0", []any{}
	if st := c.Query("status"); st != "" {
		cond += " AND r.status = ?"
		args = append(args, st)
	}
	var total int64
	_ = h.DB.Raw(fmt.Sprintf("SELECT COUNT(*) FROM recharge_requests r WHERE %s", cond), args...).Scan(&total).Error
	type row struct {
		model.RechargeRequest
		OrgName string `json:"org_name"`
	}
	var rows []row
	_ = h.DB.Raw(fmt.Sprintf(`
		SELECT r.*, o.name AS org_name FROM recharge_requests r
		JOIN orgs o ON o.id = r.org_id
		WHERE %s ORDER BY r.id DESC LIMIT ? OFFSET ?`, cond),
		append(args, size, offset)...).Scan(&rows).Error
	if rows == nil {
		rows = []row{}
	}
	httpx.PageResult(c, rows, total, page, size)
}

// HandleRecharge PUT /api/platform/recharges/:id：审批（批准=自动加额度+邮件通知）
func (h *Handler) HandleRecharge(c *gin.Context) {
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
	var r model.RechargeRequest
	if err := h.DB.Where("id = ? AND status = 'pending'", id).First(&r).Error; err != nil {
		httpx.Fail(c, http.StatusNotFound, "充值申请不存在或已处理")
		return
	}
	now := time.Now().Unix()
	operator := middleware.GetUID(c)
	err := h.DB.Transaction(func(tx *gorm.DB) error {
		newStatus := "approved"
		if req.Action == "reject" {
			newStatus = "rejected"
		}
		if err := tx.Exec(`UPDATE recharge_requests SET status = ?, handled_by = ?, handled_at = ?, reply = ? WHERE id = ?`,
			newStatus, operator, now, req.Reply, id).Error; err != nil {
			return err
		}
		if req.Action == "approve" {
			if err := tx.Exec("UPDATE orgs SET quota_limit = quota_limit + ?, updated_at = ? WHERE id = ?",
				r.Amount, now, r.OrgID).Error; err != nil {
				return err
			}
			// 流水与加额度同事务，杜绝"额度已动、流水缺失"的审计断裂
			return tx.Create(&model.QuotaGrant{
				SubjectType: "org", SubjectID: r.OrgID, Amount: r.Amount,
				Remark:     fmt.Sprintf("充值申请 #%d 到账", id),
				OperatorID: opID(operator), CreatedAt: now,
			}).Error
		}
		return nil
	})
	if err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "审批失败")
		return
	}
	if req.Action == "approve" {
		notifyNote := notifyOrgQuota(h.DB, r.OrgID, r.Amount, "充值到账")
		httpx.OK(c, gin.H{"message": "已批准并到账" + notifyNote})
		return
	}
	httpx.OK(c, gin.H{"message": "已驳回"})
}

// ListAudit GET /api/platform/audit：操作审计日志
func (h *Handler) ListAudit(c *gin.Context) {
	page, size, offset := httpx.PageParams(c)
	cond, args := "1=1", []any{}
	if v := c.Query("path"); v != "" {
		cond += " AND path LIKE ?"
		args = append(args, "%"+v+"%")
	}
	var total int64
	_ = h.DB.Raw(fmt.Sprintf("SELECT COUNT(*) FROM audit_logs WHERE %s", cond), args...).Scan(&total).Error
	var rows []model.AuditLog
	_ = h.DB.Raw(fmt.Sprintf("SELECT * FROM audit_logs WHERE %s ORDER BY id DESC LIMIT ? OFFSET ?", cond),
		append(args, size, offset)...).Scan(&rows).Error
	if rows == nil {
		rows = []model.AuditLog{}
	}
	httpx.PageResult(c, rows, total, page, size)
}
