package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// pipeSSE 流式转发：按 SSE 事件（空行分隔）逐块写客户端并立即 flush，
// 同时轻量解析每个 data: 载荷中的 usage 字段（正常只出现在最后一个 chunk）。
// 客户端断开（ctx 取消 / 写失败）即停止读取并关闭上游。
func pipeSSE(w io.Writer, ctx context.Context, body io.Reader) (usage *Usage, err error) {
	flusher, _ := w.(http.Flusher)
	reader := bufio.NewReaderSize(body, 32*1024)
	var event bytes.Buffer

	writeEvent := func() error {
		if event.Len() == 0 {
			return nil
		}
		if _, err := w.Write(event.Bytes()); err != nil {
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
			event.WriteString(line)
			if t := strings.TrimSpace(line); strings.HasPrefix(t, "data:") {
				payload := strings.TrimSpace(strings.TrimPrefix(t, "data:"))
				if payload != "" && payload != "[DONE]" {
					var ur struct {
						Usage *Usage `json:"usage"`
					}
					if json.Unmarshal([]byte(payload), &ur) == nil && ur.Usage != nil {
						usage = ur.Usage
					}
				}
			}
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
