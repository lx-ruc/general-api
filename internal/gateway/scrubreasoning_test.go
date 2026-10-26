package gateway

import (
	"bytes"
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

// scrubEmptyReasoning：DeepSeek v4 系上游在正文阶段每个流式 chunk 都带
// 空串 "reasoning_content":""，零信息量，但旧版 cc-switch 等第三方
// OpenAI→Anthropic 转换器见到字段即重置文本块状态，把每个增量渲染成
// 独立列表项——透传前剥掉空字段，非空思考内容保留。
func TestScrubEmptyReasoning(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"无字段原样", `{"delta":{"content":"hi"}}`, `{"delta":{"content":"hi"}}`},
		{"字段在中间-紧凑", `{"delta":{"content":"你好","reasoning_content":"","role":"assistant"}}`, `{"delta":{"content":"你好","role":"assistant"}}`},
		{"字段在末尾-紧凑", `{"delta":{"role":"assistant","reasoning_content":""}}`, `{"delta":{"role":"assistant"}}`},
		{"唯一字段", `{"reasoning_content":""}`, `{}`},
		{"字段在中间-带空格", `{"delta":{"content":"hi","reasoning_content": "","role":"assistant"}}`, `{"delta":{"content":"hi","role":"assistant"}}`},
		{"唯一字段-带空格", `{"reasoning_content": ""}`, `{}`},
		{"非空思考保留", `{"delta":{"content":"","reasoning_content":"思考中"}}`, `{"delta":{"content":"","reasoning_content":"思考中"}}`},
		{"content空串不误伤", `{"delta":{"reasoning_content":"思","content":""}}`, `{"delta":{"reasoning_content":"思","content":""}}`},
		{"多处出现全删", `{"a":{"reasoning_content":""},"b":{"reasoning_content":""}}`, `{"a":{},"b":{}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := string(scrubEmptyReasoning([]byte(c.in)))
			if got != c.want {
				t.Fatalf("scrubEmptyReasoning(%s)\n got: %s\nwant: %s", c.in, got, c.want)
			}
		})
	}
}

// 管道级：pipeSSE 透传时逐事件剥掉空 reasoning_content，其余字节原样
func TestPipeSSEScrubsEmptyReasoning(t *testing.T) {
	upstream := "event: chunk\n" +
		`data: {"choices":[{"delta":{"content":"你好","reasoning_content":"","role":"assistant"},"index":0}]}` + "\n\n" +
		"event: chunk\n" +
		`data: {"choices":[{"delta":{"content":"！","reasoning_content":"","role":"assistant"},"index":0}]}` + "\n\n" +
		"data: [DONE]\n\n"

	rec := httptest.NewRecorder()
	_, _, err := pipeSSE(rec.Body, context.Background(), strings.NewReader(upstream), [2]string{})
	if err != nil {
		t.Fatalf("pipeSSE: %v", err)
	}
	out := rec.Body.String()
	if bytes.Contains([]byte(out), []byte(`"reasoning_content":""`)) {
		t.Fatalf("输出仍含空 reasoning_content:\n%s", out)
	}
	for _, frag := range []string{`"content":"你好"`, `"content":"！"`, "data: [DONE]"} {
		if !bytes.Contains([]byte(out), []byte(frag)) {
			t.Fatalf("输出丢失正文片段 %s:\n%s", frag, out)
		}
	}
}
