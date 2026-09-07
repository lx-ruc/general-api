package platform

import (
	"github.com/gin-gonic/gin"

	"token-gateway/internal/httpx"
)

// CostCenterCrossReport GET /api/platform/reports/cost-centers?start&end&org_id
// 平台视角：org × 中心交叉聚合，含厂商成本与毛利列（org 视角永远不可见）。
func (h *Handler) CostCenterCrossReport(c *gin.Context) {
	cond, args := "1=1", []any{}
	if v := httpx.QueryInt64(c, "start", 0); v > 0 {
		cond += " AND l.created_at >= ?"
		args = append(args, v)
	}
	if v := httpx.QueryInt64(c, "end", 0); v > 0 {
		cond += " AND l.created_at < ?"
		args = append(args, v)
	}
	if v := httpx.QueryInt64(c, "org_id", 0); v > 0 {
		cond += " AND l.org_id = ?"
		args = append(args, v)
	}

	type row struct {
		OrgID         int64  `json:"org_id"`
		OrgName       string `json:"org_name"`
		CostCenterID  *int64 `json:"cost_center_id"`
		CenterName    string `json:"center_name"` // "" = 未归集
		CenterStatus  int    `json:"center_status"`
		Requests      int64  `json:"requests"`
		CacheHits     int64  `json:"cache_hits"`
		PromptTokens  int64  `json:"prompt_tokens"`
		CompleteToks  int64  `json:"completion_tokens"`
		Cost          int64  `json:"cost"`        // 平台营收（客户扣减）
		VendorCost    int64  `json:"vendor_cost"` // 厂商成本
	}
	var rows []row
	_ = h.DB.Raw(`
		SELECT l.org_id, o.name AS org_name,
		       l.cost_center_id, COALESCE(cc.name,'') AS center_name,
		       COALESCE(cc.status,1) AS center_status,
		       COUNT(CASE WHEN l.status = 200 THEN 1 END) AS requests,
		       COUNT(CASE WHEN l.cache_hit = 1 THEN 1 END) AS cache_hits,
		       SUM(l.prompt_tokens) AS prompt_tokens,
		       SUM(l.completion_tokens) AS completion_tokens,
		       SUM(l.cost) AS cost,
		       SUM(l.vendor_cost) AS vendor_cost
		FROM usage_logs l
		JOIN orgs o ON o.id = l.org_id
		LEFT JOIN cost_centers cc ON cc.id = l.cost_center_id
		WHERE `+cond+`
		GROUP BY l.org_id, o.name, l.cost_center_id, cc.name, cc.status
		ORDER BY l.org_id, (l.cost_center_id IS NULL), cc.name`, args...).Scan(&rows).Error
	if rows == nil {
		rows = []row{}
	}

	// 毛利在应用层计算（cost − vendor_cost），顺带算合计
	type outRow struct {
		row
		Margin int64 `json:"margin"`
	}
	out := make([]outRow, 0, len(rows))
	var totCost, totVendor int64
	for _, r := range rows {
		out = append(out, outRow{row: r, Margin: r.Cost - r.VendorCost})
		totCost += r.Cost
		totVendor += r.VendorCost
	}
	httpx.OK(c, gin.H{
		"list": out, "total_cost": totCost,
		"total_vendor_cost": totVendor, "total_margin": totCost - totVendor,
	})
}
