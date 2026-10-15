package gateway

import (
	"context"
	"net"
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

// B4：客户端中途断开导致的请求失败不是渠道的错——不计熔断（连续断开多次
// 不得触发自动禁用），usage_logs 记 499；且上游照常生成并收费，断连收割
// 计费照常结算（逃单不可行：故意掐线不能躲掉已生成的 token 费用）
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
		e.engine.ServeHTTP(w, req) // 断连收割：等到上游生成完（含 usage）才返回
	}
	waitFor(t, 2*time.Second, func() bool { return hits.Load() >= 5 })

	var status int64
	_ = e.f.db.Raw("SELECT status FROM channels WHERE id = 1").Scan(&status).Error
	if status != 1 {
		t.Fatalf("客户端断开不应计入熔断：渠道被误禁用（status=%d）", status)
	}
	var row struct {
		N                int64
		Status           int64
		PromptTokens     int64
		CompletionTokens int64
		Cost             int64
	}
	_ = e.f.db.Raw(`SELECT COUNT(*) AS n FROM usage_logs WHERE status = 499`).Scan(&row.N).Error
	if row.N == 0 {
		t.Fatal("客户端断开应记 499 usage_log")
	}
	_ = e.f.db.Raw(`SELECT status, prompt_tokens, completion_tokens, cost FROM usage_logs
		WHERE status = 499 ORDER BY id DESC LIMIT 1`).Scan(&row).Error
	if row.Status != 499 {
		t.Fatalf("断连收割应记 499，got %d", row.Status)
	}
	// okBody usage 7/3 ×（¥2/M 入 ¥8/M 出）= 14+24 = 38 点：客户没收到但上游生成了，照扣
	if row.PromptTokens != 7 || row.CompletionTokens != 3 || row.Cost != 38 {
		t.Fatalf("断连收割应按上游 usage 全额计费: 入=%d 出=%d cost=%d", row.PromptTokens, row.CompletionTokens, row.Cost)
	}
}

// B4 流式版：SSE 客户端中途断开 → usage_logs 同样记 499（与非流式同口径，
// 错误率统计不把断连误算成成功 200）；末块 usage 经断连收割到达 → 正常计量计费；
// 不计熔断
func TestClientCancelDuringStreamHarvestsUsage(t *testing.T) {
	e := newTestEnv(t)
	e.h.Breaker = NewBreaker(3)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"he\"}}]}\n\n"))
		w.(http.Flusher).Flush()
		time.Sleep(250 * time.Millisecond) // 断连落在飞行途中（首块后、usage 块前）
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{}}],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":4}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer up.Close()
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
	start := time.Now()
	e.engine.ServeHTTP(httptest.NewRecorder(), req) // 收割模式：读到 usage 块才返回
	if elapsed := time.Since(start); elapsed < 200*time.Millisecond {
		t.Fatalf("断连后应继续读上游收割 usage（约等到 250ms 生成完），实际 %s 就返回了", elapsed)
	}

	var row struct{ Status, NoUsage, Prompt, Completion, Cost int64 }
	_ = e.f.db.Raw(`SELECT status, no_usage, prompt_tokens AS prompt, completion_tokens AS completion, cost
		FROM usage_logs WHERE is_stream = 1`).Scan(&row).Error
	if row.Status != 499 {
		t.Fatalf("流式断连应记 499（与非流式同口径），got status=%d", row.Status)
	}
	// 9/4 ×（¥2/M 入 ¥8/M 出）= 18+32 = 50 点：usage 块收割成功，照常计费
	if row.NoUsage != 0 || row.Prompt != 9 || row.Completion != 4 || row.Cost != 50 {
		t.Fatalf("断连收割应按末块 usage 正常计量计费: %+v", row)
	}
	var status int64
	_ = e.f.db.Raw("SELECT status FROM channels WHERE id = 1").Scan(&status).Error
	if status != 1 {
		t.Fatalf("流式断连不应计入熔断：渠道被误禁用（status=%d）", status)
	}
}

// B4 收割上限：客户端断开后收割窗口届满仍未读完 → 放弃等待（有界返回、
// usage 未到手则不计费）——防上游悬挂读把 goroutine 与连接拖成无界泄漏
func TestHarvestWindowBounds(t *testing.T) {
	e := newTestEnv(t)
	e.h.HarvestTimeout = 120 * time.Millisecond
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond) // 远超收割窗口
		_, _ = w.Write([]byte(okBody))
	}))
	defer up.Close()
	e.seedUpstreamChannel(t, 1, "ch", up.URL, nil, 10)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(chatBody("harvest-cap", "")))
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	e.engine.ServeHTTP(httptest.NewRecorder(), req)
	if elapsed := time.Since(start); elapsed > 400*time.Millisecond {
		t.Fatalf("收割窗口届满应放弃等待（≈30ms 断开 + 120ms 窗口），实际等了 %s", elapsed)
	}
	var row struct{ Status, Prompt, Cost int64 }
	_ = e.f.db.Raw(`SELECT status, prompt_tokens AS prompt, cost FROM usage_logs
		WHERE model_name = 'm1' ORDER BY id DESC LIMIT 1`).Scan(&row).Error
	if row.Status != 499 || row.Prompt != 0 || row.Cost != 0 {
		t.Fatalf("收割窗口届满 usage 未到手：应记 499 且不计费, got %+v", row)
	}
}

