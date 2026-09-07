package org

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"token-gateway/internal/httpx"
	"token-gateway/internal/service"
)

// csvName Content-Disposition filename*（RFC 5987 百分号编码，中文文件名跨浏览器）
func csvName(name string) string { return url.PathEscape(name) }

// 月度对账单（org 视角：售价口径，无厂商成本/毛利字段）。
// 三段式：勾稽段（快照期初 + 流水分正负 + 消耗Σ + 链式校验）· 冲减段 · 明细段。

func statementMonth(c *gin.Context) (string, bool) {
	month := c.DefaultQuery("month", time.Now().In(service.BillingLoc()).Format("2006-01"))
	if _, _, err := service.PeriodBounds(service.BillingLoc(), month); err != nil {
		httpx.Fail(c, http.StatusBadRequest, "month 格式应为 YYYY-MM")
		return "", false
	}
	return month, true
}

// BillingStatement GET /api/org/billing/statement?month=YYYY-MM
func (h *Handler) BillingStatement(c *gin.Context) {
	oid, ok := orgID(c)
	if !ok {
		return
	}
	month, ok := statementMonth(c)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "500"))
	st, err := service.BuildBillStatement(h.DB, service.BillingLoc(), oid, month, false, limit)
	if err != nil {
		if err == service.ErrNotFound {
			httpx.Fail(c, http.StatusNotFound, "公司不存在")
			return
		}
		httpx.Fail(c, http.StatusBadRequest, err.Error())
		return
	}
	httpx.OK(c, st)
}

// BillingStatementCSV GET /api/org/billing/statement/csv?month=YYYY-MM
func (h *Handler) BillingStatementCSV(c *gin.Context) {
	oid, ok := orgID(c)
	if !ok {
		return
	}
	month, ok := statementMonth(c)
	if !ok {
		return
	}
	var orgName string
	_ = h.DB.Raw("SELECT name FROM orgs WHERE id = ?", oid).Scan(&orgName).Error
	st, err := service.BuildBillStatement(h.DB, service.BillingLoc(), oid, month, false, 2000)
	if err != nil {
		httpx.Fail(c, http.StatusBadRequest, err.Error())
		return
	}
	b := service.WriteStatementCSV(st, orgName, false)
	c.Header("Content-Disposition",
		fmt.Sprintf("attachment; filename*=UTF-8''%s", csvName("对账单_"+orgName+"_"+month+".csv")))
	c.Data(http.StatusOK, "text/csv; charset=utf-8", b)
}
