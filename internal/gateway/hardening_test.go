package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// B3：闸门排队必须受 QueueWaitTimeout 约束——即使客户端不断开，
// 等待超过上限也要放弃该渠道（不能无限等下去）
func TestQueueWaitTimeoutApplies(t *testing.T) {
	e := newTestEnv(t)
	e.h.MaxConcurrency = 1
	e.h.QueueWaitTimeout = 80 * time.Millisecond
	release := make(chan struct{})
	var hits atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		<-release // 第一笔占住唯一名额
		_, _ = w.Write([]byte(okBody))
	}))
	defer up.Close()
	e.seedUpstreamChannel(t, 1, "ch", up.URL, nil, 10)

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- e.post(chatBody("first", "")) }()
	waitFor(t, 2*time.Second, func() bool { return hits.Load() >= 1 })

	start := time.Now()
	w := e.post(chatBody("second", "")) // 客户端未断开，只能靠 QueueWaitTimeout 放弃
	elapsed := time.Since(start)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("排队超时且无备用渠道应回 502，got %d: %s", w.Code, w.Body.String())
	}
	if elapsed > 600*time.Millisecond {
		t.Fatalf("应在 QueueWaitTimeout 附近放弃等待，实际等了 %s", elapsed)
	}
	if e.m.QueueTimeouts.Value() < 1 {
		t.Fatal("排队超时应触发 queue_timeouts 指标")
	}
	close(release) // 放行第一笔
	select {       // 第一笔随后正常完成
	case w1 := <-done:
		if w1.Code != http.StatusOK {
			t.Fatalf("占坑的第一笔应 200，got %d", w1.Code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("第一笔请求超时未完成")
	}
}

// waitFor 轮询直到条件成立或超时
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("等待条件超时")
}

// B4：客户端中途断开导致的请求失败不是渠道的错——
// 不计熔断（连续断开多次不得触发自动禁用），usage_logs 记 499
func TestClientCancelNotChannelFailure(t *testing.T) {
	e := newTestEnv(t)
	e.h.Breaker = NewBreaker(3)
	var hits atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		time.Sleep(250 * time.Millisecond) // 让客户端取消落在飞行途中
		_, _ = w.Write([]byte(okBody))
	}))
	defer up.Close()
	e.seedUpstreamChannel(t, 1, "ch", up.URL, nil, 10)

	for i := 0; i < 5; i++ { // 连续 5 次断开 > 熔断阈值 3
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			strings.NewReader(chatBody("cancel-me", "")))
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
		ctx, cancel := context.WithCancel(req.Context())
		req = req.WithContext(ctx)
		w := httptest.NewRecorder()
		go func() {
			time.Sleep(30 * time.Millisecond)
			cancel()
		}()
		e.engine.ServeHTTP(w, req)
	}
	waitFor(t, 2*time.Second, func() bool { return hits.Load() >= 5 })

	var status int64
	_ = e.f.db.Raw("SELECT status FROM channels WHERE id = 1").Scan(&status).Error
	if status != 1 {
		t.Fatalf("客户端断开不应计入熔断：渠道被误禁用（status=%d）", status)
	}
	var n499 int64
	_ = e.f.db.Raw("SELECT COUNT(*) FROM usage_logs WHERE status = 499").Scan(&n499).Error
	if n499 == 0 {
		t.Fatal("客户端断开应记 499 usage_log")
	}
}

