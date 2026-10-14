package service

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math/bits"
	"sort"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"token-gateway/internal/metrics"
)

// 预算告警：边沿状态机 + 竞态唯一 + 惰性复位 + 收件扇出。
// - 档位坍缩为整数 alert_level（"已达最高档"），位域连续填充不变量使三行规则覆盖 N 档
// - 检查点在 Settle 提交后异步执行（数据面零侵入）；DB 条件更新即多实例协调器
// - 拨备回落靠下次检查惰性复位（静默），不加拨备钩子
// - 两个正交维度：百分比档（alert_level 升降档）与金额档（剩余额度低于阈值，
//   alert_balance_hit 命中/复位）——两者无全序关系，各自独立状态机，任一命中即发信

const alertThrottleSeconds = 60

var alertLastCheck sync.Map // "org:1"/"user:2" → time.Time

// alertRecheckPending "org:1"/"user:2" → struct{}：窗口内已挂起到期兜底复查（防重复挂起）
var alertRecheckPending sync.Map

// alertRecheckAfter 兜底复查调度器（包级可替换：测试注入捕获定时器）
var alertRecheckAfter = time.AfterFunc

// siteBaseURL 告警邮件直达链接前缀（config server.site_url；空 = 不带链接）
var siteBaseURL string

// SetSiteURL 启动时注入
func SetSiteURL(u string) { siteBaseURL = u }

func consoleLink(path string) string {
	if siteBaseURL == "" {
		return "请登录管理台查看详情。"
	}
	return fmt.Sprintf("直达链接：%s/#%s", siteBaseURL, path)
}

// CheckBudgetAlerts 结算提交后异步检查 org 与 user 两级水位（go 调用；自带 panic 恢复）
func CheckBudgetAlerts(db *gorm.DB, m *metrics.Metrics, orgID, userID int64) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("预算告警检查 panic", "recover", r)
		}
	}()
	checkAlert(db, m, "org", orgID)
	checkAlert(db, m, "user", userID)
}

func alertTable(kind string) string {
	if kind == "org" {
		return "orgs"
	}
	return "users"
}

// throttleWindow 60s 窗口节流；返回是否放行与窗口到期时刻（被节流时非零，
// 供调用方挂起兜底复查——节流只该削峰减读，不能把档位越限检查整个吞掉）
func throttleWindow(key string, m *metrics.Metrics) (bool, time.Time) {
	now := time.Now()
	if v, ok := alertLastCheck.Load(key); ok {
		if t, _ := v.(time.Time); now.Sub(t) < alertThrottleSeconds*time.Second {
			if m != nil {
				m.AlertThrottled.Inc()
			}
			return false, t.Add(alertThrottleSeconds * time.Second)
		}
	}
	alertLastCheck.Store(key, now)
	return true, time.Time{}
}

// scheduleAlertRecheck 被节流 ≠ 不检查：突发消费跨过阈值后流量静默的场景
// （大批量任务跑完即停——恰是最该告警的时刻），若无兜底复查，告警会随
// 「60s 内没有下一次结算」而永久丢失。窗口到期自动补查一次；并发去重只挂一个。
func scheduleAlertRecheck(at time.Time, db *gorm.DB, m *metrics.Metrics, kind string, id int64, key string) {
	if _, dup := alertRecheckPending.LoadOrStore(key, struct{}{}); dup {
		return
	}
	alertRecheckAfter(time.Until(at), func() {
		alertRecheckPending.Delete(key)
		checkAlert(db, m, kind, id)
	})
}

// parseLevels 解析 alert_levels JSON（升序去重，剔除 ≤0；空/非法 → nil = 关）
func parseLevels(raw string) []int64 {
	var arr []int64
	if err := json.Unmarshal([]byte(raw), &arr); err != nil {
		return nil
	}
	sort.Slice(arr, func(i, j int) bool { return arr[i] < arr[j] })
	out := make([]int64, 0, len(arr))
	for i, v := range arr {
		if v > 0 && (i == 0 || arr[i] != arr[i-1]) {
			out = append(out, v)
		}
	}
	return out
}

// bracket 已达最高档位（1..N；0=未达任何档）。全整数比较，无浮点。
// used/limit 在额度边界可达 int64 高位，裸乘 100 会回绕成负导致档位误判：
// 乘积比较走 128 位，恒不回绕
func bracket(used, limit int64, levels []int64) int {
	lv := 0
	for i, t := range levels {
		if mulGE128(used, 100, limit, t) {
			lv = i + 1
		}
	}
	return lv
}

