package service

import (
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"token-gateway/internal/model"
)

// 额度读写的唯一入口：
// - 上限式分配（quota_limit），不做点数划拨，实际消耗始终只有 quota_used 一个真相来源
// - 结算 = 同一事务内双记账（user + org）+ 写日志

var (
	ErrNotFound    = errors.New("not found")
	ErrUserQuota   = errors.New("employee quota exceeded, please contact your company admin")
	ErrOrgQuota    = errors.New("company quota exhausted, please contact the platform admin")
	ErrUserMonthly = errors.New("employee monthly spending cap reached, resets next month")
	ErrOrgMonthly  = errors.New("company monthly spending cap reached, resets next month")
)

// currentPeriod 当前账期（账期时区 wall-clock，'YYYY-MM'）
func currentPeriod() string {
	return PeriodOf(BillingLoc(), time.Now().Unix())
}

// Precheck 额度预检查（纯读，不锁）：两级总余额与单月上限都必须未达。
// 月累计按"存储账期 == 当前账期"取值，否则视为 0（惰性跨月清零的读侧）。
func Precheck(db *gorm.DB, userID int64) error {
	period := currentPeriod()
	var row struct {
		UID           int64
		UserLimit     *int64
		UserUsed      int64
		UserMonthlyQ  int64
		UserMonthlyC  int64
		OrgLimit      int64
		OrgUsed       int64
		OrgMonthlyQ   int64
		OrgMonthlyC   int64
	}
	err := db.Raw(`
		SELECT u.id AS uid, u.quota_limit AS user_limit, u.quota_used AS user_used,
		       u.monthly_quota AS user_monthly_q,
		       CASE WHEN u.monthly_period = ? THEN u.monthly_cost ELSE 0 END AS user_monthly_c,
		       o.quota_limit AS org_limit, o.quota_used AS org_used,
		       o.monthly_quota AS org_monthly_q,
		       CASE WHEN o.monthly_period = ? THEN o.monthly_cost ELSE 0 END AS org_monthly_c
		FROM users u JOIN orgs o ON o.id = u.org_id
		WHERE u.id = ?`, period, period, userID).Scan(&row).Error
	if err != nil {
		return err
	}
	if row.UID == 0 {
		return ErrNotFound
	}
	if row.UserLimit != nil && row.UserUsed >= *row.UserLimit {
		return ErrUserQuota
	}
	if row.UserMonthlyQ > 0 && row.UserMonthlyC >= row.UserMonthlyQ {
		return ErrUserMonthly
	}
	if row.OrgUsed >= row.OrgLimit {
		return ErrOrgQuota
	}
	if row.OrgMonthlyQ > 0 && row.OrgMonthlyC >= row.OrgMonthlyQ {
		return ErrOrgMonthly
	}
	return nil
}

// Settle 事后结算：响应已发给客户端，无法回滚，故无条件记账（超扣幅度封顶在单请求成本内）。
// cost>0 时同事务双记账（总额 + 月累计，跨月首笔原子重置）+ 欠费检查：
// 客户总额度耗尽 → 自动置 status=2 欠费停服（仅从 1 迁移；手动停用 0 不受影响）。
func Settle(db *gorm.DB, rec *model.UsageLog) error {
	now := time.Now().Unix()
	period := currentPeriod()
	return db.Transaction(func(tx *gorm.DB) error {
		if rec.Cost > 0 {
			if err := tx.Exec(`UPDATE users SET quota_used = quota_used + ?,
					monthly_cost = CASE WHEN monthly_period = ? THEN monthly_cost + ? ELSE ? END,
					monthly_period = ?, updated_at = ?
				WHERE id = ?`, rec.Cost, period, rec.Cost, rec.Cost, period, now, rec.UserID).Error; err != nil {
				return err
			}
			if err := tx.Exec(`UPDATE orgs SET quota_used = quota_used + ?,
					monthly_cost = CASE WHEN monthly_period = ? THEN monthly_cost + ? ELSE ? END,
					monthly_period = ?, updated_at = ?
				WHERE id = ?`, rec.Cost, period, rec.Cost, rec.Cost, period, now, rec.OrgID).Error; err != nil {
				return err
			}
			// 欠费停服：额度耗尽即停（不影响管理台登录，数据面由鉴权层拦截）
			if err := tx.Exec(`UPDATE orgs SET status = 2, updated_at = ?
				WHERE id = ? AND status = 1 AND quota_limit > 0 AND quota_used >= quota_limit`,
				now, rec.OrgID).Error; err != nil {
				return err
			}
		}
		return tx.Create(rec).Error
	})
}

