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

// ---- 定时渠道体检（健康侧信号源）----
// 与自动恢复（拉起禁用渠道）互补：体检对【启用中】的渠道周期发探活请求，
// 连续 failThreshold 次失败自动置 status=0 并写 auto_disabled_at——交给
// AutoProbeLoop 在上游恢复后自动拉起，形成「发现 → 自愈」闭环。
// 低流量渠道的故障不再依赖业务流量触发熔断才被发现（哑渠道静默留存问题）。

// ChannelTestLoop 定时体检入口；间隔<=0 不启动。多实例各节点独立体检（幂等，重复探活无害）。
// failures 为跨轮连续失败计数（本节点内存持有）。
func ChannelTestLoop(interval time.Duration, failThreshold int, h *Handler) {
	if interval <= 0 {
		return
	}
	if failThreshold <= 0 {
		failThreshold = 3
	}
	slog.Info("定时渠道体检已启用", "interval", interval.String(), "fail_threshold", failThreshold)
	failures := map[int64]int{}
	for {
		time.Sleep(interval)
		if n, err := ChannelTestOnce(h, failThreshold, failures); err != nil {
			slog.Warn("定时渠道体检执行失败", "err", err)
		} else if n > 0 {
			slog.Warn("定时渠道体检：本轮自动禁用渠道数", "disabled", n)
		}
	}
}

// ChannelTestOnce 体检一轮全部启用渠道，返回本轮自动禁用的渠道数
func ChannelTestOnce(h *Handler, failThreshold int, failures map[int64]int) (int, error) {
	if failThreshold <= 0 {
		failThreshold = 3
	}
	var chans []model.Channel
	if err := h.DB.Where("status = 1").Find(&chans).Error; err != nil {
		return 0, err
	}
	disabled := 0
	for _, ch := range chans {
		ok, _, errStr := probeChannel(h, ch.ID)
		now := time.Now().Unix()
		if h.Metrics != nil {
			if ok {
				h.Metrics.ChannelProbeResult.With("ok").Inc()
			} else {
				h.Metrics.ChannelProbeResult.With("fail").Inc()
			}
		}
		if ok {
			delete(failures, ch.ID) // 成功一次即清零连续失败计数
			_ = h.DB.Exec("UPDATE channels SET last_test_at = ?, last_test_ok = 1, updated_at = ? WHERE id = ?",
				now, now, ch.ID).Error
			continue
		}
		failures[ch.ID]++
		_ = h.DB.Exec("UPDATE channels SET last_test_at = ?, last_test_ok = 0, updated_at = ? WHERE id = ?",
			now, now, ch.ID).Error
		if failures[ch.ID] < failThreshold {
			slog.Warn("定时渠道体检失败", "channel_id", ch.ID, "channel", ch.Name,
				"consecutive", failures[ch.ID], "threshold", failThreshold, "err", truncateStr(errStr, 200))
			continue
		}
		// 达阈值：自动禁用并写系统标记（AutoProbeLoop 探测恢复）+ 邮件告警
		res := h.DB.Exec(`UPDATE channels
			SET status = 0, auto_disabled_at = ?, remark = ?, updated_at = ?
			WHERE id = ? AND status = 1`, now,
			fmt.Sprintf("定时体检：连续 %d 次探活失败（%s），已自动禁用；恢复后将自动探测拉起",
				failThreshold, truncateStr(errStr, 100)),
			now, ch.ID)
		if res.Error != nil || res.RowsAffected == 0 {
			continue
		}
		disabled++
		delete(failures, ch.ID)
		slog.Warn("定时体检达阈值，渠道已自动禁用", "channel_id", ch.ID, "channel", ch.Name,
			"err", truncateStr(errStr, 200))
		service.NotifyPlatformAdmins(h.DB,
			fmt.Sprintf("渠道「%s」体检连续失败已自动禁用", ch.Name),
			fmt.Sprintf("启用中的渠道「%s」（#%d）连续 %d 次定时体检失败：%s\n渠道已自动禁用，探测成功后将自动恢复；也可到管理台手动处理。\n时间：%s\n—— token 中转站",
				ch.Name, ch.ID, failThreshold, truncateStr(errStr, 300), time.Now().Format("2006-01-02 15:04:05")))
	}
	return disabled, nil
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
