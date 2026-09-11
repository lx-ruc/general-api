// mockupstream 是压测用的 OpenAI 兼容假上游：按 Authorization 里的 key 分桶计 RPM，
// 超限回 429（带 Retry-After），用于不动真实厂商配额的前提下测网关的多 Key 轮询容量。
//
// 用法：
//
//	go run ./tools/mockupstream -addr :9100 -rpm 120 -latency 300 -jitter 150
//
// 网关渠道 base_url 指到 http://127.0.0.1:9100，Key 池每行一把 mk-xxx；
// -rpm 0 表示不限速（测网关自身吞吐时用）。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"
)

var (
	addr       = flag.String("addr", ":9100", "监听地址")
	rpm        = flag.Int("rpm", 120, "每把 key 每分钟允许的请求数；0=不限")
	latency    = flag.Int("latency", 300, "基础响应延迟（毫秒）")
	jitter     = flag.Int("jitter", 150, "延迟抖动幅度（毫秒，±）")
	streamHold = flag.Int("stream_hold", 0, "流式响应总时长（毫秒），用于压测并发长连接；0=默认三块")
)

// keyWindow 按 key 维护 60s 滑动窗口的请求时间戳（RPM 判额）
type keyWindow struct {
	mu       sync.Mutex
	hits     map[string][]time.Time
	limit429 map[string]int
	total    int
	four29   int
}

func newKeyWindow() *keyWindow {
	return &keyWindow{hits: map[string][]time.Time{}, limit429: map[string]int{}}
}

// allow 判额：窗口内已满则记一次 429 并拒绝（不把被拒请求记入窗口）
func (w *keyWindow) allow(key string, rpm int) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	cut := now.Add(-time.Minute)
	fresh := make([]time.Time, 0, len(w.hits[key]))
	for _, t := range w.hits[key] {
		if t.After(cut) {
			fresh = append(fresh, t)
		}
	}
	if rpm > 0 && len(fresh) >= rpm {
		w.four29++
		w.limit429[key]++
		return false
	}
	w.hits[key] = append(fresh, now)
	w.total++
	return true
}

func (w *keyWindow) snapshot() (total, four29 int, perKey map[string]int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	perKey = make(map[string]int, len(w.hits))
	cut := time.Now().Add(-time.Minute)
	for k, hits := range w.hits {
		n := 0
		for _, t := range hits {
			if t.After(cut) {
				n++
			}
		}
		perKey[k] = n
	}
	return w.total, w.four29, perKey
}

var win = newKeyWindow()

type chatReq struct {
	Model   string `json:"model"`
	Stream  bool   `json:"stream"`
	MaxTok  int    `json:"max_tokens"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

func bearerKey(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	return strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// openaiError 模拟厂商的错误响应体格式
func openaiError(w http.ResponseWriter, status int, code, msg string, retryAfter int) {
	if retryAfter > 0 {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
	}
	writeJSON(w, status, map[string]any{
		"error": map[string]any{"code": code, "message": msg, "type": code},
	})
}

// sleepLatency 模拟上游推理耗时：基础延迟 ± 抖动
func sleepLatency() {
	j := 0
	if *jitter > 0 {
		j = rand.Intn(2*(*jitter)+1) - *jitter
	}
	time.Sleep(time.Duration(*latency+j) * time.Millisecond)
}

func fakeUsage() map[string]int {
	completion := 24 + rand.Intn(96)
	prompt := 32 + rand.Intn(64)
	return map[string]int{"prompt_tokens": prompt, "completion_tokens": completion, "total_tokens": prompt + completion}
}

func handleChat(w http.ResponseWriter, r *http.Request) {
	key := bearerKey(r)
	if key == "" {
		openaiError(w, http.StatusUnauthorized, "invalid_api_key", "missing bearer key", 0)
		return
	}
	var req chatReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil || req.Model == "" {
		openaiError(w, http.StatusBadRequest, "invalid_request_error", "invalid JSON body or missing model", 0)
		return
	}
	if !win.allow(key, *rpm) {
		openaiError(w, http.StatusTooManyRequests, "rate_limit_exceeded",
			fmt.Sprintf("key %s exceeded %d rpm (sliding window)", mask(key), *rpm), 5)
		return
	}
	sleepLatency()
	if req.Stream {
		writeStream(w, req.Model)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":      fmt.Sprintf("chatcmpl-mock-%d", time.Now().UnixNano()),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   req.Model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": "mock 上游的固定回复，用于压测计量。"},
			"finish_reason": "stop",
		}},
		"usage": fakeUsage(),
	})
}

// writeStream 模拟 SSE：两个内容块 + 带 usage 的收尾块 + [DONE]
func writeStream(w http.ResponseWriter, model string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	fl := w.(http.Flusher)
	chunk := func(delta map[string]any, finish any, usage map[string]int) {
		ev := map[string]any{
			"id": fmt.Sprintf("chatcmpl-mock-%d", time.Now().UnixNano()),
			"object": "chat.completion.chunk", "model": model,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
		}
		if usage != nil {
			ev["usage"] = usage
		}
		b, _ := json.Marshal(ev)
		fmt.Fprintf(w, "data: %s\n\n", b)
		fl.Flush()
	}
	if *streamHold > 0 {
		// 压测模式：每 500ms 一块，撑满 stream_hold 毫秒，模拟真实长流
		for i := 0; i < *streamHold/500; i++ {
			chunk(map[string]any{"content": "…"}, nil, nil)
			time.Sleep(500 * time.Millisecond)
		}
	} else {
		chunk(map[string]any{"role": "assistant", "content": "mock "}, nil, nil)
		time.Sleep(60 * time.Millisecond)
		chunk(map[string]any{"content": "流式回复"}, nil, nil)
		time.Sleep(60 * time.Millisecond)
	}
	chunk(map[string]any{}, "stop", fakeUsage())
	fmt.Fprint(w, "data: [DONE]\n\n")
	fl.Flush()
}

func mask(k string) string {
	if len(k) <= 6 {
		return k
	}
	return k[:6] + "..."
}

func statsLoop() {
	for range time.Tick(5 * time.Second) {
		total, four29, perKey := win.snapshot()
		parts := make([]string, 0, len(perKey))
		for k, n := range perKey {
			parts = append(parts, fmt.Sprintf("%s=%d", mask(k), n))
		}
		log.Printf("total=%d 429=%d window[%s]", total, four29, strings.Join(parts, " "))
	}
}

func main() {
	flag.Parse()
	go statsLoop()
	http.HandleFunc("/v1/chat/completions", handleChat)
	// 健康检查（渠道连通性测试用）
	http.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	log.Printf("mock upstream listening on %s (rpm=%d/key, latency=%d±%dms)", *addr, *rpm, *latency, *jitter)
	if err := http.ListenAndServe(*addr, nil); err != nil {
		log.Fatal(err)
	}
}
