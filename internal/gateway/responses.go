package gateway

// OpenAI Responses 协议适配（POST /v1/responses）。
// Codex CLI 0.142 起自定义 provider 只接受 wire_api="responses"（chat 已移除，
// 见 openai/codex#7782），本端点让 Codex 系客户端直连本站。
// 网关编排内核统一说 OpenAI chat/completions，这里只做边界双向翻译：
//   入站：Responses 请求体（input 数组 / 扁平 tools / instructions）→ OpenAI bodyMap
//   出站：OpenAI 响应（非流式对象 + SSE 逐块）→ Responses 对象与事件流
// 授权 / 额度 / 渠道选择 / 模型映射 / 计费全部继承 relay 编排，零特殊分支。

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ---------------- 入站：Responses 请求 → OpenAI bodyMap ----------------

// respPart Responses content part：input_text / output_text / summary_text / input_image
type respPart struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	ImageURL string `json:"image_url"` // input_image：data URL 或远程地址
}

// respItem Responses input 数组元素
type respItem struct {
	Type string `json:"type"` // message | function_call | function_call_output | reasoning
	// message
	Role    string          `json:"role"` // user | assistant | system | developer
	Content json.RawMessage `json:"content"`
	// function_call
	Name      string `json:"name"`
	CallID    string `json:"call_id"`
	Arguments string `json:"arguments"`
	// function_call_output
	Output json.RawMessage `json:"output"` // string | []part
}

// respTool Responses 扁平函数定义（type/name/parameters 同级）→ OpenAI 嵌套形态
type respTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type respRequest struct {
	Model              string          `json:"model"`
	Instructions       string          `json:"instructions"`
	Input              json.RawMessage `json:"input"` // string | []item
	Tools              []respTool      `json:"tools"`
	ToolChoice         json.RawMessage `json:"tool_choice"`
	Temperature        *float64        `json:"temperature"`
	TopP               *float64        `json:"top_p"`
	MaxOutputTokens    *int64          `json:"max_output_tokens"`
	ParallelToolCalls  *bool           `json:"parallel_tool_calls"`
	Stream             bool            `json:"stream"`
}

// respOutputText function_call_output 的 output（string | part 数组）统一取文本
func respOutputText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []respPart
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var texts []string
	for _, p := range parts {
		if p.Text != "" {
			texts = append(texts, p.Text)
		}
	}
	return strings.Join(texts, "\n")
}

// respMessageContent message 的 content（string | part 数组）→ OpenAI content：
// 纯文本合并为字符串；含图片则输出 text+image_url 部件数组
func respMessageContent(raw json.RawMessage) any {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []respPart
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var texts []string
	var images []map[string]any
	for _, p := range parts {
		switch p.Type {
		case "input_text", "output_text", "summary_text", "refusal":
			if p.Text != "" {
				texts = append(texts, p.Text)
			}
		case "input_image":
			if p.ImageURL != "" {
				images = append(images, map[string]any{
					"type": "image_url", "image_url": map[string]any{"url": p.ImageURL}})
			}
		}
	}
	if len(images) == 0 {
		return strings.Join(texts, "\n")
	}
	out := make([]map[string]any, 0, len(texts)+len(images))
	for _, t := range texts {
		out = append(out, map[string]any{"type": "text", "text": t})
	}
	out = append(out, images...)
	return out
}

