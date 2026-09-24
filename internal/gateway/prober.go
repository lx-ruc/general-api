package gateway

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"gorm.io/gorm"

	"token-gateway/internal/coord"
	"token-gateway/internal/crypto"
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
		pr := ProbeChannel(h.DB, h.Cipher, h.Client, h.Coord, h.KeyCooldown, ch.ID)
		ok, errStr := pr.OK, pr.Err
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
		if pr.Quota {
			// 厂商侧配额耗尽是 Key/账户级问题：渠道本身与其它 Key 健康，数据面已按
			// 配额冷却处理（全部耗尽时对客户端回 402）。不计连续失败、不禁用渠道——
			// 否则限额恢复前渠道被整条拉黑，恢复后还要多等一轮探测
			_ = h.DB.Exec("UPDATE channels SET last_test_at = ?, last_test_ok = 0, updated_at = ? WHERE id = ?",
				now, now, ch.ID).Error
			slog.Warn("定时体检：上游配额耗尽（不计渠道故障）", "channel_id", ch.ID, "channel", ch.Name,
				"key", pr.KeyDesc, "err", truncateStr(errStr, 200))
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
			fmt.Sprintf("启用中的渠道「%s」（#%d）连续 %d 次定时体检失败：%s\n渠道已自动禁用，探测成功后将自动恢复；也可到管理台手动处理。\n时间：%s\n—— 慧沐引擎",
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
		res := ProbeChannel(h.DB, h.Cipher, h.Client, h.Coord, h.KeyCooldown, ch.ID)
		ok, latency, errStr := res.OK, res.LatencyMs, res.Err
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
					fmt.Sprintf("此前因连续失败被熔断禁用的渠道「%s」（#%d）探测成功（%d ms），已自动重新启用。\n时间：%s\n—— 慧沐引擎",
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

// ProbeResult 单次渠道探测结果。管理台「渠道测试」按钮与体检/自动恢复共用同一内核，
// 错误语义保持一致（配额耗尽 ≠ 渠道故障）。
type ProbeResult struct {
	OK        bool
	Status    int    // 上游 HTTP 状态码（网络失败/配置问题为 0）
	LatencyMs int64
	Err       string
	KeyDesc   string // 探测使用的 Key 描述（如 "池内 Key #3" / "legacy 单 Key"）
	Quota     bool   // 失败是否因厂商侧配额耗尽（SetLimitExceeded 等）——换 Key/等恢复可解，禁用渠道无意义
}

// ProbeChannel 实发一次 max_tokens=1 请求测连通。Key 选择与数据面选路同口径：
// 池内优先挑非冷却的启用 Key（全部冷却时仍取第一把——管理员点测试通常就是想看它为什么不行），
// 回退 legacy 单 Key。keyCooldown <= 0 时按 60s（探测侧标记配额冷却用）。
//
// 探测同时充当 Key 自愈入口：成功即 ClearCooldown（配额恢复后点一次「测试」，
// 被冷却的 Key 立即回池，不必等冷却到期）；配额类 429 则按数据面同款策略标记
// 该 Key 的配额冷却（其它 Key 不连坐）。
func ProbeChannel(db *gorm.DB, cipher *crypto.Cipher, client *http.Client, cd coord.Coordinator,
	keyCooldown time.Duration, channelID int64) ProbeResult {
	var ch model.Channel
	if err := db.Where("id = ?", channelID).First(&ch).Error; err != nil {
		return ProbeResult{Err: "channel not found: " + err.Error()}
	}
	var ab model.ChannelAbility
	// 优先挑 chat 模型探测：embedding 模型不吃 messages，用 chat 请求探测必然 400，
	// 会把健康渠道误判为故障（体检误杀）。渠道全是 embedding 模型时改发 embeddings 请求
	ab, isEmbed, err := PickProbeAbility(db, channelID)
	if err != nil {
		return ProbeResult{Err: "渠道未配置模型，无法探测"}
	}
	if keyCooldown <= 0 {
		keyCooldown = time.Minute
	}

	// Key 选择：池内优先非冷却 → 全冷却取第一把 → 回退 legacy 单 Key
	key, scope, keyDesc := "", "", ""
	var poolKeys []model.ChannelKey
	if err := db.Where("channel_id = ? AND status = 1", channelID).Order("id").Find(&poolKeys).Error; err == nil && len(poolKeys) > 0 {
		pick := poolKeys[0]
		for _, k := range poolKeys {
			if s := fmt.Sprintf("ck:%d:%d", channelID, k.ID); !cd.IsCooling(s) {
				pick = k
				break
			}
		}
		scope = fmt.Sprintf("ck:%d:%d", channelID, pick.ID)
		keyDesc = fmt.Sprintf("池内 Key #%d", pick.ID)
		key, _ = cipher.Decrypt(pick.KeyEnc)
	} else if ch.UpstreamKeyEnc != "" {
		key, _ = cipher.Decrypt(ch.UpstreamKeyEnc)
		scope = fmt.Sprintf("ck:%d:legacy", channelID)
		keyDesc = "legacy 单 Key"
	}
	if key == "" {
		return ProbeResult{Err: "渠道无可用 Key（池内全部禁用或未配置）"}
	}

	upModel := ab.ModelName
	if ab.UpstreamModelName != nil && *ab.UpstreamModelName != "" {
		upModel = *ab.UpstreamModelName
	}
	var body string
	if isEmbed {
		body = fmt.Sprintf(`{"model":%q,"input":"ping"}`, upModel)
	} else {
		body = fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"ping"}],"max_tokens":1,"stream":false}`, upModel)
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(ch.BaseURL, "/")+probePath(ch.Path, isEmbed), strings.NewReader(body))
	if err != nil {
		return ProbeResult{Err: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("User-Agent", "token-gateway")

	start := time.Now()
	resp, derr := client.Do(req)
	latency := time.Since(start).Milliseconds()
	if derr != nil {
		return ProbeResult{LatencyMs: latency, Err: derr.Error(), KeyDesc: keyDesc}
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	_ = resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if scope != "" {
			cd.ClearCooldown(scope) // 探测成功即证明可用：立即回池（配额恢复后的手动自愈入口）
		}
		return ProbeResult{OK: true, Status: resp.StatusCode, LatencyMs: latency, KeyDesc: keyDesc}
	}
	// 配额类 429：Key/账户级问题，按数据面同款策略只冷却该 Key（不连坐），
	// 并显式分类——体检据此不计渠道故障，管理台据此展示「配额耗尽」而非裸 429
	if resp.StatusCode == http.StatusTooManyRequests {
		if qc := Quota429Code(data); qc != "" {
			if scope != "" {
				d := cd.Backoff(scope, keyCooldown, quota429CooldownMax)
				cd.MarkQuotaCooling(scope, d)
			}
			return ProbeResult{Status: resp.StatusCode, LatencyMs: latency, KeyDesc: keyDesc, Quota: true,
				Err: fmt.Sprintf("上游配额耗尽（%s）：厂商侧限额暂停，渠道与其它 Key 不受影响；限额恢复后冷却到期（约 %s）自动回池，也可在 Key 池点「清除冷却」立即复用。原始响应：HTTP 429 %s",
					qc, keyCooldown, truncateStr(string(data), 200))}
		}
	}
	return ProbeResult{Status: resp.StatusCode, LatencyMs: latency, KeyDesc: keyDesc,
		Err: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, truncateStr(string(data), 300))}
}

// isEmbeddingModel 按 openai 生态惯例：embedding 系模型名都含 "embedding"
func isEmbeddingModel(name string) bool {
	return strings.Contains(strings.ToLower(name), "embedding")
}

// probePath embedding 探测时把 chat 路径换成 /embeddings（复用数据面的派生规则）
func probePath(path string, embed bool) string {
	if !embed {
		return path
	}
	return endpointURL("", path, "/embeddings")
}

// PickProbeAbility 选探测用模型（管理台「渠道测试」与体检/自动恢复共用）：
// 优先挑非 embedding 的 chat 模型（embedding 模型不吃 messages，用 chat 请求
// 探测必然 400，会把健康渠道误判为故障）；返回 (能力, 是否按 embeddings 协议探测)
func PickProbeAbility(db *gorm.DB, channelID int64) (model.ChannelAbility, bool, error) {
	var ab model.ChannelAbility
	if err := db.Where("channel_id = ? AND model_name NOT LIKE '%embedding%'", channelID).
		Order("model_name").First(&ab).Error; err != nil {
		if err2 := db.Where("channel_id = ?", channelID).Order("model_name").First(&ab).Error; err2 != nil || ab.ModelName == "" {
			return ab, false, fmt.Errorf("渠道未配置模型")
		}
	}
	return ab, isEmbeddingModel(ab.ModelName), nil
}
