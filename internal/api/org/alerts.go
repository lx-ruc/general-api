package org

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"token-gateway/internal/httpx"
	"token-gateway/internal/service"
)

// 额度预警阈值（单阈值 UI）：写入 [T]（升序数组，留多档扩展）；0/关 = []。
// 编辑后静默重算 alert_level（策略变更非水位事件，不发信）。

func bindThreshold(c *gin.Context) (int, bool) {
	var req struct {
		Threshold *int `json:"threshold"`
	}
	if !httpx.BindJSON(c, &req) || req.Threshold == nil {
		return 0, false
	}
	if *req.Threshold < 0 || *req.Threshold > 100 {
		httpx.Fail(c, http.StatusBadRequest, "threshold 取值 0-100（0=关闭预警）")
		return 0, false
	}
	return *req.Threshold, true
}

// GetAlertLevels GET /api/org/alert-levels —— 本公司当前预警配置与状态
func (h *Handler) GetAlertLevels(c *gin.Context) {
	oid, ok := orgID(c)
	if !ok {
		return
	}
	var row struct {
		AlertLevels   string
		AlertLevel    int
		AlertSince    int64
		QuotaLimit    int64
		QuotaUsed     int64
		MonthlyQuota  int64
		MonthlyCost   int64
		MonthlyPeriod string
	}
	if err := h.DB.Raw(
		"SELECT alert_levels, alert_level, alert_since, quota_limit, quota_used, monthly_quota, monthly_cost, monthly_period FROM orgs WHERE id = ?",
		oid).Scan(&row).Error; err != nil || row.AlertLevels == "" {
		httpx.Fail(c, http.StatusNotFound, "公司不存在")
		return
	}
	// 月累计仅在存储账期 == 当前账期时有效（跨月惰性清零的读侧）
	period := service.PeriodOf(service.BillingLoc(), time.Now().Unix())
	monthlyUsed := int64(0)
	if row.MonthlyPeriod == period {
		monthlyUsed = row.MonthlyCost
	}
	httpx.OK(c, gin.H{
		"threshold":     service.LevelsToThreshold(row.AlertLevels),
		"alert_level":   row.AlertLevel,
		"alert_since":   row.AlertSince,
		"quota_limit":   row.QuotaLimit,
		"quota_used":    row.QuotaUsed,
		"monthly_quota": row.MonthlyQuota,
		"monthly_used":  monthlyUsed,
	})
}

// UpdateAlertLevels PUT /api/org/alert-levels {threshold}
func (h *Handler) UpdateAlertLevels(c *gin.Context) {
	oid, ok := orgID(c)
	if !ok {
		return
	}
	t, ok := bindThreshold(c)
	if !ok {
		return
	}
	res := h.DB.Exec("UPDATE orgs SET alert_levels = ?, updated_at = ? WHERE id = ?",
		service.ThresholdToLevels(t), time.Now().Unix(), oid)
	if res.Error != nil || res.RowsAffected == 0 {
		httpx.Fail(c, http.StatusInternalServerError, "更新失败")
		return
	}
	service.RecomputeAlertLevel(h.DB, "org", oid)
	httpx.OK(c, gin.H{"message": "预警阈值已更新"})
}
