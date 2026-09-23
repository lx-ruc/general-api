package member

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

func uid(c *gin.Context) int64 { return middleware.GetUID(c) }

// ownKey 校验密钥属于当前子账号
func (h *Handler) ownKey(c *gin.Context, id int64) (*model.APIKey, bool) {
	var k model.APIKey
	if err := h.DB.Where("id = ? AND user_id = ?", id, uid(c)).First(&k).Error; err != nil {
		httpx.Fail(c, http.StatusNotFound, "密钥不存在")
		return nil, false
	}
	return &k, true
}

// ---------------- 密钥管理 ----------------

// ListKeys GET /api/member/keys
func (h *Handler) ListKeys(c *gin.Context) {
	type keyRow struct {
		model.APIKey
		CostCenterName string `json:"cost_center_name"` // "" = 未归集
	}
	var rows []keyRow
	_ = h.DB.Raw(`
		SELECT k.*, COALESCE(cc.name,'') AS cost_center_name FROM api_keys k
		LEFT JOIN cost_centers cc ON cc.id = k.cost_center_id
		WHERE k.user_id = ? ORDER BY k.id DESC`, uid(c)).Scan(&rows).Error
	if rows == nil {
		rows = []keyRow{}
	}
	httpx.OK(c, rows)
}

// ListCostCenters GET /api/member/cost-centers：本 org 启用中的中心（建 key 下拉用）
func (h *Handler) ListCostCenters(c *gin.Context) {
	_, o, ok := h.myOrg(c)
	if !ok {
		return
	}
	var centers []model.CostCenter
	_ = h.DB.Where("org_id = ? AND status = 1", o.ID).Order("id").Find(&centers).Error
	if centers == nil {
		centers = []model.CostCenter{}
	}
	httpx.OK(c, centers)
}

// myOrg 当前子账号与其 org（org 必须存在且启用）
func (h *Handler) myOrg(c *gin.Context) (*model.User, *model.Org, bool) {
	var u model.User
	if err := h.DB.Where("id = ?", uid(c)).First(&u).Error; err != nil || u.OrgID == nil {
		httpx.Fail(c, http.StatusForbidden, "账号状态异常")
		return nil, nil, false
	}
	var o model.Org
	if err := h.DB.Where("id = ? AND status = 1", *u.OrgID).First(&o).Error; err != nil {
		httpx.Fail(c, http.StatusForbidden, "账号状态异常")
		return nil, nil, false
	}
	return &u, &o, true
}

// CreateKey POST /api/member/keys：明文完整 key 仅此一次返回；
// expires_at 可选（unix 秒，须晚于当前时刻，0/缺省=永久）；
// cost_center_id 可选（org 开启 require_cost_center 后必填），须为本 org 启用中的中心
func (h *Handler) CreateKey(c *gin.Context) {
	var req struct {
		Name         string `json:"name"`
		ExpiresAt    *int64 `json:"expires_at"`
		CostCenterID *int64 `json:"cost_center_id"`
	}
	if !httpx.BindJSON(c, &req) {
		return
	}
	if req.ExpiresAt != nil && *req.ExpiresAt <= time.Now().Unix() {
		httpx.Fail(c, http.StatusBadRequest, "过期时间必须晚于当前时刻")
		return
	}
	u, o, ok := h.myOrg(c)
	if !ok {
		return
	}
	if req.CostCenterID != nil {
		var cnt int64
		_ = h.DB.Raw("SELECT COUNT(*) FROM cost_centers WHERE id = ? AND org_id = ? AND status = 1",
			*req.CostCenterID, o.ID).Scan(&cnt).Error
		if cnt != 1 {
			httpx.Fail(c, http.StatusBadRequest, "成本中心不存在或已归档")
			return
		}
	} else if o.RequireCostCenter == 1 {
		httpx.Fail(c, http.StatusBadRequest, "本客户已开启强制归集：创建密钥必须选择成本中心")
		return
	}
	plain, prefix, hash, err := auth.GenerateAPIKey()
	if err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "生成密钥失败")
		return
	}
	k := model.APIKey{
		OrgID: o.ID, UserID: u.ID, Name: req.Name,
		KeyPrefix: prefix, KeyHash: hash, Status: 1,
		ExpiredAt: req.ExpiresAt, CostCenterID: req.CostCenterID,
	}
	if err := h.DB.Create(&k).Error; err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "保存密钥失败")
		return
	}
	httpx.OK(c, gin.H{
		"id": k.ID, "name": k.Name, "key": plain,
		"key_prefix": prefix, "created_at": k.CreatedAt, "expires_at": k.ExpiredAt,
		"cost_center_id": k.CostCenterID,
		"message":        "请立即保存完整密钥，关闭后将无法再次查看",
	})
}

