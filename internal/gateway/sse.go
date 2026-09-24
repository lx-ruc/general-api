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

// pipeSSE 流式转发：按 SSE 事件（空行分隔）逐块写客户端并立即 flush，
// 同时轻量解析每个 data: 载荷中的 usage 字段（正常只出现在最后一个 chunk）。
// modelSwap 非空时把事件里的 "model":"<上游名>" 字面量改写回外部名（映射对客户不可见）。
// 客户端断开（ctx 取消 / 写失败）即停止读取并关闭上游。
func pipeSSE(w io.Writer, ctx context.Context, body io.Reader, modelSwap [2]string) (usage *Usage, err error) {
	flusher, _ := w.(http.Flusher)
	reader := bufio.NewReaderSize(body, 32*1024)
	var event bytes.Buffer

	writeEvent := func() error {
		if event.Len() == 0 {
			return nil
		}
		var out []byte = event.Bytes()
		if modelSwap[0] != "" {
			out = swapModelBytes(out, modelSwap)
		}
		if _, err := w.Write(out); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		event.Reset()
		return nil
	}

	for {
		if ctx.Err() != nil {
			return usage, ctx.Err()
		}
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
			_ = writeEvent() // 尽力冲掉残留
			if rerr == io.EOF {
				return usage, nil
			}
			return usage, rerr
		}
		// 事件结束（空行）→ 整块写出
		if line == "\n" || line == "\r\n" {
			if werr := writeEvent(); werr != nil {
				return usage, werr
			}
		}
	}
}