// mulGE128 非负整数比较 a×sa >= b×sb（128 位精确，无回绕）
func mulGE128(a, sa, b, sb int64) bool {
	hi1, lo1 := bits.Mul64(uint64(a), uint64(sa))
	hi2, lo2 := bits.Mul64(uint64(b), uint64(sb))
	if hi1 != hi2 {
		return hi1 > hi2
	}
	return lo1 >= lo2
}

// alertMailCtx 一次告警发信的上下文。PctThreshold / BalThreshold 恰一非零
// （分别来自百分比档与金额档两条独立状态机），sendAlertMail 据此组织两套文案
type alertMailCtx struct {
	Kind                string // "org" | "user"
	ID, Used, Limit     int64
	PctThreshold        int64 // 百分比档触发时 > 0
	BalThreshold        int64 // 金额档触发时 > 0（点）
}

// sendAlertMailFn 发信动作（包级可替换：测试注入观察器，避免依赖邮件副作用）
var sendAlertMailFn = sendAlertMail

func checkAlert(db *gorm.DB, m *metrics.Metrics, kind string, id int64) {
	key := kind + ":" + fmt.Sprint(id)
	if pass, dueAt := throttleWindow(key, m); !pass {
		scheduleAlertRecheck(dueAt, db, m, kind, id, key)
		return
	}

	var limit, used int64
	var levelsRaw string
	var level int
	var alertBalance int64
	if kind == "org" {
		var row struct {
			ID           int64
			QuotaLimit   int64
			QuotaUsed    int64
			AlertLevels  string
			AlertLevel   int
			AlertBalance int64
		}
		if err := db.Raw(`SELECT id, quota_limit, quota_used, alert_levels, alert_level, alert_balance FROM orgs WHERE id = ?`,
			id).Scan(&row).Error; err != nil || row.ID == 0 {
			return
		}
		limit, used, levelsRaw, level, alertBalance = row.QuotaLimit, row.QuotaUsed, row.AlertLevels, row.AlertLevel, row.AlertBalance
		if limit <= 0 {
			return
		}
	} else {
		var row struct {
			ID           int64
			QuotaLimit   *int64
			QuotaUsed    int64
			AlertLevels  string
			AlertLevel   int
			AlertBalance int64
		}
		if err := db.Raw(`SELECT id, quota_limit, quota_used, alert_levels, alert_level, alert_balance FROM users WHERE id = ?`,
			id).Scan(&row).Error; err != nil || row.ID == 0 {
			return
		}
		if row.QuotaLimit == nil || *row.QuotaLimit <= 0 {
			return // 不限额者不参与（金额档同样无从谈起）
		}
		limit, used, levelsRaw, level, alertBalance = *row.QuotaLimit, row.QuotaUsed, row.AlertLevels, row.AlertLevel, row.AlertBalance
	}

	// 双开关：百分比档（alert_levels）与金额档（alert_balance）任一配置即参与检查；
	// 只配了金额（levels='[]'）不能被「空数组=关闭」早退吞掉
	levels := parseLevels(levelsRaw)
	hasBal := alertBalance > 0
	if len(levels) == 0 && !hasBal {
		return
	}
	newLevel := bracket(used, limit, levels) // levels 空 → 恒 0，下方百分比 switch 无动作
	now := time.Now().Unix()
	table := alertTable(kind)

	switch {
	case newLevel > level:
		res := db.Exec(fmt.Sprintf(
			"UPDATE %s SET alert_level = ?, alert_since = ? WHERE id = ? AND alert_level < ?", table),
			newLevel, now, id, newLevel)
		if res.Error != nil {
			slog.Warn("告警档位抢占失败", "kind", kind, "id", id, "err", res.Error)
			return
		}
		if res.RowsAffected == 1 { // 并发只有一个胜者（多实例同样成立）
			if m != nil {
				m.AlertTriggers.With(kind).Inc()
			}
			sendAlertMailFn(db, alertMailCtx{Kind: kind, ID: id, Used: used, Limit: limit,
				PctThreshold: levels[newLevel-1]})
		}
	case newLevel < level:
		_ = db.Exec(fmt.Sprintf("UPDATE %s SET alert_level = ?, alert_since = ? WHERE id = ?", table),
			newLevel, now, id).Error
	}

	// 金额档（正交状态机）：剩余 = limit - used < 阈值 → CAS 抢占命中发信；
	// 拨备/回落使剩余回到阈值之上 → 惰性静默复位。超扣时差为负恒命中（语义正确）。
	if hasBal {
		if remaining := limit - used; remaining < alertBalance {
			res := db.Exec(fmt.Sprintf(
				"UPDATE %s SET alert_balance_hit = ? WHERE id = ? AND alert_balance_hit = 0", table), now, id)
			if res.Error != nil {
				slog.Warn("金额档告警抢占失败", "kind", kind, "id", id, "err", res.Error)
				return
			}
			if res.RowsAffected == 1 {
				if m != nil {
					m.AlertTriggers.With(kind + "_balance").Inc()
				}
				sendAlertMailFn(db, alertMailCtx{Kind: kind, ID: id, Used: used, Limit: limit,
					BalThreshold: alertBalance})
			}
		} else {
			_ = db.Exec(fmt.Sprintf(
				"UPDATE %s SET alert_balance_hit = 0 WHERE id = ? AND alert_balance_hit > 0", table), id).Error
		}
	}
}

