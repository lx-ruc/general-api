package gateway

// /v1/responses OpenAI Responses 协议适配测试：两个方向的翻译纯函数 + 全链路（鉴权/计费/SSE）。

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

func TestDecodeResponsesBasic(t *testing.T) {
	bm, err := decodeResponsesRequest([]byte(`{
		"model": "deepseek-v4-flash",
		"instructions": "你是个助手",
		"input": "你好",
		"max_output_tokens": 512,
		"temperature": 0.3,
		"top_p": 0.9,
		"stream": false
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(bm["model"]) != `"deepseek-v4-flash"` {
		t.Fatalf("model 翻译错: %s", bm["model"])
	}
	var msgs []map[string]any
	if err := json.Unmarshal(bm["messages"], &msgs); err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0]["role"] != "system" || msgs[0]["content"] != "你是个助手" {
		t.Fatalf("instructions 应译为 system 消息: %+v", msgs)
	}
	if msgs[1]["role"] != "user" || msgs[1]["content"] != "你好" {
		t.Fatalf("字符串 input 应译为 user 消息: %+v", msgs)
	}
	if string(bm["max_tokens"]) != "512" || string(bm["temperature"]) != "0.3" || string(bm["top_p"]) != "0.9" {
		t.Fatalf("采样参数翻译错: %s %s %s", bm["max_tokens"], bm["temperature"], bm["top_p"])
	}
	if _, ok := bm["stream"]; ok {
		t.Fatal("非流式不应携带 stream 字段")
	}
}

func TestDecodeResponsesMissingParams(t *testing.T) {
	if _, err := decodeResponsesRequest([]byte(`{"input":"x"}`)); err == nil || !strings.Contains(err.Error(), "model") {
		t.Fatalf("缺 model 应报错，得 %v", err)
	}
	if _, err := decodeResponsesRequest([]byte(`{"model":"m"}`)); err == nil || !strings.Contains(err.Error(), "input") {
		t.Fatalf("缺 input 应报错，得 %v", err)
	}
	if _, err := decodeResponsesRequest([]byte(`{"model":"m","input":{"bad":1}}`)); err == nil {
		t.Fatal("input 非法形态应报错")
	}
	if _, err := decodeResponsesRequest([]byte(`{"model":"m","input":[]}`)); err == nil {
		t.Fatal("空 input 数组应报错（无可用消息）")
	}
}

func TestDecodeResponsesItems(t *testing.T) {
	// 数组 input：developer→system、function_call 连续合并、function_call_output→tool、reasoning 丢弃
	bm, err := decodeResponsesRequest([]byte(`{
		"model": "m1",
		"input": [
			{"type":"message","role":"developer","content":[{"type":"input_text","text":"系统提示"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"查天气"}]},
			{"type":"reasoning","summary":[],"content":[]},
			{"type":"function_call","name":"get_weather","call_id":"call_a","arguments":"{\"city\":\"北京\"}"},
			{"type":"function_call","name":"get_time","call_id":"call_b","arguments":""},
			{"type":"function_call_output","call_id":"call_a","output":[{"type":"output_text","text":"晴，26 度"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"总结"}]}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	var msgs []map[string]any
	if err := json.Unmarshal(bm["messages"], &msgs); err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 5 {
		t.Fatalf("应翻出 5 条消息，得 %d: %+v", len(msgs), msgs)
	}
	if msgs[0]["role"] != "system" || msgs[0]["content"] != "系统提示" {
		t.Fatalf("developer 应译为 system: %+v", msgs[0])
	}
	if msgs[1]["role"] != "user" || msgs[1]["content"] != "查天气" {
		t.Fatalf("user 消息翻译错: %+v", msgs[1])
	}
	asst := msgs[2]
	tcs, _ := asst["tool_calls"].([]any)
	if asst["role"] != "assistant" || len(tcs) != 2 {
		t.Fatalf("连续 function_call 应合并进一条 assistant: %+v", asst)
	}
	tc0 := tcs[0].(map[string]any)
	fn0 := tc0["function"].(map[string]any)
	if tc0["id"] != "call_a" || fn0["name"] != "get_weather" {
		t.Fatalf("tool_call 翻译错: %+v", tc0)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(fn0["arguments"].(string)), &args); err != nil || args["city"] != "北京" {
		t.Fatalf("arguments 应是合法 JSON 对象: %v", fn0["arguments"])
	}
	fn1 := tcs[1].(map[string]any)["function"].(map[string]any)
	if fn1["arguments"] != "{}" {
		t.Fatalf("空 arguments 应补 {}: %v", fn1["arguments"])
	}
	toolMsg := msgs[3]
	if toolMsg["role"] != "tool" || toolMsg["tool_call_id"] != "call_a" || toolMsg["content"] != "晴，26 度" {
		t.Fatalf("function_call_output 翻译错: %+v", toolMsg)
	}
	if msgs[4]["role"] != "user" || msgs[4]["content"] != "总结" {
		t.Fatalf("尾部 user 消息翻译错: %+v", msgs[4])
	}
}

func TestDecodeResponsesTools(t *testing.T) {
	bm, err := decodeResponsesRequest([]byte(`{
		"model": "m1", "input": "hi",
		"tools": [
			{"type":"function","name":"get_weather","description":"查天气","parameters":{"type":"object","properties":{"city":{"type":"string"}}}},
			{"type":"function","name":"no_schema","description":"无参数"},
			{"type":"local_shell"}
		],
		"tool_choice": "auto"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	var tools []map[string]any
	if err := json.Unmarshal(bm["tools"], &tools); err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 {
		t.Fatalf("local_shell 应被丢弃，得 %d 个: %+v", len(tools), tools)
	}
	f0 := tools[0]["function"].(map[string]any)
	if tools[0]["type"] != "function" || f0["name"] != "get_weather" {
		t.Fatalf("扁平 tools 应译为嵌套形态: %+v", tools[0])
	}
	if _, ok := f0["parameters"]; !ok {
		t.Fatal("parameters 应透传")
	}
	f1 := tools[1]["function"].(map[string]any)
	schema, ok := f1["parameters"].(map[string]any)
	if !ok || schema["type"] != "object" {
		t.Fatalf("缺省 parameters 应补 {\"type\":\"object\"}: %+v", f1["parameters"])
	}
	if string(bm["tool_choice"]) != `"auto"` {
		t.Fatalf("tool_choice 字符串应透传: %s", bm["tool_choice"])
	}
}

func TestDecodeResponsesToolChoiceObject(t *testing.T) {
	bm, err := decodeResponsesRequest([]byte(`{
		"model":"m","input":"x",
		"tool_choice":{"type":"function","name":"f1"}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	var tc map[string]any
	if err := json.Unmarshal(bm["tool_choice"], &tc); err != nil || tc["type"] != "function" {
		t.Fatalf("扁平 tool_choice 应译为嵌套对象: %s", bm["tool_choice"])
	}
	fn, _ := tc["function"].(map[string]any)
	if fn["name"] != "f1" {
		t.Fatalf("tool_choice 函数名错: %s", bm["tool_choice"])
	}
}

// ---------------- 出站翻译（非流式）----------------

func TestEncodeResponsesResponse(t *testing.T) {
	in := `{"id":"abc123","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"你好","tool_calls":[{"id":"call_1","function":{"name":"get_weather","arguments":"{\"city\":\"北京\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":3}}}`
	out, usage, err := encodeResponsesResponse([]byte(in), "fallback")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m["object"] != "response" || m["id"] != "resp_abc123" || m["status"] != "completed" || m["model"] != "m1" {
		t.Fatalf("response 骨架错: %s", out)
	}
	output, _ := m["output"].([]any)
	if len(output) != 2 {
		t.Fatalf("应翻出 message + function_call 两个 item: %s", out)
	}
	msg := output[0].(map[string]any)
	if msg["type"] != "message" || msg["role"] != "assistant" || msg["id"] != "msg_0" {
		t.Fatalf("message item 错: %+v", msg)
	}
	content, _ := msg["content"].([]any)
	part := content[0].(map[string]any)
	if part["type"] != "output_text" || part["text"] != "你好" {
		t.Fatalf("output_text 部件错: %+v", part)
	}
	fc := output[1].(map[string]any)
	if fc["type"] != "function_call" || fc["call_id"] != "call_1" || fc["name"] != "get_weather" {
		t.Fatalf("function_call item 错: %+v", fc)
	}
	// arguments 必须是「JSON 编码后的字符串」而非对象：codex 的 FunctionCall.arguments
	// 为 String 类型，发对象会让整个 output_item 反序列化失败被静默丢弃（界面无下文）
	fcArgs, _ := fc["arguments"].(string)
	var argObj map[string]any
	if err := json.Unmarshal([]byte(fcArgs), &argObj); err != nil || argObj["city"] != "北京" {
		t.Fatalf("function_call arguments 应为合法 JSON 字符串: %v", fc["arguments"])
	}
	u, _ := m["usage"].(map[string]any)
	if u["input_tokens"] != float64(10) || u["output_tokens"] != float64(5) || u["total_tokens"] != float64(15) {
		t.Fatalf("usage 翻译错: %+v", u)
	}
	itd, _ := u["input_tokens_details"].(map[string]any)
	if itd["cached_tokens"] != float64(3) {
		t.Fatalf("缓存命中应映射 input_tokens_details.cached_tokens: %+v", itd)
	}
	if usage == nil || usage.PromptTokens != 10 || usage.CompletionTokens != 5 {
		t.Fatalf("返回的计费用 usage 错: %+v", usage)
	}
}

func TestEncodeResponsesIncomplete(t *testing.T) {
	in := `{"id":"x","model":"m","choices":[{"message":{"content":"太长了"},"finish_reason":"length"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`
	out, _, err := encodeResponsesResponse([]byte(in), "m")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	if m["status"] != "incomplete" {
		t.Fatalf("finish=length 应译为 incomplete: %s", out)
	}
	inc, _ := m["incomplete_details"].(map[string]any)
	if inc["reason"] != "max_output_tokens" {
		t.Fatalf("incomplete_details 错: %+v", m["incomplete_details"])
	}
}

func TestResponsesUsageFrom(t *testing.T) {
	// 官方命名 cached_tokens；回放旧形状（cache_read_input_tokens）也能读
	u := responsesUsageFrom([]byte(`{"object":"response","usage":{"input_tokens":12,"output_tokens":34,"input_tokens_details":{"cached_tokens":5}}}`))
	if u == nil || u.PromptTokens != 12 || u.CompletionTokens != 34 || u.PromptCacheHitTokens != 5 {
		t.Fatalf("responsesUsageFrom 错: %+v", u)
	}
	u2 := responsesUsageFrom([]byte(`{"usage":{"input_tokens":1,"output_tokens":1,"input_tokens_details":{"cache_read_input_tokens":2}}}`))
	if u2 == nil || u2.PromptCacheHitTokens != 2 {
		t.Fatalf("旧字段名 cache_read_input_tokens 应兼容: %+v", u2)
	}
	if responsesUsageFrom([]byte(`{"object":"response"}`)) != nil {
		t.Fatal("无 usage 应返回 nil")
	}
}

// ---------------- 出站翻译（SSE）----------------

func runRespPipe(t *testing.T, upstream string) (string, *Usage) {
	t.Helper()
	var sb strings.Builder
	usage, err := responsesPipeSSE(&sb, context.Background(), strings.NewReader(upstream), [2]string{})
	if err != nil {
		t.Fatalf("SSE 翻译失败: %v", err)
	}
	return sb.String(), usage
}

func TestResponsesPipeSSEText(t *testing.T) {
	out, usage := runRespPipe(t, "data: {\"model\":\"m1\",\"choices\":[{\"delta\":{\"content\":\"你\"}}]}\n\n"+
		"data: {\"model\":\"m1\",\"choices\":[{\"delta\":{\"content\":\"好\"}}]}\n\n"+
		"data: {\"model\":\"m1\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"+
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":8,\"completion_tokens\":2}}\n\n"+
		"data: [DONE]\n\n")
	for _, want := range []string{
		"event: response.created",
		`"status":"in_progress"`,
		"event: response.output_item.added",
		`"role":"assistant","status":"in_progress","type":"message"`,
		"event: response.content_part.added",
		"event: response.output_text.delta",
		`"delta":"你"`,
		`"delta":"好"`,
		"event: response.output_text.done",
		"event: response.content_part.done",
		"event: response.output_item.done",
		"event: response.completed",
		`"input_tokens":8`,
		`"output_tokens":2`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("SSE 输出缺少 %q:\n%s", want, out)
		}
	}
	if usage == nil || usage.PromptTokens != 8 || usage.CompletionTokens != 2 {
		t.Fatalf("计费 usage 错: %+v", usage)
	}
	// 文本 item 只开一次（两个 delta 共用一个 msg_0）
	if strings.Count(out, "event: response.output_item.added") != 1 {
		t.Fatalf("连续文本 delta 不应重复开 item:\n%s", out)
	}
	// 事件都带递增 sequence_number
	first := strings.Index(out, `"sequence_number":0`)
	if first < 0 || strings.Index(out, `"sequence_number":1`) < first {
		t.Fatalf("sequence_number 应从 0 递增:\n%s", out)
	}
}

func TestResponsesPipeSSEToolCall(t *testing.T) {
	frag := func(s string) string { // 构造带转义 arguments 片段的 SSE 块
		return fmt.Sprintf("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":%s}}]}}]}\n\n", mustQuoteJSON(t, s))
	}
	out, _ := runRespPipe(t,
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_9\",\"function\":{\"name\":\"get_weather\",\"arguments\":\"\"}}]}}]}\n\n"+
			frag("{")+
			frag(`"city":"北京"}`)+
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n"+
			"data: [DONE]\n\n")
	for _, want := range []string{
		"event: response.output_item.added",
		`"item":{"arguments":"","call_id":"call_9","id":"fc_0","name":"get_weather","status":"in_progress","type":"function_call"}`,
		"event: response.function_call_arguments.delta",
		"event: response.function_call_arguments.done",
		`"arguments":"{\"city\":\"北京\"}"`,
		"event: response.output_item.done",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("工具流翻译缺少 %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "event: response.output_item.done") != 1 {
		t.Fatalf("工具 item 应恰好一个 done 事件:\n%s", out)
	}
	// done 条目的 arguments 必须是字符串形态（codex FunctionCall.arguments: String，
	// 发对象会被反序列化丢弃——生产实测「扫描当前项目」触发首次工具调用即无下文）
	var doneEvt struct {
		Item struct {
			Type      string `json:"type"`
			Arguments string `json:"arguments"`
		} `json:"item"`
	}
	if !parseSSEEvent(t, out, "response.output_item.done", &doneEvt) {
		t.Fatalf("缺少 output_item.done 事件:\n%s", out)
	}
	if doneEvt.Item.Type != "function_call" {
		t.Fatalf("done 条目类型错: %+v", doneEvt.Item)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(doneEvt.Item.Arguments), &args); err != nil || args["city"] != "北京" {
		t.Fatalf("done 条目 arguments 应为可解析回对象的 JSON 字符串: %q", doneEvt.Item.Arguments)
	}
}

// parseSSEEvent 从翻译器输出的事件流中取指定事件的 data JSON
func parseSSEEvent(t *testing.T, stream, event string, out any) bool {
	t.Helper()
	lines := strings.Split(stream, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == "event: "+event && i+1 < len(lines) {
			data := strings.TrimPrefix(strings.TrimSpace(lines[i+1]), "data: ")
			if err := json.Unmarshal([]byte(data), out); err != nil {
				t.Fatalf("解析 %s 事件 data 失败: %v\n%s", event, err, data)
			}
			return true
		}
	}
	return false
}

func TestResponsesPipeSSEEmptyThenEOF(t *testing.T) {
	// 上游直接 EOF（无任何块）：仍需产出 created + completed 完整事件，codex 不悬空
	out, usage := runRespPipe(t, "")
	if !strings.Contains(out, "event: response.created") || !strings.Contains(out, "event: response.completed") {
		t.Fatalf("空流也应有完整事件骨架:\n%s", out)
	}
	if usage != nil {
		t.Fatalf("无 usage 应返回 nil: %+v", usage)
	}
}

func TestResponsesPipeSSEModelSwap(t *testing.T) {
	var sb strings.Builder
	_, err := responsesPipeSSE(&sb, context.Background(),
		strings.NewReader("data: {\"model\":\"up-name\",\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\ndata: [DONE]\n\n"),
		[2]string{"up-name", "ext-name"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sb.String(), `"model":"ext-name"`) {
		t.Fatalf("事件流应使用外部名（映射对客户不可见）:\n%s", sb.String())
	}
}

// ---------------- 全链路（鉴权/渠道/计费/SSE）----------------

func respBody(model, input string) string {
	return fmt.Sprintf(`{"model":%q,"input":%q,"max_output_tokens":64}`, model, input)
}

func (e *testEnv) postResponses(t *testing.T, body string, header [2]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set(header[0], header[1])
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.engine.ServeHTTP(w, req)
	return w
}

// 非流式全链路：Responses 入 → OpenAI 上游 → Responses 出，计费与 usage_logs 落账
func TestResponsesRelayNonStream(t *testing.T) {
	e := newTestEnv(t)
	var gotUpstream map[string]any
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotUpstream)
		_, _ = w.Write([]byte(okBody))
	}))
	defer up.Close()
	e.seedUpstreamChannel(t, 1, "ch", up.URL, nil, 1)

	w := e.postResponses(t, respBody("m1", "你好"), [2]string{"Authorization", "Bearer " + e.apiKey})
	if w.Code != http.StatusOK {
		t.Fatalf("应 200，得 %d: %s", w.Code, w.Body.String())
	}
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m["object"] != "response" || m["status"] != "completed" {
		t.Fatalf("响应应为 Responses 形状: %s", w.Body.String())
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
		t.Fatalf("max_output_tokens 应译为 max_tokens: %+v", gotUpstream)
	}
	// 计费：7×2 + 3×8 = 38（m1 单价 2M/8M 点）
	id := e.lastUsage(t)
	cost, cacheHit, status := e.usageRow(t, id)
	if status != 200 || cacheHit != 0 || cost != 38 {
		t.Fatalf("计费错: cost=%d cache_hit=%d status=%d", cost, cacheHit, status)
	}
}

