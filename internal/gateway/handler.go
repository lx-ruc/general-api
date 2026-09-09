package gateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"token-gateway/internal/config"
	"token-gateway/internal/coord"
	"token-gateway/internal/crypto"
	"token-gateway/internal/metrics"
	"token-gateway/internal/middleware"
	"token-gateway/internal/model"
	"token-gateway/internal/service"
)

// Usage OpenAI 兼容 usage 结构（只取计费需要的两个字段）
type Usage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
}

type Handler struct {
	DB      *gorm.DB
	Cipher  *crypto.Cipher
	Client  *http.Client
	MaxBody int64
	Limiter *middleware.RateLimiter
	Breaker *Breaker
	Metrics *metrics.Metrics
	Coord   coord.Coordinator

	MaxConcurrency   int           // 渠道并发闸门（0=不限）
	QueueWaitTimeout time.Duration // 闸门排队等待上限
	KeyCooldown      time.Duration // 429 后 Key 冷却时长
	CacheTTL         time.Duration // 精确缓存 TTL（0=关）
	CacheIsolateOrg  bool          // 缓存按组织隔离
}

func NewHandler(db *gorm.DB, cipher *crypto.Cipher, cfg *config.Config,
	limiter *middleware.RateLimiter, m *metrics.Metrics, cd coord.Coordinator) *Handler {
	if cd == nil {
		cd = coord.Nop{}
	}
	return &Handler{
		DB:               db,
		Cipher:           cipher,
		Client:           NewHTTPClient(cfg.Gateway.UpstreamFirstByteTimeout.Duration),
		MaxBody:          int64(cfg.Gateway.MaxBodyMB) << 20,
		Limiter:          limiter,
		Breaker:          NewBreaker(cfg.Gateway.ChannelBreakerThreshold),
		Metrics:          m,
		Coord:            cd,
		MaxConcurrency:   cfg.Gateway.ChannelMaxConcurrency,
		QueueWaitTimeout: cfg.Gateway.QueueWaitTimeout.Duration,
		KeyCooldown:      cfg.Gateway.KeyCooldown.Duration,
		CacheTTL:         cfg.Gateway.CacheTTL.Duration,
		CacheIsolateOrg:  cfg.Gateway.CacheIsolateOrg,
	}
}

// NewHTTPClient 上游 HTTP 客户端：不设整体 Timeout（会杀 SSE 长连接），
// 只限制首字节（响应头）超时
func NewHTTPClient(firstByteTimeout time.Duration) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   16,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: firstByteTimeout,
		},
	}
}

// CalcCost 全整数计费：cost = ceil((pt×ip + ct×op) / 1M)，向上取整避免零成本刷量
func CalcCost(promptTokens, completionTokens, inputPrice, outputPrice int64) int64 {
	total := promptTokens*inputPrice + completionTokens*outputPrice
	if total <= 0 {
		return 0
	}
	return (total + 999_999) / 1_000_000
}