// sendAlertMail 收件扇出：org → 全部 org_admin（耗尽附加系统管理员）；user → 本人（有邮箱）+ org_admin。
// ctx.PctThreshold / BalThreshold 恰一非零：分别生成百分比档与金额档文案；金额一律元口径（YuanString）
func sendAlertMail(db *gorm.DB, ctx alertMailCtx) {
	kind, id, used, limit := ctx.Kind, ctx.ID, ctx.Used, ctx.Limit
	ppy := PointsPerYuan(db)
	if kind == "org" {
		var name string
		_ = db.Raw("SELECT name FROM orgs WHERE id = ?", id).Scan(&name).Error
		var subject, body string
		if ctx.BalThreshold > 0 { // 金额档
			remaining := limit - used
			subject = fmt.Sprintf("【额度预警】客户「%s」剩余额度已低于 %s", name, YuanString(ctx.BalThreshold, ppy))
			if remaining <= 0 {
				subject = fmt.Sprintf("【额度耗尽】客户「%s」额度已用尽", name)
			}
			body = fmt.Sprintf(`客户「%s」剩余额度跌破预警线：

  预警阈值：剩余低于 %s
  当前剩余：%s（已用 %s / 限额 %s）
%s
%s
如需继续使用，请联系系统管理员追加额度。`,
				name, YuanString(ctx.BalThreshold, ppy), YuanString(remaining, ppy),
				YuanString(used, ppy), YuanString(limit, ppy),
				map[bool]string{true: "\n客户额度已耗尽：新请求将被拒绝（429），直至追加额度。\n", false: ""}[remaining <= 0],
				consoleLink("/org/billing"))
		} else { // 百分比档
			threshold := ctx.PctThreshold
			pct := fmt.Sprintf("%.1f", float64(used)/float64(limit)*100)
			exhausted := threshold >= 100
			subject = fmt.Sprintf("【额度预警】客户「%s」使用率已达 %d%%", name, threshold)
			if exhausted {
				subject = fmt.Sprintf("【额度耗尽】客户「%s」额度已用尽（100%%）", name)
			}
			body = fmt.Sprintf(`客户「%s」额度使用率达到预警线：

  预警阈值：%d%%
  当前水位：%s%%（已用 %s / 限额 %s）
%s
%s
如需继续使用，请联系系统管理员追加额度。`,
				name, threshold, pct, YuanString(used, ppy), YuanString(limit, ppy),
				map[bool]string{true: "\n客户额度已耗尽：新请求将被拒绝（429），直至追加额度。\n", false: ""}[exhausted],
				consoleLink("/org/billing"))
		}
		NotifyOrgAdmins(db, id, subject, body)
		// 站内同步扇出（顶栏铃铛）：耗尽时平台管理员也收（续费线索）
		NotifyOrgAdminsInsite(db, id, NotifyTypeQuotaAlert, subject, body)
		if strings.Contains(subject, "额度耗尽") {
			NotifyPlatformAdminsInsite(db, NotifyTypeQuotaAlert, subject, body)
		}
		return
	}

	var u struct {
		DisplayName string
		Username    string
		Email       string
		OrgID       int64
	}
	_ = db.Raw("SELECT display_name, username, email, org_id FROM users WHERE id = ?", id).Scan(&u).Error
	who := u.DisplayName
	if who == "" {
		who = u.Username
	}
	var subject, body string
	if ctx.BalThreshold > 0 { // 金额档
		remaining := limit - used
		subject = fmt.Sprintf("【额度预警】子账号「%s」剩余额度已低于 %s", who, YuanString(ctx.BalThreshold, ppy))
		if remaining <= 0 {
			subject = fmt.Sprintf("【额度耗尽】子账号「%s」额度已用尽", who)
		}
		body = fmt.Sprintf(`子账号「%s」(%s) 的个人剩余额度跌破预警线：

  预警阈值：剩余低于 %s
  当前剩余：%s（已用 %s / 限额 %s）
%s
%s
如需继续使用，请联系客户管理员追加额度。`,
			who, u.Username, YuanString(ctx.BalThreshold, ppy), YuanString(remaining, ppy),
			YuanString(used, ppy), YuanString(limit, ppy),
			map[bool]string{true: "\n个人额度已耗尽：新请求将被拒绝（429），直至追加额度。\n", false: ""}[remaining <= 0],
			consoleLink("/member/quota"))
	} else { // 百分比档
		threshold := ctx.PctThreshold
		pct := fmt.Sprintf("%.1f", float64(used)/float64(limit)*100)
		exhausted := threshold >= 100
		subject = fmt.Sprintf("【额度预警】子账号「%s」使用率已达 %d%%", who, threshold)
		if exhausted {
			subject = fmt.Sprintf("【额度耗尽】子账号「%s」额度已用尽（100%%）", who)
		}
		body = fmt.Sprintf(`子账号「%s」(%s) 的个人额度使用率达到预警线：

  预警阈值：%d%%
  当前水位：%s%%（已用 %s / 限额 %s）
%s
%s
如需继续使用，请联系客户管理员追加额度。`,
			who, u.Username, threshold, pct, YuanString(used, ppy), YuanString(limit, ppy),
			map[bool]string{true: "\n个人额度已耗尽：新请求将被拒绝（429），直至追加额度。\n", false: ""}[exhausted],
			consoleLink("/member/quota"))
	}
	NotifyUserAndAdmins(db, u.OrgID, u.Email, subject, body)
	// 站内同步扇出：本人 + 客户管理员（顶栏铃铛）
	NotifyUserInsite(db, id, NotifyTypeQuotaAlert, subject, body)
	NotifyOrgAdminsInsite(db, u.OrgID, NotifyTypeQuotaAlert, subject, body)
}

