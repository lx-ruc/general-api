package gateway

// /v1/messages Anthropic 协议适配测试：两个方向的翻译纯函数 + 全链路（鉴权/计费/SSE）。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------------- 入站翻译 ----------------

func TestDecodeAnthropicBasic(t *testing.T) {
	bm, err := decodeAnthropicRequest([]byte(`{
		"model": "claude-x",
		"max_tokens": 1024,
		"system": "你是个助手",
		"messages": [{"role": "user", "content": "你好"}],
		"temperature": 0.7,
		"stop_sequences": ["END"],
		"stream": false
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(bm["model"]) != `"claude-x"` {
		t.Fatalf("model 翻译错: %s", bm["model"])
	}
	var msgs []map[string]any
	if err := json.Unmarshal(bm["messages"], &msgs); err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0]["role"] != "system" || msgs[0]["content"] != "你是个助手" {
		t.Fatalf("system 消息翻译错: %+v", msgs)
	}
	if msgs[1]["role"] != "user" || msgs[1]["content"] != "你好" {
		t.Fatalf("user 消息翻译错: %+v", msgs)
	}
	if string(bm["max_tokens"]) != "1024" || string(bm["temperature"]) != "0.7" {
		t.Fatalf("采样参数翻译错: %s %s", bm["max_tokens"], bm["temperature"])
	}
	var stop []string
	_ = json.Unmarshal(bm["stop"], &stop)
	if len(stop) != 1 || stop[0] != "END" {
		t.Fatalf("stop_sequences 翻译错: %s", bm["stop"])
	}
	if _, ok := bm["stream"]; ok {
		t.Fatal("非流式不应携带 stream 字段")
	}
}

func TestDecodeAnthropicMissingParams(t *testing.T) {
	if _, err := decodeAnthropicRequest([]byte(`{"max_tokens":10}`)); err == nil || !strings.Contains(err.Error(), "model") {
		t.Fatalf("缺 model 应报错，得 %v", err)
	}
	if _, err := decodeAnthropicRequest([]byte(`{"model":"m"}`)); err == nil || !strings.Contains(err.Error(), "messages") {
		t.Fatalf("缺 messages 应报错，得 %v", err)
	}
}

func TestDecodeAnthropicToolRoundtrip(t *testing.T) {
	// assistant 的 tool_use → tool_calls；user 的 tool_result → tool 消息；
	// tools / tool_choice 同步映射
	bm, err := decodeAnthropicRequest([]byte(`{
		"model": "m1",
		"max_tokens": 100,
		"messages": [
			{"role": "user", "content": "查天气"},
			{"role": "assistant", "content": [
				{"type": "text", "text": "我查一下"},
				{"type": "tool_use", "id": "tu_1", "name": "get_weather", "input": {"city": "北京"}}
			]},
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "tu_1", "content": [
					{"type": "text", "text": "晴，26 度"}
				]},
				{"type": "text", "text": "再总结一下"}
			]}
		],
		"tools": [{"name": "get_weather", "description": "查天气", "input_schema": {"type": "object", "properties": {"city": {"type": "string"}}}}],
		"tool_choice": {"type": "auto"}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	var msgs []map[string]any
	if err := json.Unmarshal(bm["messages"], &msgs); err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 4 {
		t.Fatalf("应翻出 4 条消息，得 %d: %+v", len(msgs), msgs)
	}
	asst := msgs[1]
	tcs, _ := asst["tool_calls"].([]any)
	if asst["content"] != "我查一下" || len(tcs) != 1 {
		t.Fatalf("assistant 翻译错: %+v", asst)
	}
	tc := tcs[0].(map[string]any)
	fn := tc["function"].(map[string]any)
	if tc["id"] != "tu_1" || fn["name"] != "get_weather" {
		t.Fatalf("tool_call 翻译错: %+v", tc)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(fn["arguments"].(string)), &args); err != nil || args["city"] != "北京" {
		t.Fatalf("tool_call arguments 应是合法 JSON 对象: %v", fn["arguments"])
	}
	toolMsg := msgs[2]
	if toolMsg["role"] != "tool" || toolMsg["tool_call_id"] != "tu_1" || toolMsg["content"] != "晴，26 度" {
		t.Fatalf("tool 消息翻译错: %+v", toolMsg)
	}
	if msgs[3]["role"] != "user" || msgs[3]["content"] != "再总结一下" {
		t.Fatalf("tool_result 后的文本块应独立成 user 消息: %+v", msgs[3])
	}
	var tools []map[string]any
	if err := json.Unmarshal(bm["tools"], &tools); err != nil {
		t.Fatal(err)
	}
	f := tools[0]["function"].(map[string]any)
	if tools[0]["type"] != "function" || f["name"] != "get_weather" {
		t.Fatalf("tools 翻译错: %+v", tools)
	}
	if _, ok := f["parameters"]; !ok {
		t.Fatal("input_schema 应映射为 parameters")
	}
	if string(bm["tool_choice"]) != `"auto"` {
		t.Fatalf("tool_choice auto 翻译错: %s", bm["tool_choice"])
	}
}