// decodeResponsesRequest Responses 请求 → OpenAI chat/completions bodyMap。
// reasoning / store / include / metadata / previous_response_id 等无 OpenAI
// 对应语义或无状态语义的字段丢弃（codex 每轮全量回放 input，不依赖服务端会话）
func decodeResponsesRequest(raw []byte) (map[string]json.RawMessage, error) {
	var req respRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("invalid JSON body")
	}
	if req.Model == "" {
		return nil, fmt.Errorf("missing required parameter: model")
	}
	if len(req.Input) == 0 {
		return nil, fmt.Errorf("missing required parameter: input")
	}

	var oaiMsgs []map[string]any
	if req.Instructions != "" {
		oaiMsgs = append(oaiMsgs, map[string]any{"role": "system", "content": req.Instructions})
	}

	// input 字符串形态：单条 user 消息
	var inputStr string
	if json.Unmarshal(req.Input, &inputStr) == nil && strings.TrimSpace(inputStr) != "" {
		oaiMsgs = append(oaiMsgs, map[string]any{"role": "user", "content": inputStr})
	} else {
		var items []respItem
		if err := json.Unmarshal(req.Input, &items); err != nil {
			return nil, fmt.Errorf("invalid input: expected string or array of items")
		}
		// 连续 function_call 合并进同一条 assistant（OpenAI 规范形态：并行工具调用同消息）
		var pendingCalls []map[string]any
		flushCalls := func() {
			if len(pendingCalls) > 0 {
				oaiMsgs = append(oaiMsgs, map[string]any{
					"role": "assistant", "content": nil, "tool_calls": pendingCalls})
				pendingCalls = nil
			}
		}
		for _, it := range items {
			switch it.Type {
			case "message":
				flushCalls()
				role := it.Role
				if role == "developer" {
					role = "system" // OpenAI 对 developer 的等价槽位
				}
				if role == "" {
					role = "user"
				}
				oaiMsgs = append(oaiMsgs, map[string]any{
					"role": role, "content": respMessageContent(it.Content)})
			case "function_call":
				args := strings.TrimSpace(it.Arguments)
				if args == "" {
					args = "{}"
				}
				pendingCalls = append(pendingCalls, map[string]any{
					"id":   it.CallID,
					"type": "function",
					"function": map[string]any{
						"name": it.Name, "arguments": args},
				})
			case "function_call_output":
				flushCalls()
				oaiMsgs = append(oaiMsgs, map[string]any{
					"role": "tool", "tool_call_id": it.CallID,
					"content": respOutputText(it.Output)})
			// reasoning / local_shell_call 等无 chat 对应语义：丢弃
			}
		}
		flushCalls()
	}
	if len(oaiMsgs) == 0 {
		return nil, fmt.Errorf("input contains no usable items")
	}

	out := map[string]any{"model": req.Model, "messages": oaiMsgs}
	if req.MaxOutputTokens != nil && *req.MaxOutputTokens > 0 {
		out["max_tokens"] = *req.MaxOutputTokens
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
	if req.ParallelToolCalls != nil {
		out["parallel_tool_calls"] = *req.ParallelToolCalls
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			if t.Type != "function" && t.Type != "" { // local_shell 等无 chat 语义
				continue
			}
			schema := t.Parameters
			if len(strings.TrimSpace(string(schema))) == 0 {
				schema = json.RawMessage(`{"type":"object"}`)
			}
			tools = append(tools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name": t.Name, "description": t.Description, "parameters": schema,
				},
			})
		}
		if len(tools) > 0 {
			out["tools"] = tools
		}
	}
	if len(req.ToolChoice) > 0 {
		var choice string
		if json.Unmarshal(req.ToolChoice, &choice) == nil && choice != "" {
			out["tool_choice"] = choice // auto / none / required 同名透传
		} else {
			var obj struct {
				Type string `json:"type"`
				Name string `json:"name"`
			}
			if json.Unmarshal(req.ToolChoice, &obj) == nil && obj.Type == "function" && obj.Name != "" {
				out["tool_choice"] = map[string]any{
					"type": "function", "function": map[string]any{"name": obj.Name}}
			}
		}
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

// ---------------- 出站：OpenAI 响应 → Responses（非流式）----------------

// toolArgsString 工具调用参数 → Responses 协议的字符串形态。function_call.arguments
// 是「JSON 编码后的字符串」——codex 的 FunctionCall.arguments 为 String 类型，发对象
// 会让 output_item.done 反序列化失败、整个条目被客户端静默丢弃（表现为界面无下文）；
// Anthropic 端点的 tool_use.input 才是对象（toolArgsJSON），两者形态不同勿混用
func toolArgsString(args string) string {
	return string(toolArgsJSON(args))
}

// respUsage OpenAI usage → Responses usage 形状。
// input_tokens_details 用 Responses 官方命名 cached_tokens（codex 0.142 的
// ResponseCompleted 反序列化把该字段视为必填，缺失会导致客户端判流断开重试）
func respUsage(u *Usage) map[string]any {
	if u == nil {
		return map[string]any{
			"input_tokens": 0, "output_tokens": 0, "total_tokens": 0,
			"input_tokens_details":  map[string]any{"cached_tokens": 0},
			"output_tokens_details": map[string]any{"reasoning_tokens": 0},
		}
	}
	cached := u.CachedTokens()
	if cached > u.PromptTokens {
		cached = u.PromptTokens
	}
	return map[string]any{
		"input_tokens": u.PromptTokens, "output_tokens": u.CompletionTokens,
		"total_tokens":  u.PromptTokens + u.CompletionTokens,
		"input_tokens_details":  map[string]any{"cached_tokens": cached},
		"output_tokens_details": map[string]any{"reasoning_tokens": 0},
	}
}

// encodeResponsesResponse OpenAI chat/completions 响应 → Responses 对象；
// 同时返回抽出的 usage（计费用）
func encodeResponsesResponse(data []byte, fallbackModel string) ([]byte, *Usage, error) {
	var r oaiResponse
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, nil, err
	}
	model := r.Model
	if model == "" {
		model = fallbackModel
	}
	id := "resp_gateway"
	if r.ID != "" {
		id = "resp_" + r.ID
	}

	status, incomplete := "completed", map[string]any(nil)
	finish := ""
	if len(r.Choices) > 0 {
		finish = r.Choices[0].FinishReason
	}
	if finish == "length" {
		status = "incomplete"
		incomplete = map[string]any{"reason": "max_output_tokens"}
	}

	output := []map[string]any{}
	if len(r.Choices) > 0 {
		msg := r.Choices[0].Message
		var text string
		if len(msg.Content) > 0 {
			_ = json.Unmarshal(msg.Content, &text)
		}
		if text != "" {
			output = append(output, map[string]any{
				"id": "msg_0", "type": "message", "role": "assistant", "status": "completed",
				"content": []map[string]any{{
					"type": "output_text", "text": text, "annotations": []any{}}},
			})
		}
		for i, tc := range msg.ToolCalls {
			output = append(output, map[string]any{
				"id": fmt.Sprintf("fc_%d", i), "type": "function_call", "status": "completed",
				"name": tc.Function.Name, "call_id": tc.ID,
				"arguments": toolArgsString(tc.Function.Arguments),
			})
		}
	}

	resp := map[string]any{
		"id": id, "object": "response", "created_at": time.Now().Unix(),
		"status": status, "model": model, "output": output,
		"parallel_tool_calls": false, "usage": respUsage(r.Usage),
		"error": nil, "incomplete_details": incomplete,
	}
	b, err := json.Marshal(resp)
	if err != nil {
		return nil, nil, err
	}
	return b, r.Usage, nil
}

