package gateway

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"token-gateway/internal/model"
	"token-gateway/internal/service"
)

// 渠道自动恢复（对标 new-api 的 auto-enable）：熔断禁用的渠道定期用一次
// max_tokens=1 的真实请求探测，成功即自动启用并通知平台管理员；
// 管理员手动禁用的渠道（auto_disabled_at=0）永远不会被探测——禁用意图优先于自愈。

// AutoProbeLoop 周期探测入口；间隔<=0 时不启动。随进程退出（与限流器清理协程同生命周期）
func AutoProbeLoop(interval time.Duration, h *Handler) {
	if interval <= 0 {
		return
	}
	slog.Info("渠道自动恢复已启用", "interval", interval.String())
	for {
		time.Sleep(interval)
		if n, err := AutoProbeOnce(h); err != nil {
			slog.Warn("渠道自动探测失败", "err", err)
		} else if n > 0 {
			slog.Info("渠道自动探测完成", "recovered", n)
		}
	}
}

// AutoProbeOnce 探测一轮：所有"系统熔断禁用"（status=0 且 auto_disabled_at>0）的渠道；
// 返回本轮成功恢复的渠道数。探测逻辑与平台管理台的渠道测试一致（Key 选择、请求体）。
func AutoProbeOnce(h *Handler) (int, error) {
	var chans []model.Channel
	if err := h.DB.Where("status = 0 AND auto_disabled_at > 0").Find(&chans).Error; err != nil {
		return 0, err
	}
	recovered := 0
	for _, ch := range chans {
		ok, latency, errStr := probeChannel(h, ch.ID)
		now := time.Now().Unix()
		if ok {
			// 一次成功即恢复：清禁用标记、记测试结果、清零熔断计数
			res := h.DB.Exec(`UPDATE channels
				SET status = 1, auto_disabled_at = 0, last_test_at = ?, last_test_ok = 1, updated_at = ?
				WHERE id = ? AND status = 0 AND auto_disabled_at > 0`, now, now, ch.ID)
			if res.Error != nil {
				slog.Error("自动恢复写库失败", "channel_id", ch.ID, "channel", ch.Name, "err", res.Error)
				continue
			}
			if res.RowsAffected > 0 {
				h.Breaker.RecordSuccess(ch.ID) // 清零连续失败计数，避免恢复后一次失败又被熔断
				recovered++
				slog.Info("渠道自动恢复：探测成功已重新启用", "channel_id", ch.ID, "channel", ch.Name,
					"latency_ms", latency)
				service.NotifyPlatformAdmins(h.DB,
					fmt.Sprintf("渠道「%s」已自动恢复", ch.Name),
					fmt.Sprintf("此前因连续失败被熔断禁用的渠道「%s」（#%d）探测成功（%d ms），已自动重新启用。\n时间：%s\n—— token 中转站",
						ch.Name, ch.ID, latency, time.Now().Format("2006-01-02 15:04:05")))
			}
			continue
		}
		_ = h.DB.Exec("UPDATE channels SET last_test_at = ?, last_test_ok = 0, updated_at = ? WHERE id = ?",
			now, now, ch.ID).Error
		slog.Info("渠道自动探测未通过，保持禁用", "channel_id", ch.ID, "channel", ch.Name,
			"err", truncateStr(errStr, 200))
	}
	return recovered, nil
}

// probeChannel 实发一次 max_tokens=1 请求测连通（Key 选择与数据面/管理台测试一致：
// 池内第一把启用 Key → 回退 legacy 单 Key）
func probeChannel(h *Handler, channelID int64) (ok bool, latencyMs int64, errMsg string) {
	var ch model.Channel
	if err := h.DB.Where("id = ?", channelID).First(&ch).Error; err != nil {
		return false, 0, "channel not found: " + err.Error()
	}
	var ab model.ChannelAbility
	if err := h.DB.Where("channel_id = ?", channelID).Order("model_name").First(&ab).Error; err != nil || ab.ModelName == "" {
		return false, 0, "渠道未配置模型，无法探测"
	}
	key := ""
	var poolKey model.ChannelKey
	if h.DB.Where("channel_id = ? AND status = 1", channelID).Order("id").First(&poolKey).Error == nil {
		key, _ = h.Cipher.Decrypt(poolKey.KeyEnc)
	} else if ch.UpstreamKeyEnc != "" {
		key, _ = h.Cipher.Decrypt(ch.UpstreamKeyEnc)
	}
	if key == "" {
		return false, 0, "渠道无可用 Key（池内全部禁用或未配置）"
	}
	upModel := ab.ModelName
	if ab.UpstreamModelName != nil && *ab.UpstreamModelName != "" {
		upModel = *ab.UpstreamModelName
	}
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"ping"}],"max_tokens":1,"stream":false}`, upModel)
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(ch.BaseURL, "/")+ch.Path, strings.NewReader(body))
	if err != nil {
		return false, 0, err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("User-Agent", "token-gateway")

	start := time.Now()
	resp, derr := h.Client.Do(req)
	latency := time.Since(start).Milliseconds()
	if derr != nil {
		return false, latency, derr.Error()
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	_ = resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return true, latency, ""
	}
	return false, latency, fmt.Sprintf("HTTP %d: %s", resp.StatusCode, truncateStr(string(data), 300))
}
