package gateway

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// ---- 账户级冷却（key_cooldown_scope: channel，默认）----

// 同渠道 3 把 Key：第一把 429 后全池一起冷却，本请求不再逐个试错（上游只被打 1 次），
// 后续请求直接走"全部冷却"快速路径
func TestAccountWideCooldownCoolsWholePool(t *testing.T) {
	e := newTestEnv(t)
	var hits atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer up.Close()
	e.seedUpstreamChannel(t, 1, "ch", up.URL, []string{"k1", "k2", "k3"}, 10)

	w := e.post(chatBody("q", ""))
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "upstream_busy") {
		t.Fatalf("应回 429 upstream_busy，got %d body=%s", w.Code, w.Body)
	}
	// 关键断言：渠道级冷却后，同渠道其余 Key 不再被尝试（厂商限额按账户，重试只会再吃 429）
	if hits.Load() != 1 {
		t.Fatalf("渠道级冷却应只打上游 1 次，got %d", hits.Load())
	}
	for kid := 1; kid <= 3; kid++ {
		if !e.cd.IsCooling(scopeOf(1, int64(kid))) {
			t.Fatalf("渠道 1 的 key #%d 应进入冷却", kid)
		}
	}
	if e.m.KeyCooldown.Value() != 3 {
		t.Fatalf("key_cooldown 指标应按冷却 Key 计数=3，got %d", e.m.KeyCooldown.Value())
	}
}

// key_cooldown_scope: key 时只冷却当前 Key：本请求继续换下一把试（3 把各挨一次 429）
func TestKeyScopeCooldownOnlyCurrentKey(t *testing.T) {
	e := newTestEnv(t)
	e.h.KeyCooldownScope = "key"
	var hits atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer up.Close()
	e.seedUpstreamChannel(t, 1, "ch", up.URL, []string{"k1", "k2", "k3"}, 10)

	w := e.post(chatBody("q", ""))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("应回 429，got %d", w.Code)
	}
	if hits.Load() != 3 {
		t.Fatalf("key 粒度应逐把尝试（3 次），got %d", hits.Load())
	}
}

// ---- 重试预算（max_candidates）----

// 3 个渠道全 5xx，max_candidates=2：只打 2 次上游即熔线，回 502
func TestMaxCandidatesBudget(t *testing.T) {
	e := newTestEnv(t)
	e.h.MaxCandidates = 2
	var hits atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer up.Close()
	e.seedUpstreamChannel(t, 1, "c1", up.URL, nil, 30)
	e.seedUpstreamChannel(t, 2, "c2", up.URL, nil, 20)
	e.seedUpstreamChannel(t, 3, "c3", up.URL, nil, 10)

	w := e.post(chatBody("q", ""))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("预算耗尽应回 502，got %d body=%s", w.Code, w.Body)
	}
	if hits.Load() != 2 {
		t.Fatalf("预算=2 应只尝试 2 个候选，got %d", hits.Load())
	}
}

// 预算触发不改变失败分类：唯一候选 429 + 预算=1 → 仍按限流语义回 429（而非 502）
func TestBudgetKeepsRateLimitClassification(t *testing.T) {
	e := newTestEnv(t)
	e.h.MaxCandidates = 1
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer up.Close()
	e.seedUpstreamChannel(t, 1, "c1", up.URL, nil, 30)

	w := e.post(chatBody("q", ""))
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "upstream_busy") {
		t.Fatalf("预算触发应保留 429 分类，got %d body=%s", w.Code, w.Body)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("应携带 Retry-After")
	}
}

// ---- 状态码处置可配置 ----

// 把 429 从"换 Key"改配为"换渠道"：A 渠道 429 直接跳渠道（Key 不冷却），B 渠道 200
func TestRetryChannelCodesConfigurable(t *testing.T) {
	e := newTestEnv(t)
	e.h.RetryKeyCodes = map[int]bool{}      // 429 不再换 Key
	e.h.RetryChannelCodes = parseCodeSet([]string{"429", "5xx"}) // 429 升级为渠道级故障
	upA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer upA.Close()
	upB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(okBody))
	}))
	defer upB.Close()
	e.seedUpstreamChannel(t, 1, "ch429", upA.URL, []string{"k1", "k2"}, 10)
	e.seedUpstreamChannel(t, 2, "chok", upB.URL, nil, 1)

	w := e.post(chatBody("q", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("429 配置为换渠道后应降级成功，got %d body=%s", w.Code, w.Body)
	}
	if got := w.Header().Get("X-Tg-Channel-Id"); got != "2" {
		t.Fatalf("应由渠道 2 应答，got %q", got)
	}
	if e.cd.IsCooling(scopeOf(1, 1)) || e.cd.IsCooling(scopeOf(1, 2)) {
		t.Fatal("渠道级处置下 429 不应冷却 Key")
	}
}

// scopeOf 测试辅助：拼 Key 冷却 scope
func scopeOf(channelID, keyID int64) string {
	return fmt.Sprintf("ck:%d:%d", channelID, keyID)
}
