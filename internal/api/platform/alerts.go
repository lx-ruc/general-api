package platform

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"token-gateway/internal/httpx"
	"token-gateway/internal/service"
)

// UpdateOrgAlertLevels PUT /api/platform/orgs/:id/alert-levels {threshold}
// 平台代运营视角：为某公司设预警阈值（0=关）。编辑后静默重算 alert_level。
func (h *Handler) UpdateOrgAlertLevels(c *gin.Context) {
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	var req struct {
		Threshold *int `json:"threshold"`
	}
	if !httpx.BindJSON(c, &req) || req.Threshold == nil {
		return
	}
	if *req.Threshold < 0 || *req.Threshold > 100 {
		httpx.Fail(c, http.StatusBadRequest, "threshold 取值 0-100（0=关闭预警）")
		return
	}
	levels := service.ThresholdToLevels(*req.Threshold)
	res := h.DB.Exec("UPDATE orgs SET alert_levels = ?, updated_at = ? WHERE id = ?",
		levels, time.Now().Unix(), id)
	if res.Error != nil {
		httpx.Fail(c, http.StatusInternalServerError, "更新失败")
		return
	}
	if res.RowsAffected == 0 {
		httpx.Fail(c, http.StatusNotFound, "公司不存在")
		return
	}
	service.RecomputeAlertLevel(h.DB, "org", id)
	httpx.OK(c, gin.H{"message": "预警阈值已更新"})
}
