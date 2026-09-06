package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"token-gateway/internal/config"
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
}

func NewHandler(db *gorm.DB, cipher *crypto.Cipher, cfg *config.Config,
	limiter *middleware.RateLimiter, m *metrics.Metrics) *Handler {
	return &Handler{
		DB:      db,
		Cipher:  cipher,
		Client:  NewHTTPClient(cfg.Gateway.UpstreamFirstByteTimeout.Duration),
		MaxBody: int64(cfg.Gateway.MaxBodyMB) << 20,
		Limiter: limiter,
		Breaker: NewBreaker(cfg.Gateway.ChannelBreakerThreshold),
		Metrics: m,
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
		RequestID: middleware.GetRequestID(c),
		OrgID:     ki.OrgID,
		UserID:    ki.UserID,
		APIKeyID:  ki.KeyID,
		ClientIP:  c.ClientIP(),
	}
	defer func() {
		rec.LatencyMs = time.Since(start).Milliseconds()
		if h.Metrics != nil {
			h.Metrics.Requests.With(strconv.Itoa(rec.Status)).Inc()
			h.Metrics.Latency.Observe(time.Since(start).Seconds())
		}
		if err := service.Settle(h.DB, rec); err != nil {
			if h.Metrics != nil {
				h.Metrics.SettleErrors.Inc()
			}
			c.Error(err) //nolint: 仅记录，不影响已发出的响应
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

	// 模型授权（员工白名单）
	var cnt int64
	if err := h.DB.Model(&model.UserModelGrant{}).
		Where("user_id = ? AND model_name = ?", ki.UserID, modelName).Count(&cnt).Error; err != nil || cnt == 0 {
		rec.Status, rec.Error = http.StatusForbidden, "model not allowed: "+modelName
		openaiError(c, http.StatusForbidden, "model_not_allowed",
			fmt.Sprintf("you are not allowed to use model %q, please contact your company admin", modelName))
		return
	}

	// 额度预检查（读）；真实扣减在响应结束后的结算事务里
	if err := service.Precheck(h.DB, ki.UserID); err != nil {
		if errors.Is(err, service.ErrUserQuota) || errors.Is(err, service.ErrOrgQuota) {
			rec.Status, rec.Error = http.StatusTooManyRequests, err.Error()
			openaiError(c, http.StatusTooManyRequests, "insufficient_quota", err.Error())
			return
		}
		rec.Status, rec.Error = http.StatusInternalServerError, err.Error()
		openaiError(c, http.StatusInternalServerError, "internal_error", "quota precheck failed")
		return
	}

	// 渠道候选
	cands, err := SelectCandidates(h.DB, h.Cipher, modelName)
	if err != nil || len(cands) == 0 {
		rec.Status, rec.Error = http.StatusServiceUnavailable, "no available channel"
		openaiError(c, http.StatusServiceUnavailable, "service_unavailable",
			"no available upstream channel for model "+modelName)
		return
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

	var lastErr string
	for i, cand := range cands {
		if c.Request.Context().Err() != nil {
			rec.Status, rec.Error = 499, "client disconnected"
			return
		}
		bm := bodyMap
		if cand.UpstreamModel != "" && cand.UpstreamModel != modelName {
			bm = cloneMap(bodyMap)
			bm["model"] = json.RawMessage(strconv.Quote(cand.UpstreamModel))
		}
		upBody, merr := json.Marshal(bm)
		if merr != nil {
			lastErr = merr.Error()
			continue
		}
		req, rerr := http.NewRequestWithContext(c.Request.Context(), http.MethodPost,
			cand.URL(), bytes.NewReader(upBody))
		if rerr != nil {
			lastErr = rerr.Error()
			continue
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
			h.noteChannelFailure(cand)
			continue // 网络失败 → 换下一个渠道（尚未向客户端写出任何字节）
		}

		// 上游 5xx 且还有备选 → 重试下一个渠道
		if resp.StatusCode >= 500 && i < len(cands)-1 {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
			_ = resp.Body.Close()
			lastErr = fmt.Sprintf("upstream %s returned %d", cand.ChannelName, resp.StatusCode)
			h.noteChannelFailure(cand)
			continue
		}

		// 确定由该渠道应答：2xx 视为成功（熔断计数清零）
		if resp.StatusCode < 500 {
			h.Breaker.RecordSuccess(cand.ChannelID)
		}

		rec.ChannelID = &cand.ChannelID
		rec.Status = resp.StatusCode
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
			return
		}

		// 非流式：读完上游再透传（上限 20MB）
		data, rerr2 := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
		_ = resp.Body.Close()
		if rerr2 != nil {
			lastErr = rerr2.Error()
			continue
		}
		if ct == "" {
			ct = "application/json"
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
			return
		}
		var ur struct {
			Usage *Usage `json:"usage"`
		}
		_ = json.Unmarshal(data, &ur)
		applyUsage(rec, ur.Usage, m)
		return
	}

	rec.Status, rec.Error = http.StatusBadGateway, "all channels failed: "+lastErr
	openaiError(c, http.StatusBadGateway, "upstream_error",
		"all upstream channels failed: "+truncateStr(lastErr, 200))
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

// applyUsage 把 usage 折算为成本快照；上游未回 usage 则标记 no_usage、不计费
func applyUsage(rec *model.UsageLog, u *Usage, m model.Model) {
	if u == nil {
		rec.NoUsage = 1
		return
	}
	rec.PromptTokens = u.PromptTokens
	rec.CompletionTokens = u.CompletionTokens
	rec.InputPrice = m.InputPrice
	rec.OutputPrice = m.OutputPrice
	rec.Cost = CalcCost(u.PromptTokens, u.CompletionTokens, m.InputPrice, m.OutputPrice)
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
