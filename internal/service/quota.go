package service

import (
	"errors"
	"time"

	"gorm.io/gorm"

	"token-gateway/internal/model"
)

// 额度读写的唯一入口：
// - 上限式分配（quota_limit），不做点数划拨，实际消耗始终只有 quota_used 一个真相来源
// - 结算 = 同一事务内双记账（user + org）+ 写日志

var (
	ErrNotFound  = errors.New("not found")
	ErrUserQuota = errors.New("employee quota exceeded, please contact your company admin")
	ErrOrgQuota  = errors.New("company quota exhausted, please contact the platform admin")
)

// Precheck 额度预检查（纯读，不锁）：两级剩余额度都必须 > 0
func Precheck(db *gorm.DB, userID int64) error {
	var row struct {
		UID       int64
		UserLimit *int64
		UserUsed  int64
		OrgLimit  int64
		OrgUsed   int64
	}
	err := db.Raw(`
		SELECT u.id AS uid, u.quota_limit AS user_limit, u.quota_used AS user_used,
		       o.quota_limit AS org_limit, o.quota_used AS org_used
		FROM users u JOIN orgs o ON o.id = u.org_id
		WHERE u.id = ?`, userID).Scan(&row).Error
	if err != nil {
		return err
	}
	if row.UID == 0 {
		return ErrNotFound
	}
	if row.UserLimit != nil && row.UserUsed >= *row.UserLimit {
		return ErrUserQuota
	}
	if row.OrgUsed >= row.OrgLimit {
		return ErrOrgQuota
	}
	return nil
}

// Settle 事后结算：响应已发给客户端，无法回滚，故无条件记账（超扣幅度封顶在单请求成本内）。
// cost>0 时同事务双记账；日志无论如何都写（含被拦截的请求，cost=0）。
func Settle(db *gorm.DB, rec *model.UsageLog) error {
	now := time.Now().Unix()
	return db.Transaction(func(tx *gorm.DB) error {
		if rec.Cost > 0 {
			if err := tx.Exec("UPDATE users SET quota_used = quota_used + ?, updated_at = ? WHERE id = ?",
				rec.Cost, now, rec.UserID).Error; err != nil {
				return err
			}
			if err := tx.Exec("UPDATE orgs SET quota_used = quota_used + ?, updated_at = ? WHERE id = ?",
				rec.Cost, now, rec.OrgID).Error; err != nil {
				return err
			}
		}
		return tx.Create(rec).Error
	})
}

// AddOrgQuota 平台给公司追加限额（带审计流水）；amount 可为负用于回收
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
		return tx.Create(&model.QuotaGrant{
			SubjectType: "org", SubjectID: orgID, Amount: amount,
			Remark: remark, OperatorID: opID(operatorID), CreatedAt: now,
		}).Error
	})
}

// AddUserQuota 公司管理员给员工追加限额；强制 org 归属校验防越权
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