// AddOrgQuota 平台给客户追加限额（带审计流水）；amount 可为负用于回收。
// 追加后如有余量，欠费停服（status=2）自动恢复为启用——手动停用（0）不会被误恢复。
func AddOrgQuota(db *gorm.DB, orgID, amount, operatorID int64, remark string) error {
	now := time.Now().Unix()
	return db.Transaction(func(tx *gorm.DB) error {
		res := tx.Exec("UPDATE orgs SET quota_limit = quota_limit + ?, updated_at = ? WHERE id = ?",
			amount, now, orgID)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrNotFound
		}
		if err := tx.Exec(`UPDATE orgs SET status = 1, updated_at = ?
			WHERE id = ? AND status = 2 AND quota_limit > quota_used`, now, orgID).Error; err != nil {
			return err
		}
		return tx.Create(&model.QuotaGrant{
			SubjectType: "org", SubjectID: orgID, Amount: amount,
			Remark: remark, OperatorID: opID(operatorID), CreatedAt: now,
		}).Error
	})
}

// AddUserQuota 客户管理员给子账号追加限额；强制 org 归属校验防越权
func AddUserQuota(db *gorm.DB, orgID, userID, amount, operatorID int64, remark string) error {
	now := time.Now().Unix()
	return db.Transaction(func(tx *gorm.DB) error {
		res := tx.Exec("UPDATE users SET quota_limit = COALESCE(quota_limit, 0) + ?, updated_at = ? WHERE id = ? AND org_id = ?",
			amount, now, userID, orgID)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrNotFound
		}
		return tx.Create(&model.QuotaGrant{
			SubjectType: "user", SubjectID: userID, Amount: amount,
			Remark: remark, OperatorID: opID(operatorID), CreatedAt: now,
		}).Error
	})
}

// SetUserQuotaUnlimited 子账号限额"设值"路径（转不限 / 转限额）：同事务读旧值→更新→差值入流水，
// 使 Σgrants == COALESCE(quota_limit, 0) 恒成立（不限额以 0 为基）。
// 仅状态实际切换时动作：转不限记 amount=−旧值；转限额以当前消耗为起点记 amount=起点；同态重复调用为无操作。
func SetUserQuotaUnlimited(db *gorm.DB, orgID, userID int64, unlimited bool, operatorID int64) error {
	now := time.Now().Unix()
	return db.Transaction(func(tx *gorm.DB) error {
		q := tx.Model(&model.User{}).Where("id = ? AND org_id = ?", userID, orgID)
		if tx.Dialector.Name() == "postgres" {
			q = q.Clauses(clause.Locking{Strength: "UPDATE"}) // PG 行锁；SQLite 由 _txlock=immediate 串行化护住窗口
		}
		var m model.User
		if err := q.First(&m).Error; err != nil {
			return ErrNotFound
		}
		var newLimit *int64
		var delta int64
		remark := "设值调整"
		switch {
		case unlimited && m.QuotaLimit != nil: // 限额 → 不限：归还全部上限
			newLimit = nil
			delta = -*m.QuotaLimit
			remark += "：转不限额"
		case !unlimited && m.QuotaLimit == nil: // 不限 → 限额：以当前消耗为起点
			v := m.QuotaUsed
			newLimit = &v
			delta = v
			remark += "：转限额"
		default: // 同态，无操作
			return nil
		}
		if err := tx.Model(&model.User{}).Where("id = ?", userID).
			Updates(map[string]any{"quota_limit": newLimit, "updated_at": now}).Error; err != nil {
			return err
		}
		if delta == 0 {
			return nil // 状态变了但差值为 0（如不限→限额且消耗为 0），无流水必要
		}
		return tx.Create(&model.QuotaGrant{
			SubjectType: "user", SubjectID: userID, Amount: delta,
			Remark: remark, OperatorID: opID(operatorID), CreatedAt: now,
		}).Error
	})
}

func opID(id int64) *int64 {
	if id == 0 {
		return nil
	}
	return &id
}

// PointsPerYuan 额度点数与元的兑换率（settings 可调，默认 1 元 = 1,000,000 点）
func PointsPerYuan(db *gorm.DB) int64 {
	var v string
	if err := db.Raw("SELECT value FROM settings WHERE key = 'points_per_yuan'").Scan(&v).Error; err != nil || v == "" {
		return 1_000_000
	}
	var n int64
	for _, ch := range v { // 简易 atoi，避免非法值崩溃
		if ch < '0' || ch > '9' {
			return 1_000_000
		}
		n = n*10 + int64(ch-'0')
	}
	if n <= 0 {
		return 1_000_000
	}
	return n
}