// responsesUsageFrom 从 Responses 形状响应体抽 usage（精确缓存回放路径计费用）
func responsesUsageFrom(data []byte) *Usage {
	var r struct {
		Usage *struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
			InputTokensDetails *struct {
				CachedTokens int64 `json:"cached_tokens"`
				CacheRead    int64 `json:"cache_read_input_tokens"`
			} `json:"input_tokens_details"`
		} `json:"usage"`
	}
	if json.Unmarshal(data, &r) != nil || r.Usage == nil {
		return nil
	}
	u := &Usage{
		PromptTokens:     r.Usage.InputTokens,
		CompletionTokens: r.Usage.OutputTokens,
	}
	if d := r.Usage.InputTokensDetails; d != nil {
		u.PromptCacheHitTokens = max(d.CachedTokens, d.CacheRead)
	}
	return u
}

// ---------------- 出站：OpenAI SSE → Responses 事件流 ----------------

// respStreamTranslator OpenAI SSE 块流 → Responses 事件流的有状态翻译器。
// 事件序列：response.created → (output_item.added / *delta / *done)*
// → response.completed（携带最终 output 与 usage）；失败时以 response.failed 终结
type respStreamTranslator struct {
	w       io.Writer
	flusher http.Flusher
	model   string
	created int64
	id      string

	seq     int
	started bool

	openIdx  int    // 当前打开 output 下标；-1=无
	openKind string // "message" | "function_call"
	nextOut  int

	msgID, fcID    string // item id
	callID, fcName string
	textBuf        strings.Builder
	argsBuf        strings.Builder

	output []map[string]any
	usage  *Usage
	status string // "" | incomplete
}

