package service

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"gorm.io/gorm"

	"token-gateway/internal/metrics"
)

// 预算告警：边沿状态机 + 竞态唯一 + 惰性复位 + 收件扇出。
// - 档位坍缩为整数 alert_level（"已达最高档"），位域连续填充不变量使三行规则覆盖 N 档
// - 检查点在 Settle 提交后异步执行（数据面零侵入）；DB 条件更新即多实例协调器
// - 拨备回落靠下次检查惰性复位（静默），不加拨备钩子

const alertThrottleSeconds = 60

var alertLastCheck sync.Map // "org:1"/"user:2" → time.Time

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

func throttlePass(key string, m *metrics.Metrics) bool {
	now := time.Now()
	if v, ok := alertLastCheck.Load(key); ok {
		if t, _ := v.(time.Time); now.Sub(t) < alertThrottleSeconds*time.Second {
			if m != nil {
				m.AlertThrottled.Inc()
			}
			return false
		}
	}
	alertLastCheck.Store(key, now)
	return true
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
func bracket(used, limit int64, levels []int64) int {
	lv := 0
	for i, t := range levels {
		if used*100 >= limit*t {
			lv = i + 1
		}
	}
	return lv
}

// checkAlert 单主体水位检查。升降档规则：
//
//	new > level  → CAS 抢占（UPDATE ... WHERE alert_level < new），胜者发信（跳档只发最高档）
//	new < level  → 静默降级（拨备回落的惰性复位，不发信）
//	new == level → 无动作
// sendAlertMailFn 发信动作（包级可替换：测试注入观察器，避免依赖邮件副作用）
var sendAlertMailFn = sendAlertMail

func checkAlert(db *gorm.DB, m *metrics.Metrics, kind string, id int64) {
	key := kind + ":" + fmt.Sprint(id)
	if !throttlePass(key, m) {
		return
	}

	var limit, used int64
	var levelsRaw string
	var level int
	if kind == "org" {
		var row struct {
			ID          int64
			QuotaLimit  int64
			QuotaUsed   int64
			AlertLevels string
			AlertLevel  int
		}
		if err := db.Raw(`SELECT id, quota_limit, quota_used, alert_levels, alert_level FROM orgs WHERE id = ?`,
			id).Scan(&row).Error; err != nil || row.ID == 0 {
			return
		}
		limit, used, levelsRaw, level = row.QuotaLimit, row.QuotaUsed, row.AlertLevels, row.AlertLevel
		if limit <= 0 {
			return
		}
	} else {
		var row struct {
			ID          int64
			QuotaLimit  *int64
			QuotaUsed   int64
			AlertLevels string
			AlertLevel  int
		}
		if err := db.Raw(`SELECT id, quota_limit, quota_used, alert_levels, alert_level FROM users WHERE id = ?`,
			id).Scan(&row).Error; err != nil || row.ID == 0 {
			return
		}
		if row.QuotaLimit == nil || *row.QuotaLimit <= 0 {
			return // 不限额者不参与
		}
		limit, used, levelsRaw, level = *row.QuotaLimit, row.QuotaUsed, row.AlertLevels, row.AlertLevel
	}

	levels := parseLevels(levelsRaw)
	if len(levels) == 0 {
		return // 空数组 = 关闭
	}
	newLevel := bracket(used, limit, levels)
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
			sendAlertMailFn(db, kind, id, used, limit, levels[newLevel-1])
		}
	case newLevel < level:
		_ = db.Exec(fmt.Sprintf("UPDATE %s SET alert_level = ?, alert_since = ? WHERE id = ?", table),
			newLevel, now, id).Error
	}
}

// sendAlertMail 收件扇出：org → 全部 org_admin（耗尽附加平台管理员）；user → 本人（有邮箱）+ org_admin。
func sendAlertMail(db *gorm.DB, kind string, id, used, limit, threshold int64) {
	pct := fmt.Sprintf("%.1f", float64(used)/float64(limit)*100)
	if kind == "org" {
		var name string
		_ = db.Raw("SELECT name FROM orgs WHERE id = ?", id).Scan(&name).Error
		exhausted := threshold >= 100
		subject := fmt.Sprintf("【额度预警】公司「%s」使用率已达 %d%%", name, threshold)
		if exhausted {
			subject = fmt.Sprintf("【额度耗尽】公司「%s」额度已用尽（100%%）", name)
		}
		body := fmt.Sprintf(`公司「%s」额度使用率达到预警线：

  预警阈值：%d%%
  当前水位：%s%%（已用 %s / 限额 %s token）
%s
%s
如需继续使用，请联系平台管理员追加额度。`,
			name, threshold, pct, fmtInt(used), fmtInt(limit),
			map[bool]string{true: "\n公司额度已耗尽：新请求将被拒绝（429），直至追加额度。\n", false: ""}[exhausted],
			consoleLink("/org/billing"))
		NotifyOrgAdmins(db, id, subject, body)
		if exhausted {
			NotifyPlatformAdmins(db, subject+"（续费线索）", body)
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
	exhausted := threshold >= 100
	subject := fmt.Sprintf("【额度预警】员工「%s」使用率已达 %d%%", who, threshold)
	if exhausted {
		subject = fmt.Sprintf("【额度耗尽】员工「%s」额度已用尽（100%%）", who)
	}
	body := fmt.Sprintf(`员工「%s」(%s) 的个人额度使用率达到预警线：

  预警阈值：%d%%
  当前水位：%s%%（已用 %s / 限额 %s token）
%s
%s
如需继续使用，请联系公司管理员追加额度。`,
		who, u.Username, threshold, pct, fmtInt(used), fmtInt(limit),
		map[bool]string{true: "\n个人额度已耗尽：新请求将被拒绝（429），直至追加额度。\n", false: ""}[exhausted],
		consoleLink("/member/quota"))
	NotifyUserAndAdmins(db, u.OrgID, u.Email, subject, body)
}

func fmtInt(n int64) string { return fmt.Sprintf("%d", n) }

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
func RecomputeAlertLevel(db *gorm.DB, kind string, id int64) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("告警重算 panic", "recover", r)
		}
	}()
	var limit, used int64
	var levelsRaw string
	if kind == "org" {
		var row struct {
			ID          int64
			QuotaLimit  int64
			QuotaUsed   int64
			AlertLevels string
		}
		_ = db.Raw("SELECT id, quota_limit, quota_used, alert_levels FROM orgs WHERE id = ?", id).Scan(&row).Error
		if row.ID == 0 {
			return
		}
		limit, used, levelsRaw = row.QuotaLimit, row.QuotaUsed, row.AlertLevels
	} else {
		var row struct {
			ID          int64
			QuotaLimit  *int64
			QuotaUsed   int64
			AlertLevels string
		}
		_ = db.Raw("SELECT id, quota_limit, quota_used, alert_levels FROM users WHERE id = ?", id).Scan(&row).Error
		if row.ID == 0 {
			return
		}
		if row.QuotaLimit == nil {
			limit = 0
		} else {
			limit = *row.QuotaLimit
		}
		used = row.QuotaUsed
		levelsRaw = row.AlertLevels
	}
	levels := parseLevels(levelsRaw)
	lv := 0
	if len(levels) > 0 && limit > 0 {
		lv = bracket(used, limit, levels)
	}
	_ = db.Exec(fmt.Sprintf("UPDATE %s SET alert_level = ?, alert_since = ? WHERE id = ?", alertTable(kind)),
		lv, time.Now().Unix(), id).Error
}
