package org

import (
	"fmt"

	"github.com/gin-gonic/gin"

	"token-gateway/internal/httpx"
	"token-gateway/internal/model"
)

// 配额下发页的数据面：子账号额度流水（org 隔离）。额度池/子账号列表复用
// members 与 stats 现有端点，这里只补「谁在什么时候给谁发了多少」的审计查询。

// ListQuotaGrants GET /api/org/quota-grants?user_id=&page= —— 本客户子账号额度下发流水。
// org 隔离：流水主体必须是本客户的 users（JOIN 限定 org_id），跨客户流水不可见；
// operator 显示操作人用户名（子账号申请审批通过时为审批的管理员）
func (h *Handler) ListQuotaGrants(c *gin.Context) {
	oid, ok := orgID(c)
	if !ok {
		return
	}
	page, size, offset := httpx.PageParams(c)
	cond, args := "g.subject_type = 'user' AND u.org_id = ?", []any{oid}
	if uid := httpx.QueryInt64(c, "user_id", 0); uid > 0 {
		cond += " AND g.subject_id = ?"
		args = append(args, uid)
	}
	var total int64
	_ = h.DB.Raw(fmt.Sprintf(`
		SELECT COUNT(*) FROM quota_grants g JOIN users u ON u.id = g.subject_id WHERE %s`, cond),
		args...).Scan(&total).Error
	type row struct {
		model.QuotaGrant
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		Operator    string `json:"operator"`
	}
	var rows []row
	_ = h.DB.Raw(fmt.Sprintf(`
		SELECT g.*, u.username, COALESCE(u.display_name, '') AS display_name, COALESCE(op.username, '') AS operator
		FROM quota_grants g
		JOIN users u ON u.id = g.subject_id
		LEFT JOIN users op ON op.id = g.operator_id
		WHERE %s ORDER BY g.id DESC LIMIT ? OFFSET ?`, cond),
		append(args, size, offset)...).Scan(&rows).Error
	if rows == nil {
		rows = []row{}
	}
	httpx.PageResult(c, rows, total, page, size)
}
