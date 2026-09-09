// loadgen 是轻量压测器：按目标 rps + 并发 worker 打网关 /v1/chat/completions，
// 统计状态码分布与延迟分位数。每个请求的消息带唯一序号，避免命中网关的精确缓存。
//
// 用法：
//
//	go run ./tools/loadgen -c 8 -rps 3 -d 60s \
//	  -key sk-xxx -model mock-chat [-stream]
//
// -rps 0 表示不限速（worker 全速打，测的是并发上限而非吞吐节奏）。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

var (
	url    = flag.String("url", "http://127.0.0.1:8081/v1/chat/completions", "网关端点")
	key    = flag.String("key", "", "API key（必填）")
	model  = flag.String("model", "mock-chat", "模型名")
	rps    = flag.Int("rps", 0, "目标速率（请求/秒）；0=不限速")
	conc   = flag.Int("c", 8, "并发 worker 数")
	dur    = flag.Duration("d", 30*time.Second, "压测时长")
	stream = flag.Bool("stream", false, "用 SSE 流式请求")
)

type result struct {
	status   int64 // http 状态码；0=传输层错误
	latencyMs int64
}

func main() {
	flag.Parse()
	if *key == "" {
		fmt.Fprintln(os.Stderr, "必须指定 -key")
		os.Exit(1)
	}

	var (
		mu       sync.Mutex
		latencies []float64
		statuses  = map[int64]int64{}
		sendErrs  int64
		sent      int64
	)
	record := func(r result) {
		mu.Lock()
		defer mu.Unlock()
		if r.status == 0 {
			sendErrs++
		}
		statuses[r.status]++
		latencies = append(latencies, float64(r.latencyMs))
	}

	// 节流：固定间隔发令牌；不限速时用带缓冲的令牌槽让 worker 全速跑
	tokens := make(chan struct{})
	stopPacer := make(chan struct{})
	if *rps > 0 {
		go func() {
			t := time.NewTicker(time.Second / time.Duration(*rps))
			defer t.Stop()
			for {
				select {
				case <-t.C:
					select {
					case tokens <- struct{}{}:
					default: // worker 消费不过来时丢弃令牌，避免堆积造成压测尾部长尾
					}
				case <-stopPacer:
					return
				}
			}
		}()
	} else {
		tokens = make(chan struct{}, 1<<20)
		go func() {
			for {
				select {
				case tokens <- struct{}{}:
				case <-stopPacer:
					return
				}
			}
		}()
	}

	// 流式响应不设整体超时（SSE 长连接），只约束响应头到达时间
	client := &http.Client{Transport: &http.Transport{
		MaxIdleConns: 256, MaxIdleConnsPerHost: 256,
		ResponseHeaderTimeout: 30 * time.Second,
	}}

	ctx, cancel := context.WithTimeout(context.Background(), *dur)
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < *conc; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case <-tokens:
				}
				n := atomic.AddInt64(&sent, 1)
				start := time.Now()
				status, err := fire(client, n)
				if err != nil {
					record(result{status: 0, latencyMs: time.Since(start).Milliseconds()})
					continue
				}
				record(result{status: int64(status), latencyMs: time.Since(start).Milliseconds()})
			}
		}(i)
	}

	start := time.Now()
	wg.Wait()
	elapsed := time.Since(start)
	close(stopPacer)

	// ---- 汇总 ----
	total := int64(0)
	for _, n := range statuses {
		total += n
	}
	keys := make([]int64, 0, len(statuses))
	for k := range statuses {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	fmt.Printf("时长 %.1fs  发出 %d  完成 %d  实测 %.1f rps  传输错误 %d\n",
		elapsed.Seconds(), atomic.LoadInt64(&sent), total,
		float64(total)/elapsed.Seconds(), sendErrs)
	for _, k := range keys {
		label := fmt.Sprintf("HTTP %d", k)
		if k == 0 {
			label = "传输错误"
		}
		fmt.Printf("  %-10s %6d  (%.1f%%)\n", label, statuses[k], 100*float64(statuses[k])/float64(total))
	}
	printLatency(latencies)
}

// fire 发一个对话请求并完整读完响应体（流式读到 [DONE]/EOF），返回状态码
func fire(client *http.Client, seq int64) (int, error) {
	body, _ := json.Marshal(map[string]any{
		"model":      *model,
		"max_tokens": 64,
		"stream":     *stream,
		"messages": []any{map[string]any{
			"role": "user", "content": fmt.Sprintf("压测请求 #%d（唯一内容，防缓存命中）", seq),
		}},
	})
	req, err := http.NewRequest(http.MethodPost, *url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+*key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

func printLatency(lats []float64) {
	if len(lats) == 0 {
		return
	}
	sort.Float64s(lats)
	pct := func(p float64) float64 {
		i := int(p * float64(len(lats)-1))
		return lats[i]
	}
	sum := 0.0
	for _, l := range lats {
		sum += l
	}
	fmt.Printf("延迟  avg %.0fms  p50 %.0fms  p95 %.0fms  p99 %.0fms  max %.0fms\n",
		sum/float64(len(lats)), pct(0.5), pct(0.95), pct(0.99), lats[len(lats)-1])
}