// B4 流式版：SSE 客户端中途断开 → usage_logs 同样记 499（与非流式同口径，
// 错误率统计不把断连误算成成功 200）；usage 块未到达 → 不计量（no_usage=1）；
// 不计熔断
func TestClientCancelDuringStreamLogged499(t *testing.T) {
	e := newTestEnv(t)
	e.h.Breaker = NewBreaker(3)
	block := make(chan struct{}) // 上游写完首块后挂住，制造"断连落在飞行途中"
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"he\"}}]}\n\n"))
		w.(http.Flusher).Flush()
		<-block
	}))
	defer up.Close()
	defer close(block)
	e.seedUpstreamChannel(t, 1, "ch", up.URL, nil, 10)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(chatBody("stream-cancel", `,"stream":true`)))
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	e.engine.ServeHTTP(httptest.NewRecorder(), req)

	waitFor(t, 2*time.Second, func() bool {
		var n int64
		_ = e.f.db.Raw("SELECT COUNT(*) FROM usage_logs WHERE is_stream = 1 AND status = 499").Scan(&n).Error
		return n == 1
	})
	var row struct{ Status, NoUsage, Cost int64 }
	_ = e.f.db.Raw("SELECT status, no_usage, cost FROM usage_logs WHERE is_stream = 1").Scan(&row).Error
	if row.Status != 499 {
		t.Fatalf("流式断连应记 499（与非流式同口径），got status=%d", row.Status)
	}
	if row.NoUsage != 1 || row.Cost != 0 {
		t.Fatalf("断连时 usage 块未到达，应不计量: %+v", row)
	}
	var status int64
	_ = e.f.db.Raw("SELECT status FROM channels WHERE id = 1").Scan(&status).Error
	if status != 1 {
		t.Fatalf("流式断连不应计入熔断：渠道被误禁用（status=%d）", status)
	}
}

// B6：n / max_completion_tokens / logprobs / parallel_tool_calls 参与
// 精确缓存 key——仅这些字段不同的请求不得共享缓存条目
func TestCacheKeyDistinguishesSemantics(t *testing.T) {
	e := newTestEnv(t)
	e.h.CacheTTL = time.Minute
	var hits atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(okBody))
	}))
	defer up.Close()
	e.seedUpstreamChannel(t, 1, "ch", up.URL, nil, 10)

	base := e.post(chatBody("same", ""))
	if base.Header().Get("X-Tg-Cache") == "hit" || hits.Load() != 1 {
		t.Fatalf("首请求应 miss，cache=%q hits=%d", base.Header().Get("X-Tg-Cache"), hits.Load())
	}

	// 每个变体只改一个语义字段：均不得命中
	for _, extra := range []string{
		`,"n":2`, `,"max_completion_tokens":128`, `,"logprobs":true`, `,"parallel_tool_calls":false`,
	} {
		w := e.post(chatBody("same", extra))
		if w.Code != http.StatusOK {
			t.Fatalf("变体 %s 应 200，got %d", extra, w.Code)
		}
		if w.Header().Get("X-Tg-Cache") == "hit" {
			t.Fatalf("变体 %s 语义不同，不得命中缓存", extra)
		}
	}
	if hits.Load() != 5 {
		t.Fatalf("四个变体应各自打上游（共 5 次含首笔），hits=%d", hits.Load())
	}
	// 完全相同的请求：命中
	w := e.post(chatBody("same", ""))
	if w.Header().Get("X-Tg-Cache") != "hit" {
		t.Fatal("完全相同请求应命中缓存")
	}
}

// C3：超长模型名（客户端可控文本）入库前必须截断，防 usage_logs 被撑爆
func TestModelNameTruncatedInUsageLog(t *testing.T) {
	e := newTestEnv(t)
	long := strings.Repeat("x", 5000)
	w := e.post(`{"model":"` + long + `","messages":[{"role":"user","content":"hi"}]}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("未知模型应 404，got %d", w.Code)
	}
	var name, errStr string
	_ = e.f.db.Raw("SELECT model_name, error FROM usage_logs ORDER BY id DESC LIMIT 1").Row().Scan(&name, &errStr)
	if len(name) > 193 { // 190 + "..."
		t.Fatalf("model_name 应被截断，长度 %d", len(name))
	}
	if len(errStr) > 400 {
		t.Fatalf("error 信息应被截断，长度 %d", len(errStr))
	}
}