// 错误形状：Responses 端点错误保持 OpenAI 形状（codex 客户端可解析）
func TestResponsesErrorShape(t *testing.T) {
	e := newTestEnv(t)
	w := e.postResponses(t, respBody("no-such-model", "hi"), [2]string{"Authorization", "Bearer " + e.apiKey})
	if w.Code != http.StatusNotFound {
		t.Fatalf("应 404，得 %d: %s", w.Code, w.Body.String())
	}
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	errObj, ok := m["error"].(map[string]any)
	if !ok || errObj["message"] == nil {
		t.Fatalf("错误应保持 OpenAI 形状: %s", w.Body.String())
	}
	if m["type"] == "error" {
		t.Fatal("Responses 端点错误不应是 Anthropic 形状")
	}
}

// 缺参校验：缺 input 直接 400
func TestResponsesMissingInput(t *testing.T) {
	e := newTestEnv(t)
	w := e.postResponses(t, `{"model":"m1"}`, [2]string{"Authorization", "Bearer " + e.apiKey})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("应 400，得 %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "input") {
		t.Fatalf("错误应指向 input: %s", w.Body.String())
	}
}

// 流式全链路：Responses 入（stream:true）→ 上游 OpenAI SSE → 客户端 Responses 事件流；
// 顺带验证 stream_options.include_usage 注入对翻译路径同样生效
func TestResponsesRelayStream(t *testing.T) {
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

	body := strings.Replace(respBody("m1", "讲个故事"), `"max_output_tokens":64`, `"max_output_tokens":64,"stream":true`, 1)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
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
	for _, want := range []string{
		"event: response.created",
		"event: response.output_text.delta",
		"event: response.output_item.done",
		"event: response.completed",
	} {
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

// 工具调用往返：请求扁平 tools 翻译给上游，上游 tool_calls 翻译回 function_call item；
// 第二轮把 function_call/function_call_output 回放进来（codex 多轮形态）
func TestResponsesToolRoundtripE2E(t *testing.T) {
	e := newTestEnv(t)
	var gotUpstream map[string]any
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotUpstream)
		_, _ = w.Write([]byte(`{"id":"x","choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"c1","function":{"name":"get_weather","arguments":"{\"city\":\"北京\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`))
	}))
	defer up.Close()
	e.seedUpstreamChannel(t, 1, "ch", up.URL, nil, 1)

	body := `{"model":"m1","input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"查天气"}]}],
		"tools":[{"type":"function","name":"get_weather","description":"d","parameters":{"type":"object"}}],
		"tool_choice":"auto"}`
	w := e.postResponses(t, body, [2]string{"Authorization", "Bearer " + e.apiKey})
	if w.Code != http.StatusOK {
		t.Fatalf("应 200，得 %d: %s", w.Code, w.Body.String())
	}
	// 上游收到 OpenAI 嵌套 tools
	tools, _ := gotUpstream["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["type"] != "function" {
		t.Fatalf("上游应收到 OpenAI tools: %+v", gotUpstream)
	}
	// 客户端收到 function_call item
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	output, _ := m["output"].([]any)
	if len(output) != 1 {
		t.Fatalf("应只有一个 function_call item: %s", w.Body.String())
	}
	fc := output[0].(map[string]any)
	if fc["type"] != "function_call" || fc["name"] != "get_weather" || fc["call_id"] != "c1" {
		t.Fatalf("function_call 翻译错: %+v", fc)
	}
	// arguments 必须是「JSON 编码后的字符串」而非对象（codex FunctionCall.arguments: String）
	fcArgs, _ := fc["arguments"].(string)
	var args map[string]any
	if err := json.Unmarshal([]byte(fcArgs), &args); err != nil || args["city"] != "北京" {
		t.Fatalf("function_call arguments 应为合法 JSON 字符串: %v", fc["arguments"])
	}

	// 第二轮回放（多轮会话形态）：上游应收到 assistant(tool_calls) + tool 两条消息
	body2 := `{"model":"m1","input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"查天气"}]},
			{"type":"function_call","name":"get_weather","call_id":"c1","arguments":"{\"city\":\"北京\"}"},
			{"type":"function_call_output","call_id":"c1","output":"晴"}],
		"max_output_tokens":32}`
	w2 := e.postResponses(t, body2, [2]string{"Authorization", "Bearer " + e.apiKey})
	if w2.Code != http.StatusOK {
		t.Fatalf("第二轮应 200，得 %d: %s", w2.Code, w2.Body.String())
	}
	msgs, _ := gotUpstream["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("第二轮应翻出 user + assistant + tool 三条消息，得 %d: %+v", len(msgs), gotUpstream)
	}
	asst := msgs[1].(map[string]any)
	if asst["role"] != "assistant" || asst["tool_calls"] == nil {
		t.Fatalf("回放的 function_call 应译为 assistant.tool_calls: %+v", asst)
	}
	tool := msgs[2].(map[string]any)
	if tool["role"] != "tool" || tool["tool_call_id"] != "c1" || tool["content"] != "晴" {
		t.Fatalf("回放的 function_call_output 应译为 tool 消息: %+v", tool)
	}
}
