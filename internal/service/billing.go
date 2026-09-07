package service

import (
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"token-gateway/internal/database"
	"token-gateway/internal/model"
)

// 月度对账单：口径三声明 + 三段式（勾稽/冲减/明细）+ 月末快照。
//
// 口径（印在账单头）：
//   - 归期 = 结算完成时刻（跨月流式归次月，即 usage_logs.created_at）
//   - 月边界 = billing.timezone 的 wall-clock（默认 Asia/Shanghai）
//   - 计价 = 全整数点数；价格取结算快照，改价不影响已出账单

const snapshotPeriodKey = "balance_snapshot_period"

// billingTZ 启动时注入的账期时区配置（空 = 默认 Asia/Shanghai）
var billingTZ string

// SetBillingTimezone 启动时注入
func SetBillingTimezone(tz string) { billingTZ = tz }

// BillingLoc 当前生效的账期时区
func BillingLoc() *time.Location { return BillingLocation(billingTZ) }

// BillingLocation 解析账期时区；非法名回退 Asia/Shanghai 再回退 UTC
func BillingLocation(tz string) *time.Location {
	if loc, err := time.LoadLocation(tz); err == nil && tz != "" {
		return loc
	}
	if loc, err := time.LoadLocation("Asia/Shanghai"); err == nil {
		return loc
	}
	return time.UTC
}

// PeriodBounds 账期 [start, end) unix 秒：period 'YYYY-MM' 在 loc 的自然月边界。
// 用"目标时区 wall-clock → unix"换算，服务器本地时区无关。
func PeriodBounds(loc *time.Location, period string) (int64, int64, error) {
	start, err := time.ParseInLocation("2006-01", period, loc)
	if err != nil {
		return 0, 0, fmt.Errorf("period 格式应为 YYYY-MM: %w", err)
	}
	end := start.AddDate(0, 1, 0)
	return start.Unix(), end.Unix(), nil
}

// PrevPeriod 上一个月 'YYYY-MM'
func PrevPeriod(period string) (string, error) {
	t, err := time.ParseInLocation("2006-01", period, time.UTC)
	if err != nil {
		return "", fmt.Errorf("period 格式应为 YYYY-MM: %w", err)
	}
	return t.AddDate(0, -1, 0).Format("2006-01"), nil
}

// PeriodOf 时刻所在账期（按 loc wall-clock）
func PeriodOf(loc *time.Location, ts int64) string {
	return time.Unix(ts, 0).In(loc).Format("2006-01")
}

// ---------- 月末快照 ----------