// ThresholdToLevels 单阈值 UI → alert_levels JSON（0/负 = 关闭）。留多档扩展空间。
func ThresholdToLevels(t int) string {
	if t <= 0 {
		return "[]"
	}
	return fmt.Sprintf("[%d]", t)
}

// LevelsToThreshold alert_levels JSON → UI 展示阈值（取最高档；空/非法 = 0 关）
func LevelsToThreshold(raw string) int {
	levels := parseLevels(raw)
	if len(levels) == 0 {
		return 0
	}
	return int(levels[len(levels)-1])
}

// RecomputeAlertLevel 阈值变更后的静默重算（策略变更非水位事件，不发信）。
// 百分比档与金额档一并重算（金额位 = 命中则置当前时刻、未命中清零）。
func RecomputeAlertLevel(db *gorm.DB, kind string, id int64) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("告警重算 panic", "recover", r)
		}
	}()
	var limit, used, alertBalance int64
	var levelsRaw string
	if kind == "org" {
		var row struct {
			ID           int64
			QuotaLimit   int64
			QuotaUsed    int64
			AlertLevels  string
			AlertBalance int64
		}
		_ = db.Raw("SELECT id, quota_limit, quota_used, alert_levels, alert_balance FROM orgs WHERE id = ?", id).Scan(&row).Error
		if row.ID == 0 {
			return
		}
		limit, used, levelsRaw, alertBalance = row.QuotaLimit, row.QuotaUsed, row.AlertLevels, row.AlertBalance
	} else {
		var row struct {
			ID           int64
			QuotaLimit   *int64
			QuotaUsed    int64
			AlertLevels  string
			AlertBalance int64
		}
		_ = db.Raw("SELECT id, quota_limit, quota_used, alert_levels, alert_balance FROM users WHERE id = ?", id).Scan(&row).Error
		if row.ID == 0 {
			return
		}
		if row.QuotaLimit == nil {
			limit = 0
		} else {
			limit = *row.QuotaLimit
		}
		used, levelsRaw, alertBalance = row.QuotaUsed, row.AlertLevels, row.AlertBalance
	}
	levels := parseLevels(levelsRaw)
	lv := 0
	if len(levels) > 0 && limit > 0 {
		lv = bracket(used, limit, levels)
	}
	now := time.Now().Unix()
	balHit := int64(0)
	if alertBalance > 0 && limit > 0 && limit-used < alertBalance {
		balHit = now
	}
	_ = db.Exec(fmt.Sprintf(
		"UPDATE %s SET alert_level = ?, alert_since = ?, alert_balance_hit = ? WHERE id = ?", alertTable(kind)),
		lv, now, balHit, id).Error
}
