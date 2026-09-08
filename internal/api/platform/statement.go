package platform

import (
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"token-gateway/internal/httpx"
	"token-gateway/internal/middleware"
	"token-gateway/internal/model"
	"token-gateway/internal/service"
)

// csvName Content-Disposition filename*（RFC 5987 百分号编码，中文文件名跨浏览器）
func csvName(name string) string { return url.PathEscape(name) }

// 平台视角账单：任意 org + 毛利列；渠道厂商账单录入与差异对账；月末快照补跑。

// OrgStatement GET /api/platform/orgs/:id/statement?month=YYYY-MM
func (h *Handler) OrgStatement(c *gin.Context) {
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	month := c.DefaultQuery("month", time.Now().In(service.BillingLoc()).Format("2006-01"))
	if _, _, err := service.PeriodBounds(service.BillingLoc(), month); err != nil {
		httpx.Fail(c, http.StatusBadRequest, "month 格式应为 YYYY-MM")
		return
	}
	st, err := service.BuildBillStatement(h.DB, service.BillingLoc(), id, month, true, 500)
	if err != nil {
		if err == service.ErrNotFound {
			httpx.Fail(c, http.StatusNotFound, "客户不存在")
			return
		}
		httpx.Fail(c, http.StatusBadRequest, err.Error())
		return
	}
	httpx.OK(c, st)
}

// OrgStatementCSV GET /api/platform/orgs/:id/statement/csv?month=YYYY-MM
func (h *Handler) OrgStatementCSV(c *gin.Context) {
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	month := c.DefaultQuery("month", time.Now().In(service.BillingLoc()).Format("2006-01"))
	var orgName string
	_ = h.DB.Raw("SELECT name FROM orgs WHERE id = ?", id).Scan(&orgName).Error
	st, err := service.BuildBillStatement(h.DB, service.BillingLoc(), id, month, true, 2000)
	if err != nil {
		httpx.Fail(c, http.StatusBadRequest, err.Error())
		return
	}
	b := service.WriteStatementCSV(st, orgName, true)
	c.Header("Content-Disposition",
		fmt.Sprintf("attachment; filename*=UTF-8''%s", csvName("对账单_"+orgName+"_"+month+".csv")))
	c.Data(http.StatusOK, "text/csv; charset=utf-8", b)
}

// RunSnapshot POST /api/platform/billing/snapshots {period} —— 月末快照手动补跑（断链修复）
func (h *Handler) RunSnapshot(c *gin.Context) {
	var req struct {
		Period string `json:"period" binding:"required"`
	}
	if !httpx.BindJSON(c, &req) {
		return
	}
	if _, _, err := service.PeriodBounds(service.BillingLoc(), req.Period); err != nil {
		httpx.Fail(c, http.StatusBadRequest, "period 格式应为 YYYY-MM")
		return
	}
	if err := service.SnapshotBalances(h.DB, req.Period); err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "快照写入失败: "+err.Error())
		return
	}
	httpx.OK(c, gin.H{"message": "已写入 " + req.Period + " 月末快照"})
}

// ---------- 厂商账单对账 ----------

// VendorDiffRow 渠道 × 账期 对账行：我方 Σvendor_cost vs 录入厂商账单
type VendorDiffRow struct {
	ChannelID    int64  `json:"channel_id"`
	ChannelName  string `json:"channel_name"`
	Requests     int64  `json:"requests"`
	OurCost      int64  `json:"our_cost"`      // 我方按渠道记的厂商成本
	NoUsageCount int64  `json:"no_usage_count"` // 不计量笔数（漏损候选）
	BillID       *int64 `json:"bill_id"`
	BilledPoints int64  `json:"billed_points"`
	Note         string `json:"note"`
	HasBill      bool   `json:"has_bill"`
	Diff         int64  `json:"diff"`      // 我方 − 录入
	DiffPct      int64  `json:"diff_pct"`  // |diff|/录入 ×100（>2 标红）
	OverPct      bool   `json:"over_pct"`  // true=偏差超 2%
}