func TestDecodeAnthropicToolChoiceVariants(t *testing.T) {
	cases := map[string]string{
		`{"type":"any"}`:  `"required"`,
		`{"type":"none"}`: `"none"`,
	}
	for in, want := range cases {
		bm, err := decodeAnthropicRequest([]byte(fmt.Sprintf(
			`{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"x"}],"tool_choice":%s}`, in)))
		if err != nil {
			t.Fatal(err)
		}
		if string(bm["tool_choice"]) != want {
			t.Fatalf("tool_choice %s 应译为 %s，得 %s", in, want, bm["tool_choice"])
		}
	}
	bm, err := decodeAnthropicRequest([]byte(`{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"x"}],"tool_choice":{"type":"tool","name":"f1"}}`))
	if err != nil {
		t.Fatal(err)
	}
	var tc map[string]any
	if err := json.Unmarshal(bm["tool_choice"], &tc); err != nil || tc["type"] != "function" {
		t.Fatalf("tool_choice tool 应译为 function 对象: %s", bm["tool_choice"])
	}
}

func TestDecodeAnthropicImage(t *testing.T) {
	bm, err := decodeAnthropicRequest([]byte(`{
		"model": "m1", "max_tokens": 10,
		"messages": [{"role": "user", "content": [
			{"type": "text", "text": "这是什么"},
			{"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "QUJD"}}
		]}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	var msgs []map[string]any
	_ = json.Unmarshal(bm["messages"], &msgs)
	parts, _ := msgs[0]["content"].([]any)
	if len(parts) != 2 {
		t.Fatalf("应翻出 text+image 两个部件: %+v", msgs[0]["content"])
	}
	img := parts[1].(map[string]any)
	iu := img["image_url"].(map[string]any)
	if iu["url"] != "data:image/png;base64,QUJD" {
		t.Fatalf("image data URL 构造错: %v", iu)
	}
}

// ---------------- 出站翻译（非流式）----------------

func TestEncodeAnthropicResponse(t *testing.T) {
	in := `{"id":"chatcmpl-1","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"你好","tool_calls":[{"id":"call_1","function":{"name":"get_weather","arguments":"{\"city\":\"北京\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`
	out, usage, err := encodeAnthropicResponse([]byte(in), "fallback")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m["type"] != "message" || m["role"] != "assistant" || m["model"] != "m1" || m["id"] != "chatcmpl-1" {
		t.Fatalf("message 骨架错: %+v", m)
	}
	if m["stop_reason"] != "tool_use" {
		t.Fatalf("finish_reason=tool_calls 应译为 tool_use: %v", m["stop_reason"])
	}
	content, _ := m["content"].([]any)
	if len(content) != 2 || content[0].(map[string]any)["text"] != "你好" {
		t.Fatalf("content 翻译错: %+v", m["content"])
	}
	tu := content[1].(map[string]any)
	if tu["type"] != "tool_use" || tu["id"] != "call_1" || tu["name"] != "get_weather" {
		t.Fatalf("tool_use 块翻译错: %+v", tu)
	}
	input, _ := tu["input"].(map[string]any)
	if input["city"] != "北京" {
		t.Fatalf("tool_use input 应为对象: %+v", tu["input"])
	}
	u, _ := m["usage"].(map[string]any)
	if u["input_tokens"] != float64(10) || u["output_tokens"] != float64(5) {
		t.Fatalf("usage 翻译错: %+v", u)
	}
	if usage == nil || usage.PromptTokens != 10 || usage.CompletionTokens != 5 {
		t.Fatalf("返回的计费用 usage 错: %+v", usage)
	}
}

func TestEncodeAnthropicStopReasonMap(t *testing.T) {
	cases := map[string]string{
		"stop": "end_turn", "length": "max_tokens",
		"content_filter": "refusal", "tool_calls": "tool_use", "": "end_turn",
	}
	for finish, want := range cases {
		in := fmt.Sprintf(`{"id":"x","choices":[{"message":{"content":"a"},"finish_reason":%q}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`, finish)
		out, _, err := encodeAnthropicResponse([]byte(in), "m")
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		_ = json.Unmarshal(out, &m)
		if m["stop_reason"] != want {
			t.Fatalf("finish_reason=%q 应译为 %s，得 %v", finish, want, m["stop_reason"])
		}
	}
}

func TestAnthropicUsageFrom(t *testing.T) {
	u := anthropicUsageFrom([]byte(`{"type":"message","usage":{"input_tokens":12,"output_tokens":34,"cache_read_input_tokens":5}}`))
	if u == nil || u.PromptTokens != 12 || u.CompletionTokens != 34 || u.PromptCacheHitTokens != 5 {
		t.Fatalf("anthropicUsageFrom 错: %+v", u)
	}
	if anthropicUsageFrom([]byte(`{"type":"message"}`)) != nil {
		t.Fatal("无 usage 应返回 nil")
	}
}

// ---------------- 出站翻译（SSE）----------------

func runAnthPipe(t *testing.T, upstream string) (string, *Usage) {
	t.Helper()
	var sb strings.Builder
	usage, err := anthropicPipeSSE(&sb, context.Background(), strings.NewReader(upstream), [2]string{})
	if err != nil {
		t.Fatalf("SSE 翻译失败: %v", err)
	}
	return sb.String(), usage
}

func TestAnthropicPipeSSEText(t *testing.T) {
	out, usage := runAnthPipe(t, "data: {\"model\":\"m1\",\"choices\":[{\"delta\":{\"content\":\"你\"}}]}\n\n"+
		"data: {\"model\":\"m1\",\"choices\":[{\"delta\":{\"content\":\"好\"}}]}\n\n"+
		"data: {\"model\":\"m1\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"+
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":8,\"completion_tokens\":2}}\n\n"+
		"data: [DONE]\n\n")
	// 事件序列：message_start → block start/delta/delta/stop → message_delta(usage) → message_stop
	for _, want := range []string{
		"event: message_start",
		`"model":"m1","role":"assistant"`,
		"event: content_block_start",
		`"text":"你"`,
		`"text":"好"`,
		"event: content_block_stop",
		"event: message_delta",
		`"stop_reason":"end_turn"`,
		`"usage":{"input_tokens":8,"output_tokens":2}`,
		"event: message_stop",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("SSE 输出缺少 %q:\n%s", want, out)
		}
	}
	if usage == nil || usage.PromptTokens != 8 || usage.CompletionTokens != 2 {
		t.Fatalf("计费 usage 错: %+v", usage)
	}
	// 文本块只开一次（两个 delta 共用一个 block 0）
	if strings.Count(out, "event: content_block_start") != 1 {
		t.Fatalf("连续文本 delta 不应重复开块:\n%s", out)
	}
}

func TestAnthropicPipeSSEToolUse(t *testing.T) {
	frag := func(s string) string { // 构造带转义 arguments 片段的 SSE 块
		return fmt.Sprintf("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":%s}}]}}]}\n\n", mustQuoteJSON(t, s))
	}
	out, _ := runAnthPipe(t,
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_9\",\"function\":{\"name\":\"get_weather\",\"arguments\":\"\"}}]}}]}\n\n"+
			frag("{")+
			frag(`"city":"北京"}`)+
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n"+
			"data: [DONE]\n\n")
	for _, want := range []string{
		`"content_block":{"id":"call_9","input":{},"name":"get_weather","type":"tool_use"}`,
		`"delta":{"partial_json":"{","type":"input_json_delta"}`,
		`"partial_json":"\"city\":\"北京\"}"`,
		`"stop_reason":"tool_use"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("工具流翻译缺少 %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "event: content_block_stop") != 1 {
		t.Fatalf("工具块应恰好一个 stop 事件:\n%s", out)
	}
}

func mustQuoteJSON(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAnthropicPipeSSEEmptyThenEOF(t *testing.T) {
	// 上游直接 EOF（无任何块）：仍需产出完整合法事件序列，客户端不悬空
	out, usage := runAnthPipe(t, "")
	if !strings.Contains(out, "event: message_start") || !strings.Contains(out, "event: message_stop") {
		t.Fatalf("空流也应有完整事件骨架:\n%s", out)
	}
	if usage != nil {
		t.Fatalf("无 usage 应返回 nil: %+v", usage)
	}
}

func TestAnthropicPipeSSEModelSwap(t *testing.T) {
	var sb strings.Builder
	_, err := anthropicPipeSSE(&sb, context.Background(),
		strings.NewReader("data: {\"model\":\"up-name\",\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\ndata: [DONE]\n\n"),
		[2]string{"up-name", "ext-name"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sb.String(), `"model":"ext-name"`) {
		t.Fatalf("message_start 应使用外部名（映射对客户不可见）:\n%s", sb.String())
	}
}

// ---------------- 全链路（鉴权/渠道/计费/SSE）----------------

func anthBody(model, content string) string {
	return fmt.Sprintf(`{"model":%q,"max_tokens":64,"messages":[{"role":"user","content":%q}]}`, model, content)
}

func (e *testEnv) postMessages(t *testing.T, body string, header [2]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set(header[0], header[1])
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.engine.ServeHTTP(w, req)
	return w
}

// 非流式全链路：Anthropic 入 → OpenAI 上游 → Anthropic 出，计费与 usage_logs 落账
func TestMessagesRelayNonStream(t *testing.T) {
	e := newTestEnv(t)
	var gotUpstream map[string]any
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotUpstream)
		_, _ = w.Write([]byte(okBody))
	}))
	defer up.Close()
	e.seedUpstreamChannel(t, 1, "ch", up.URL, nil, 1)

	w := e.postMessages(t, anthBody("m1", "你好"), [2]string{"Authorization", "Bearer " + e.apiKey})
	if w.Code != http.StatusOK {
		t.Fatalf("应 200，得 %d: %s", w.Code, w.Body.String())
	}
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m["type"] != "message" || m["role"] != "assistant" {
		t.Fatalf("响应应为 Anthropic message 形状: %s", w.Body.String())
	}
	// 上游收到的是 OpenAI 形状
	if gotUpstream["model"] != "m1" {
		t.Fatalf("上游应收到 OpenAI model: %+v", gotUpstream)
	}
	msgs, _ := gotUpstream["messages"].([]any)
	if len(msgs) == 0 || msgs[0].(map[string]any)["content"] != "你好" {
		t.Fatalf("上游应收到 OpenAI messages: %+v", gotUpstream)
	}
	if gotUpstream["max_tokens"] != float64(64) {
		t.Fatalf("max_tokens 应透传: %+v", gotUpstream)
	}
	// 计费：7×2 + 3×8 = 38（m1 单价 2M/8M 点）
	id := e.lastUsage(t)
	cost, cacheHit, status := e.usageRow(t, id)
	if status != 200 || cacheHit != 0 || cost != 38 {
		t.Fatalf("计费错: cost=%d cache_hit=%d status=%d", cost, cacheHit, status)
	}
}

// x-api-key 头（Anthropic 客户端原生鉴权方式）应等价于 Bearer
func TestMessagesXApiKeyAuth(t *testing.T) {
	e := newTestEnv(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(okBody))
	}))
	defer up.Close()
	e.seedUpstreamChannel(t, 1, "ch", up.URL, nil, 1)

	w := e.postMessages(t, anthBody("m1", "hi"), [2]string{"x-api-key", e.apiKey})
	if w.Code != http.StatusOK {
		t.Fatalf("x-api-key 应等价 Bearer，得 %d: %s", w.Code, w.Body.String())
	}
}

// 错误形状：未授权模型 → Anthropic 错误体
func TestMessagesErrorShape(t *testing.T) {
	e := newTestEnv(t)
	w := e.postMessages(t, anthBody("no-such-model", "hi"), [2]string{"Authorization", "Bearer " + e.apiKey})
	if w.Code != http.StatusNotFound {
		t.Fatalf("应 404，得 %d", w.Code)
	}
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m["type"] != "error" {
		t.Fatalf("错误应为 Anthropic 形状: %s", w.Body.String())
	}
	if _, ok := m["error"].(map[string]any); !ok {
		t.Fatalf("error 字段应为对象: %s", w.Body.String())
	}
}

// 鉴权层错误也要按端点区分协议：/v1/messages 的 401 用 Anthropic 形状，
// /v1/chat/completions 的 401 维持 OpenAI 形状
func TestMessagesAuthErrorShape(t *testing.T) {
	e := newTestEnv(t)
	w := e.postMessages(t, anthBody("m1", "hi"), [2]string{"Authorization", "Bearer sk-wrong-key"})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("应 401，得 %d", w.Code)
	}
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	errObj, ok := m["error"].(map[string]any)
	if m["type"] != "error" || !ok {
		t.Fatalf("/v1/messages 的 401 应为 Anthropic 形状: %s", w.Body.String())
	}
	if errObj["type"] != "authentication_error" {
		t.Fatalf("错误类型应为 authentication_error: %s", w.Body.String())
	}
	// 对照：chat 端点同错误仍是 OpenAI 形状
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatBody("hi", "")))
	req.Header.Set("Authorization", "Bearer sk-wrong-key")
	req.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	e.engine.ServeHTTP(w2, req)
	var m2 map[string]any
	if err := json.Unmarshal(w2.Body.Bytes(), &m2); err != nil {
		t.Fatal(err)
	}
	if m2["type"] == "error" {
		t.Fatalf("chat 端点 401 不应变成 Anthropic 形状: %s", w2.Body.String())
	}
	if _, ok := m2["error"].(map[string]any); !ok {
		t.Fatalf("chat 端点 401 应保持 OpenAI 形状: %s", w2.Body.String())
	}
}

// 流式全链路：Anthropic 入（stream:true）→ 上游 OpenAI SSE → 客户端 Anthropic SSE；
// 顺带验证 stream_options.include_usage 注入对翻译路径同样生效
func TestMessagesRelayStream(t *testing.T) {
	e := newTestEnv(t)
	var gotUpstream map[string]any
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotUpstream)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"model\":\"m1\",\"choices\":[{\"delta\":{\"content\":\"你好\"}}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
			"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":3}}\n\n" +
			"data: [DONE]\n\n"))
	}))
	defer up.Close()
	e.seedUpstreamChannel(t, 1, "ch", up.URL, nil, 1)

	body := strings.Replace(anthBody("m1", "讲个故事"), `"max_tokens":64`, `"max_tokens":64,"stream":true`, 1)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("应 200，得 %d: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("应回 SSE Content-Type: %s", ct)
	}
	out := w.Body.String()
	for _, want := range []string{"event: message_start", "event: content_block_delta", "event: message_delta", "event: message_stop"} {
		if !strings.Contains(out, want) {
			t.Fatalf("SSE 缺少 %q:\n%s", want, out)
		}
	}
	// 上游收到了注入的 include_usage 与 stream 标记
	so, _ := gotUpstream["stream_options"].(map[string]any)
	if gotUpstream["stream"] != true || so["include_usage"] != true {
		t.Fatalf("翻译路径应同样注入 stream_options.include_usage: %+v", gotUpstream)
	}
	// 计费落账
	id := e.lastUsage(t)
	cost, _, status := e.usageRow(t, id)
	if status != 200 || cost != 38 {
		t.Fatalf("流式计费错: cost=%d status=%d", cost, status)
	}
}

// 工具调用往返：请求 tools 翻译给上游，上游 tool_calls 翻译回 tool_use
func TestMessagesToolRoundtripE2E(t *testing.T) {
	e := newTestEnv(t)
	var gotUpstream map[string]any
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotUpstream)
		_, _ = w.Write([]byte(`{"id":"x","choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"c1","function":{"name":"get_weather","arguments":"{\"city\":\"北京\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`))
	}))
	defer up.Close()
	e.seedUpstreamChannel(t, 1, "ch", up.URL, nil, 1)

	body := `{"model":"m1","max_tokens":100,
		"messages":[{"role":"user","content":"查天气"}],
		"tools":[{"name":"get_weather","description":"d","input_schema":{"type":"object"}}],
		"tool_choice":{"type":"auto"}}`
	w := e.postMessages(t, body, [2]string{"Authorization", "Bearer " + e.apiKey})
	if w.Code != http.StatusOK {
		t.Fatalf("应 200，得 %d: %s", w.Code, w.Body.String())
	}
	// 上游收到 OpenAI tools / tool_choice
	tools, _ := gotUpstream["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["type"] != "function" {
		t.Fatalf("上游应收到 OpenAI tools: %+v", gotUpstream)
	}
	if gotUpstream["tool_choice"] != "auto" {
		t.Fatalf("tool_choice 应译为 auto: %+v", gotUpstream)
	}
	// 客户端收到 tool_use
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	content, _ := m["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("应只有一个 tool_use 块: %s", w.Body.String())
	}
	tu := content[0].(map[string]any)
	if tu["type"] != "tool_use" || tu["name"] != "get_weather" || m["stop_reason"] != "tool_use" {
		t.Fatalf("tool_use 翻译错: %+v", tu)
	}
}
