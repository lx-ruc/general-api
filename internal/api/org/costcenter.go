package org

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"token-gateway/internal/database"
	"token-gateway/internal/httpx"
	"token-gateway/internal/model"
)

// 成本中心：org 内受控归集词表。词表 CRUD / key 改派 / org 报表。
// 一切查询强制 WHERE org_id；归档不删除（历史引用保留）。

// dayExpr 报表按日分桶的方言表达式（各自取服务器本地时区）
func dayExpr() string {
	if database.Dialect == "postgres" {
		return "to_char(to_timestamp(l.created_at), 'YYYY-MM-DD')"
	}
	return "date(l.created_at, 'unixepoch', 'localtime')"
}

// monthStartUnix 本月零点（本地时区）
func monthStartUnix() int64 {
	n := time.Now()
	return time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, time.Local).Unix()
}

// validCenter 校验中心属本 org 且启用（nil = 取消归集，放行）
func (h *Handler) validCenter(c *gin.Context, oid int64, centerID *int64) bool {
	if centerID == nil {
		return true
	}
	var cnt int64
	_ = h.DB.Raw("SELECT COUNT(*) FROM cost_centers WHERE id = ? AND org_id = ? AND status = 1",
		*centerID, oid).Scan(&cnt).Error
	if cnt != 1 {
		httpx.Fail(c, http.StatusBadRequest, "成本中心不存在或已归档")
		return false
	}
	return true
}

// ListCostCenters GET /api/org/cost-centers：词表（归档在后）+ 本月消耗 + 挂 key 数 + org 开关
func (h *Handler) ListCostCenters(c *gin.Context) {
	oid, ok := orgID(c)
	if !ok {
		return
	}
	type row struct {
		model.CostCenter
		MonthCost int64 `json:"month_cost"` // 本月消耗（点）
		KeyCount  int64 `json:"key_count"`  // 当前挂靠 key 数
	}
	var rows []row
	_ = h.DB.Raw(`
		SELECT cc.*,
		       (SELECT COALESCE(SUM(l.cost),0) FROM usage_logs l
		         WHERE l.cost_center_id = cc.id AND l.created_at >= ?) AS month_cost,
		       (SELECT COUNT(*) FROM api_keys k WHERE k.cost_center_id = cc.id) AS key_count
		FROM cost_centers cc WHERE cc.org_id = ?
		ORDER BY cc.status DESC, cc.id`, monthStartUnix(), oid).Scan(&rows).Error
	if rows == nil {
		rows = []row{}
	}
	var o model.Org
	_ = h.DB.Select("require_cost_center").Where("id = ?", oid).First(&o).Error
	httpx.OK(c, gin.H{"list": rows, "require_cost_center": o.RequireCostCenter})
}

// CreateCostCenter POST /api/org/cost-centers（org 内重名拒绝）
func (h *Handler) CreateCostCenter(c *gin.Context) {
	oid, ok := orgID(c)
	if !ok {
		return
	}
	var req struct {
		Name string `json:"name" binding:"required"`
	}
	if !httpx.BindJSON(c, &req) {
		return
	}
	cc := model.CostCenter{OrgID: oid, Name: req.Name, Status: 1}
	if err := h.DB.Create(&cc).Error; err != nil {
		httpx.Fail(c, http.StatusBadRequest, "创建失败：名称在本公司内已存在")
		return
	}
	httpx.OK(c, cc)
}

// UpdateCostCenter PUT /api/org/cost-centers/:id（改名/归档；无删除入口）
func (h *Handler) UpdateCostCenter(c *gin.Context) {
	oid, ok := orgID(c)
	if !ok {
		return
	}
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	var req struct {
		Name   *string `json:"name"`
		Status *int    `json:"status"`
	}
	if !httpx.BindJSON(c, &req) {
		return
	}
	updates := map[string]any{"updated_at": time.Now().Unix()}
	if req.Name != nil && *req.Name != "" {
		updates["name"] = *req.Name
	}
	if req.Status != nil {
		if *req.Status != 0 && *req.Status != 1 {
			httpx.Fail(c, http.StatusBadRequest, "status 只能为 0 或 1")
			return
		}
		updates["status"] = *req.Status
	}
	res := h.DB.Model(&model.CostCenter{}).Where("id = ? AND org_id = ?", id, oid).Updates(updates)
	if res.Error != nil || res.RowsAffected == 0 {
		httpx.Fail(c, http.StatusNotFound, "成本中心不存在")
		return
	}
	httpx.OK(c, gin.H{"message": "已更新"})
}

