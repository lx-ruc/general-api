package gateway

// Anthropic Messages 协议适配（POST /v1/messages）。
// 网关编排内核统一说 OpenAI 格式，这里只做边界的两个方向翻译：
//   入站：Anthropic 请求体 → OpenAI chat/completions bodyMap
//   出站：OpenAI 响应（含 SSE 逐块与错误形状）→ Anthropic 形状
// Claude Code 等 Anthropic 系客户端由此直连本站，无需本地再架协议路由器。
// 授权 / 额度 / 渠道选择 / 模型映射 / 计费全部继承 relay 编排，零特殊分支。

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// ---------------- 错误形状 ----------------

// anthropicError Anthropic 错误形状（Claude 系客户端只认 {"type":"error","error":{...}}）
func anthropicError(c *gin.Context, status int, errType, msg string) {
	c.JSON(status, gin.H{
		"type":  "error",
		"error": gin.H{"type": errType, "message": msg},
	})
}

// anthropicErrorBody 上游 OpenAI 形状错误体 → Anthropic 错误体（透传前包装）
func anthropicErrorBody(msg string) []byte {
	b, _ := json.Marshal(map[string]any{
		"type":  "error",
		"error": map[string]any{"type": "api_error", "message": msg},
	})
	return b
}

// ---------------- 入站：Anthropic 请求 → OpenAI bodyMap ----------------

type anthImageSource struct {
	Type      string `json:"type"` // base64 | url
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
	URL       string `json:"url"`
}

type anthBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
	// tool_use
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
	// tool_result
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"` // string | []block
	// image
	Source *anthImageSource `json:"source"`
}

type anthMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"` // string | []block
}

type anthTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type anthToolChoice struct {
	Type string `json:"type"` // auto | any | tool | none
	Name string `json:"name"`
}

type anthRequest struct {
	Model         string          `json:"model"`
	MaxTokens     int64           `json:"max_tokens"`
	System        json.RawMessage `json:"system"` // string | []{type,text}
	Messages      []anthMessage   `json:"messages"`
	StopSequences []string        `json:"stop_sequences"`
	Temperature   *float64        `json:"temperature"`
	TopP          *float64        `json:"top_p"`
	Stream        bool            `json:"stream"`
	Tools         []anthTool      `json:"tools"`
	ToolChoice    *anthToolChoice `json:"tool_choice"`
}

// anthSystemText system 字段两种形态（纯字符串 / 文本块数组）统一取文本
func anthSystemText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		var parts []string
		for _, b := range blocks {
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// anthImagePart Anthropic image 块 → OpenAI image_url 部件（data URL 直构）
func anthImagePart(src *anthImageSource) map[string]any {
	if src == nil {
		return nil
	}
	switch {
	case src.Type == "base64" && src.Data != "":
		return map[string]any{"type": "image_url", "image_url": map[string]any{
			"url": "data:" + src.MediaType + ";base64," + src.Data}}
	case src.URL != "":
		return map[string]any{"type": "image_url", "image_url": map[string]any{"url": src.URL}}
	}
	return nil
}

// anthToolResultContent tool_result 的 content（string | 块数组）→ OpenAI tool 消息 content
func anthToolResultContent(raw json.RawMessage) any {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []anthBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var parts []map[string]any
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text != "" {
				parts = append(parts, map[string]any{"type": "text", "text": b.Text})
			}
		case "image":
			if p := anthImagePart(b.Source); p != nil {
				parts = append(parts, p)
			}
		}
	}
	if len(parts) == 0 {
		return ""
	}
	if len(parts) == 1 && parts[0]["type"] == "text" {
		return parts[0]["text"]
	}
	return parts
}