// ListVendorBills GET /api/platform/vendor-bills?period=YYYY-MM —— 录入列表 + 差异报表（合并）
func (h *Handler) ListVendorBills(c *gin.Context) {
	period := c.DefaultQuery("period", time.Now().In(service.BillingLoc()).Format("2006-01"))
	if _, _, err := service.PeriodBounds(service.BillingLoc(), period); err != nil {
		httpx.Fail(c, http.StatusBadRequest, "period 格式应为 YYYY-MM")
		return
	}
	s, e, _ := service.PeriodBounds(service.BillingLoc(), period)

	type usageRow struct {
		ChannelID    int64
		Requests     int64
		OurCost      int64
		NoUsageCount int64
	}
	var usage []usageRow
	_ = h.DB.Raw(`SELECT channel_id, COUNT(*) AS requests,
			COALESCE(SUM(vendor_cost),0) AS our_cost, COALESCE(SUM(no_usage),0) AS no_usage_count
		FROM usage_logs WHERE created_at >= ? AND created_at < ? AND channel_id IS NOT NULL
		GROUP BY channel_id`, s, e).Scan(&usage).Error

	var bills []model.VendorBill
	_ = h.DB.Where("period = ?", period).Find(&bills).Error

	merged := map[int64]*VendorDiffRow{}
	for _, u := range usage {
		merged[u.ChannelID] = &VendorDiffRow{
			ChannelID: u.ChannelID, Requests: u.Requests,
			OurCost: u.OurCost, NoUsageCount: u.NoUsageCount,
		}
	}
	for _, b := range bills {
		r, ok := merged[b.ChannelID]
		if !ok {
			r = &VendorDiffRow{ChannelID: b.ChannelID}
			merged[b.ChannelID] = r
		}
		id := b.ID
		r.BillID, r.BilledPoints, r.Note, r.HasBill = &id, b.BilledPoints, b.Note, true
	}
	names := h.channelNames()
	out := make([]VendorDiffRow, 0, len(merged))
	for _, r := range merged {
		r.ChannelName = names[r.ChannelID]
		if r.HasBill {
			r.Diff = r.OurCost - r.BilledPoints
			if r.BilledPoints > 0 {
				d := r.Diff
				if d < 0 {
					d = -d
				}
				r.DiffPct = d * 100 / r.BilledPoints
				r.OverPct = r.DiffPct > 2
			}
		}
		out = append(out, *r)
	}
	// 有用量/有录入的渠道在前，未用渠道置底；再按我方成本降序
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			a, b := out[i], out[j]
			if (a.OurCost+a.BilledPoints) < (b.OurCost+b.BilledPoints) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	httpx.OK(c, gin.H{"period": period, "list": out})
}

func (h *Handler) channelNames() map[int64]string {
	var chs []model.Channel
	_ = h.DB.Select("id, name").Find(&chs).Error
	m := map[int64]string{}
	for _, c := range chs {
		m[c.ID] = c.Name
	}
	return m
}

// UpsertVendorBill PUT /api/platform/vendor-bills {period, channel_id, billed_points, note}
func (h *Handler) UpsertVendorBill(c *gin.Context) {
	var req struct {
		Period       string `json:"period" binding:"required"`
		ChannelID    int64  `json:"channel_id" binding:"required"`
		BilledPoints int64  `json:"billed_points"`
		Note         string `json:"note"`
	}
	if !httpx.BindJSON(c, &req) {
		return
	}
	if _, _, err := service.PeriodBounds(service.BillingLoc(), req.Period); err != nil {
		httpx.Fail(c, http.StatusBadRequest, "period 格式应为 YYYY-MM")
		return
	}
	var cnt int64
	_ = h.DB.Model(&model.Channel{}).Where("id = ?", req.ChannelID).Count(&cnt).Error
	if cnt == 0 {
		httpx.Fail(c, http.StatusNotFound, "渠道不存在")
		return
	}
	now := time.Now().Unix()
	uid := middleware.GetUID(c)
	err := h.DB.Transaction(func(tx *gorm.DB) error {
		res := tx.Exec(`UPDATE vendor_bills SET billed_points = ?, note = ?, updated_at = ?
			WHERE period = ? AND channel_id = ?`, req.BilledPoints, req.Note, now, req.Period, req.ChannelID)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return tx.Exec(`INSERT INTO vendor_bills (period, channel_id, billed_points, note, created_by, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?)`,
				req.Period, req.ChannelID, req.BilledPoints, req.Note, uid, now, now).Error
		}
		return nil
	})
	if err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "保存失败")
		return
	}
	httpx.OK(c, gin.H{"message": "厂商账单已保存"})
}

// DeleteVendorBill DELETE /api/platform/vendor-bills/:id
func (h *Handler) DeleteVendorBill(c *gin.Context) {
	id, ok := httpx.PathID(c)
	if !ok {
		return
	}
	if err := h.DB.Delete(&model.VendorBill{}, id).Error; err != nil {
		httpx.Fail(c, http.StatusInternalServerError, "删除失败")
		return
	}
	httpx.OK(c, gin.H{"message": "已删除"})
}
