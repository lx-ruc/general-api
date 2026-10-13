package service

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"gorm.io/gorm"

	"token-gateway/internal/config"
	"token-gateway/internal/database"
)

// 迁移表顺序（满足外键依赖）。
// 历史教训（两回）：recharge_requests/audit_logs、org_model_*/notifications 都曾漏在
// 清单外，切 PG 静默丢数据——清单必须与 schema.sql 的业务表保持一致，漏表由下方
// 源表差集守卫兜底报错，不允许静默。
var migrateTables = []string{
	"orgs", "users", "channels", "channel_keys", "models", "channel_abilities",
	"user_model_grants", "cost_centers", "api_keys", "access_tokens", "usage_logs",
	"quota_grants", "quota_requests", "recharge_requests",
	"org_model_prices", "org_model_grants", "org_model_requests", "notifications",
	"audit_logs", "period_balances", "vendor_bills", "settings",
}

// 刻意不迁的瞬态表：分钟级时效（验证码/重置令牌），迁移停机窗口内必然过期，
// 搬过去只剩误导。新增瞬态表放这里，业务表一律进 migrateTables。
var migrateTransientTables = []string{"verification_codes", "password_reset_tokens"}

// MigrateFromSQLite 把一个 SQLite 库的全部业务数据搬到当前库（通常为 postgres）。
// 目标表非空时跳过该表（防重复导入）；迁移后打印逐表行数核对。
func MigrateFromSQLite(dst *gorm.DB, srcPath string) error {
	// SQLite 驱动对不存在的路径会静默新建空库：源路径打错字会"成功迁移 0 行"，
	// 必须先确认文件真实存在
	if _, err := os.Stat(srcPath); err != nil {
		return fmt.Errorf("源库文件不可访问: %w", err)
	}
	src, err := database.Open(config.Database{Driver: "sqlite", Path: srcPath})
	if err != nil {
		return fmt.Errorf("打开源 SQLite 失败: %w", err)
	}
	sqlSrc, _ := src.DB()
	defer sqlSrc.Close()

	// 漏表守卫：源库存在而清单未覆盖的表（瞬态白名单除外）→ 拒迁报错。
	// schema.sql 未来新增表若忘了同步清单，这里硬失败而不是静默丢数据。
	if err := guardAllTablesCovered(src); err != nil {
		return err
	}

	var totalSrc, totalDst int64
	for _, t := range migrateTables {
		var srcCount, dstCount int64
		_ = src.Table(t).Count(&srcCount).Error
		_ = dst.Table(t).Count(&dstCount).Error
		if srcCount == 0 {
			slog.Info("迁移跳过（源为空）", "table", t)
			continue
		}
		if dstCount > 0 {
			slog.Warn("迁移跳过（目标表非空，防重复导入）", "table", t, "dst_rows", dstCount)
			continue
		}
		var rows []map[string]any
		// settings 无 id 列（key 为主键），排序键随表调整；其余表按 id 保证迁移顺序确定
		order := "id"
		if t == "settings" {
			order = "key"
		}
		if err := src.Table(t).Order(order).Find(&rows).Error; err != nil {
			return fmt.Errorf("读取 %s 失败: %w", t, err)
		}
		// 批量写入
		if err := dst.Table(t).CreateInBatches(&rows, 500).Error; err != nil {
			return fmt.Errorf("写入 %s 失败: %w", t, err)
		}
		_ = dst.Table(t).Count(&dstCount).Error
		if dstCount != srcCount {
			return fmt.Errorf("表 %s 行数不一致：源 %d / 目标 %d", t, srcCount, dstCount)
		}
		slog.Info("迁移完成", "table", t, "rows", dstCount)
		totalSrc += srcCount
		totalDst += dstCount
	}

	// PG：重置 identity 序列到最大 id（显式 is_called=true），并回读校验。
	// 用 dst 自身方言判断——database.Dialect 是全局量，上面打开源 SQLite 已把它改成 sqlite
	if dst.Dialector.Name() == "postgres" {
		for _, t := range migrateTables {
			if t == "settings" {
				continue
			}
			if err := dst.Exec(fmt.Sprintf(
				`SELECT setval(pg_get_serial_sequence('%s', 'id'), COALESCE((SELECT MAX(id) FROM %s), 1), true)`, t, t)).Error; err != nil {
				slog.Warn("序列重置失败", "table", t, "err", err)
			}
			var next int64
			_ = dst.Raw(fmt.Sprintf(
				`SELECT last_value + 1 FROM pg_sequences WHERE sequencename = '%s_id_seq'`, t)).Scan(&next).Error
			var maxID int64
			_ = dst.Raw(fmt.Sprintf("SELECT COALESCE(MAX(id),0) FROM %s", t)).Scan(&maxID).Error
			if next <= maxID {
				// 校验失败：强制再来一次（防御 identity 列序列名差异）
				_ = dst.Exec(fmt.Sprintf(
					`SELECT setval(pg_get_serial_sequence('%s','id'), %d, true)`, t, maxID)).Error
				slog.Warn("序列校验不符，已强制重置", "table", t, "expect_next", maxID+1, "saw_next", next)
			}
		}
	}
	slog.Info("SQLite → 当前库 迁移结束", "总行数", totalDst)
	if totalDst != totalSrc {
		return fmt.Errorf("存在被跳过的表：源总计 %d 行，实际迁移 %d 行", totalSrc, totalDst)
	}
	return nil
}

// guardAllTablesCovered 源库用户表必须全部落在 migrateTables ∪ migrateTransientTables，
// 否则报错列出漏网表。检查的是表存在性而非行数：空表也必须被显式归类，
// 迫使新增 schema 时做出有意识的决定，而不是靠运气。
func guardAllTablesCovered(src *gorm.DB) error {
	var names []string
	if err := src.Raw(
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`,
	).Scan(&names).Error; err != nil {
		return fmt.Errorf("读取源库表清单失败: %w", err)
	}
	covered := make(map[string]struct{}, len(migrateTables)+len(migrateTransientTables))
	for _, t := range append(append([]string{}, migrateTables...), migrateTransientTables...) {
		covered[t] = struct{}{}
	}
	var missing []string
	for _, n := range names {
		if _, ok := covered[n]; !ok {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf(
			"源库存在未纳入迁移清单的表：%s；请在 migrate_sqlite.go 的 migrateTables（业务表）或 migrateTransientTables（瞬态表）补齐后重跑，避免静默丢数据",
			strings.Join(missing, ", "))
	}
	return nil
}
