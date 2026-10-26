package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"token-gateway/internal/scrub"
)

// gatedWriter 客户端断连后的收割写侧：客户 ctx 取消或首次写失败即进入丢弃态
// （写侧静默吞错），读侧由调用方继续读完上游响应体以收割 usage——
// 厂商服务端在客户端断开后照常生成并收费，钱必须跟着 usage 穿透计费。
type gatedWriter struct {
	w      io.Writer
	client func() bool // 客户端是否已断开（客户 ctx 取消判定，权威信号）
	failed bool        // 写失败兜底（内核缓冲可能延迟暴露断开）
}

func (g *gatedWriter) gone() bool { return g.failed || (g.client != nil && g.client()) }

// Write 断开后丢弃并伪报成功：读取循环不得因客户端断开而终止
func (g *gatedWriter) Write(p []byte) (int, error) {
	if g.gone() {
		return len(p), nil
	}
	n, err := g.w.Write(p)
	if err != nil {
		g.failed = true
		return len(p), nil
	}
	return n, nil
}

// Flush 实现 http.Flusher（SSE 逐块冲刷）；断开后为空操作
func (g *gatedWriter) Flush() {
	if g.gone() {
		return
	}
	if f, ok := g.w.(http.Flusher); ok {
		f.Flush()
	}
}

// pipeSSE 流式转发：按 SSE 事件（空行分隔）逐块写客户端并立即 flush，
// 同时轻量解析每个 data: 载荷中的 usage 字段（正常只出现在最后一个 chunk）。
// modelSwap 非空时把事件里的 "model":"<上游名>" 字面量改写回外部名（映射对客户不可见）。
// ctx 仅作客户端断开判定：断开后停写不停读（收割模式）——丢弃写出、继续读上游
// 直到 usage/EOF，返回 clientGone 供调用方记 499 并对已收割 usage 照常结算。
func pipeSSE(w io.Writer, ctx context.Context, body io.Reader, modelSwap [2]string) (usage *Usage, clientGone bool, err error) {
	gw := &gatedWriter{w: w, client: func() bool { return ctx.Err() != nil }}
	reader := bufio.NewReaderSize(body, 32*1024)
	var event bytes.Buffer

	writeEvent := func() {
		if event.Len() == 0 {
			return
		}
		var out []byte = event.Bytes()
		if modelSwap[0] != "" {
			out = swapModelBytes(out, modelSwap)
		}
		// 剥掉空 reasoning_content 字段：DeepSeek v4 系每块都带该空字段，
		// 会触发部分第三方 OpenAI→Anthropic 转换器的逐块重置 bug（见
		// scrubEmptyReasoning 注释）；不含字段的块零开销直通
		out = scrubEmptyReasoning(out)
		_, _ = gw.Write(out)
		gw.Flush()
		event.Reset()
	}

	for {
		line, rerr := reader.ReadString('\n')
		if line != "" {
			if t := strings.TrimSpace(line); strings.HasPrefix(t, "data:") {
				payload := strings.TrimSpace(strings.TrimPrefix(t, "data:"))
				if payload != "" && payload != "[DONE]" {
					var ur struct {
						Usage *Usage          `json:"usage"`
						Error json.RawMessage `json:"error"`
					}
					if json.Unmarshal([]byte(payload), &ur) == nil {
						if ur.Usage != nil {
							usage = ur.Usage
						}
						// 上游错误事件（data: {"error":{...}}）：消毒后再透传。
						// 只动含 error 键的块——正文 delta 里合法出现链接属于客户内容，不碰
						if len(ur.Error) > 0 {
							payload = string(scrub.Bytes([]byte(payload)))
							line = "data: " + payload + "\n"
						}
					}
				}
			}
			event.WriteString(line)
		}
		if rerr != nil {
			writeEvent() // 尽力冲掉残留（断开后为丢弃，无副作用）
			if rerr == io.EOF {
				return usage, gw.gone(), nil
			}
			return usage, gw.gone(), rerr
		}
		// 事件结束（空行）→ 整块写出
		if line == "\n" || line == "\r\n" {
			writeEvent()
		}
	}
}
