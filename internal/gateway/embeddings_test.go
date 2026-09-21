package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const embedOkBody = `{"object":"list","data":[{"object":"embedding","embedding":[0.1,-0.2,0.3],"index":0}],"model":"m1","usage":{"prompt_tokens":7}}`

// 基本链路：路径改写为 /v1/embeddings、上游收到原样 body、completion=0 退化计费（7×2 元/M → 14）
func TestEmbeddingsBasic(t *testing.T) {
	e := newTestEnv(t)
	var mu sync.Mutex
	var gotPath, gotBody string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotPath, gotBody = r.URL.Path, string(b)
		mu.Unlock()
		_, _ = w.Write([]byte(embedOkBody))
	}))
	defer up.Close()
	e.seedUpstreamChannel(t, 1, "ch", up.URL, nil, 10)

	w := e.postEmbed(`{"model":"m1","input":"hello text"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("应 200，got %d body=%s", w.Code, w.Body)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotPath != "/v1/embeddings" {
		t.Fatalf("出站路径应改写为 /v1/embeddings，got %s", gotPath)
	}
	if !strings.Contains(gotBody, `"model":"m1"`) || !strings.Contains(gotBody, `"input":"hello text"`) {
		t.Fatalf("上游应收到原样 body，got %s", gotBody)
	}
	cost, cacheHit, status := e.usageRow(t, e.lastUsage(t))
	if status != 200 || cacheHit != 0 || cost != 14 {
		t.Fatalf("计费应 7×2=14，got cost=%d cache_hit=%d status=%d", cost, cacheHit, status)
	}
}

// 未授权模型 → 403 model_not_allowed（授权链路对 embeddings 同样生效）
func TestEmbeddingsUnauthorized(t *testing.T) {
	e := newTestEnv(t)
	mustExec(t, e.f, `INSERT INTO models (name, input_price, output_price, status, created_at, updated_at)
		VALUES ('m2', 500000, 0, 1, 0, 0)`) // 未授予 tester
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("未授权不应触达上游")
	}))
	defer up.Close()
	e.seedUpstreamChannel(t, 1, "ch", up.URL, nil, 10)

	w := e.postEmbed(`{"model":"m2","input":"x"}`)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "model_not_allowed") {
		t.Fatalf("应 403 model_not_allowed，got %d body=%s", w.Code, w.Body)
	}
}

// 缺 input → 400；缺 model → 400（与 chat 同）
func TestEmbeddingsMissingInput(t *testing.T) {
	e := newTestEnv(t)
	w := e.postEmbed(`{"model":"m1"}`)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "missing required parameter: input") {
		t.Fatalf("缺 input 应 400，got %d body=%s", w.Code, w.Body)
	}
}

// 缓存：相同请求第二次命中（body 逐字节相同、cost=0）；input 数组顺序不同不命中
func TestEmbeddingsCache(t *testing.T) {
	e := newTestEnv(t)
	e.h.CacheTTL = time.Minute
	var hits int
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte(embedOkBody))
	}))
	defer up.Close()
	e.seedUpstreamChannel(t, 1, "ch", up.URL, nil, 10)

	w1 := e.postEmbed(`{"model":"m1","input":["a","b"]}`)
	if w1.Code != http.StatusOK || w1.Header().Get("X-Tg-Cache") != "" {
		t.Fatalf("首次应 200 且非缓存，got %d cache=%q", w1.Code, w1.Header().Get("X-Tg-Cache"))
	}
	w2 := e.postEmbed(`{"model":"m1","input":["a","b"]}`)
	if w2.Code != http.StatusOK || w2.Header().Get("X-Tg-Cache") != "hit" {
		t.Fatalf("第二次应命中缓存，got %d cache=%q", w2.Code, w2.Header().Get("X-Tg-Cache"))
	}
	if w1.Body.String() != w2.Body.String() {
		t.Fatalf("缓存回放应逐字节相同")
	}
	cost, cacheHit, _ := e.usageRow(t, e.lastUsage(t))
	if cacheHit != 1 || cost != 0 {
		t.Fatalf("缓存命中应 cost=0 cache_hit=1，got cost=%d cache_hit=%d", cost, cacheHit)
	}
	// input 顺序不同 = 不同请求，不命中
	w3 := e.postEmbed(`{"model":"m1","input":["b","a"]}`)
	if w3.Code != http.StatusOK || w3.Header().Get("X-Tg-Cache") == "hit" {
		t.Fatalf("input 顺序不同不应命中，got %d cache=%q", w3.Code, w3.Header().Get("X-Tg-Cache"))
	}
	if hits != 2 {
		t.Fatalf("上游应被打 2 次（首次+顺序变化），got %d", hits)
	}
}

// Key 池与 429 账户级冷却对 embeddings 同样生效：2 把 Key 池，上游 429 → 只打 1 次上游、全池冷却、回 429
func TestEmbeddingsKeyPoolCooldown(t *testing.T) {
	e := newTestEnv(t)
	var hits int
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer up.Close()
	e.seedUpstreamChannel(t, 1, "ch", up.URL, []string{"k1", "k2"}, 10)

	w := e.postEmbed(`{"model":"m1","input":"x"}`)
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "upstream_busy") {
		t.Fatalf("应 429 upstream_busy，got %d body=%s", w.Code, w.Body)
	}
	if hits != 1 {
		t.Fatalf("账户级冷却应只打上游 1 次，got %d", hits)
	}
	if !e.cd.IsCooling(scopeOf(1, 1)) || !e.cd.IsCooling(scopeOf(1, 2)) {
		t.Fatal("全池 Key 应进入冷却")
	}
}

// 模型映射对 embeddings 同样生效（共用 relay 内核）：出站改写为上游名，
// 响应改写回对外名，计费锚定对外名
func TestEmbeddingsModelMapping(t *testing.T) {
	e := newTestEnv(t)
	var mu sync.Mutex
	var gotModel string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var req struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(b, &req)
		mu.Lock()
		gotModel = req.Model
		mu.Unlock()
		// 按上游模型名应答（真实厂商行为）
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"model":"` + req.Model + `","usage":{"prompt_tokens":7,"total_tokens":7}}`))
	}))
	defer up.Close()
	e.seedMappedChannel(t, 1, "ch-emb-map", up.URL)

	w := e.postEmbed(`{"model":"m1","input":"map me"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("应 200，got %d body=%s", w.Code, w.Body)
	}
	mu.Lock()
	upModel := gotModel
	mu.Unlock()
	if upModel != "up-m1" {
		t.Fatalf("上游应收到的模型名为 up-m1，got %s", upModel)
	}
	if !strings.Contains(w.Body.String(), `"model":"m1"`) || strings.Contains(w.Body.String(), "up-m1") {
		t.Fatalf("客户端应看到对外名且无上游名泄漏: %s", w.Body)
	}
	var billed string
	_ = e.f.db.Raw(`SELECT model_name FROM usage_logs ORDER BY id DESC LIMIT 1`).Scan(&billed).Error
	if billed != "m1" {
		t.Fatalf("计费应锚定对外名 m1，got %s", billed)
	}
}
