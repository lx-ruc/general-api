package org

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"token-gateway/internal/httpx"
	"token-gateway/internal/service"
)

// 额度预警阈值（双阈值 UI）：百分比写入 [T]（升序数组，留多档扩展；0/关 = []），
// 金额档写 alert_balance（剩余额度低于 X 元，存点；0=关）。编辑后静默重算两档状态（不发信）。

// bindAlertConfig 解析 {threshold?, alert_balance?}——两者至少给一个；
// threshold 0-100 百分比（0=关），alert_balance 为点数（前端按元输入换算，0=关）。
// 返回「是否提供」标志区分「未提供（不动既有值）」与「提供为 0（显式关闭）」
func bindAlertConfig(c *gin.Context) (threshold int, hasT bool, balance int64, hasB bool, ok bool) {
	var req struct {
		Threshold    *int   `json:"threshold"`
		AlertBalance *int64 `json:"alert_balance"`
	}
	if !httpx.BindJSON(c, &req) {
		return 0, false, 0, false, false
	}
	if req.Threshold == nil && req.AlertBalance == nil {
		httpx.Fail(c, http.StatusBadRequest, "请提供 threshold（百分比）或 alert_balance（剩余额度阈值，点）")
		return 0, false, 0, false, false
	}
	if req.Threshold != nil {
		if *req.Threshold < 0 || *req.Threshold > 100 {
			httpx.Fail(c, http.StatusBadRequest, "threshold 取值 0-100（0=关闭预警）")
			return 0, false, 0, false, false
		}
		threshold, hasT = *req.Threshold, true
	}
	if req.AlertBalance != nil {
		if *req.AlertBalance < 0 || *req.AlertBalance > 1_000_000_000_000_000 {
			httpx.Fail(c, http.StatusBadRequest, "alert_balance 取值非法（点数，0=关闭）")
			return 0, false, 0, false, false
		}
		balance, hasB = *req.AlertBalance, true
	}
	return threshold, hasT, balance, hasB, true
}

// GetAlertLevels GET /api/org/alert-levels —— 本客户当前预警配置与状态
func (h *Handler) GetAlertLevels(c *gin.Context) {
	oid, ok := orgID(c)
	if !ok {
		return
	}
	var row struct {
		ID              int64
		AlertLevels     string
		AlertLevel      int
		AlertSince      int64
		AlertBalance    int64
		AlertBalanceHit int64
		QuotaLimit      int64
		QuotaUsed       int64
		MonthlyQuota    int64
		MonthlyCost     int64
		MonthlyPeriod   string
	}
	// 未配置过预警（alert_levels 空）不是错误：threshold 回 0（关闭）；
	// 404 只留给客户真不存在（Raw+Scan 无行时零值，靠 ID 判别）
	if err := h.DB.Raw(
		"SELECT id, alert_levels, alert_level, alert_since, alert_balance, alert_balance_hit, quota_limit, quota_used, monthly_quota, monthly_cost, monthly_period FROM orgs WHERE id = ?",
		oid).Scan(&row).Error; err != nil || row.ID == 0 {
		httpx.Fail(c, http.StatusNotFound, "客户不存在")
		return
	}
	// 月累计仅在存储账期 == 当前账期时有效（跨月惰性清零的读侧）
	period := service.PeriodOf(service.BillingLoc(), time.Now().Unix())
	monthlyUsed := int64(0)
	if row.MonthlyPeriod == period {
		monthlyUsed = row.MonthlyCost
	}
	httpx.OK(c, gin.H{
		"threshold":         service.LevelsToThreshold(row.AlertLevels),
		"alert_level":       row.AlertLevel,
		"alert_since":       row.AlertSince,
		"alert_balance":     row.AlertBalance,
		"alert_balance_hit": row.AlertBalanceHit,
		"points_per_yuan":   service.PointsPerYuan(h.DB),
		"quota_limit":       row.QuotaLimit,
		"quota_used":        row.QuotaUsed,
		"monthly_quota":     row.MonthlyQuota,
		"monthly_used":      monthlyUsed,
	})
}

// UpdateAlertLevels PUT /api/org/alert-levels {threshold?, alert_balance?}
func (h *Handler) UpdateAlertLevels(c *gin.Context) {
	oid, ok := orgID(c)
	if !ok {
		return
	}
	t, hasT, balance, hasB, ok := bindAlertConfig(c)
	if !ok {
		return
	}
	// 未提供的字段不动既有值（两阈值独立开关，提供 0 = 显式关闭）
	res := h.DB.Exec(`UPDATE orgs SET
		alert_levels = CASE WHEN ? THEN ? ELSE alert_levels END,
		alert_balance = CASE WHEN ? THEN ? ELSE alert_balance END,
		updated_at = ? WHERE id = ?`,
		hasT, service.ThresholdToLevels(t), hasB, balance, time.Now().Unix(), oid)
	if res.Error != nil || res.RowsAffected == 0 {
		httpx.Fail(c, http.StatusInternalServerError, "更新失败")
		return
	}
	service.RecomputeAlertLevel(h.DB, "org", oid)
	httpx.OK(c, gin.H{"message": "预警配置已更新"})
}

// GetMemberAlertLevels GET /api/org/members/:id/alert-levels —— 子账号个人预警配置（org 隔离）
func (h *Handler) GetMemberAlertLevels(c *gin.Context) {
	oid, ok := orgID(c)
	if !ok {
		return
	}
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	var row struct {
		ID              int64
		AlertLevels     string
		AlertLevel      int
		AlertSince      int64
		AlertBalance    int64
		AlertBalanceHit int64
		QuotaLimit      *int64
		QuotaUsed       int64
	}
	if err := h.DB.Raw(
		"SELECT id, alert_levels, alert_level, alert_since, alert_balance, alert_balance_hit, quota_limit, quota_used FROM users WHERE id = ? AND org_id = ? AND role = 'member'",
		id, oid).Scan(&row).Error; err != nil || row.ID == 0 {
		httpx.Fail(c, http.StatusNotFound, "子账号不存在")
		return
	}
	httpx.OK(c, gin.H{
		"threshold":         service.LevelsToThreshold(row.AlertLevels),
		"alert_level":       row.AlertLevel,
		"alert_since":       row.AlertSince,
		"alert_balance":     row.AlertBalance,
		"alert_balance_hit": row.AlertBalanceHit,
		"points_per_yuan":   service.PointsPerYuan(h.DB),
		"quota_limit":       row.QuotaLimit,
		"quota_used":        row.QuotaUsed,
	})
}

// UpdateMemberAlertLevels PUT /api/org/members/:id/alert-levels {threshold?, alert_balance?}
func (h *Handler) UpdateMemberAlertLevels(c *gin.Context) {
	oid, ok := orgID(c)
	if !ok {
		return
	}
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	t, hasT, balance, hasB, ok := bindAlertConfig(c)
	if !ok {
		return
	}
	res := h.DB.Exec(`UPDATE users SET
		alert_levels = CASE WHEN ? THEN ? ELSE alert_levels END,
		alert_balance = CASE WHEN ? THEN ? ELSE alert_balance END,
		updated_at = ? WHERE id = ? AND org_id = ? AND role = 'member'`,
		hasT, service.ThresholdToLevels(t), hasB, balance, time.Now().Unix(), id, oid)
	if res.Error != nil || res.RowsAffected == 0 {
		httpx.Fail(c, http.StatusNotFound, "子账号不存在")
		return
	}
	service.RecomputeAlertLevel(h.DB, "user", id)
	httpx.OK(c, gin.H{"message": "预警配置已更新"})
}
