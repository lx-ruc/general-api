package member

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"token-gateway/internal/auth"
	"token-gateway/internal/httpx"
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
// expires_at 可选（unix 秒，须晚于当前时刻，0/缺省=永久）。
// 成本中心是客户管理员侧的核算概念，子账号建 key 不再涉及归集（由客户管理员在密钥一览里挂靠）。
func (h *Handler) CreateKey(c *gin.Context) {
	var req struct {
		Name      string `json:"name"`
		ExpiresAt *int64 `json:"expires_at"`
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
	plain, prefix, hash, err := auth.GenerateAPIKey()
	if err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "生成密钥失败")
		return
	}
	k := model.APIKey{
		OrgID: o.ID, UserID: u.ID, Name: req.Name,
		KeyPrefix: prefix, KeyHash: hash, Status: 1,
		ExpiredAt: req.ExpiresAt,
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
	// 月累计仅在存储账期为当前月时有效（跨月惰性清零的读侧，与 Precheck 同口径）
	monthlyUsed := int64(0)
	if u.MonthlyPeriod == service.PeriodOf(service.BillingLoc(), time.Now().Unix()) {
		monthlyUsed = u.MonthlyCost
	}
	httpx.OK(c, gin.H{
		"models":          models,
		"quota_limit":     u.QuotaLimit,
		"quota_used":      u.QuotaUsed,
		"monthly_quota":   u.MonthlyQuota,
		"monthly_used":    monthlyUsed,
		"points_per_yuan": service.PointsPerYuan(h.DB),
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
	// 模型明细只统计真实消耗（status=200 或有计费）：被拒尝试（403 未授权/404 不存在等
	// 零成本行）不得以 0 token 行出现——与成本报表/账单同口径；上方各汇总与按 key 统计
	// （含 errors）仍计全量，用户能看到自己 key 的失败情况
	_ = h.DB.Raw(fmt.Sprintf(`
		SELECT 0 AS id, l.model_name AS name, COUNT(*) AS requests,
		       COALESCE(SUM(l.prompt_tokens + l.completion_tokens), 0) AS tokens,
		       COALESCE(SUM(l.cost), 0) AS cost,
		       COALESCE(SUM(%s), 0) AS errors
		FROM usage_logs l
		WHERE l.user_id = ? AND l.created_at >= ? AND l.created_at < ? AND l.model_name != ''
		  AND (l.status = 200 OR l.cost > 0)
		GROUP BY l.model_name ORDER BY tokens DESC, requests DESC`, usageErrExpr),
		id, start, end).Scan(&byModel).Error
	if byModel == nil {
		byModel = []usageAgg{}
	}

	httpx.OK(c, gin.H{
		"start": start, "end": end,
		"today":  totals(dayStart, now.Unix()),
		"month":  totals(monthStart, now.Unix()),
		"range":  totals(start, end),
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

// ---------------- 额度/模型申请 ----------------

// ListRequests GET /api/member/requests
func (h *Handler) ListRequests(c *gin.Context) {
	var rows []model.QuotaRequest
	_ = h.DB.Where("user_id = ?", uid(c)).Order("id DESC").Limit(50).Find(&rows).Error
	if rows == nil {
		rows = []model.QuotaRequest{}
	}
	httpx.OK(c, rows)
}

// ListAvailableModels GET /api/member/models/available：全部启用模型 + 本人是否已授权，
// 供模型申请页勾选（前端过滤掉 granted 的即可，已授权模型重复申请无意义）
func (h *Handler) ListAvailableModels(c *gin.Context) {
	type row struct {
		Name        string `json:"name"`
		DisplayName string `json:"display_name"`
		Vendor      string `json:"vendor"`
		Granted     bool   `json:"granted"`
	}
	var rows []row
	_ = h.DB.Raw(`
		SELECT m.name, m.display_name, m.vendor,
		       CASE WHEN g.user_id IS NULL THEN 0 ELSE 1 END AS granted
		FROM models m
		LEFT JOIN user_model_grants g ON g.model_name = m.name AND g.user_id = ?
		WHERE m.status = 1 ORDER BY m.name`, uid(c)).Scan(&rows).Error
	if rows == nil {
		rows = []row{}
	}
	httpx.OK(c, rows)
}

// CreateRequest POST /api/member/requests：额度申请（amount>0）或模型授权申请
// （models 非空，剔除本人已授权项后至少剩一个才受理）。kind 由服务端按提交内容
// 推导，不信任客户端传入；两者都空 → 400
func (h *Handler) CreateRequest(c *gin.Context) {
	var req struct {
		Amount int64    `json:"amount"`
		Reason string   `json:"reason"`
		Models []string `json:"models"`
	}
	if !httpx.BindJSON(c, &req) {
		return
	}
	var u model.User
	if err := h.DB.Where("id = ?", uid(c)).First(&u).Error; err != nil || u.OrgID == nil {
		httpx.Fail(c, http.StatusForbidden, "账号状态异常")
		return
	}

	qr := model.QuotaRequest{OrgID: *u.OrgID, UserID: u.ID, Reason: req.Reason, Status: "pending"}
	switch {
	case len(req.Models) > 0:
		// 模型申请：校验均为存在且启用的模型；已授权的静默剔除（前端已过滤，
		// 这里兜底防重复申请），剔除后为空 → 视为空申请
		names := uniqueNonEmpty(req.Models)
		var valid []string
		rows, err := h.DB.Raw(`SELECT name FROM models WHERE status = 1`).Rows()
		if err != nil {
			httpx.Fail(c, http.StatusInternalServerError, "查询模型失败")
			return
		}
		enabled := map[string]bool{}
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err == nil {
				enabled[n] = true
			}
		}
		_ = rows.Close()
		var granted []struct{ ModelName string }
		_ = h.DB.Raw(`SELECT model_name FROM user_model_grants WHERE user_id = ?`, u.ID).Scan(&granted).Error
		mine := map[string]bool{}
		for _, g := range granted {
			mine[g.ModelName] = true
		}
		for _, n := range names {
			if !enabled[n] {
				httpx.Fail(c, http.StatusBadRequest, "模型不存在或已下架："+n)
				return
			}
			if !mine[n] {
				valid = append(valid, n)
			}
		}
		if len(valid) == 0 {
			httpx.Fail(c, http.StatusBadRequest, "申请的模型均已授权，无需重复申请")
			return
		}
		qr.Kind, qr.ModelNames = "model", strings.Join(valid, ",")
	case req.Amount > 0:
		qr.Kind, qr.Amount = "quota", req.Amount
	default:
		httpx.Fail(c, http.StatusBadRequest, "请填写申请额度或选择申请的模型")
		return
	}
	if err := h.DB.Create(&qr).Error; err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "提交申请失败")
		return
	}
	// 站内通知客户管理员审批（顶栏铃铛；邮件不给审批人——站内即达且不打扰）
	go func() {
		who := u.DisplayName
		if who == "" {
			who = u.Username
		}
		title, body := fmt.Sprintf("额度申请待审批：%s", who), fmt.Sprintf(
			"子账号「%s」(%s) 提交了额度申请 +%d token。事由：%s。\n到【申请审批】处理。",
			who, u.Username, qr.Amount, qr.Reason)
		if qr.Kind == "model" {
			title, body = fmt.Sprintf("模型申请待审批：%s", who), fmt.Sprintf(
				"子账号「%s」(%s) 申请使用模型：%s。\n到【申请审批】处理。",
				who, u.Username, qr.ModelNames)
		}
		service.NotifyOrgAdminsInsite(h.DB, *u.OrgID, service.NotifyTypeRequestPending, title, body)
	}()
	httpx.OK(c, qr)
}

// uniqueNonEmpty 去重去空
func uniqueNonEmpty(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