// SnapshotBalances 写某账期月末的 org limit/used 快照（UPSERT，可重复补跑）。
// period 'YYYY-MM' 语义 = 该月末时点；调用时刻的现值即近似月末值（正常由次月 00:05 cron 触达）。
func SnapshotBalances(db *gorm.DB, period string) error {
	now := time.Now().Unix()
	var orgs []model.Org
	if err := db.Find(&orgs).Error; err != nil {
		return fmt.Errorf("读取公司列表失败: %w", err)
	}
	return db.Transaction(func(tx *gorm.DB) error {
		for _, o := range orgs {
			if err := tx.Exec(`INSERT INTO period_balances (org_id, period, quota_limit, quota_used, snapshot_at, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(org_id, period) DO UPDATE SET
					quota_limit = excluded.quota_limit, quota_used = excluded.quota_used,
					snapshot_at = excluded.snapshot_at, updated_at = excluded.updated_at`,
				o.ID, period, o.QuotaLimit, o.QuotaUsed, now, now, now).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// RunBalanceSnapshotter 常驻协程：启动即补跑一次（漏跑自愈），此后每月 1 日 00:05（账期时区）快照上月。
// settings CAS（balance_snapshot_period != 当前待写期才动）保证多实例只写一次。
func RunBalanceSnapshotter(db *gorm.DB, tz string) {
	loc := BillingLocation(tz)
	snapshotPrev := func() {
		// 待写期 = 上一个自然月（其月末 ≈ 当前时刻）
		prev := PeriodOf(loc, time.Now().AddDate(0, -1, 0).Unix())
		won := db.Exec(`UPDATE settings SET value = ? WHERE key = ? AND value <> ?`,
			prev, snapshotPeriodKey, prev)
		if won.Error != nil {
			slog.Warn("月末快照 CAS 失败", "period", prev, "err", won.Error)
			return
		}
		if won.RowsAffected == 0 { // 已写过
			return
		}
		if err := SnapshotBalances(db, prev); err != nil {
			slog.Error("月末快照写入失败", "period", prev, "err", err)
			return
		}
		slog.Info("月末余额快照已写入", "period", prev, "timezone", loc.String())
	}

	snapshotPrev() // 启动自愈：部署后/漏跑后首次启动补上
	for {
		now := time.Now().In(loc)
		next := time.Date(now.Year(), now.Month(), 1, 0, 5, 0, 0, loc).AddDate(0, 1, 0)
		time.Sleep(time.Until(next))
		snapshotPrev()
	}
}

// ---------- 三段式账单 ----------

// BillDetailRow 明细段行：模型 × 成本中心 × 日。org 视角 VendorCost/Margin 恒 0（防毛利泄漏）。
type BillDetailRow struct {
	Day              string `json:"day"`
	ModelName        string `json:"model_name"`
	CostCenterID     *int64 `json:"cost_center_id"`
	CostCenterName   string `json:"cost_center_name"`
	Requests         int64  `json:"requests"`
	CacheHits        int64  `json:"cache_hits"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	Cost             int64  `json:"cost"`
	VendorCost       int64  `json:"vendor_cost,omitempty"`
	Margin           int64  `json:"margin,omitempty"`
}

// BillGrantRow 冲减/授权流水行
type BillGrantRow struct {
	ID        int64  `json:"id"`
	Amount    int64  `json:"amount"`
	Remark    string `json:"remark"`
	CreatedAt int64  `json:"created_at"`
}

// BillStatement 三段式账单。ChainOK nil = 无法勾稽（期初快照缺失，显示"—"）。
type BillStatement struct {
	Month     string `json:"month"`
	Timezone  string `json:"timezone"`
	StartUnix int64  `json:"start_unix"`
	EndUnix   int64  `json:"end_unix"`

	// 勾稽段
	OpeningLimit   *int64 `json:"opening_limit"`   // 期初限额（snapshot[M-1]；nil=无快照显示"—"）
	OpeningUsed    *int64 `json:"opening_used"`    // 期初已用
	TotalGranted   int64  `json:"total_granted"`   // 期内正 grant Σ
	TotalRevoked   int64  `json:"total_revoked"`   // 期内负 grant Σ（冲减，负数）
	Consumption    int64  `json:"consumption"`     // 期内 Σcost
	ClosingLimit   int64  `json:"closing_limit"`   // 期末限额（snapshot[M]，当月无快照用实时值）
	ClosingUsed    int64  `json:"closing_used"`
	ClosingIsLive  bool   `json:"closing_is_live"` // true=当月实时值（未快照）
	ChainOK        *bool  `json:"chain_ok"`        // 期末used−期初used==消耗？nil=期初缺失无法校验
	NoUsageCount   int64  `json:"no_usage_count"`  // 期内 no_usage 笔数（漏计费披露）
	OpeningMissing bool   `json:"opening_missing"` // 首个快照月之前

	// 冲减段（期内负 grant 明细）
	Revokes []BillGrantRow `json:"revokes"`
	Grants  []BillGrantRow `json:"grants"` // 期内正 grant 明细

	// 明细段
	Rows         []BillDetailRow `json:"rows"`
	TotalRows    int             `json:"total_rows"`
	TotalCostSum int64           `json:"total_cost_sum"` // 明细 Σ（勾稽消耗应相等；行多时前端只展示部分）
}

// BuildBillStatement 聚合某 org 某月账单。includeVendor=false 时明细不含厂商成本/毛利（org 视角防泄漏）。
func BuildBillStatement(db *gorm.DB, loc *time.Location, orgID int64, period string, includeVendor bool, detailLimit int) (*BillStatement, error) {
	s, e, err := PeriodBounds(loc, period)
	if err != nil {
		return nil, err
	}
	if detailLimit <= 0 {
		detailLimit = 500
	}
	st := &BillStatement{Month: period, Timezone: loc.String(), StartUnix: s, EndUnix: e}

	// ---- 勾稽段：期初 = snapshot[M-1]，期末 = snapshot[M]（缺则实时） ----
	prev, _ := PrevPeriod(period)
	var open model.PeriodBalance
	hasOpen := db.Where("org_id = ? AND period = ?", orgID, prev).First(&open).Error == nil
	var close model.PeriodBalance
	hasClose := db.Where("org_id = ? AND period = ?", orgID, period).First(&close).Error == nil

	var org model.Org
	if err := db.Where("id = ?", orgID).First(&org).Error; err != nil {
		return nil, ErrNotFound
	}
	if hasOpen {
		st.OpeningLimit, st.OpeningUsed = &open.QuotaLimit, &open.QuotaUsed
	} else {
		st.OpeningMissing = true
	}
	if hasClose {
		st.ClosingLimit, st.ClosingUsed = close.QuotaLimit, close.QuotaUsed
	} else {
		st.ClosingLimit, st.ClosingUsed, st.ClosingIsLive = org.QuotaLimit, org.QuotaUsed, true
	}

	// ---- 期内流水（正/负分桶）----
	var grants []model.QuotaGrant
	_ = db.Where("subject_type = 'org' AND subject_id = ? AND created_at >= ? AND created_at < ?",
		orgID, s, e).Order("id").Find(&grants).Error
	st.Grants, st.Revokes = []BillGrantRow{}, []BillGrantRow{}
	for _, g := range grants {
		row := BillGrantRow{ID: g.ID, Amount: g.Amount, Remark: g.Remark, CreatedAt: g.CreatedAt}
		if g.Amount >= 0 {
			st.TotalGranted += g.Amount
			st.Grants = append(st.Grants, row)
		} else {
			st.TotalRevoked += g.Amount
			st.Revokes = append(st.Revokes, row)
		}
	}

	// ---- 消耗 Σ + no_usage 笔数 ----
	var agg struct{ Cost, NoUsage int64 }
	_ = db.Raw(`SELECT COALESCE(SUM(cost),0) AS cost, COALESCE(SUM(no_usage),0) AS no_usage
		FROM usage_logs WHERE org_id = ? AND created_at >= ? AND created_at < ?`,
		orgID, s, e).Scan(&agg).Error
	st.Consumption, st.NoUsageCount = agg.Cost, agg.NoUsage

	// ---- 链式勾稽：期末used − 期初used == 期内消耗（used 单调，grants 不动 used）----
	if hasOpen {
		ok := st.ClosingUsed-*st.OpeningUsed == st.Consumption
		st.ChainOK = &ok
	}

	// ---- 明细段：模型 × 成本中心 × 日（未归集置底）----
	// 日分桶按账期时区：Go 侧算出该月的固定偏移秒数（无夏令时区的月份内恒定），
	// SQL 里对 unix 秒整体平移后按 UTC 渲染日期 —— SQLite/PG 同一表达式，避免 localtime 依赖服务器时区
	// 注意限定 l.：明细段 JOIN cost_centers（也有 created_at）
	_, off := time.Unix(s, 0).In(loc).Zone()
	dayExpr := fmt.Sprintf("strftime('%%Y-%%m-%%d', l.created_at + %d, 'unixepoch')", off)
	if database.Dialect == "postgres" {
		dayExpr = fmt.Sprintf("to_char(to_timestamp(l.created_at + %d), 'YYYY-MM-DD')", off)
	}
	vendorSel := ""
	if includeVendor {
		vendorSel = ", COALESCE(SUM(vendor_cost),0) AS vendor_cost"
	}
	var rows []BillDetailRow
	q := fmt.Sprintf(`
		SELECT %s AS day, l.model_name,
		       l.cost_center_id, COALESCE(cc.name, '') AS cost_center_name,
		       COUNT(*) AS requests, COALESCE(SUM(l.cache_hit),0) AS cache_hits,
		       COALESCE(SUM(l.prompt_tokens),0) AS prompt_tokens,
		       COALESCE(SUM(l.completion_tokens),0) AS completion_tokens,
		       COALESCE(SUM(l.cost),0) AS cost%s
		FROM usage_logs l LEFT JOIN cost_centers cc ON cc.id = l.cost_center_id
		WHERE l.org_id = ? AND l.created_at >= ? AND l.created_at < ?
		GROUP BY day, l.model_name, l.cost_center_id, cc.name
		ORDER BY (l.cost_center_id IS NULL), day DESC, cost DESC
		LIMIT ?`, dayExpr, vendorSel)
	if err := db.Raw(q, orgID, s, e, detailLimit).Scan(&rows).Error; err != nil {
		return nil, err
	}
	if rows == nil {
		rows = []BillDetailRow{}
	}
	if includeVendor {
		for i := range rows {
			rows[i].Margin = rows[i].Cost - rows[i].VendorCost
		}
	}
	var cnt int64
	_ = db.Raw(`SELECT COUNT(*) FROM (
		SELECT %s AS day, l.model_name, l.cost_center_id FROM usage_logs l
		WHERE l.org_id = ? AND created_at >= ? AND created_at < ?
		GROUP BY day, l.model_name, l.cost_center_id) t`, dayExpr, orgID, s, e).Scan(&cnt).Error
	st.Rows, st.TotalRows = rows, int(cnt)
	for _, r := range rows {
		st.TotalCostSum += r.Cost
	}
	return st, nil
}

// WriteStatementCSV 把账单序列化为 CSV（UTF-8 BOM；org 视角无厂商列）。
func WriteStatementCSV(st *BillStatement, orgName string, includeVendor bool) []byte {
	var b []byte
	b = append(b, []byte("\xEF\xBB\xBF")...) // BOM：Excel 中文第一坑
	line := func(ss ...string) {
		for i, s := range ss {
			if i > 0 {
				b = append(b, ',')
			}
			// 含逗号/引号/换行的字段加引号转义
			if containsAny(s, ",\"\n\r") {
				b = append(b, '"')
				b = append(b, []byte(escapeCSV(s))...)
				b = append(b, '"')
			} else {
				b = append(b, []byte(s)...)
			}
		}
		b = append(b, '\n')
	}
	i64 := func(v int64) string { return fmt.Sprintf("%d", v) }
	p := func(v *int64) string {
		if v == nil {
			return "—"
		}
		return i64(*v)
	}
	chain := "—"
	if st.ChainOK != nil && *st.ChainOK {
		chain = "✓"
	} else if st.ChainOK != nil {
		chain = "✗"
	}

	line("公司", orgName)
	line("账期", st.Month, "（时区 "+st.Timezone+"）")
	line("口径", "归期=结算完成时刻；月边界按账期时区；计价=整数点数（结算快照价）")
	line("")
	line("—— 勾稽段 ——")
	line("期初限额", p(st.OpeningLimit), "期初已用", p(st.OpeningUsed))
	line("期内授权", i64(st.TotalGranted), "期内冲减", i64(st.TotalRevoked), "期内消耗", i64(st.Consumption))
	line("期末限额", i64(st.ClosingLimit), "期末已用", i64(st.ClosingUsed),
		map[bool]string{true: "（实时）", false: ""}[st.ClosingIsLive])
	line("链式校验（期末−期初==消耗）", chain, "不计量笔数", i64(st.NoUsageCount))
	line("")
	if len(st.Revokes) > 0 {
		line("—— 冲减段 ——")
		line("时间", "金额", "事由")
		for _, r := range st.Revokes {
			line(time.Unix(r.CreatedAt, 0).Format("2006-01-02 15:04"), i64(r.Amount), r.Remark)
		}
		line("")
	}
	line("—— 明细段（模型 × 成本中心 × 日）——")
	if includeVendor {
		line("日期", "模型", "成本中心", "请求数", "缓存命中", "输入tokens", "输出tokens", "金额（点）", "厂商成本", "毛利")
	} else {
		line("日期", "模型", "成本中心", "请求数", "缓存命中", "输入tokens", "输出tokens", "金额（点）")
	}
	for _, r := range st.Rows {
		center := r.CostCenterName
		if r.CostCenterID == nil {
			center = "未归集"
		}
		if includeVendor {
			line(r.Day, r.ModelName, center, i64(r.Requests), i64(r.CacheHits),
				i64(r.PromptTokens), i64(r.CompletionTokens), i64(r.Cost),
				i64(r.VendorCost), i64(r.Margin))
		} else {
			line(r.Day, r.ModelName, center, i64(r.Requests), i64(r.CacheHits),
				i64(r.PromptTokens), i64(r.CompletionTokens), i64(r.Cost))
		}
	}
	line("合计", "", "", "", "", "", "", i64(st.TotalCostSum))
	return b
}

func containsAny(s string, set string) bool {
	for _, r := range set {
		for _, c := range s {
			if c == r {
				return true
			}
		}
	}
	return false
}

func escapeCSV(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '"' {
			out = append(out, '"')
		}
		out = append(out, r)
	}
	return string(out)
}