// decodeAnthropicRequest Anthropic Messages 请求 → OpenAI chat/completions bodyMap。
// 只保留 OpenAI 侧有对应语义的字段；thinking / metadata / top_k 等丢弃
func decodeAnthropicRequest(raw []byte) (map[string]json.RawMessage, error) {
	var req anthRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("invalid JSON body")
	}
	if req.Model == "" {
		return nil, fmt.Errorf("missing required parameter: model")
	}
	if len(req.Messages) == 0 {
		return nil, fmt.Errorf("missing required parameter: messages")
	}

	var oaiMsgs []map[string]any
	if sys := anthSystemText(req.System); sys != "" {
		oaiMsgs = append(oaiMsgs, map[string]any{"role": "system", "content": sys})
	}
	for _, m := range req.Messages {
		// content 为纯字符串：直接同构
		var s string
		if json.Unmarshal(m.Content, &s) == nil {
			oaiMsgs = append(oaiMsgs, map[string]any{"role": m.Role, "content": s})
			continue
		}
		var blocks []anthBlock
		if err := json.Unmarshal(m.Content, &blocks); err != nil {
			return nil, fmt.Errorf("invalid content in %s message", m.Role)
		}
		var texts []string
		var images []map[string]any
		var toolCalls []map[string]any
		var toolResults []map[string]any
		for _, b := range blocks {
			switch b.Type {
			case "text":
				if b.Text != "" {
					texts = append(texts, b.Text)
				}
			case "image":
				if p := anthImagePart(b.Source); p != nil {
					images = append(images, p)
				}
			case "tool_use":
				args := b.Input
				if len(strings.TrimSpace(string(args))) == 0 {
					args = json.RawMessage(`{}`)
				}
				toolCalls = append(toolCalls, map[string]any{
					"id":   b.ID,
					"type": "function",
					"function": map[string]any{
						"name":      b.Name,
						"arguments": string(args),
					},
				})
			case "tool_result":
				toolResults = append(toolResults, map[string]any{
					"role":         "tool",
					"tool_call_id": b.ToolUseID,
					"content":      anthToolResultContent(b.Content),
				})
			}
			// thinking / redacted_thinking 等无 OpenAI 对应语义的块：丢弃
		}
		if m.Role == "assistant" {
			msg := map[string]any{"role": "assistant"}
			if t := strings.Join(texts, "\n"); t != "" {
				msg["content"] = t
			} else if len(toolCalls) == 0 {
				msg["content"] = ""
			} else {
				msg["content"] = nil // 纯工具调用：OpenAI 允许 content=null
			}
			if len(toolCalls) > 0 {
				msg["tool_calls"] = toolCalls
			}
			oaiMsgs = append(oaiMsgs, msg)
			continue
		}
		// user：tool_result 块 → 独立 tool 消息（跟在含 tool_use 的 assistant 之后，顺序天然正确）
		oaiMsgs = append(oaiMsgs, toolResults...)
		switch {
		case len(images) > 0:
			parts := make([]map[string]any, 0, len(texts)+len(images))
			for _, t := range texts {
				parts = append(parts, map[string]any{"type": "text", "text": t})
			}
			parts = append(parts, images...)
			oaiMsgs = append(oaiMsgs, map[string]any{"role": "user", "content": parts})
		case len(texts) > 0:
			oaiMsgs = append(oaiMsgs, map[string]any{"role": "user", "content": strings.Join(texts, "\n")})
		}
	}

	out := map[string]any{"model": req.Model, "messages": oaiMsgs}
	if req.MaxTokens > 0 {
		out["max_tokens"] = req.MaxTokens
	}
	if len(req.StopSequences) > 0 {
		out["stop"] = req.StopSequences
	}
	if req.Temperature != nil {
		out["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		out["top_p"] = *req.TopP
	}
	if req.Stream {
		out["stream"] = true
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			schema := t.InputSchema
			if len(strings.TrimSpace(string(schema))) == 0 {
				schema = json.RawMessage(`{"type":"object"}`)
			}
			tools = append(tools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        t.Name,
					"description": t.Description,
					"parameters":  schema,
				},
			})
		}
		out["tools"] = tools
	}
	switch {
	case req.ToolChoice == nil: // 未指定：保持默认
	case req.ToolChoice.Type == "auto":
		out["tool_choice"] = "auto"
	case req.ToolChoice.Type == "any":
		out["tool_choice"] = "required"
	case req.ToolChoice.Type == "none":
		out["tool_choice"] = "none"
	case req.ToolChoice.Type == "tool":
		out["tool_choice"] = map[string]any{
			"type": "function", "function": map[string]any{"name": req.ToolChoice.Name}}
	}

	bm := make(map[string]json.RawMessage, len(out))
	for k, v := range out {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		bm[k] = b
	}
	return bm, nil
}

// ---------------- 出站：OpenAI 响应 → Anthropic（非流式）----------------

