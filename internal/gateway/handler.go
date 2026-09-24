package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/bits"
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
	"token-gateway/internal/scrub"
	"token-gateway/internal/service"
)

// Usage OpenAI 兼容 usage 结构（计费需要的字段）。缓存命中 tokens 兼容两种方言：
// DeepSeek 风格 prompt_cache_hit_tokens / OpenAI 风格 usage.prompt_tokens_details.cached_tokens
type Usage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	// DeepSeek 方言：命中/未命中的输入 tokens（两者之和 = prompt_tokens）
	PromptCacheHitTokens int64 `json:"prompt_cache_hit_tokens"`
	PromptCacheMissTokens int64 `json:"prompt_cache_miss_tokens"`
	// OpenAI 方言：prompt_tokens_details.cached_tokens
	PromptTokensDetails *struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

// CachedTokens 归一化取缓存命中输入 tokens：两种方言都可能出现，取较大者；
// 调用方负责夹到 [0, prompt_tokens]（缓存命中是 prompt_tokens 的一部分）
func (u Usage) CachedTokens() int64 {
	c := u.PromptCacheHitTokens
	if u.PromptTokensDetails != nil && u.PromptTokensDetails.CachedTokens > c {
		c = u.PromptTokensDetails.CachedTokens
	}
	return c
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

	// ---- 上游调度强化 ----
	KeyCooldownScope  string // channel=429 时同渠道全部 Key 一起冷却（厂商按账户限速）；key=仅当前 Key
	MaxCandidates     int    // 单请求最多尝试候选数（渠道×Key）；0=不限
	RetryKeyCodes     map[int]bool // 命中 → 冷却 Key + 同渠道换下一把（默认 429）
	DisableKeyCodes   map[int]bool // 命中 → 禁用 Key + 换渠道（默认 401/403）
	RetryChannelCodes map[int]bool // 命中 → 熔断计数 + 跳过渠道（默认 5xx）
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
		KeyCooldownScope: cfg.Gateway.KeyCooldownScope,
		MaxCandidates:    cfg.Gateway.MaxCandidates,
		RetryKeyCodes:    parseCodeSet(cfg.Gateway.RetryKeyCodes),
		DisableKeyCodes:  parseCodeSet(cfg.Gateway.DisableKeyCodes),
		RetryChannelCodes: parseCodeSet(cfg.Gateway.RetryChannelCodes),
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

// 计费域钳制：tokens 与单价均来自外部不可信输入（上游 usage / 管理台定价），
// 负值按 0 计；tokens 封顶 1e10（真实请求的量级上限），单笔成本封顶 1e15 点
// （=10 亿元/M token 价 × 1e10 tokens，远超真实账单；防下游 quota_used 累加回绕）。
const (
	maxUsageTokens = int64(10_000_000_000)
	maxUsagePoints = int64(1_000_000_000_000_000)
)

func clampTokens(n int64) int64 {
	if n < 0 {
		return 0
	}
	if n > maxUsageTokens {
		return maxUsageTokens
	}
	return n
}

func clampPrice(p int64) int64 {
	if p < 0 {
		return 0
	}
	return p
}

// CalcCost 全整数计费：cost = ceil((pt×ip + ct×op) / 1M)，向上取整避免零成本刷量。
// 输入先钳制到安全域，再以 128 位精确乘除——裸 int64 乘法在 tokens×价格上
// 可回绕为负（免费放行）或任意值（错误计费），ceil 的 +999_999 也可回绕出负成本。
// 无缓存命中语义的等价入口（cachedTokens=0、缓存价 0=同输入价），历史调用方与测试不变。
func CalcCost(promptTokens, completionTokens, inputPrice, outputPrice int64) int64 {
	return CalcCostCached(promptTokens, 0, completionTokens, inputPrice, 0, outputPrice)
}

// CalcCostCached 缓存计费内核：输入 tokens 拆成「未命中×输入价 + 命中×缓存价」两段，
// cost = ceil(((pt-hit)×ip + hit×chp + ct×op) / 1M)。
// cacheHitPrice=0 表示未配置缓存价 → 命中段按输入价计（与旧计费完全一致）。
// cachedTokens 超出 promptTokens 时按 promptTokens 计（上游脏数据不放大账单）。
func CalcCostCached(promptTokens, cachedTokens, completionTokens, inputPrice, cacheHitPrice, outputPrice int64) int64 {
	pt, ct := clampTokens(promptTokens), clampTokens(completionTokens)
	hit := clampTokens(cachedTokens)
	if hit > pt {
		hit = pt
	}
	ip, chp, op := clampPrice(inputPrice), clampPrice(cacheHitPrice), clampPrice(outputPrice)
	if chp == 0 {
		chp = ip // 0=同输入单价：未配置缓存价的模型计费零变化
	}
	hi, lo := mulAdd128(pt-hit, ip, hit, chp, ct, op)
	if hi >= 1_000_000 { // 商超出 64 位，必然远超单笔天花板
		return maxUsagePoints
	}
	q, r := bits.Div64(hi, lo, 1_000_000)
	if q > uint64(maxUsagePoints) || q == uint64(maxUsagePoints) && r > 0 {
		return maxUsagePoints
	}
	if r > 0 {
		q++
	}
	return int64(q)
}

// mulAdd128 计算 a×b + c×d + e×f 的 128 位结果（高位，低位），全程无回绕
func mulAdd128(a, b, c, d, e, f int64) (hi, lo uint64) {
	hi1, lo1 := bits.Mul64(uint64(a), uint64(b))
	hi2, lo2 := bits.Mul64(uint64(c), uint64(d))
	hi3, lo3 := bits.Mul64(uint64(e), uint64(f))
	lo = lo1 + lo2
	carry := uint64(0)
	if lo < lo1 { // lo1+lo2 进位
		carry = 1
	}
	lo += lo3
	if lo < lo3 { // +lo3 再进位
		carry++
	}
	hi = hi1 + hi2 + hi3 + carry
	return
}

// openaiError 数据面 OpenAI 形状错误出口：消息统一消毒（不携带任何 URL/主机——
// 网络类 err.Error() 的标准形态是 `Post "https://上游地址": ...`，会把渠道拓扑漏给客户）
func openaiError(c *gin.Context, status int, errType, msg string) {
	c.JSON(status, gin.H{
		"error": gin.H{"message": scrub.Str(msg), "type": errType, "code": errType},
	})
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// relayProto 数据面端点的协议形态：决定边界的翻译方向，编排内核完全一致
type relayProto int

const (
	protoOpenAI     relayProto = iota // OpenAI chat/completions 原生直通
	protoAnthropic                    // /v1/messages：Anthropic Messages 协议
	protoResponses                    // /v1/responses：OpenAI Responses 协议（Codex 系客户端）
)

// relaySpec 端点差异参数化：chat / embeddings / messages / responses 共用同一条编排链路
// （限流→解析→授权→预检→缓存→选渠道→闸门→转发→分类→结算），chat 行为零变化
type relaySpec struct {
	cacheFields []string // 参与精确缓存 key 的字段白名单（固定顺序）
	allowStream bool     // 是否支持流式（embeddings 不支持）
	fixedPath   string   // 非空 → 出站路径最后一段替换为该值（如 /embeddings）
	require     []string // 除 model 外的必填请求体字段
	// 非 OpenAI 协议端点：入站翻译为 OpenAI 格式、出站（响应/SSE/错误形状）
	// 翻译回该协议形状；翻译只发生在边界（见 anthropic.go / responses.go）
	proto relayProto
}

// ChatCompletions POST /v1/chat/completions
func (h *Handler) ChatCompletions(c *gin.Context) {
	h.relay(c, relaySpec{cacheFields: chatCacheFields, allowStream: true})
}

// Messages POST /v1/messages：Anthropic Messages 协议（Claude Code 等 Anthropic 系
// 客户端直连）。协议翻译只发生在边界，编排内核（授权/额度/渠道/映射/计费）完全复用
func (h *Handler) Messages(c *gin.Context) {
	h.relay(c, relaySpec{cacheFields: chatCacheFields, allowStream: true, proto: protoAnthropic})
}

// Responses POST /v1/responses：OpenAI Responses 协议（Codex CLI 0.142+ 等客户端
// 直连，自定义 provider 仅支持 wire_api="responses"）。协议翻译只发生在边界，
// 编排内核（授权/额度/渠道/映射/计费）完全复用
func (h *Handler) Responses(c *gin.Context) {
	h.relay(c, relaySpec{cacheFields: chatCacheFields, allowStream: true, proto: protoResponses})
}

// Embeddings POST /v1/embeddings：向量接口（RAG/知识库场景）；
// 计费走 CalcCost 的 completion=0 退化路径（纯输入计费）
func (h *Handler) Embeddings(c *gin.Context) {
	h.relay(c, relaySpec{
		cacheFields: embeddingsCacheFields,
		fixedPath:   "/embeddings",
		require:     []string{"input"},
	})
}

// relay 共享中继编排：限流 → 解析 body → 模型/授权/额度校验 → 渠道选择 → 转发（流式/非流式）→ 结算
func (h *Handler) relay(c *gin.Context, spec relaySpec) {
	start := time.Now()
	ki := middleware.GetKeyInfo(c)
	// 错误响应形状：Anthropic 端点用 Anthropic 形状，其余（含 Responses）维持 OpenAI 形状
	werr := openaiError
	if spec.proto == protoAnthropic {
		werr = anthropicError
	}

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
		werr(c, http.StatusTooManyRequests, "rate_limit_error", "rate limit exceeded, please retry later")
		return
	}

	// 读取 body（限长）
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, h.MaxBody+1))
	if err != nil || int64(len(body)) > h.MaxBody {
		// 已读 limit+1，剩余部分有界排空后再回 413：不读直接关会让
		// "先写完再读" 的客户端（urllib/curl）撞上 RST 读不到错误响应
		middleware.DrainRequestBody(c.Request.Body, h.MaxBody)
		rec.Status, rec.Error = http.StatusRequestEntityTooLarge, "request body too large"
		werr(c, http.StatusRequestEntityTooLarge, "request_too_large", "request body too large")
		return
	}
	var bodyMap map[string]json.RawMessage
	switch spec.proto {
	case protoAnthropic:
		// Anthropic → OpenAI 翻译；失败即请求非法（model/messages 缺失、块格式错误等）
		bodyMap, err = decodeAnthropicRequest(body)
		if err != nil {
			rec.Status, rec.Error = http.StatusBadRequest, err.Error()
			werr(c, http.StatusBadRequest, "invalid_request_error", err.Error())
			return
		}
	case protoResponses:
		// Responses → OpenAI 翻译；失败即请求非法（model/input 缺失、input 数组格式错误等）。
		// 必填字段校验在解码器内完成（input 是 string|array，无法走 spec.require 的键存在性检查）
		bodyMap, err = decodeResponsesRequest(body)
		if err != nil {
			rec.Status, rec.Error = http.StatusBadRequest, err.Error()
			werr(c, http.StatusBadRequest, "invalid_request_error", err.Error())
			return
		}
	default:
		if err := json.Unmarshal(body, &bodyMap); err != nil || len(bodyMap) == 0 {
			rec.Status, rec.Error = http.StatusBadRequest, "invalid JSON body"
			werr(c, http.StatusBadRequest, "invalid_request_error", "invalid JSON body")
			return
		}
	}

	modelName := rawString(bodyMap["model"])
	if modelName == "" {
		rec.Status, rec.Error = http.StatusBadRequest, "missing model"
		werr(c, http.StatusBadRequest, "invalid_request_error", "missing required parameter: model")
		return
	}
	// 模型名是客户端可控文本：计量入库前截断，防止恶意超长名撑爆 usage_logs
	rec.ModelName = truncateStr(modelName, 190)

	// 端点必填字段（embeddings: input）
	for _, f := range spec.require {
		if _, ok := bodyMap[f]; !ok {
			rec.Status, rec.Error = http.StatusBadRequest, "missing "+f
			werr(c, http.StatusBadRequest, "invalid_request_error",
				fmt.Sprintf("missing required parameter: %s", f))
			return
		}
	}

	stream := rawBool(bodyMap["stream"]) && spec.allowStream
	if stream {
		rec.IsStream = 1
	}

	// 模型存在且启用（禁用模型与不存在同响应，不泄漏存在性；与 /v1/models、playground 的 status=1 口径一致）
	var m model.Model
	if err := h.DB.Where("name = ? AND status = 1", modelName).First(&m).Error; err != nil {
		rec.Status, rec.Error = http.StatusNotFound, "model not found: "+truncateStr(modelName, 100)
		werr(c, http.StatusNotFound, "invalid_request_error",
			fmt.Sprintf("model %q does not exist or is not available", modelName))
		return
	}

	// 模型授权（子账号白名单）；在线体验（Playground）由管理面按角色预授权，跳过此检查
	if !ki.Playground {
		var cnt int64
		if err := h.DB.Model(&model.UserModelGrant{}).
			Where("user_id = ? AND model_name = ?", ki.UserID, modelName).Count(&cnt).Error; err != nil || cnt == 0 {
			rec.Status, rec.Error = http.StatusForbidden, "model not allowed: "+truncateStr(modelName, 100)
			werr(c, http.StatusForbidden, "model_not_allowed",
				fmt.Sprintf("you are not allowed to use model %q, please contact your company admin", modelName))
			return
		}
	}

	// 额度预检查（读）；真实扣减在响应结束后的结算事务里。
	// 系统管理员在线体验无客户归属（OrgID=0），无额度语义，跳过。
	if ki.OrgID > 0 {
		if err := service.Precheck(h.DB, ki.UserID); err != nil {
			// 额度/月限不足用 402（DeepSeek 官方同款口径）：重试不可能恢复，
			// 429 会让 OpenAI 系客户端（codex 等）指数退避重试到上限再报
			// "exceeded retry limit"，把真正的月限信息丢掉
			if errors.Is(err, service.ErrUserQuota) || errors.Is(err, service.ErrOrgQuota) {
				rec.Status, rec.Error = http.StatusPaymentRequired, err.Error()
				werr(c, http.StatusPaymentRequired, "insufficient_balance", err.Error())
				return
			}
			if errors.Is(err, service.ErrUserMonthly) || errors.Is(err, service.ErrOrgMonthly) {
				rec.Status, rec.Error = http.StatusPaymentRequired, err.Error()
				werr(c, http.StatusPaymentRequired, "monthly_limit_exceeded", err.Error())
				return
			}
			rec.Status, rec.Error = http.StatusInternalServerError, err.Error()
			werr(c, http.StatusInternalServerError, "internal_error", "quota precheck failed")
			return
		}
	}

	// 精确缓存查询（授权/预检之后、选渠道之前）：命中直接回，不触发上游调用与扣费
	cacheKey := ""
	if h.CacheTTL > 0 && !stream {
		cacheKey = h.cacheKey(bodyMap, ki.OrgID, spec.cacheFields)
		if data, ok := h.Coord.CacheGet(cacheKey); ok {
			if h.Metrics != nil {
				h.Metrics.CacheHits.Inc()
			}
			rec.Status = http.StatusOK
			rec.CacheHit = 1
			c.Writer.Header().Set("X-Tg-Cache", "hit")
			c.Data(http.StatusOK, "application/json", data)
			// 缓存里存的是本协议形状（翻译型端点存翻译后的体），usage 按协议取
			var usage *Usage
			switch spec.proto {
			case protoAnthropic:
				usage = anthropicUsageFrom(data)
			case protoResponses:
				usage = responsesUsageFrom(data)
			default:
				var ur struct {
					Usage *Usage `json:"usage"`
				}
				_ = json.Unmarshal(data, &ur)
				usage = ur.Usage
			}
			if usage != nil {
				rec.PromptTokens = usage.PromptTokens
				rec.CompletionTokens = usage.CompletionTokens
				rec.CachedTokens = usage.CachedTokens()
				if rec.CachedTokens > rec.PromptTokens {
					rec.CachedTokens = rec.PromptTokens
				}
				rec.InputPrice = m.InputPrice
				rec.OutputPrice = m.OutputPrice
				rec.InputCacheHitPrice = m.InputCacheHitPrice
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
		werr(c, http.StatusInternalServerError, "internal_error", "failed to select channels")
		return
	}
	if len(cands) == 0 {
		// 区分错误语义：配置问题（重试无意义）回 503 并给出可行动的信息；限流回 429
		switch {
		case selStats.Channels == 0:
			rec.Status, rec.Error = http.StatusServiceUnavailable, "no enabled channel for model"
			werr(c, http.StatusServiceUnavailable, "no_available_channel",
				"no enabled channel serves this model, please contact the platform admin")
			return
		case selStats.KeyedChannels == 0:
			rec.Status, rec.Error = http.StatusServiceUnavailable, "no usable upstream key (not configured or disabled)"
			werr(c, http.StatusServiceUnavailable, "channel_key_missing",
				"upstream key is not configured or disabled, please contact the platform admin")
			return
		case selStats.DecryptFailed > 0 && selStats.DecryptFailed == selStats.KeyedChannels:
			// 密钥材料齐备但全部解不开：aes_key 轮换后未重建渠道密钥 / 密文损坏。
			// 也是配置问题（重试无意义），不得落入"全冷却"429 误导客户端退避重试
			rec.Status, rec.Error = http.StatusServiceUnavailable, "all upstream keys failed to decrypt"
			werr(c, http.StatusServiceUnavailable, "channel_key_missing",
				"upstream keys cannot be decrypted (aes_key changed?), please contact the platform admin")
			return
		default:
			// 全部 Key 均因配额耗尽冷却（厂商侧限额，火山 SetLimitExceeded 等）：
			// 重试不可能恢复 → 402（与额度预检同口径），429 会让客户端盲退避
			if selStats.QuotaCoolingKeys > 0 && selStats.QuotaCoolingKeys == selStats.CoolingKeys {
				rec.Status, rec.Error = http.StatusPaymentRequired, "no available key (all quota cooling)"
				werr(c, http.StatusPaymentRequired, "upstream_quota_exceeded",
					"upstream vendor quota is exhausted, please contact the platform admin")
				return
			}
			// 其余冷却（普通限流）语义不变：所有 Key 冷却中 → 429 而非 503
			rec.Status, rec.Error = http.StatusTooManyRequests, "no available key (all cooling)"
			h.writeRetryAfter(c, h.KeyCooldown)
			werr(c, http.StatusTooManyRequests, "upstream_busy",
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
	authOnly := true        // 全部失败均因 401/403（Key 失效自动禁用）→ 回 503 而非 502
	quotaOnly := true       // 全部失败均因配额类 429（厂商侧限额）→ 回 402 而非 429
	coolApplied := time.Duration(0) // 本请求实际应用过的最大 Key 冷却时长（429 耗尽时如实回报客户端）
	tryCandidate := func(cand Candidate) attemptResult {
		// 渠道并发闸门（有界等待）。超时换渠道：同渠道其他 Key 面对同一个满闸门，重试无意义。
		// 等待上限 QueueWaitTimeout：<=0 时退化为仅随客户端断开取消（不无限等）
		qStart := time.Now()
		qCtx := c.Request.Context()
		if h.QueueWaitTimeout > 0 {
			var qCancel context.CancelFunc
			qCtx, qCancel = context.WithTimeout(qCtx, h.QueueWaitTimeout)
			defer qCancel() // 计时器随本次尝试结束释放
		}
		release, ok := h.Coord.AcquireSlot(qCtx, cand.SlotScope(), h.MaxConcurrency)
		if !ok {
			if h.Metrics != nil {
				h.Metrics.QueueTimeouts.Inc()
			}
			lastErr = fmt.Sprintf("queue wait timeout on channel %s", cand.ChannelName)
			onlyRateLimited, authOnly, quotaOnly = false, false, false
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
			onlyRateLimited, authOnly, quotaOnly = false, false, false
			return attemptNextKey
		}
		req, rerr := http.NewRequestWithContext(c.Request.Context(), http.MethodPost,
			endpointURL(cand.BaseURL, cand.Path, spec.fixedPath), bytes.NewReader(upBody))
		if rerr != nil {
			lastErr = rerr.Error()
			onlyRateLimited, authOnly, quotaOnly = false, false, false
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
			if c.Request.Context().Err() != nil {
				// 客户端已断开导致的取消：不是渠道的错，不计熔断，也不再换渠道重试
				rec.Status, rec.Error = 499, "client disconnected"
				return attemptDone
			}
			lastErr = derr.Error()
			onlyRateLimited, authOnly, quotaOnly = false, false, false
			// 原始错误（含上游地址）只进服务端日志供平台管理员排障，客户侧出口统一消毒
			slog.Warn("上游连接失败", "channel_id", cand.ChannelID, "channel", cand.ChannelName, "err", lastErr)
			h.noteChannelFailure(cand)
			return attemptNextChannel // 网络失败 → 跳过该渠道（尚未向客户端写出任何字节）
		}

		// drain 读空并关闭，保证连接可复用
		drain := func() {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
			_ = resp.Body.Close()
		}

		switch {
		case codeMatch(h.RetryKeyCodes, resp.StatusCode):
			// 429（默认）→ 同渠道下一把 Key。先读错误体区分两类语义：
			// 配额类（火山 SetLimitExceeded / OpenAI insufficient_quota 等）是持久的账户
			// 配额暂停而非瞬时限流——只冷却该 Key（Key 池可能跨账户混布，同渠道其它
			// Key 不应连坐）并按指数退避拉长冷却（起步 10×KeyCooldown ≥10min，连击翻倍
			// 封顶 24h：死 Key 的探测开销随时间衰减到每天一次；冷却到期归零，配额恢复后
			// 自动回池），错误码写入 lastErr 落 usage_logs 便于排障；
			// 普通限流 429 维持原语义：按 key_cooldown_scope 冷却（Retry-After 优先，
			// channel 粒度时同渠道全部 Key 一起冷却——厂商限额按账户，逐个试错纯浪费）
			eb, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
			if h.Metrics != nil {
				h.Metrics.Upstream429.Inc()
			}
			if qc := quota429Code(eb); qc != "" {
				d := h.Coord.Backoff(cand.KeyScope(), longKeyCooldown(h.KeyCooldown), quota429CooldownMax)
				if h.Metrics != nil {
					h.Metrics.KeyCooldown.Inc()
				}
				if d > coolApplied {
					coolApplied = d
				}
				slog.Warn("上游配额类 429，该 Key 指数退避冷却（同渠道其它 Key 不连坐）",
					"channel_id", cand.ChannelID, "channel", cand.ChannelName,
					"key_id", cand.KeyID, "code", qc, "cooldown", d.String())
				h.Coord.MarkQuotaCooling(cand.KeyScope(), d) // 供选路无候选时区分"配额冷却"→ 402
				lastErr = fmt.Sprintf("upstream %s returned %d %s (key %d quota cooldown %s)",
					cand.ChannelName, resp.StatusCode, qc, cand.KeyID, d)
				authOnly = false
				return attemptNextKey
			}
			cd := parseRetryAfter(resp.Header.Get("Retry-After"))
			if cd <= 0 {
				cd = h.KeyCooldown
			}
			if cd > coolApplied {
				coolApplied = cd
			}
			h.coolKeys(cands, cand, cd)
			lastErr = fmt.Sprintf("upstream %s returned %d (key %d cooling %s)",
				cand.ChannelName, resp.StatusCode, cand.KeyID, cd)
			authOnly, quotaOnly = false, false
			return attemptNextKey

		case codeMatch(h.DisableKeyCodes, resp.StatusCode):
			// 401/403（默认）：Key 失效 → 禁用该 Key → 同渠道下一把 Key（主 Key 报错自动切备用；
			// 候选列表只前进不回看，被禁 Key 不会在本请求内重复选中）
			drain()
			onlyRateLimited, quotaOnly = false, false
			h.disableKey(cand, resp.StatusCode)
			lastErr = fmt.Sprintf("upstream %s key %d returned %d", cand.ChannelName, cand.KeyID, resp.StatusCode)
			return attemptNextKey

		case codeMatch(h.RetryChannelCodes, resp.StatusCode):
			// 5xx（默认）：渠道级故障，熔断计数并跳过该渠道全部剩余 Key（同 endpoint 换 Key 无意义）
			drain()
			onlyRateLimited, authOnly, quotaOnly = false, false, false
			lastErr = fmt.Sprintf("upstream %s returned %d", cand.ChannelName, resp.StatusCode)
			h.noteChannelFailure(cand)
			return attemptNextChannel
		}

		// 该渠道应答（2xx 成功或 4xx 客户端错透传）。
		// 熔断成功计数刻意后置：2xx 只代表响应头健康，body 中途断流的渠道
		// 不应被计为成功——否则每次"头 200 + 体断"净计数恒 1，永远到不了熔断阈值。
		// 流式在 pipeSSE 干净收流后、非流式在读全体后各自 RecordSuccess

		rec.ChannelID = &cand.ChannelID
		rec.Status = resp.StatusCode
		c.Writer.Header().Set("X-Tg-Channel-Id", strconv.FormatInt(cand.ChannelID, 10))
		ct := resp.Header.Get("Content-Type")

		// 模型映射：响应中的上游名改写回外部名（含 SSE 每块），映射对客户不可见
		var modelSwap [2]string
		if cand.UpstreamModel != "" && cand.UpstreamModel != modelName {
			modelSwap = [2]string{cand.UpstreamModel, modelName}
		}

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
			// SSE 翻译：协议型端点逐块翻译成本协议事件流，OpenAI 原生直通
			pipe := pipeSSE
			switch spec.proto {
			case protoAnthropic:
				pipe = anthropicPipeSSE
			case protoResponses:
				pipe = responsesPipeSSE
			}
			usage, perr := pipe(c.Writer, c.Request.Context(), resp.Body, modelSwap)
			_ = resp.Body.Close()
			if h.Metrics != nil {
				h.Metrics.ActiveStreams.Dec()
			}
			if perr != nil {
				// 客户端断开（ctx 取消连带上游连接被拆）：非渠道之过，记 499，
				// 与非流式路径同口径——否则错误率统计把断连误算成成功 200
				if c.Request.Context().Err() != nil {
					rec.Status = 499
				} else {
					// 上游中途断流（读错误，非干净 EOF）：与非流式读体失败同口径计入
					// 渠道失败——否则持续断流的坏渠道永远不被熔断，客户端一直收截断流。
					// 已写出字节无法换渠道重试，这里只为后续路由健康记账
					h.noteChannelFailure(cand)
				}
				if usage == nil {
					rec.Error = truncateStr(perr.Error(), 500)
				}
			}
			if perr == nil && resp.StatusCode < 300 {
				h.Breaker.RecordSuccess(cand.ChannelID) // 干净收流（含合法 EOF）才算渠道成功
			}
			applyUsage(rec, usage, m)
			return attemptDone
		}

		// 非流式：读完上游再透传（上限 20MB）
		data, rerr2 := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
		_ = resp.Body.Close()
		if rerr2 != nil {
			if c.Request.Context().Err() != nil {
				// 客户端中途断开：读体失败非渠道之过，不计熔断不重试
				rec.Status, rec.Error = 499, "client disconnected"
				return attemptDone
			}
			onlyRateLimited, authOnly, quotaOnly = false, false, false
			lastErr = rerr2.Error()
			h.noteChannelFailure(cand)
			return attemptNextChannel // 尚未向客户端写出字节，可换渠道
		}
		if ct == "" {
			ct = "application/json"
		}
		// 模型映射：缓存与客户端拿到的都是外部名（缓存回放不泄漏上游名）
		data = rewriteModel(data, modelSwap)
		if resp.StatusCode >= 400 {
			// 上游错误体：提取 message 记日志；Anthropic 端点包成 Anthropic 错误形状再透传。
			// 透传前消毒——部分厂商错误体里带文档链接，且 usage_logs 的 error 列客户管理员可见
			var er struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			_ = json.Unmarshal(data, &er)
			msg := scrub.Str(er.Error.Message)
			if msg == "" {
				msg = fmt.Sprintf("upstream returned %d", resp.StatusCode)
			}
			rec.Error = truncateStr(msg, 500)
			if spec.proto == protoAnthropic {
				data = anthropicErrorBody(msg)
			} else {
				data = scrub.Bytes(data)
			}
			c.Data(resp.StatusCode, ct, data)
			return attemptDone
		}
		// 2xx：协议型端点先翻译成本协议形状（缓存与客户端拿到的都是本协议形状，
		// 回放零再翻译）；翻译失败视为渠道应答异常，换下一渠道
		var usage *Usage
		switch spec.proto {
		case protoAnthropic:
			out, u, eerr := encodeAnthropicResponse(data, modelName)
			if eerr != nil {
				onlyRateLimited, authOnly, quotaOnly = false, false, false
				lastErr = "translate response: " + eerr.Error()
				h.noteChannelFailure(cand)
				return attemptNextChannel
			}
			data, usage = out, u
		case protoResponses:
			out, u, eerr := encodeResponsesResponse(data, modelName)
			if eerr != nil {
				onlyRateLimited, authOnly, quotaOnly = false, false, false
				lastErr = "translate response: " + eerr.Error()
				h.noteChannelFailure(cand)
				return attemptNextChannel
			}
			data, usage = out, u
		}
		// 非流式、体积受限 → 写精确缓存
		if resp.StatusCode < 300 && cacheKey != "" && len(data) <= 1<<20 {
			h.Coord.CacheSet(cacheKey, data, h.CacheTTL)
		}
		c.Data(resp.StatusCode, ct, data)
		if spec.proto == protoOpenAI {
			var ur struct {
				Usage *Usage `json:"usage"`
			}
			_ = json.Unmarshal(data, &ur)
			usage = ur.Usage
		}
		h.Breaker.RecordSuccess(cand.ChannelID) // 2xx 且读全体成功
		applyUsage(rec, usage, m)
		return attemptDone
	}

	tried, skipped, skippedQuota := 0, 0, 0
	for i := 0; i < len(cands); i++ {
		if c.Request.Context().Err() != nil {
			rec.Status, rec.Error = 499, "client disconnected"
			return
		}
		// 选择后可能已被置入冷却（本请求的渠道级冷却、或并发请求的 429）：尝试前再过滤一次
		if h.Coord.IsCooling(cands[i].KeyScope()) {
			skipped++
			if h.Coord.IsQuotaCooling(cands[i].KeyScope()) {
				skippedQuota++
			}
			continue
		}
		// 重试预算熔线：单请求最多尝试 N 个候选（渠道×Key），防止极端配置下打爆上游。
		// 触发时不改写失败分类标志，按已积累的失败原因回 429/503/502
		if h.MaxCandidates > 0 && tried >= h.MaxCandidates {
			lastErr = fmt.Sprintf("candidate budget (%d) exhausted, last: %s", h.MaxCandidates, lastErr)
			break
		}
		tried++
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

	// 全部候选耗尽：按失败原因分类回错（402 配额耗尽 / 429 限流 / 503 密钥失效 / 502 其他上游故障）
	rec.Error = truncateStr("all channels failed: "+lastErr, 500)
	switch {
	case quotaOnly && skipped == skippedQuota && (tried > 0 || skipped > 0):
		// 全部失败/跳过均因配额类 429（厂商侧限额耗尽，火山 SetLimitExceeded 等）：
		// 重试不可能恢复 → 402（与额度预检同口径）。429 会让 OpenAI 系客户端
		// （codex 等）指数退避重试到上限，把真实原因丢成 "exceeded retry limit"
		rec.Status = http.StatusPaymentRequired
		werr(c, http.StatusPaymentRequired, "upstream_quota_exceeded",
			"upstream vendor quota is exhausted, please contact the platform admin")
	case onlyRateLimited:
		// 仅剩限流类失败 → 429（OpenAI SDK 对 429 有专门退避）。
		// Retry-After 如实回报本请求实际应用的冷却时长（上游 Retry-After 可能远短于
		// key_cooldown，恒报 key_cooldown 会让客户端过度退避）
		rec.Status = http.StatusTooManyRequests
		ra := coolApplied
		if ra <= 0 {
			ra = h.KeyCooldown
		}
		h.writeRetryAfter(c, ra)
		werr(c, http.StatusTooManyRequests, "upstream_busy",
			"upstream is rate limited, please retry later")
	case authOnly:
		// 全部 Key 因 401/403 被上游拒绝并自动禁用 → 503，明确指向平台管理员配置问题
		rec.Status = http.StatusServiceUnavailable
		werr(c, http.StatusServiceUnavailable, "channel_key_invalid",
			"upstream rejected all keys (401/403); the invalid keys are auto-disabled, please contact the platform admin")
	default:
		rec.Status = http.StatusBadGateway
		werr(c, http.StatusBadGateway, "upstream_error",
			"all upstream channels failed: "+truncateStr(lastErr, 200))
	}
}

// attemptResult 单个候选尝试后的流转动作
type attemptResult int

const (
	attemptDone        attemptResult = iota // 已向客户端写出响应
	attemptNextKey                          // 同渠道下一把 Key
	attemptNextChannel                      // 跳过该渠道全部剩余 Key
)

// chatCacheFields chat 端点参与精确缓存 key 的请求字段白名单（固定顺序迭代，绝不 range map）。
// 影响输出的采样/行为参数必须全部入 key，否则语义不同的请求会命中同一缓存条目
var chatCacheFields = []string{
	"model", "messages", "temperature", "top_p", "seed",
	"presence_penalty", "frequency_penalty", "max_tokens", "max_completion_tokens", "stop",
	"n", "logit_bias", "logprobs", "parallel_tool_calls",
	"response_format", "tools", "tool_choice",
}

// embeddingsCacheFields embeddings 端点缓存白名单（input 数组顺序不同 = 不同请求，不命中）
var embeddingsCacheFields = []string{"model", "input", "encoding_format", "dimensions"}

// cacheKey 精确缓存 key：白名单字段按固定顺序拼接原始 JSON 后取 SHA-256（model 为对外名）
func (h *Handler) cacheKey(bm map[string]json.RawMessage, orgID int64, fields []string) string {
	var sb strings.Builder
	if h.CacheIsolateOrg {
		fmt.Fprintf(&sb, "org:%d:", orgID)
	}
	for _, f := range fields {
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

// endpointURL 出站完整地址 = base_url + path；fixed 非空时改写端点段：
// OpenAI 兼容约定 chat={base}/chat/completions、embeddings={base}/embeddings，
// 故 /chat/completions 后缀整体替换（/v1/chat/completions → /v1/embeddings）；
// 自定义 path 退化为替换最后一段
func endpointURL(baseURL, path, fixed string) string {
	if fixed != "" {
		if strings.HasSuffix(path, "/chat/completions") {
			path = strings.TrimSuffix(path, "/chat/completions") + fixed
		} else if idx := strings.LastIndex(path, "/"); idx >= 0 {
			path = path[:idx] + fixed
		} else {
			path = fixed
		}
	}
	return strings.TrimRight(baseURL, "/") + path
}

// rewriteModel 把响应体中 "model":"<上游名>" 字面量替换回外部名；映射未启用时原样返回（零开销）
func rewriteModel(data []byte, swap [2]string) []byte {
	if swap[0] == "" {
		return data
	}
	return swapModelBytes(data, swap)
}

// swapModelBytes 字面量替换的两种写法都覆盖：紧凑 "model":"x" 与带空格 "model": "x"
// （部分厂商/代理返回 pretty-print JSON）；带空格形态替换后同样保留原空格
func swapModelBytes(data []byte, swap [2]string) []byte {
	data = bytes.ReplaceAll(data,
		[]byte(`"model":"`+swap[0]+`"`),
		[]byte(`"model":"`+swap[1]+`"`))
	return bytes.ReplaceAll(data,
		[]byte(`"model": "`+swap[0]+`"`),
		[]byte(`"model": "`+swap[1]+`"`))
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

// quota429Codes 配额类 429 错误码（小写比较）：命中的是持久性账户配额暂停，
// 短冷却后重试注定再吃一次 429，且 Key 池跨账户混布时同渠道其它 Key 不应被渠道级冷却连坐
var quota429Codes = map[string]struct{}{
	"setlimitexceeded":       {}, // 火山方舟：账号用量达上限/安全体验模式，模型服务暂停
	"insufficient_quota":     {}, // OpenAI：配额耗尽
	"quota_exceeded":         {}, // 通用配额超限
	"exceeded_current_quota": {}, // OpenAI 计费文案变体
}

// quota429CooldownMax 配额类 429 指数退避的冷却封顶：死 Key 衰减到每 24h 最多探测一次。
// 默认档位序列：10m→20m→40m→1h20m→2h40m→5h20m→10h40m→21h20m→24h（连击翻倍、到期归零）
const quota429CooldownMax = 24 * time.Hour

// longKeyCooldown 长效冷却起步档：10×key_cooldown、下限 10min。
// 配额 429 指数退避的 base 与 legacy 单 Key 401/403 的长效冷却共用此式
func longKeyCooldown(kc time.Duration) time.Duration {
	d := 10 * kc
	if d < 10*time.Minute {
		d = 10 * time.Minute
	}
	return d
}

// quota429Code 从上游错误体提取配额类错误码（OpenAI 形状 error.code，大小写不敏感）；非配额类返回空
func quota429Code(body []byte) string {
	var er struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &er) != nil || er.Error.Code == "" {
		return ""
	}
	code := strings.ToLower(strings.TrimSpace(er.Error.Code))
	if _, ok := quota429Codes[code]; ok {
		return er.Error.Code
	}
	return ""
}

// coolKeys 429 后设置冷却：key 粒度只冷却当前 Key；channel 粒度把候选中同渠道的全部
// Key 一起冷却——主流厂商（智谱等）限额按账户不按 Key，同账户其余 Key 立刻重试只会
// 再吃一次 429（实测 3 Key 池逐个试错浪费 147 次探测）。
func (h *Handler) coolKeys(cands []Candidate, cur Candidate, d time.Duration) {
	h.Coord.SetCooldown(cur.KeyScope(), d)
	n := 1
	if h.KeyCooldownScope == "channel" {
		for _, c := range cands {
			if c.ChannelID == cur.ChannelID && c.KeyID != cur.KeyID {
				h.Coord.SetCooldown(c.KeyScope(), d)
				n++
			}
		}
	}
	if h.Metrics != nil {
		for i := 0; i < n; i++ {
			h.Metrics.KeyCooldown.Inc()
		}
	}
	if n > 1 {
		slog.Info("429 触发渠道级冷却（厂商限额按账户）",
			"channel_id", cur.ChannelID, "channel", cur.ChannelName, "keys_cooled", n, "cooldown", d.String())
	}
}

// disableKey 401/403 后禁用 Key：池内 Key 异步置 status=0（可在 Key 池管理手动恢复）；
// legacy 单 Key 用长效冷却代替禁用（到期自动恢复，避免把渠道一刀切死）
func (h *Handler) disableKey(cand Candidate, status int) {
	if cand.KeyID == 0 {
		d := longKeyCooldown(h.KeyCooldown)
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

// applyUsage 把 usage 折算为成本快照（售卖价扣客户 + 成本价记厂商成本）；上游未回 usage 则标记 no_usage、不计费。
// tokens 来自不可信的上游响应体：负值/超限一律钳制后再入库，防统计被污染与计费回绕。
// 输入 tokens 按缓存命中/未命中分段计价（缓存价 0=同输入价，未配置的模型计费不变）。
func applyUsage(rec *model.UsageLog, u *Usage, m model.Model) {
	if u == nil {
		rec.NoUsage = 1
		return
	}
	rec.PromptTokens = clampTokens(u.PromptTokens)
	rec.CompletionTokens = clampTokens(u.CompletionTokens)
	rec.CachedTokens = clampTokens(u.CachedTokens())
	if rec.CachedTokens > rec.PromptTokens { // 命中是 prompt_tokens 的一部分，脏数据不放大账单
		rec.CachedTokens = rec.PromptTokens
	}
	rec.InputPrice = m.InputPrice
	rec.OutputPrice = m.OutputPrice
	rec.InputCacheHitPrice = m.InputCacheHitPrice
	rec.CostInputPrice = m.CostInputPrice
	rec.CostOutputPrice = m.CostOutputPrice
	rec.CostInputCacheHitPrice = m.CostInputCacheHitPrice
	rec.Cost = CalcCostCached(rec.PromptTokens, rec.CachedTokens, rec.CompletionTokens, m.InputPrice, m.InputCacheHitPrice, m.OutputPrice)
	rec.VendorCost = CalcCostCached(rec.PromptTokens, rec.CachedTokens, rec.CompletionTokens, m.CostInputPrice, m.CostInputCacheHitPrice, m.CostOutputPrice)
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