func openaiError(c *gin.Context, status int, errType, msg string) {
	c.JSON(status, gin.H{
		"error": gin.H{"message": msg, "type": errType, "code": errType},
	})
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ChatCompletions POST /v1/chat/completions 编排入口：
// 限流 → 解析 body → 模型/授权/额度校验 → 渠道选择 → 转发（流式/非流式）→ 结算
func (h *Handler) ChatCompletions(c *gin.Context) {
	start := time.Now()
	ki := middleware.GetKeyInfo(c)

	rec := &model.UsageLog{
		RequestID:    middleware.GetRequestID(c),
		OrgID:        ki.OrgID,
		UserID:       ki.UserID,
		APIKeyID:     ki.KeyID,
		CostCenterID: ki.CostCenterID, // 结算时快照：改派只影响未来
		ClientIP:     c.ClientIP(),
	}
	defer func() {
		rec.LatencyMs = time.Since(start).Milliseconds()
		if h.Metrics != nil {
			h.Metrics.Requests.With(strconv.Itoa(rec.Status)).Inc()
			h.Metrics.Latency.Observe(time.Since(start).Seconds())
		}
		// 系统管理员在线体验：无客户归属、无额度语义，不产生计费（与渠道测试一致），只留计量日志
		if ki.Playground && ki.OrgID == 0 {
			rec.Cost = 0
		}
		if err := service.Settle(h.DB, rec); err != nil {
			if h.Metrics != nil {
				h.Metrics.SettleErrors.Inc()
			}
			c.Error(err) //nolint: 仅记录，不影响已发出的响应
		} else if rec.Cost > 0 {
			// 水位实际变动才检查预算告警（缓存命中/被拦截请求不动水位）；异步 + 节流，数据面零等待
			go service.CheckBudgetAlerts(h.DB, h.Metrics, rec.OrgID, rec.UserID)
		}
	}()

	// per-key 限流
	if h.Limiter != nil && !h.Limiter.Allow(fmt.Sprintf("key:%d", ki.KeyID)) {
		rec.Status, rec.Error = http.StatusTooManyRequests, "rate limit exceeded"
		openaiError(c, http.StatusTooManyRequests, "rate_limit_error", "rate limit exceeded, please retry later")
		return
	}

	// 读取 body（限长）
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, h.MaxBody+1))
	if err != nil || int64(len(body)) > h.MaxBody {
		rec.Status, rec.Error = http.StatusRequestEntityTooLarge, "request body too large"
		openaiError(c, http.StatusRequestEntityTooLarge, "request_too_large", "request body too large")
		return
	}
	var bodyMap map[string]json.RawMessage
	if err := json.Unmarshal(body, &bodyMap); err != nil || len(bodyMap) == 0 {
		rec.Status, rec.Error = http.StatusBadRequest, "invalid JSON body"
		openaiError(c, http.StatusBadRequest, "invalid_request_error", "invalid JSON body")
		return
	}

	modelName := rawString(bodyMap["model"])
	if modelName == "" {
		rec.Status, rec.Error = http.StatusBadRequest, "missing model"
		openaiError(c, http.StatusBadRequest, "invalid_request_error", "missing required parameter: model")
		return
	}
	rec.ModelName = modelName

	stream := rawBool(bodyMap["stream"])
	if stream {
		rec.IsStream = 1
	}

	// 模型存在且启用
	var m model.Model
	if err := h.DB.Where("name = ?", modelName).First(&m).Error; err != nil {
		rec.Status, rec.Error = http.StatusNotFound, "model not found: "+modelName
		openaiError(c, http.StatusNotFound, "invalid_request_error",
			fmt.Sprintf("model %q does not exist or is not available", modelName))
		return
	}

	// 模型授权（子账号白名单）；在线体验（Playground）由管理面按角色预授权，跳过此检查
	if !ki.Playground {
		var cnt int64
		if err := h.DB.Model(&model.UserModelGrant{}).
			Where("user_id = ? AND model_name = ?", ki.UserID, modelName).Count(&cnt).Error; err != nil || cnt == 0 {
			rec.Status, rec.Error = http.StatusForbidden, "model not allowed: "+modelName
			openaiError(c, http.StatusForbidden, "model_not_allowed",
				fmt.Sprintf("you are not allowed to use model %q, please contact your company admin", modelName))
			return
		}
	}

	// 额度预检查（读）；真实扣减在响应结束后的结算事务里。
	// 系统管理员在线体验无客户归属（OrgID=0），无额度语义，跳过。
	if ki.OrgID > 0 {
		if err := service.Precheck(h.DB, ki.UserID); err != nil {
			if errors.Is(err, service.ErrUserQuota) || errors.Is(err, service.ErrOrgQuota) {
				rec.Status, rec.Error = http.StatusTooManyRequests, err.Error()
				openaiError(c, http.StatusTooManyRequests, "insufficient_balance", err.Error())
				return
			}
			if errors.Is(err, service.ErrUserMonthly) || errors.Is(err, service.ErrOrgMonthly) {
				rec.Status, rec.Error = http.StatusTooManyRequests, err.Error()
				openaiError(c, http.StatusTooManyRequests, "monthly_limit_exceeded", err.Error())
				return
			}
			rec.Status, rec.Error = http.StatusInternalServerError, err.Error()
			openaiError(c, http.StatusInternalServerError, "internal_error", "quota precheck failed")
			return
		}
	}

	// 精确缓存查询（授权/预检之后、选渠道之前）：命中直接回，不触发上游调用与扣费
	cacheKey := ""
	if h.CacheTTL > 0 && !stream {
		cacheKey = h.cacheKey(bodyMap, ki.OrgID)
		if data, ok := h.Coord.CacheGet(cacheKey); ok {
			if h.Metrics != nil {
				h.Metrics.CacheHits.Inc()
			}
			rec.Status = http.StatusOK
			rec.CacheHit = 1
			c.Writer.Header().Set("X-Tg-Cache", "hit")
			c.Data(http.StatusOK, "application/json", data)
			var ur struct {
				Usage *Usage `json:"usage"`
			}
			_ = json.Unmarshal(data, &ur)
			if ur.Usage != nil {
				rec.PromptTokens = ur.Usage.PromptTokens
				rec.CompletionTokens = ur.Usage.CompletionTokens
				rec.InputPrice = m.InputPrice
				rec.OutputPrice = m.OutputPrice
			} else {
				rec.NoUsage = 1
			}
			return // Cost 保持 0：缓存命中不扣费
		}
	}

	// 渠道候选（渠道 × Key，冷却/禁用已过滤）
	cands, selStats, err := SelectCandidates(h.DB, h.Cipher, modelName, h.Coord)
	if err != nil {
		rec.Status, rec.Error = http.StatusInternalServerError, err.Error()
		openaiError(c, http.StatusInternalServerError, "internal_error", "failed to select channels")
		return
	}
	if len(cands) == 0 {
		// 区分错误语义：配置问题（重试无意义）回 503 并给出可行动的信息；限流回 429
		switch {
		case selStats.Channels == 0:
			rec.Status, rec.Error = http.StatusServiceUnavailable, "no enabled channel for model"
			openaiError(c, http.StatusServiceUnavailable, "no_available_channel",
				"no enabled channel serves this model, please contact the platform admin")
			return
		case selStats.KeyedChannels == 0:
			rec.Status, rec.Error = http.StatusServiceUnavailable, "no usable upstream key (not configured or disabled)"
			openaiError(c, http.StatusServiceUnavailable, "channel_key_missing",
				"upstream key is not configured or disabled, please contact the platform admin")
			return
		default:
			// 所有 Key 冷却中：语义是"上游限流中"，回 429 而非 503
			rec.Status, rec.Error = http.StatusTooManyRequests, "no available key (all cooling)"
			h.writeRetryAfter(c, h.KeyCooldown)
			openaiError(c, http.StatusTooManyRequests, "upstream_busy",
				"upstream is rate limited, please retry later")
			return
		}
	}

	// 流式请求注入 stream_options.include_usage（保证末块带 usage 用于计费）
	if stream {
		needInject := true
		if raw, ok := bodyMap["stream_options"]; ok {
			var so struct {
				IncludeUsage bool `json:"include_usage"`
			}
			if json.Unmarshal(raw, &so) == nil && so.IncludeUsage {
				needInject = false
			}
		}
		if needInject {
			bodyMap["stream_options"] = json.RawMessage(`{"include_usage":true}`)
		}
	}

	// 单个候选的完整尝试：acquire 闸门 → 转发 → 按响应分类。
	// 闭包内 defer release，从结构上避免 defer-in-loop 泄漏。
	var lastErr string
	onlyRateLimited := true // 全部失败均因 429/排队超时 → 最终回 429 而非 502
	tryCandidate := func(cand Candidate) attemptResult {
		// 渠道并发闸门（有界等待）。超时换渠道：同渠道其他 Key 面对同一个满闸门，重试无意义
		qStart := time.Now()
		release, ok := h.Coord.AcquireSlot(c.Request.Context(), cand.SlotScope(), h.MaxConcurrency)
		if !ok {
			if h.Metrics != nil {
				h.Metrics.QueueTimeouts.Inc()
			}
			lastErr = fmt.Sprintf("queue wait timeout on channel %s", cand.ChannelName)
			return attemptNextChannel
		}
		if h.Metrics != nil {
			h.Metrics.QueueWait.Observe(time.Since(qStart).Seconds())
		}
		defer release()

		bm := bodyMap
		if cand.UpstreamModel != "" && cand.UpstreamModel != modelName {
			bm = cloneMap(bodyMap)
			bm["model"] = json.RawMessage(strconv.Quote(cand.UpstreamModel))
		}
		upBody, merr := json.Marshal(bm)
		if merr != nil {
			lastErr = merr.Error()
			return attemptNextKey
		}
		req, rerr := http.NewRequestWithContext(c.Request.Context(), http.MethodPost,
			cand.URL(), bytes.NewReader(upBody))
		if rerr != nil {
			lastErr = rerr.Error()
			return attemptNextKey
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+cand.UpstreamKey)
		if accept := c.GetHeader("Accept"); accept != "" {
			req.Header.Set("Accept", accept)
		}
		req.Header.Set("User-Agent", "token-gateway")

		resp, derr := h.Client.Do(req)
		if derr != nil {
			lastErr = derr.Error()
			onlyRateLimited = false
			h.noteChannelFailure(cand)
			return attemptNextChannel // 网络失败 → 跳过该渠道（尚未向客户端写出任何字节）
		}

		// drain 读空并关闭，保证连接可复用
		drain := func() {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
			_ = resp.Body.Close()
		}

		switch {
		case resp.StatusCode == http.StatusTooManyRequests:
			// 429：Key 冷却（Retry-After 优先）→ 同渠道下一把 Key
			drain()
			if h.Metrics != nil {
				h.Metrics.Upstream429.Inc()
				h.Metrics.KeyCooldown.Inc()
			}
			cd := parseRetryAfter(resp.Header.Get("Retry-After"))
			if cd <= 0 {
				cd = h.KeyCooldown
			}
			h.Coord.SetCooldown(cand.KeyScope(), cd)
			lastErr = fmt.Sprintf("upstream %s returned 429 (key %d cooling %s)",
				cand.ChannelName, cand.KeyID, cd)
			return attemptNextKey

		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			// 401/403：Key 失效 → 禁用该 Key → 同渠道下一把 Key（主 Key 报错自动切备用；
			// 候选列表只前进不回看，被禁 Key 不会在本请求内重复选中）
			drain()
			onlyRateLimited = false
			h.disableKey(cand, resp.StatusCode)
			lastErr = fmt.Sprintf("upstream %s key %d returned %d", cand.ChannelName, cand.KeyID, resp.StatusCode)
			return attemptNextKey

		case resp.StatusCode >= 500:
			// 5xx：渠道级故障，熔断计数并跳过该渠道全部剩余 Key（同 endpoint 换 Key 无意义）
			drain()
			onlyRateLimited = false
			lastErr = fmt.Sprintf("upstream %s returned %d", cand.ChannelName, resp.StatusCode)
			h.noteChannelFailure(cand)
			return attemptNextChannel
		}

		// 该渠道应答（2xx 成功或 4xx 客户端错透传）。仅 2xx 计熔断成功
		if resp.StatusCode < 300 {
			h.Breaker.RecordSuccess(cand.ChannelID)
		}

		rec.ChannelID = &cand.ChannelID
		rec.Status = resp.StatusCode
		c.Writer.Header().Set("X-Tg-Channel-Id", strconv.FormatInt(cand.ChannelID, 10))
		ct := resp.Header.Get("Content-Type")

		if stream {
			if ct == "" {
				ct = "text/event-stream"
			}
			if h.Metrics != nil {
				h.Metrics.ActiveStreams.Inc()
			}
			c.Writer.Header().Set("Content-Type", ct)
			c.Writer.Header().Set("Cache-Control", "no-cache")
			c.Writer.Header().Set("X-Accel-Buffering", "no")
			c.Writer.WriteHeader(resp.StatusCode)
			usage, perr := pipeSSE(c.Writer, c.Request.Context(), resp.Body)
			_ = resp.Body.Close()
			if h.Metrics != nil {
				h.Metrics.ActiveStreams.Dec()
			}
			if perr != nil && usage == nil {
				rec.Error = truncateStr(perr.Error(), 500)
			}
			applyUsage(rec, usage, m)
			return attemptDone
		}

		// 非流式：读完上游再透传（上限 20MB）
		data, rerr2 := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
		_ = resp.Body.Close()
		if rerr2 != nil {
			onlyRateLimited = false
			lastErr = rerr2.Error()
			h.noteChannelFailure(cand)
			return attemptNextChannel // 尚未向客户端写出字节，可换渠道
		}
		if ct == "" {
			ct = "application/json"
		}
		// 2xx 且非流式、体积受限 → 写精确缓存
		if resp.StatusCode < 300 && cacheKey != "" && len(data) <= 1<<20 {
			h.Coord.CacheSet(cacheKey, data, h.CacheTTL)
		}
		c.Data(resp.StatusCode, ct, data)
		if resp.StatusCode >= 400 {
			var er struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			_ = json.Unmarshal(data, &er)
			msg := er.Error.Message
			if msg == "" {
				msg = fmt.Sprintf("upstream returned %d", resp.StatusCode)
			}
			rec.Error = truncateStr(msg, 500)
			return attemptDone
		}
		var ur struct {
			Usage *Usage `json:"usage"`
		}
		_ = json.Unmarshal(data, &ur)
		applyUsage(rec, ur.Usage, m)
		return attemptDone
	}

	for i := 0; i < len(cands); i++ {
		if c.Request.Context().Err() != nil {
			rec.Status, rec.Error = 499, "client disconnected"
			return
		}
		switch tryCandidate(cands[i]) {
		case attemptDone:
			return
		case attemptNextChannel:
			ch := cands[i].ChannelID
			for i+1 < len(cands) && cands[i+1].ChannelID == ch {
				i++ // 跳过该渠道剩余 Key
			}
		}
	}

	// 全部候选耗尽：仅剩限流类失败时回 429（OpenAI SDK 对 429 有专门退避），否则 502
	rec.Error = "all channels failed: " + lastErr
	if onlyRateLimited {
		rec.Status = http.StatusTooManyRequests
		h.writeRetryAfter(c, h.KeyCooldown)
		openaiError(c, http.StatusTooManyRequests, "upstream_busy",
			"upstream is rate limited, please retry later")
		return
	}
	rec.Status = http.StatusBadGateway
	openaiError(c, http.StatusBadGateway, "upstream_error",
		"all upstream channels failed: "+truncateStr(lastErr, 200))
}

