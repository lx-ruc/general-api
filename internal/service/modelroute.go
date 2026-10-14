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

// 渠道「有可用上游 Key」的统一口径（与 ModelHasLiveUpstream 内嵌判定一致）：
// Key 池内有启用行（channel_keys.status=1），或 legacy 单 Key 密文非空。
const channelHasUsableKey = `(COALESCE(channels.upstream_key_enc, '') <> '' OR EXISTS (
	SELECT 1 FROM channel_keys hk WHERE hk.channel_id = channels.id AND hk.status = 1))`

// SyncChannelKeyState 把渠道状态面与上游 Key 可用性对齐（幂等，可重复调用）：
//   - 停用方向：启用中但已无任何可用上游 Key（Key 被上游 401/402 自动禁用、管理员禁用/
//     删除最后一把、legacy 密文清空）→ 联动停用，写 nokey_disabled_at 与中文备注；
//   - 恢复方向：因无 Key 停用的渠道重新出现可用 Key（补 Key / 重新启用 Key）→ 立即自动启用。
//
// 「无密钥的渠道不能启用」此前只拦启用动作，Key 流失后渠道仍以启用态悬挂——本函数补齐状态面。
// 与熔断/体检停用的分工：那两类写 auto_disabled_at、由 AutoProbeLoop 探测恢复；本函数写
// nokey_disabled_at、按 Key 存在性恢复（无 Key 时探测必失败，探测对这类渠道没有意义），
// 两个标记互不干扰。手动停用（两标记皆为 0）永不被触碰；熔断/体检停用的渠道不经本函数恢复。
// 返回本轮停用/恢复的渠道名（日志与通知用）。
func SyncChannelKeyState(db *gorm.DB, cause string) (disabled, recovered []string) {
	now := time.Now()
	return disableNoKeyChannels(db, cause, now), recoverKeyedChannels(db, cause, now)
}

// sweepRows 按条件取渠道 id/name 并执行 UPDATE，返回本轮命中的渠道名（查询/更新失败
// 记日志返回 nil，不影响另一方向独立生效）。
func sweepRows(db *gorm.DB, cause, dir, where, set string, args ...any) []string {
	var rows []struct {
		ID   int64
		Name string
	}
	if err := db.Raw(`SELECT id, name FROM channels ` + where).Scan(&rows).Error; err != nil {
		slog.Error("渠道状态面对齐查询失败", "dir", dir, "cause", cause, "err", err)
		return nil
	}
	if len(rows) == 0 {
		return nil
	}
	names := make([]string, 0, len(rows))
	ids := make([]int64, 0, len(rows))
	for _, r := range rows {
		names = append(names, r.Name)
		ids = append(ids, r.ID)
	}
	if err := db.Exec(`UPDATE channels `+set, append(args, ids)...).Error; err != nil {
		slog.Error("渠道状态面对齐更新失败", "dir", dir, "cause", cause, "err", err)
		return nil
	}
	return names
}

// disableNoKeyChannels 停用方向：启用中但已无任何可用上游 Key → status=0 + nokey_disabled_at。
func disableNoKeyChannels(db *gorm.DB, cause string, now time.Time) []string {
	remark := fmt.Sprintf("%s 自动停用：渠道已无可用密钥（%s）；补充或启用 Key 后自动恢复",
		now.Format("2006-01-02 15:04:05"), cause)
	names := sweepRows(db, cause, "停用",
		`WHERE status = 1 AND NOT (`+channelHasUsableKey+`)`,
		`SET status = 0, nokey_disabled_at = ?, remark = ?, updated_at = ? WHERE id IN ? AND status = 1`,
		now.Unix(), remark, now.Unix())
	if len(names) > 0 {
		slog.Warn("启用中渠道已无可用密钥，联动停用", "cause", cause, "channels", strings.Join(names, ","))
	}
	return names
}

// recoverKeyedChannels 恢复方向：因无 Key 停用（nokey_disabled_at>0）且现已重新有可用 Key
// → status=1 + 清标记。只认 nokey 标记，熔断/体检停用（auto_disabled_at>0）不经此恢复。
func recoverKeyedChannels(db *gorm.DB, cause string, now time.Time) []string {
	remark := fmt.Sprintf("%s 自动恢复：渠道重新有可用密钥（%s）",
		now.Format("2006-01-02 15:04:05"), cause)
	names := sweepRows(db, cause, "恢复",
		`WHERE status = 0 AND nokey_disabled_at > 0 AND (`+channelHasUsableKey+`)`,
		`SET status = 1, nokey_disabled_at = 0, remark = ?, updated_at = ? WHERE id IN ? AND status = 0 AND nokey_disabled_at > 0`,
		remark, now.Unix())
	if len(names) > 0 {
		slog.Info("无密钥停用的渠道已重新有密钥，自动恢复", "cause", cause, "channels", strings.Join(names, ","))
	}
	return names
}

// SyncModelsAndNotify = 渠道状态面 + 模型启用态对齐 + 日志通知，是所有 Key 变更路径
// （上游 401/402 自动禁 Key、管理员禁/删/补/启用 Key、渠道编辑、启动清扫）的统一收口。
// 渠道对齐先于模型对齐：补 Key 恢复时一次调用内渠道先启用、模型随后恢复（换 Key 一步恢复）。
// 渠道停用/恢复发站内+邮件告警（承接种类的变化必须让管理员知道）；模型自动停用同样告警，
// 模型恢复只记日志（伴随的渠道恢复已有通知）。cause 写进通知与日志，标明触发的链路。
func SyncModelsAndNotify(db *gorm.DB, cause string) {
	chDisabled, chRecovered := SyncChannelKeyState(db, cause)
	disabled, recovered, err := SyncModelRoutableStatus(db)
	if err != nil {
		slog.Error("模型启用态对齐上游可用性失败", "cause", cause, "err", err)
		return
	}
	if len(chDisabled) > 0 {
		NotifyPlatformAdmins(db,
			fmt.Sprintf("渠道已无可用密钥，自动停用（%d 个）", len(chDisabled)),
			fmt.Sprintf("以下启用中的渠道密钥全部不可用，已自动停用（%s）：\n%s\n\n补充或重新启用渠道密钥后，渠道与模型会自动恢复，授权与定价不受影响。\n时间：%s\n—— %s",
				cause, strings.Join(chDisabled, "\n"), time.Now().Format("2006-01-02 15:04:05"), BrandName))
	}
	if len(chRecovered) > 0 {
		NotifyPlatformAdmins(db,
			fmt.Sprintf("渠道密钥恢复，自动启用（%d 个）", len(chRecovered)),
			fmt.Sprintf("以下此前因无密钥停用的渠道已重新有可用密钥，自动恢复启用（%s）：\n%s\n时间：%s\n—— %s",
				cause, strings.Join(chRecovered, "\n"), time.Now().Format("2006-01-02 15:04:05"), BrandName))
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