// UpdateCostCenterConfig PUT /api/org/cost-centers/config（require_cost_center 开关，默认关）
func (h *Handler) UpdateCostCenterConfig(c *gin.Context) {
	oid, ok := orgID(c)
	if !ok {
		return
	}
	var req struct {
		RequireCostCenter *int `json:"require_cost_center" binding:"required"`
	}
	if !httpx.BindJSON(c, &req) {
		return
	}
	if *req.RequireCostCenter != 0 && *req.RequireCostCenter != 1 {
		httpx.Fail(c, http.StatusBadRequest, "require_cost_center 只能为 0 或 1")
		return
	}
	if err := h.DB.Model(&model.Org{}).Where("id = ?", oid).
		Update("require_cost_center", *req.RequireCostCenter).Error; err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "更新失败")
		return
	}
	httpx.OK(c, gin.H{"message": "已更新"})
}

// ReassignKeyCenter PUT /api/org/keys/:id/cost-center：org 管理员改派本公司任何 key。
// 只影响未来结算——历史 usage_logs 快照不可变。
func (h *Handler) ReassignKeyCenter(c *gin.Context) {
	oid, ok := orgID(c)
	if !ok {
		return
	}
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	var req struct {
		CostCenterID *int64 `json:"cost_center_id"` // 缺省/null = 取消归集
	}
	if !httpx.BindJSON(c, &req) {
		return
	}
	if !h.validCenter(c, oid, req.CostCenterID) {
		return
	}
	res := h.DB.Exec("UPDATE api_keys SET cost_center_id = ? WHERE id = ? AND org_id = ?",
		req.CostCenterID, id, oid)
	if res.Error != nil || res.RowsAffected == 0 {
		httpx.Fail(c, http.StatusNotFound, "密钥不存在")
		return
	}
	httpx.OK(c, gin.H{"message": "已改派（历史账单不变，未来消耗归新中心）"})
}

// CostCenterReport GET /api/org/reports/cost-centers?start&end&center_id&model
// 中心 × 模型 × 日聚合；"未归集"恒置底并披露占比。org 视角无任何毛利字段。
func (h *Handler) CostCenterReport(c *gin.Context) {
	oid, ok := orgID(c)
	if !ok {
		return
	}
	cond, args := "l.org_id = ?", []any{oid}
	if v := httpx.QueryInt64(c, "start", 0); v > 0 {
		cond += " AND l.created_at >= ?"
		args = append(args, v)
	}
	if v := httpx.QueryInt64(c, "end", 0); v > 0 {
		cond += " AND l.created_at < ?"
		args = append(args, v)
	}
	if v := httpx.QueryInt64(c, "center_id", 0); v > 0 {
		cond += " AND l.cost_center_id = ?"
		args = append(args, v)
	}
	if v := c.Query("model"); v != "" {
		cond += " AND l.model_name = ?"
		args = append(args, v)
	}

	type row struct {
		CostCenterID     *int64 `json:"cost_center_id"`
		CenterName       string `json:"center_name"` // "" = 未归集
		CenterStatus     int    `json:"center_status"`
		ModelName        string `json:"model_name"`
		Day              string `json:"day"`
		Requests         int64  `json:"requests"`
		CacheHits        int64  `json:"cache_hits"`
		PromptTokens     int64  `json:"prompt_tokens"`
		CompletionTokens int64  `json:"completion_tokens"`
		Cost             int64  `json:"cost"`
	}
	var rows []row
	_ = h.DB.Raw(fmt.Sprintf(`
		SELECT l.cost_center_id, COALESCE(cc.name,'') AS center_name,
		       COALESCE(cc.status,1) AS center_status,
		       l.model_name, %s AS day,
		       COUNT(CASE WHEN l.status = 200 THEN 1 END) AS requests,
		       COUNT(CASE WHEN l.cache_hit = 1 THEN 1 END) AS cache_hits,
		       SUM(l.prompt_tokens) AS prompt_tokens,
		       SUM(l.completion_tokens) AS completion_tokens,
		       SUM(l.cost) AS cost
		FROM usage_logs l
		LEFT JOIN cost_centers cc ON cc.id = l.cost_center_id
		WHERE %s
		GROUP BY l.cost_center_id, cc.name, cc.status, l.model_name, %s
		ORDER BY (l.cost_center_id IS NULL), cc.name, l.model_name, day`,
		dayExpr(), cond, dayExpr()), args...).Scan(&rows).Error
	if rows == nil {
		rows = []row{}
	}

	var totals struct{ Total, Unallocated int64 }
	_ = h.DB.Raw(fmt.Sprintf(`
		SELECT COALESCE(SUM(l.cost),0) AS total,
		       COALESCE(SUM(CASE WHEN l.cost_center_id IS NULL THEN l.cost END),0) AS unallocated
		FROM usage_logs l WHERE %s`, cond), args...).Scan(&totals).Error
	pct := 0.0
	if totals.Total > 0 {
		pct = float64(totals.Unallocated) / float64(totals.Total) * 100
	}
	httpx.OK(c, gin.H{
		"list": rows, "total_cost": totals.Total,
		"unallocated_cost": totals.Unallocated, "unallocated_pct": pct,
	})
}