// B7：上游流式响应中途硬断（读错误，非客户端断开）必须计入渠道失败——
// 否则持续断流的坏渠道永远不被熔断，客户端一直收到截断流（非流式同错已计数，口径须一致）。
// 对照：上游干净关连接（无 [DONE] 的 EOF）是 SSE 合法结束方式，不计失败。
func TestStreamUpstreamResetCountsChannelFailure(t *testing.T) {
	e := newTestEnv(t)
	e.h.Breaker = NewBreaker(3)
	before := e.m.UpstreamErrors.Value()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"A\"}}]}\n\n"))
		w.(http.Flusher).Flush()
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("httptest 不支持 Hijack")
			return
		}
		conn, _, _ := hj.Hijack()
		tcp, ok := conn.(*net.TCPConn)
		if !ok {
			_ = conn.Close()
			return
		}
		_ = tcp.SetLinger(0)
		_ = tcp.Close() // RST：网关侧读到 read: connection reset
	}))
	defer up.Close()
	e.seedUpstreamChannel(t, 1, "ch", up.URL, nil, 10)

	for i := 0; i < 3; i++ { // 连续 3 次中途断流 == 熔断阈值
		w := e.post(chatBody("rst-me", `,"stream":true`))
		if w.Code != http.StatusOK {
			t.Fatalf("首块已写出，状态码应保持 200，got %d", w.Code)
		}
	}
	waitFor(t, 2*time.Second, func() bool { return e.m.UpstreamErrors.Value()-before >= 3 })
	var status int64
	waitFor(t, 2*time.Second, func() bool { // DisableChannel 异步执行
		_ = e.f.db.Raw("SELECT status FROM channels WHERE id = 1").Scan(&status).Error
		return status == 0
	})
	if status != 0 {
		t.Fatalf("连续中途断流应触发熔断自动禁用（status=%d）", status)
	}
	// 禁用后的第 4 笔：m1 失去唯一可用上游，随渠道熔断被联动自动停用 →
	// 干净的 404（模型不可用），客户端不再收到截断流
	if w := e.post(chatBody("rst-me", `,"stream":true`)); w.Code != http.StatusNotFound {
		t.Fatalf("渠道熔断、模型自动停用后应 404，got %d: %s", w.Code, w.Body.String())
	}
	var mRow struct{ Status, AutoAt int64 }
	_ = e.f.db.Raw("SELECT status AS status, auto_disabled_at AS auto_at FROM models WHERE name = 'm1'").Scan(&mRow)
	if mRow.Status != 0 || mRow.AutoAt <= 0 {
		t.Fatalf("唯一渠道熔断后模型应联动自动停用（status=0 且 auto_disabled_at>0），得 status=%d auto=%d", mRow.Status, mRow.AutoAt)
	}
}

// B7 对照组：上游写完事件后干净关连接（无 [DONE]）——SSE 允许的结束方式，
// 不计渠道失败、不熔断；usage 未达 → no_usage 不计量
func TestStreamUpstreamCleanEOFNotFailure(t *testing.T) {
	e := newTestEnv(t)
	e.h.Breaker = NewBreaker(3)
	before := e.m.UpstreamErrors.Value()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"A\"}}]}\n\n"))
		w.(http.Flusher).Flush()
		// 正常 return：TCP FIN，网关读到干净 EOF（无 [DONE]）
	}))
	defer up.Close()
	e.seedUpstreamChannel(t, 1, "ch", up.URL, nil, 10)

	for i := 0; i < 5; i++ {
		w := e.post(chatBody("eof-me", `,"stream":true`))
		if w.Code != http.StatusOK {
			t.Fatalf("EOF 收流应保持 200，got %d", w.Code)
		}
	}
	time.Sleep(150 * time.Millisecond) // 给可能的误计数留出时间
	if got := e.m.UpstreamErrors.Value() - before; got != 0 {
		t.Fatalf("干净 EOF 不应计渠道失败，upstream_errors +%d", got)
	}
	var status int64
	_ = e.f.db.Raw("SELECT status FROM channels WHERE id = 1").Scan(&status).Error
	if status != 1 {
		t.Fatalf("干净 EOF 不应熔断（status=%d）", status)
	}
	var row struct{ NoUsage, Cost int64 }
	_ = e.f.db.Raw("SELECT no_usage, cost FROM usage_logs WHERE is_stream = 1").Scan(&row).Error
	if row.NoUsage != 1 || row.Cost != 0 {
		t.Fatalf("无 [DONE] 无 usage 应不计量: %+v", row)
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
