package service

import (
	"fmt"
	"time"

	"gorm.io/gorm"

	"token-gateway/internal/database"
)

type Totals struct {
	Requests   int64 `json:"requests"`
	Tokens     int64 `json:"tokens"`
	Cost       int64 `json:"cost"`        // 客户消耗（平台营收）
	VendorCost int64 `json:"vendor_cost"` // 厂商成本；毛利 = Cost - VendorCost
	Errors     int64 `json:"errors"`
}

type DayPoint struct {
	Date     string `json:"date"`
	Requests int64  `json:"requests"`
	Tokens   int64  `json:"tokens"`
	Cost     int64  `json:"cost"`
}

type GroupPoint struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Requests   int64  `json:"requests"`
	Tokens     int64  `json:"tokens"`
	Cost       int64  `json:"cost"`        // 营收
	VendorCost int64  `json:"vendor_cost"` // 厂商成本
	Profit     int64  `json:"profit"`      // 毛利 = Cost - VendorCost
}

type Overview struct {
	Today   Totals       `json:"today"`
	Total   Totals       `json:"total"`
	Series  []DayPoint   `json:"series"` // 近 7 天（含今日）
	ByOrg   []GroupPoint `json:"by_org,omitempty"`
	ByUser  []GroupPoint `json:"by_user,omitempty"`
	ByModel []GroupPoint `json:"by_model"`
}

// Scope 统计范围：平台=全部；公司=org_id；员工=user_id
type Scope struct {
	OrgID  *int64
	UserID *int64
}

// cond 返回带 l. 前缀的过滤条件（所有查询统一以 l 别名引用 usage_logs，
// 避免与 users/orgs join 后 org_id/user_id 列名歧义）
func (s Scope) cond() (string, []any) {
	cond, args := "1=1", []any{}
	if s.OrgID != nil {
		cond += " AND l.org_id = ?"
		args = append(args, *s.OrgID)
	}
	if s.UserID != nil {
		cond += " AND l.user_id = ?"
		args = append(args, *s.UserID)
	}
	return cond, args
}

func localDay0(offsetDays int) int64 {
	now := time.Now()
	t := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	return t.AddDate(0, 0, offsetDays).Unix()
}

// StatsOverview 聚合统计（今日/累计/近7日序列/分组 Top）
func StatsOverview(db *gorm.DB, scope Scope) (*Overview, error) {
	cond, args := scope.cond()
	todayCond, todayArgs := scope.cond()
	todayCond += " AND created_at >= ?"
	todayArgs = append(todayArgs, localDay0(0))

	ov := &Overview{}
	if err := scanTotals(db, &ov.Today, todayCond, todayArgs); err != nil {
		return nil, err
	}
	if err := scanTotals(db, &ov.Total, cond, args); err != nil {
		return nil, err
	}

	// 近 7 日序列（含今日），缺失日期补零（日期表达式按方言分支）
	since := localDay0(-6)
	seriesCond, seriesArgs := scope.cond()
	seriesCond += " AND l.created_at >= ?"
	seriesArgs = append(seriesArgs, since)
	dateExpr := "strftime('%Y-%m-%d', l.created_at, 'unixepoch', 'localtime')"
	if database.Dialect == "postgres" {
		dateExpr = "to_char(to_timestamp(l.created_at), 'YYYY-MM-DD')"
	}
	var rows []DayPoint
	err := db.Raw(fmt.Sprintf(`
		SELECT %s AS date,
		       COUNT(*) AS requests,
		       COALESCE(SUM(l.prompt_tokens + l.completion_tokens), 0) AS tokens,
		       COALESCE(SUM(l.cost), 0) AS cost
		FROM usage_logs l WHERE %s
		GROUP BY date ORDER BY date`, dateExpr, seriesCond), seriesArgs...).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	byDate := make(map[string]DayPoint, len(rows))
	for _, r := range rows {
		byDate[r.Date] = r
	}
	now := time.Now()
	for i := 6; i >= 0; i-- {
		d := now.AddDate(0, 0, -i).Format("2006-01-02")
		if p, ok := byDate[d]; ok {
			ov.Series = append(ov.Series, p)
		} else {
			ov.Series = append(ov.Series, DayPoint{Date: d})
		}
	}

	// 分组 Top10（按范围裁剪维度）
	if scope.OrgID == nil && scope.UserID == nil {
		ov.ByOrg, err = queryGroups(db, fmt.Sprintf(`
			SELECT t.id AS id, t.name AS name, COUNT(*) AS requests,
			       COALESCE(SUM(l.prompt_tokens + l.completion_tokens), 0) AS tokens,
			       COALESCE(SUM(l.cost), 0) AS cost, COALESCE(SUM(l.vendor_cost), 0) AS vendor_cost
			FROM usage_logs l JOIN orgs t ON t.id = l.org_id
			WHERE %s GROUP BY t.id, t.name ORDER BY cost DESC, requests DESC LIMIT 10`, cond), args)
		if err != nil {
			return nil, err
		}
	}
	if scope.UserID == nil {
		cond2, args2 := scope.cond()
		ov.ByUser, err = queryGroups(db, fmt.Sprintf(`
			SELECT t.id AS id, t.username AS name, COUNT(*) AS requests,
			       COALESCE(SUM(l.prompt_tokens + l.completion_tokens), 0) AS tokens,
			       COALESCE(SUM(l.cost), 0) AS cost, COALESCE(SUM(l.vendor_cost), 0) AS vendor_cost
			FROM usage_logs l JOIN users t ON t.id = l.user_id
			WHERE %s GROUP BY t.id, t.username ORDER BY cost DESC, requests DESC LIMIT 10`, cond2), args2)
		if err != nil {
			return nil, err
		}
	}
	ov.ByModel, err = queryGroups(db, fmt.Sprintf(`
		SELECT 0 AS id, l.model_name AS name, COUNT(*) AS requests,
		       COALESCE(SUM(l.prompt_tokens + l.completion_tokens), 0) AS tokens,
		       COALESCE(SUM(l.cost), 0) AS cost, COALESCE(SUM(l.vendor_cost), 0) AS vendor_cost
		FROM usage_logs l
		WHERE %s GROUP BY l.model_name ORDER BY cost DESC, requests DESC LIMIT 10`, cond), args)
	if err != nil {
		return nil, err
	}
	return ov, nil
}

func scanTotals(db *gorm.DB, t *Totals, cond string, args []any) error {
	return db.Raw(fmt.Sprintf(`
		SELECT COUNT(*) AS requests,
		       COALESCE(SUM(l.prompt_tokens + l.completion_tokens), 0) AS tokens,
		       COALESCE(SUM(l.cost), 0) AS cost,
		       COALESCE(SUM(l.vendor_cost), 0) AS vendor_cost,
		       COALESCE(SUM(CASE WHEN l.status >= 400 OR l.error != '' THEN 1 ELSE 0 END), 0) AS errors
		FROM usage_logs l WHERE %s`, cond), args...).Scan(t).Error
}

func queryGroups(db *gorm.DB, query string, args []any) ([]GroupPoint, error) {
	var out []GroupPoint
	err := db.Raw(query, args...).Scan(&out).Error
	for i := range out {
		out[i].Profit = out[i].Cost - out[i].VendorCost
	}
	return out, err
}
