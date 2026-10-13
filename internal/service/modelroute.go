package service

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"
)

// 模型「当前可路由」的统一口径：启用模型 × 存在启用渠道 × 该渠道有可用上游 Key。
//
// 背景：渠道 Key 因上游 401 被自动禁用后，模型与渠道能力仍处启用态，各处
// 「可用模型」目录继续宣传死模型，客户端调通列表后调用却 503
// (channel_key_missing)。
//
// 语义与 gateway.SelectCandidates 的 KeyedChannels 判定保持一致：
// Key 池内有启用行（channel_keys.status=1），或 legacy 单 Key 密文非空；
// 并同样要求 base_url/path 非空（SelectCandidates 会静默剔除空值渠道）。
// 谓词引用模型别名为 m 的查询（各调用点均以 m 别名扫描 models 表）。
const ModelHasLiveUpstream = `EXISTS (
	SELECT 1 FROM channel_abilities ra
	JOIN channels rc ON rc.id = ra.channel_id
	WHERE ra.model_name = m.name
	  AND rc.status = 1 AND rc.base_url <> '' AND rc.path <> ''
	  AND (rc.upstream_key_enc <> '' OR EXISTS (
	      SELECT 1 FROM channel_keys rk WHERE rk.channel_id = rc.id AND rk.status = 1)))`

// SyncModelRoutableStatus 把模型启用态与上游可用性对齐（幂等，可重复调用）：
//   - 启用中的模型失去全部可用上游（承接渠道的 Key 全被禁用 / 渠道停用或删除）→ 自动停用
//     （status=0 + auto_disabled_at 记时间）；
//   - 自动停用的模型重新出现可用上游（换 Key / 渠道恢复）→ 自动恢复（status=1 + 清标记），
//     授权、客户开通与定价原样保留，无需重新配置。
//
// 手动停用（auto_disabled_at=0）永不自动恢复——管理员意图优先，与渠道
// auto_disabled_at 同语义。多渠道模型只在全部渠道都不可用时才停用。
// 返回本轮停用/恢复的模型名（调用方用于日志与通知）。
func SyncModelRoutableStatus(db *gorm.DB) (disabled, recovered []string, err error) {
	err = db.Transaction(func(tx *gorm.DB) error {
		now := time.Now().Unix()
		if err := tx.Raw(`SELECT name FROM models m WHERE m.status = 1 AND NOT (` + ModelHasLiveUpstream + `)`).
			Scan(&disabled).Error; err != nil {
			return err
		}
		if len(disabled) > 0 {
			if err := tx.Exec(`UPDATE models SET status = 0, auto_disabled_at = ?, updated_at = ?
				WHERE status = 1 AND name IN ?`, now, now, disabled).Error; err != nil {
				return err
			}
		}
		if err := tx.Raw(`SELECT name FROM models m WHERE m.status = 0 AND m.auto_disabled_at > 0 AND ` + ModelHasLiveUpstream).
			Scan(&recovered).Error; err != nil {
			return err
		}
		if len(recovered) > 0 {
			if err := tx.Exec(`UPDATE models SET status = 1, auto_disabled_at = 0, updated_at = ?
				WHERE status = 0 AND name IN ?`, now, recovered).Error; err != nil {
				return err
			}
		}
		return nil
	})
	return disabled, recovered, err
}

// SyncModelsAndNotify = SyncModelRoutableStatus + 日志与通知：自动停用发站内+邮件告警
// （调用流量的模型悄悄消失必须让管理员知道），自动恢复只记日志（伴随的渠道恢复已有通知）。
// cause 写进通知与日志，标明是哪条链路触发的对齐（Key 自动禁用 / 渠道编辑 / 启动对齐…）。
func SyncModelsAndNotify(db *gorm.DB, cause string) {
	disabled, recovered, err := SyncModelRoutableStatus(db)
	if err != nil {
		slog.Error("模型启用态对齐上游可用性失败", "cause", cause, "err", err)
		return
	}
	if len(disabled) > 0 {
		slog.Warn("上游不可用，模型已自动停用", "cause", cause, "models", strings.Join(disabled, ","))
		NotifyPlatformAdmins(db,
			fmt.Sprintf("模型已自动停用（%d 个）", len(disabled)),
			fmt.Sprintf("以下模型的所有承接渠道密钥均不可用，已自动停用（%s）：\n%s\n\n更换渠道密钥或恢复渠道后，模型会自动恢复启用，授权与定价不受影响。\n时间：%s\n—— %s",
				cause, strings.Join(disabled, "\n"), time.Now().Format("2006-01-02 15:04:05"), BrandName))
	}
	if len(recovered) > 0 {
		slog.Info("上游恢复，模型已自动恢复启用", "cause", cause, "models", strings.Join(recovered, ","))
	}
}