type oaiToolCall struct {
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type oaiResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content   json.RawMessage `json:"content"` // string | null
			ToolCalls []oaiToolCall   `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *Usage `json:"usage"`
}

// anthStopReason finish_reason → stop_reason
func anthStopReason(finish string) string {
	switch finish {
	case "tool_calls", "function_call":
		return "tool_use"
	case "length":
		return "max_tokens"
	case "content_filter":
		return "refusal"
	default: // "stop" / ""
		return "end_turn"
	}
}

// anthUsage OpenAI usage → Anthropic usage（缓存命中 tokens 映射到 cache_read）
func anthUsage(u *Usage) map[string]any {
	if u == nil {
		return map[string]any{
			"input_tokens": 0, "output_tokens": 0,
			"cache_creation_input_tokens": 0, "cache_read_input_tokens": 0,
		}
	}
	cached := u.CachedTokens()
	if cached > u.PromptTokens {
		cached = u.PromptTokens
	}
	return map[string]any{
		"input_tokens": u.PromptTokens, "output_tokens": u.CompletionTokens,
		"cache_creation_input_tokens": 0, "cache_read_input_tokens": cached,
	}
}

// toolArgsJSON 工具参数 JSON 规范化：空 → {}；非法 JSON → 包一层 _raw（Anthropic 要求 input 必须是对象）
func toolArgsJSON(args string) json.RawMessage {
	s := strings.TrimSpace(args)
	if s == "" {
		return json.RawMessage(`{}`)
	}
	var probe map[string]any
	if json.Unmarshal([]byte(s), &probe) == nil {
		return json.RawMessage(s)
	}
	// 宽容处理裸字符串/数组等合法 JSON 标量之外的形态
	var scalar any
	if json.Unmarshal([]byte(s), &scalar) == nil {
		b, _ := json.Marshal(map[string]any{"_raw": scalar})
		return b
	}
	return json.RawMessage(`{}`)
}

// encodeAnthropicResponse OpenAI chat/completions 响应 → Anthropic message 响应；
// 同时返回抽出的 usage（计费用，调用方不再重复解析）
func encodeAnthropicResponse(data []byte, fallbackModel string) ([]byte, *Usage, error) {
	var r oaiResponse
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, nil, err
	}
	model := r.Model
	if model == "" {
		model = fallbackModel
	}
	id := r.ID
	if id == "" {
		id = "msg_gateway"
	}
	content := []map[string]any{}
	if len(r.Choices) > 0 {
		msg := r.Choices[0].Message
		var text string
		if len(msg.Content) > 0 {
			_ = json.Unmarshal(msg.Content, &text)
		}
		if text != "" {
			content = append(content, map[string]any{"type": "text", "text": text})
		}
		for _, tc := range msg.ToolCalls {
			content = append(content, map[string]any{
				"type":  "tool_use",
				"id":    tc.ID,
				"name":  tc.Function.Name,
				"input": toolArgsJSON(tc.Function.Arguments),
			})
		}
	}
	stop := "end_turn"
	if len(r.Choices) > 0 {
		stop = anthStopReason(r.Choices[0].FinishReason)
	}
	resp := map[string]any{
		"id":            id,
		"type":          "message",
		"role":          "assistant",
		"model":         model,
		"content":       content,
		"stop_reason":   stop,
		"stop_sequence": nil,
		"usage":         anthUsage(r.Usage),
	}
	b, err := json.Marshal(resp)
	if err != nil {
		return nil, nil, err
	}
	return b, r.Usage, nil
}

// anthropicUsageFrom 从 Anthropic 形状响应体抽 usage（精确缓存回放路径计费用）
func anthropicUsageFrom(data []byte) *Usage {
	var r struct {
		Usage *struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
			CacheRead    int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(data, &r) != nil || r.Usage == nil {
		return nil
	}
	return &Usage{
		PromptTokens:         r.Usage.InputTokens,
		CompletionTokens:     r.Usage.OutputTokens,
		PromptCacheHitTokens: r.Usage.CacheRead,
	}
}

// ---------------- 出站：OpenAI SSE → Anthropic SSE ----------------

type oaiChunkToolCall struct {
	Index    *int  `json:"index"` // 部分厂商首块不带 index
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type oaiChunk struct {
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content   string             `json:"content"`
			ToolCalls []oaiChunkToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *Usage `json:"usage"`
}

// anthStreamTranslator OpenAI SSE 块流 → Anthropic 事件流的有状态翻译器。
// 事件序列：message_start → (content_block_start / content_block_delta / content_block_stop)*
// → message_delta(stop_reason+usage) → message_stop
type anthStreamTranslator struct {
	w       io.Writer
	flusher http.Flusher
	model   string

	started bool
	openIdx int    // 当前打开块下标；-1=无
	openKind string // "text" | "tool"
	nextIdx int
	toolMap map[int]int // OpenAI tool_calls index → Anthropic 块下标
	sawTool bool        // 本流是否出现过 tool_use 块（漏发 finish_reason 时兜底 stop_reason 用）

	usage *Usage
	stop  string
}

func (t *anthStreamTranslator) emit(event string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(t.w, "event: %s\ndata: %s\n\n", event, b); err != nil {
		return err
	}
	if t.flusher != nil {
		t.flusher.Flush()
	}
	return nil
}

func (t *anthStreamTranslator) ensureStart() error {
	if t.started {
		return nil
	}
	t.started = true
	return t.emit("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": "msg_gateway", "type": "message", "role": "assistant",
			"model": t.model, "content": []any{},
			"stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]any{"input_tokens": 0, "output_tokens": 0},
		},
	})
}

func (t *anthStreamTranslator) closeBlock() error {
	if t.openIdx < 0 {
		return nil
	}
	idx := t.openIdx
	t.openIdx, t.openKind = -1, ""
	return t.emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": idx})
}

func (t *anthStreamTranslator) openText() error {
	if err := t.ensureStart(); err != nil {
		return err
	}
	if t.openKind == "text" {
		return nil
	}
	if err := t.closeBlock(); err != nil {
		return err
	}
	t.openIdx, t.openKind, t.nextIdx = t.nextIdx, "text", t.nextIdx+1
	return t.emit("content_block_start", map[string]any{
		"type": "content_block_start", "index": t.openIdx,
		"content_block": map[string]any{"type": "text", "text": ""},
	})
}

func (t *anthStreamTranslator) openTool(id, name string) (int, error) {
	if err := t.ensureStart(); err != nil {
		return 0, err
	}
	if err := t.closeBlock(); err != nil {
		return 0, err
	}
	if id == "" {
		id = fmt.Sprintf("toolu_%02d", t.nextIdx)
	}
	t.openIdx, t.openKind, t.nextIdx = t.nextIdx, "tool", t.nextIdx+1
	t.sawTool = true
	err := t.emit("content_block_start", map[string]any{
		"type": "content_block_start", "index": t.openIdx,
		"content_block": map[string]any{"type": "tool_use", "id": id, "name": name, "input": map[string]any{}},
	})
	return t.openIdx, err
}

func (t *anthStreamTranslator) handle(chunk oaiChunk) error {
	if chunk.Model != "" {
		t.model = chunk.Model
	}
	if chunk.Usage != nil {
		t.usage = chunk.Usage
	}
	for _, ch := range chunk.Choices {
		if ch.FinishReason != "" {
			t.stop = anthStopReason(ch.FinishReason)
		}
		if ch.Delta.Content != "" {
			if err := t.openText(); err != nil {
				return err
			}
			if err := t.emit("content_block_delta", map[string]any{
				"type": "content_block_delta", "index": t.openIdx,
				"delta": map[string]any{"type": "text_delta", "text": ch.Delta.Content},
			}); err != nil {
				return err
			}
		}
		for _, tc := range ch.Delta.ToolCalls {
			var idx int
			// 续流判定：index 已映射且本块不带 name。部分厂商每个分片都重复携带 id，
			// 凭 id 判「新调用」会把同一次调用拆成多个块、参数全碎，故续流只看 index+name
			cont := false
			if tc.Index != nil {
				if m, ok := t.toolMap[*tc.Index]; ok && tc.Function.Name == "" {
					idx, cont = m, true
				}
			}
			if !cont {
				switch {
				case tc.ID != "" || tc.Function.Name != "": // 新工具调用首块
					mapped, err := t.openTool(tc.ID, tc.Function.Name)
					if err != nil {
						return err
					}
					idx = mapped
					if tc.Index != nil {
						t.toolMap[*tc.Index] = mapped
					}
				case tc.Index != nil:
					if m, ok := t.toolMap[*tc.Index]; ok {
						idx = m
						if t.openIdx != m { // 参数块先于首块到达的脏流：补开块
							if _, err := t.openTool("", ""); err != nil {
								return err
							}
							t.toolMap[*tc.Index] = t.openIdx
							idx = t.openIdx
						}
					} else {
						mapped, err := t.openTool("", "")
						if err != nil {
							return err
						}
						t.toolMap[*tc.Index] = mapped
						idx = mapped
					}
				default: // 既无首块也无 index：兜底新开
					mapped, err := t.openTool("", "")
					if err != nil {
						return err
					}
					idx = mapped
				}
			}
			// 空白分片必须原样透传：TrimSpace 会吞掉 JSON 字符串值内被拆分的空白
			// （如 "ls   -la" 拆成 "ls" + "   " + "-la"），拼回时参数已被改写
			if tc.Function.Arguments != "" {
				if err := t.emit("content_block_delta", map[string]any{
					"type": "content_block_delta", "index": idx,
					"delta": map[string]any{"type": "input_json_delta", "partial_json": tc.Function.Arguments},
				}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// finish 补齐尾部事件（干净 EOF 时调用；[DONE] 与无标记结束统一走这里）
func (t *anthStreamTranslator) finish() error {
	if err := t.ensureStart(); err != nil {
		return err
	}
	if err := t.closeBlock(); err != nil {
		return err
	}
	stop := t.stop
	if stop == "" {
		// 脏上游漏发 finish_reason：出过 tool_use 块按 tool_use 收尾，否则 Claude Code
		// 视为回合结束不执行工具；纯文本流维持 end_turn
		if t.sawTool {
			stop = "tool_use"
		} else {
			stop = "end_turn"
		}
	}
	usage := map[string]any{"output_tokens": 0}
	if t.usage != nil {
		usage = map[string]any{
			"input_tokens":  t.usage.PromptTokens,
			"output_tokens": t.usage.CompletionTokens,
		}
	}
	if err := t.emit("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": stop, "stop_sequence": nil},
		"usage": usage,
	}); err != nil {
		return err
	}
	return t.emit("message_stop", map[string]any{"type": "message_stop"})
}

// fail 以 error 事件终结事件流（Anthropic 形状，客户端可解析出错误而非悬空到断连），
// 随后返回原始错误。无条件补发：流式路径 WriteHeader(200) 在翻译开始前已发出，
// 即使尚无任何事件，不发终态客户端也会悬空到断连
func (t *anthStreamTranslator) fail(err error) error {
	_ = t.emit("error", map[string]any{
		"type":  "error",
		"error": map[string]any{"type": "api_error", "message": err.Error()},
	})
	return err
}

// anthropicPipeSSE OpenAI SSE 上游流 → Anthropic SSE 客户端流（逐块翻译即时写出）；
// 返回值口径与 pipeSSE 一致（usage 供计费、err 记渠道健康）
func anthropicPipeSSE(w io.Writer, ctx context.Context, body io.Reader, modelSwap [2]string) (*Usage, error) {
	flusher, _ := w.(http.Flusher)
	reader := bufio.NewReaderSize(body, 32*1024)
	tr := &anthStreamTranslator{w: w, flusher: flusher, openIdx: -1, toolMap: map[int]int{}}
	if modelSwap[1] != "" {
		tr.model = modelSwap[1] // message_start 锚外部名（映射对客户不可见）
	}
	for {
		if ctx.Err() != nil {
			return tr.usage, ctx.Err()
		}
		line, rerr := reader.ReadString('\n')
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "data:") {
			payload := strings.TrimSpace(strings.TrimPrefix(t, "data:"))
			if payload != "" && payload != "[DONE]" {
				var chunk oaiChunk
				if err := json.Unmarshal([]byte(payload), &chunk); err == nil {
					if modelSwap[0] != "" && chunk.Model == modelSwap[0] {
						chunk.Model = modelSwap[1]
					}
					if herr := tr.handle(chunk); herr != nil {
						return tr.usage, tr.fail(herr)
					}
				}
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				return tr.usage, tr.finish()
			}
			return tr.usage, tr.fail(rerr)
		}
	}
}
