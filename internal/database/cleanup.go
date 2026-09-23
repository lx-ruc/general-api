package database

import (
	"fmt"
	"log/slog"

	"gorm.io/gorm"
)

// cleanupLockKey PG 清理咨询锁的固定键（沿 migrateLockKey=...701 序列顺延）
const cleanupLockKey = 74274157703

// Cleanup 启动时幂等清理，保持两条不变量（模型一律经渠道从上游登记）：
//  1. 没配密钥的渠道（Key 池无任何行且 legacy 密文为空，与 DeleteChannelKey 口径一致）
//     不携带任何 channel_abilities —— 早期预置逻辑给无密钥渠道预填的模型在此清除；
//  2. 无任何渠道能力支撑的 models 行连同其 user_model_grants 一并删除（定价可在下次
//     配渠道时经表单自动重登，子账号授权需重新勾选）。
//
// 运行时路径（DeleteChannel / DeleteChannelKey）保持最小副作用，本函数是唯一对账点：
// 每次启动执行，清理数为 0 时不产生日志；SQLite 下逐条执行，中途崩溃由下次启动幂等收敛。
func Cleanup(db *gorm.DB) error {
	return WithAdvisoryLock(db, cleanupLockKey, cleanupAll)
}

func cleanupAll(db *gorm.DB) error {
	// ① 无密钥渠道的模型能力置空
	res := db.Exec(`DELETE FROM channel_abilities WHERE channel_id IN (
		SELECT c.id FROM channels c
		WHERE COALESCE(c.upstream_key_enc, '') = ''
		  AND NOT EXISTS (SELECT 1 FROM channel_keys k WHERE k.channel_id = c.id))`)
	if res.Error != nil {
		return fmt.Errorf("cleanup keyless channel abilities: %w", res.Error)
	}
	if n := res.RowsAffected; n > 0 {
		slog.Info("启动清理：已置空无密钥渠道的模型列表", "abilities", n)
	}

	// ② 孤儿模型的子账号授权（grants 与 models 无外键、按模型名文本关联，须先于 ③）
	res = db.Exec(`DELETE FROM user_model_grants WHERE model_name IN (
		SELECT m.name FROM models m
		WHERE NOT EXISTS (SELECT 1 FROM channel_abilities ca WHERE ca.model_name = m.name))`)
	if res.Error != nil {
		return fmt.Errorf("cleanup orphan model grants: %w", res.Error)
	}
	removedGrants := res.RowsAffected

	// ③ 无渠道能力支撑的模型目录行
	res = db.Exec(`DELETE FROM models WHERE NOT EXISTS (
		SELECT 1 FROM channel_abilities ca WHERE ca.model_name = models.name)`)
	if res.Error != nil {
		return fmt.Errorf("cleanup orphan models: %w", res.Error)
	}
	if n := res.RowsAffected; n > 0 {
		slog.Info("启动清理：已删除无渠道支撑的模型", "models", n, "grants", removedGrants)
	}
	return nil
}
