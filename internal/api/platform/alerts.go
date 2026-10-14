package platform

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"token-gateway/internal/httpx"
	"token-gateway/internal/service"
)

// UpdateOrgAlertLevels PUT /api/platform/orgs/:id/alert-levels {threshold?, alert_balance?}
// 平台代运营视角：为某客户设预警（百分比档 0-100 与金额档剩余额度阈值点数，均 0=关，两项独立开关）。
// 编辑后静默重算两档状态（策略变更非水位事件，不发信）。
func (h *Handler) UpdateOrgAlertLevels(c *gin.Context) {
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	var req struct {
		Threshold    *int   `json:"threshold"`
		AlertBalance *int64 `json:"alert_balance"`
	}
	if !httpx.BindJSON(c, &req) {
		return
	}
	if req.Threshold == nil && req.AlertBalance == nil {
		httpx.Fail(c, http.StatusBadRequest, "请提供 threshold（百分比）或 alert_balance（剩余额度阈值，点）")
		return
	}
	hasT, hasB := false, false
	t, balance := 0, int64(0)
	if req.Threshold != nil {
		if *req.Threshold < 0 || *req.Threshold > 100 {
			httpx.Fail(c, http.StatusBadRequest, "threshold 取值 0-100（0=关闭预警）")
			return
		}
		t, hasT = *req.Threshold, true
	}
	if req.AlertBalance != nil {
		if *req.AlertBalance < 0 || *req.AlertBalance > 1_000_000_000_000_000 {
			httpx.Fail(c, http.StatusBadRequest, "alert_balance 取值非法（点数，0=关闭）")
			return
		}
		balance, hasB = *req.AlertBalance, true
	}
	res := h.DB.Exec(`UPDATE orgs SET
		alert_levels = CASE WHEN ? THEN ? ELSE alert_levels END,
		alert_balance = CASE WHEN ? THEN ? ELSE alert_balance END,
		updated_at = ? WHERE id = ?`,
		hasT, service.ThresholdToLevels(t), hasB, balance, time.Now().Unix(), id)
	if res.Error != nil {
		httpx.Fail(c, http.StatusInternalServerError, "更新失败")
		return
	}
	if res.RowsAffected == 0 {
		httpx.Fail(c, http.StatusNotFound, "客户不存在")
		return
	}
	service.RecomputeAlertLevel(h.DB, "org", id)
	httpx.OK(c, gin.H{"message": "预警配置已更新"})
}