// AssignKeyCenter PUT /api/member/keys/:id/cost-center：子账号改自己 key 的归集（只影响未来）
func (h *Handler) AssignKeyCenter(c *gin.Context) {
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	var req struct {
		CostCenterID *int64 `json:"cost_center_id"`
	}
	if !httpx.BindJSON(c, &req) {
		return
	}
	k, ok := h.ownKey(c, id)
	if !ok {
		return
	}
	if req.CostCenterID != nil {
		var cnt int64
		_ = h.DB.Raw("SELECT COUNT(*) FROM cost_centers WHERE id = ? AND org_id = ? AND status = 1",
			*req.CostCenterID, k.OrgID).Scan(&cnt).Error
		if cnt != 1 {
			httpx.Fail(c, http.StatusBadRequest, "成本中心不存在或已归档")
			return
		}
	}
	if err := h.DB.Exec("UPDATE api_keys SET cost_center_id = ? WHERE id = ?",
		req.CostCenterID, id).Error; err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "更新失败")
		return
	}
	httpx.OK(c, gin.H{"message": "已更新（历史账单不变，未来消耗归新中心）"})
}

// DeleteKey DELETE /api/member/keys/:id
func (h *Handler) DeleteKey(c *gin.Context) {
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	if _, ok := h.ownKey(c, id); !ok {
		return
	}
	if err := h.DB.Where("id = ?", id).Delete(&model.APIKey{}).Error; err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "删除失败")
		return
	}
	httpx.OK(c, gin.H{"message": "已删除"})
}

// ---------------- 模型与额度 ----------------

// ListModels GET /api/member/models：可用模型 + 单价 + 我的余额
func (h *Handler) ListModels(c *gin.Context) {
	type modelRow struct {
		Name        string `json:"name"`
		DisplayName string `json:"display_name"`
		Vendor      string `json:"vendor"`
		InputPrice  int64  `json:"input_price"`
		OutputPrice int64  `json:"output_price"`
	}
	var models []modelRow
	_ = h.DB.Raw(`
		SELECT m.name, m.display_name, m.vendor, m.input_price, m.output_price
		FROM user_model_grants g JOIN models m ON m.name = g.model_name
		WHERE g.user_id = ? AND m.status = 1 ORDER BY m.name`, uid(c)).Scan(&models).Error
	if models == nil {
		models = []modelRow{}
	}
	var u model.User
	_ = h.DB.Where("id = ?", uid(c)).First(&u).Error
	httpx.OK(c, gin.H{
		"models":           models,
		"quota_limit":      u.QuotaLimit,
		"quota_used":       u.QuotaUsed,
		"points_per_yuan":  service.PointsPerYuan(h.DB),
	})
}

// StatsOverview GET /api/member/stats/overview
func (h *Handler) StatsOverview(c *gin.Context) {
	id := uid(c)
	ov, err := service.StatsOverview(h.DB, service.Scope{UserID: &id})
	if err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "统计查询失败")
		return
	}
	httpx.OK(c, ov)
}

// usageAgg 用量聚合行：按 key / 按模型两维共用形状
type usageAgg struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Requests int64  `json:"requests"`
	Tokens   int64  `json:"tokens"`
	Cost     int64  `json:"cost"`
	Errors   int64  `json:"errors"`
}

// usageRangeTotals 区间汇总（请求 / tokens / 扣减 / 失败数）
type usageRangeTotals struct {
	Requests int64 `json:"requests"`
	Tokens   int64 `json:"tokens"`
	Cost     int64 `json:"cost"`
	Errors   int64 `json:"errors"`
}

// usageErrExpr 失败行判定：与 stats.go scanTotals 同口径
const usageErrExpr = "CASE WHEN l.status >= 400 OR l.error != '' THEN 1 ELSE 0 END"

// monthStartUnix 账期时区本月 1 号零点（与对账单/统计同一时区口径）
func monthStartUnix() int64 {
	now := time.Now().In(service.BillingLoc())
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).Unix()
}