// attemptResult 单个候选尝试后的流转动作
type attemptResult int

const (
	attemptDone        attemptResult = iota // 已向客户端写出响应
	attemptNextKey                          // 同渠道下一把 Key
	attemptNextChannel                      // 跳过该渠道全部剩余 Key
)

// cacheFields 参与精确缓存 key 的请求字段白名单（固定顺序迭代，绝不 range map）
var cacheFields = []string{
	"model", "messages", "temperature", "top_p", "seed",
	"presence_penalty", "frequency_penalty", "max_tokens", "stop",
	"response_format", "tools", "tool_choice",
}

// cacheKey 精确缓存 key：白名单字段按固定顺序拼接原始 JSON 后取 SHA-256（model 为对外名）
func (h *Handler) cacheKey(bm map[string]json.RawMessage, orgID int64) string {
	var sb strings.Builder
	if h.CacheIsolateOrg {
		fmt.Fprintf(&sb, "org:%d:", orgID)
	}
	for _, f := range cacheFields {
		if raw, ok := bm[f]; ok {
			sb.WriteString(f)
			sb.WriteByte(0)
			sb.Write(raw)
			sb.WriteByte(0)
		}
	}
	sum := sha256.Sum256([]byte(sb.String()))
	return hex.EncodeToString(sum[:])
}