func (t *respStreamTranslator) emit(event string, payload map[string]any) error {
	payload["type"] = event
	payload["sequence_number"] = t.seq
	t.seq++
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

// skeleton 流中事件携带的 response 对象骨架（created / completed / failed 共用）
func (t *respStreamTranslator) skeleton(status string, errObj map[string]any) map[string]any {
	resp := map[string]any{
		"id": t.id, "object": "response", "created_at": t.created,
		"status": status, "model": t.model, "output": t.output,
		"usage": respUsage(t.usage), "error": errObj,
	}
	if t.status == "incomplete" && status == "completed" {
		resp["status"] = "incomplete"
		resp["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
	}
	return resp
}

func (t *respStreamTranslator) ensureStart() error {
	if t.started {
		return nil
	}
	t.started = true
	return t.emit("response.created", map[string]any{"response": t.skeleton("in_progress", nil)})
}

func (t *respStreamTranslator) closeItem() error {
	if t.openIdx < 0 {
		return nil
	}
	idx, kind := t.openIdx, t.openKind
	t.openIdx, t.openKind = -1, ""
	switch kind {
	case "message":
		text := t.textBuf.String()
		part := map[string]any{"type": "output_text", "text": text, "annotations": []any{}}
		if err := t.emit("response.output_text.done", map[string]any{
			"item_id": t.msgID, "output_index": idx, "content_index": 0, "text": text,
		}); err != nil {
			return err
		}
		if err := t.emit("response.content_part.done", map[string]any{
			"item_id": t.msgID, "output_index": idx, "content_index": 0, "part": part,
		}); err != nil {
			return err
		}
		item := map[string]any{
			"id": t.msgID, "type": "message", "role": "assistant", "status": "completed",
			"content": []map[string]any{{
				"type": "output_text", "text": text, "annotations": []any{}}},
		}
		t.output = append(t.output, item)
		return t.emit("response.output_item.done", map[string]any{"output_index": idx, "item": item})
	default: // function_call
		args := toolArgsString(t.argsBuf.String())
		if err := t.emit("response.function_call_arguments.done", map[string]any{
			"item_id": t.fcID, "output_index": idx, "arguments": args,
		}); err != nil {
			return err
		}
		item := map[string]any{
			"id": t.fcID, "type": "function_call", "status": "completed",
			"name": t.fcName, "call_id": t.callID, "arguments": args,
		}
		t.output = append(t.output, item)
		return t.emit("response.output_item.done", map[string]any{"output_index": idx, "item": item})
	}
}

func (t *respStreamTranslator) openMessage() error {
	if err := t.ensureStart(); err != nil {
		return err
	}
	if t.openKind == "message" {
		return nil
	}
	if err := t.closeItem(); err != nil {
		return err
	}
	t.openIdx, t.openKind, t.nextOut = t.nextOut, "message", t.nextOut+1
	t.msgID = fmt.Sprintf("msg_%d", t.openIdx)
	t.textBuf.Reset()
	if err := t.emit("response.output_item.added", map[string]any{
		"output_index": t.openIdx,
		"item": map[string]any{
			"id": t.msgID, "type": "message", "role": "assistant",
			"status": "in_progress", "content": []any{},
		},
	}); err != nil {
		return err
	}
	return t.emit("response.content_part.added", map[string]any{
		"item_id": t.msgID, "output_index": t.openIdx, "content_index": 0,
		"part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}},
	})
}

func (t *respStreamTranslator) openFunctionCall(id, name string) error {
	if err := t.ensureStart(); err != nil {
		return err
	}
	if err := t.closeItem(); err != nil {
		return err
	}
	if id == "" {
		id = fmt.Sprintf("call_%d", t.nextOut)
	}
	t.openIdx, t.openKind, t.nextOut = t.nextOut, "function_call", t.nextOut+1
	t.fcID = fmt.Sprintf("fc_%d", t.openIdx)
	t.callID, t.fcName = id, name
	t.argsBuf.Reset()
	return t.emit("response.output_item.added", map[string]any{
		"output_index": t.openIdx,
		"item": map[string]any{
			"id": t.fcID, "type": "function_call", "status": "in_progress",
			"name": name, "call_id": id, "arguments": "",
		},
	})
}

func (t *respStreamTranslator) handle(chunk oaiChunk) error {
	if chunk.Model != "" {
		t.model = chunk.Model
	}
	if chunk.Usage != nil {
		t.usage = chunk.Usage
	}
	for _, ch := range chunk.Choices {
		if ch.FinishReason == "length" {
			t.status = "incomplete"
		}
		if ch.Delta.Content != "" {
			if err := t.openMessage(); err != nil {
				return err
			}
			t.textBuf.WriteString(ch.Delta.Content)
			if err := t.emit("response.output_text.delta", map[string]any{
				"item_id": t.msgID, "output_index": t.openIdx,
				"content_index": 0, "delta": ch.Delta.Content,
			}); err != nil {
				return err
			}
		}
		for _, tc := range ch.Delta.ToolCalls {
			if tc.ID != "" || tc.Function.Name != "" { // 新工具调用首块
				if err := t.openFunctionCall(tc.ID, tc.Function.Name); err != nil {
					return err
				}
			} else if t.openKind != "function_call" {
				// 参数块先于首块到达的脏流：兜底开块
				if err := t.openFunctionCall("", ""); err != nil {
					return err
				}
			}
			if tc.Function.Arguments != "" {
				t.argsBuf.WriteString(tc.Function.Arguments)
				if err := t.emit("response.function_call_arguments.delta", map[string]any{
					"item_id": t.fcID, "output_index": t.openIdx,
					"delta": tc.Function.Arguments,
				}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// finish 补齐尾部事件（干净 EOF / [DONE] 统一走这里）
func (t *respStreamTranslator) finish() error {
	if err := t.ensureStart(); err != nil {
		return err
	}
	if err := t.closeItem(); err != nil {
		return err
	}
	return t.emit("response.completed", map[string]any{"response": t.skeleton("completed", nil)})
}

// fail 以 response.failed 终结事件流（已写出字节无法换渠道，这里保证客户端
// 收到终态事件不悬空），随后返回原始错误
func (t *respStreamTranslator) fail(err error) error {
	if t.started {
		_ = t.emit("response.failed", map[string]any{
			"response": t.skeleton("failed", map[string]any{
				"code": "api_error", "message": err.Error()}),
		})
	}
	return err
}

// responsesPipeSSE OpenAI SSE 上游流 → Responses SSE 客户端流（逐块翻译即时写出）；
// 返回值口径与 pipeSSE 一致（usage 供计费、err 记渠道健康）
func responsesPipeSSE(w io.Writer, ctx context.Context, body io.Reader, modelSwap [2]string) (*Usage, error) {
	flusher, _ := w.(http.Flusher)
	reader := bufio.NewReaderSize(body, 32*1024)
	tr := &respStreamTranslator{
		w: w, flusher: flusher, openIdx: -1,
		created: time.Now().Unix(), id: "resp_gateway",
		output: []map[string]any{},
	}
	if modelSwap[1] != "" {
		tr.model = modelSwap[1] // 事件锚外部名（映射对客户不可见）
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
