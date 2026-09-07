package service

import (
	"fmt"
	"log/slog"

	"gorm.io/gorm"

	"token-gateway/internal/config"
	"token-gateway/internal/database"
)

// 迁移表顺序（满足外键依赖）
var migrateTables = []string{
	"orgs", "users", "channels", "channel_keys", "models", "channel_abilities",
	"user_model_grants", "cost_centers", "api_keys", "usage_logs", "quota_grants",
	"quota_requests", "period_balances", "vendor_bills", "settings",
}

// MigrateFromSQLite 把一个 SQLite 库的全部业务数据搬到当前库（通常为 postgres）。
// 目标表非空时跳过该表（防重复导入）；迁移后打印逐表行数核对。
func MigrateFromSQLite(dst *gorm.DB, srcPath string) error {
	src, err := database.Open(config.Database{Driver: "sqlite", Path: srcPath})
	if err != nil {
		return fmt.Errorf("打开源 SQLite 失败: %w", err)
	}
	sqlSrc, _ := src.DB()
	defer sqlSrc.Close()

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
		if err := src.Table(t).Order("id").Find(&rows).Error; err != nil {
			return fmt.Errorf("读取 %s 失败: %w", t, err)
		}
		// settings 无 id 列时 Find map 也能工作；批量写入
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

	// PG：重置 identity 序列到最大 id（显式 is_called=true），并回读校验
	if database.Dialect == "postgres" {
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