// writeRetryAfter 输出 Retry-After 响应头（至少 1 秒，向上取整）
func (h *Handler) writeRetryAfter(c *gin.Context, d time.Duration) {
	secs := int((d + time.Second - 1) / time.Second)
	if secs < 1 {
		secs = 1
	}
	c.Writer.Header().Set("Retry-After", strconv.Itoa(secs))
}

// parseRetryAfter 解析上游 Retry-After（秒数或 HTTP 日期）；仅信任 (0, 10min]，越界视为未提供
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		return clampRetryAfter(secs)
	}
	if t, err := http.ParseTime(v); err == nil {
		return clampRetryAfter(int(time.Until(t).Seconds()))
	}
	return 0
}

func clampRetryAfter(secs int) time.Duration {
	if secs <= 0 || secs > 600 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

// disableKey 401/403 后禁用 Key：池内 Key 异步置 status=0（可在 Key 池管理手动恢复）；
// legacy 单 Key 用长效冷却代替禁用（到期自动恢复，避免把渠道一刀切死）
func (h *Handler) disableKey(cand Candidate, status int) {
	if cand.KeyID == 0 {
		d := 10 * h.KeyCooldown
		if d < 10*time.Minute {
			d = 10 * time.Minute
		}
		h.Coord.SetCooldown(cand.KeyScope(), d)
		slog.Warn("上游 401/403，legacy 单 Key 进入长效冷却",
			"channel_id", cand.ChannelID, "status", status, "cooldown", d.String())
		return
	}
	if h.Metrics != nil {
		h.Metrics.KeyDisabled.Inc()
	}
	reason := fmt.Sprintf("auto-disabled: upstream %d at %s", status, time.Now().Format("2006-01-02 15:04:05"))
	go func() {
		if err := h.DB.Model(&model.ChannelKey{}).
			Where("id = ? AND status = 1", cand.KeyID).
			Updates(map[string]any{"status": 0, "remark": reason, "updated_at": time.Now().Unix()}).Error; err != nil {
			slog.Error("自动禁用渠道 Key 失败", "channel_id", cand.ChannelID, "key_id", cand.KeyID, "err", err)
			return
		}
		slog.Warn("上游 401/403，已自动禁用渠道 Key", "channel_id", cand.ChannelID, "key_id", cand.KeyID)
	}()
}

// ListModels GET /v1/models：该 key 授权范围内启用的模型（OpenAI list 格式）
func (h *Handler) ListModels(c *gin.Context) {
	ki := middleware.GetKeyInfo(c)
	var rows []struct {
		Name        string
		DisplayName string
		Vendor      string
	}
	if err := h.DB.Raw(`
		SELECT m.name, m.display_name, m.vendor
		FROM user_model_grants g JOIN models m ON m.name = g.model_name
		WHERE g.user_id = ? AND m.status = 1
		ORDER BY m.name`, ki.UserID).Scan(&rows).Error; err != nil {
		openaiError(c, http.StatusInternalServerError, "internal_error", "failed to list models")
		return
	}
	data := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		data = append(data, gin.H{
			"id": r.Name, "object": "model", "owned_by": r.Vendor,
			"display_name": r.DisplayName,
		})
	}
	c.JSON(http.StatusOK, gin.H{"object": "list", "data": data})
}

