package service

import (
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"

	"token-gateway/internal/database"
)

// 客户级模型白名单的存量继承与共享助手。
// 白名单引入前所有启用模型对所有客户可见；首次升级时把每个客户
// 「其子账号已获授权模型的并集」写入 org_model_grants（source=backfill），
// 存量使用零感知。之后平台可审批/手动调整开通范围，回收=删行并级联
// 移除该客户子账号的相应授权（见 platform SetOrgModels）。

// orgGrantBackfillLockKey 存量继承的 PG 咨询锁键（与 migrate/bootstrap 错开）
const orgGrantBackfillLockKey = 74274157703

// SettingOrgGrantsBackfilled settings 标记键：继承只跑一次。
// 用标记而非「表空才跑」：否则无法区分「未迁移」与「已全部回收」，
// 手工回收/级联清理后一旦表空，重启会复活已回收的开通。
const SettingOrgGrantsBackfilled = "org_model_grants_backfilled"

// BackfillOrgModelGrants 首次启动时执行存量继承（幂等：settings 标记 + ON CONFLICT）。
// 必须在 SetupRouter 之前调用——收窄逻辑上线瞬间白名单必须已就绪。
func BackfillOrgModelGrants(db *gorm.DB) error {
	return database.WithAdvisoryLock(db, orgGrantBackfillLockKey, func(tx *gorm.DB) error {
		var done string
		if err := tx.Raw("SELECT value FROM settings WHERE key = ?", SettingOrgGrantsBackfilled).Scan(&done).Error; err != nil {
			return err
		}
		if done != "" {
			return nil
		}
		// 单事务：并集继承（幂等）→ 写标记。PG 多实例由咨询锁串行化。
		// granted_by 的 NULL 必须显式定型：PG 在 DISTINCT 下会把未定型字面量解析为
		// text，插入 bigint 列报 42804 并导致启动失败（SQLite 不做此检查，故单测不拦）。
		res := tx.Exec(`INSERT INTO org_model_grants (org_id, model_name, granted_by, source, created_at)
			SELECT DISTINCT u.org_id, g.model_name, CAST(NULL AS BIGINT), 'backfill', CAST(? AS BIGINT)
			FROM user_model_grants g JOIN users u ON u.id = g.user_id
			WHERE u.org_id IS NOT NULL AND u.org_id > 0
			ON CONFLICT (org_id, model_name) DO NOTHING`, time.Now().Unix())
		if res.Error != nil {
			return res.Error
		}
		if err := tx.Exec(`INSERT INTO settings (key, value) VALUES (?, '1')
			ON CONFLICT(key) DO UPDATE SET value = '1'`, SettingOrgGrantsBackfilled).Error; err != nil {
			return err
		}
		if res.RowsAffected > 0 {
			slog.Info("org grants backfill: 存量客户模型开通已继承", "rows", res.RowsAffected)
		}
		return nil
	})
}

// OrgGrantedModels 客户已开通的模型名集合（各 handler 白名单校验共用）
func OrgGrantedModels(db *gorm.DB, orgID int64) []string {
	var names []string
	_ = db.Raw("SELECT model_name FROM org_model_grants WHERE org_id = ?", orgID).Scan(&names).Error
	return names
}

// OrgGrantedSet 同上，返回 set 便于 membership 判断
func OrgGrantedSet(db *gorm.DB, orgID int64) map[string]struct{} {
	set := make(map[string]struct{}, 8)
	for _, n := range OrgGrantedModels(db, orgID) {
		set[n] = struct{}{}
	}
	return set
}

// SplitModelNames 申请单 model_names 逗号串拆分（去空、去重、保序）。
// org 包已有私有 modelNamesOf；放 service 导出一份供 platform 审批侧复用。
func SplitModelNames(s string) []string {
	seen := make(map[string]struct{}, 4)
	var out []string
	for _, part := range strings.Split(s, ",") {
		n := strings.TrimSpace(part)
		if n == "" {
			continue
		}
		if _, dup := seen[n]; dup {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	return out
}