// UsageBreakdown GET /api/member/stats/usage：多维用量统计。
// start/end 为 unix 秒闭开区间 [start, end)，默认当月（账期时区）；
// today / month 为固定口径，不受 start/end 影响；by_key 恒列本人全部
// key（含零用量，可直接当筛选下拉的数据源），by_model 限区间内非空模型名
func (h *Handler) UsageBreakdown(c *gin.Context) {
	id := uid(c)
	now := time.Now().In(service.BillingLoc())
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Unix()
	monthStart := monthStartUnix()
	start, end := monthStart, now.Unix()
	if v := httpx.QueryInt64(c, "start", 0); v > 0 {
		start = v
	}
	if v := httpx.QueryInt64(c, "end", 0); v > 0 {
		end = v
	}

	totals := func(from, to int64) usageRangeTotals {
		var t usageRangeTotals
		_ = h.DB.Raw(fmt.Sprintf(`
			SELECT COUNT(*) AS requests,
			       COALESCE(SUM(l.prompt_tokens + l.completion_tokens), 0) AS tokens,
			       COALESCE(SUM(l.cost), 0) AS cost,
			       COALESCE(SUM(%s), 0) AS errors
			FROM usage_logs l WHERE l.user_id = ? AND l.created_at >= ? AND l.created_at < ?`,
			usageErrExpr), id, from, to).Scan(&t).Error
		return t
	}

	var byKey []usageAgg
	// LEFT JOIN 使零用量 key 也出现一行（用户视角：名下每个 key 都该能看到）
	if err := h.DB.Raw(fmt.Sprintf(`
		SELECT k.id AS id, k.name AS name, COUNT(l.id) AS requests,
		       COALESCE(SUM(l.prompt_tokens + l.completion_tokens), 0) AS tokens,
		       COALESCE(SUM(l.cost), 0) AS cost,
		       COALESCE(SUM(%s), 0) AS errors
		FROM api_keys k
		LEFT JOIN usage_logs l ON l.api_key_id = k.id AND l.user_id = ?
		       AND l.created_at >= ? AND l.created_at < ?
		WHERE k.user_id = ?
		GROUP BY k.id, k.name ORDER BY tokens DESC, requests DESC`, usageErrExpr),
		id, start, end, id).Scan(&byKey).Error; err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "按 key 统计查询失败")
		return
	}
	if byKey == nil {
		byKey = []usageAgg{}
	}

	var byModel []usageAgg
	_ = h.DB.Raw(fmt.Sprintf(`
		SELECT 0 AS id, l.model_name AS name, COUNT(*) AS requests,
		       COALESCE(SUM(l.prompt_tokens + l.completion_tokens), 0) AS tokens,
		       COALESCE(SUM(l.cost), 0) AS cost,
		       COALESCE(SUM(%s), 0) AS errors
		FROM usage_logs l
		WHERE l.user_id = ? AND l.created_at >= ? AND l.created_at < ? AND l.model_name != ''
		GROUP BY l.model_name ORDER BY tokens DESC, requests DESC`, usageErrExpr),
		id, start, end).Scan(&byModel).Error
	if byModel == nil {
		byModel = []usageAgg{}
	}

	httpx.OK(c, gin.H{
		"start": start, "end": end,
		"today": totals(dayStart, now.Unix()),
		"month": totals(monthStart, now.Unix()),
		"range": totals(start, end),
		"by_key": byKey, "by_model": byModel,
	})
}

// ListUsage GET /api/member/usage
func (h *Handler) ListUsage(c *gin.Context) {
	page, size, offset := httpx.PageParams(c)
	cond, args := "l.user_id = ?", []any{uid(c)}
	if v := c.Query("model"); v != "" {
		cond += " AND l.model_name = ?"
		args = append(args, v)
	}
	if v := httpx.QueryInt64(c, "key_id", 0); v > 0 {
		cond += " AND l.api_key_id = ?"
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
	_ = h.DB.Raw("SELECT COUNT(*) FROM usage_logs l WHERE "+cond, args...).Scan(&total).Error
	var rows []model.UsageLog
	_ = h.DB.Raw("SELECT l.* FROM usage_logs l WHERE "+cond+" ORDER BY l.id DESC LIMIT ? OFFSET ?",
		append(args, size, offset)...).Scan(&rows).Error
	if rows == nil {
		rows = []model.UsageLog{}
	}
	httpx.PageResult(c, rows, total, page, size)
}

// ---------------- 额度申请 ----------------

// ListRequests GET /api/member/requests
func (h *Handler) ListRequests(c *gin.Context) {
	var rows []model.QuotaRequest
	_ = h.DB.Where("user_id = ?", uid(c)).Order("id DESC").Limit(50).Find(&rows).Error
	if rows == nil {
		rows = []model.QuotaRequest{}
	}
	httpx.OK(c, rows)
}

// CreateRequest POST /api/member/requests
func (h *Handler) CreateRequest(c *gin.Context) {
	var req struct {
		Amount int64  `json:"amount" binding:"required,gt=0"`
		Reason string `json:"reason"`
	}
	if !httpx.BindJSON(c, &req) {
		return
	}
	var u model.User
	if err := h.DB.Where("id = ?", uid(c)).First(&u).Error; err != nil || u.OrgID == nil {
		httpx.Fail(c, http.StatusForbidden, "账号状态异常")
		return
	}
	qr := model.QuotaRequest{
		OrgID: *u.OrgID, UserID: u.ID, Amount: req.Amount,
		Reason: req.Reason, Status: "pending",
	}
	if err := h.DB.Create(&qr).Error; err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "提交申请失败")
		return
	}
	httpx.OK(c, qr)
}