// noteChannelFailure 记录渠道失败并计数熔断；达到阈值异步禁用
func (h *Handler) noteChannelFailure(cand Candidate) {
	if h.Metrics != nil {
		h.Metrics.UpstreamErrors.Inc()
	}
	if h.Breaker.RecordFailure(cand.ChannelID) {
		go h.DisableChannel(cand.ChannelID, cand.ChannelName)
	}
}

// applyUsage 把 usage 折算为成本快照（售卖价扣客户 + 成本价记厂商成本）；上游未回 usage 则标记 no_usage、不计费
func applyUsage(rec *model.UsageLog, u *Usage, m model.Model) {
	if u == nil {
		rec.NoUsage = 1
		return
	}
	rec.PromptTokens = u.PromptTokens
	rec.CompletionTokens = u.CompletionTokens
	rec.InputPrice = m.InputPrice
	rec.OutputPrice = m.OutputPrice
	rec.CostInputPrice = m.CostInputPrice
	rec.CostOutputPrice = m.CostOutputPrice
	rec.Cost = CalcCost(u.PromptTokens, u.CompletionTokens, m.InputPrice, m.OutputPrice)
	rec.VendorCost = CalcCost(u.PromptTokens, u.CompletionTokens, m.CostInputPrice, m.CostOutputPrice)
}

func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return strings.TrimSpace(s)
}

func rawBool(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var b bool
	return json.Unmarshal(raw, &b) == nil && b
}

func cloneMap(m map[string]json.RawMessage) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	return out
}
