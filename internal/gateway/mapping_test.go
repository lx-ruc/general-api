package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// seedMappedChannel 建带模型映射的渠道（对外名 m1 → 上游名 up-m1）
func (e *testEnv) seedMappedChannel(t *testing.T, id int64, name, upstreamURL string) {
	t.Helper()
	now := time.Now().Unix()
	mustExec(t, e.f, `INSERT INTO channels (id,name,base_url,path,upstream_key_enc,weight,priority,status,created_at,updated_at)
		VALUES (?,?,?, '/v1/chat/completions', 'legacy-key', 1, 10, 1, ?, ?)`, id, name, upstreamURL, now, now)
	mustExec(t, e.f, `INSERT INTO channel_abilities (channel_id, model_name, upstream_model_name)
		VALUES (?, 'm1', 'up-m1')`, id)
}

// 映射全链路（非流式）：上游收到上游名、客户看到外部名、计费锚外部名
func TestModelMappingChatNonStream(t *testing.T) {
	e := newTestEnv(t)
	var gotModel string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bd, _ := io.ReadAll(r.Body)
		var b map[string]any
		_ = json.Unmarshal(bd, &b)
		gotModel, _ = b["model"].(string)
		// 上游按自己的模型名应答（真实厂商行为）
		_, _ = w.Write([]byte(`{"id":"x","model":"up-m1","choices":[{"message":{"content":"hi"}}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`))
	}))
	defer up.Close()
	e.seedMappedChannel(t, 1, "ch", up.URL)

	w := e.post(chatBody("q", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("应 200，got %d body=%s", w.Code, w.Body)
	}
	if gotModel != "up-m1" {
		t.Fatalf("上游应收到上游名，got %q", gotModel)
	}
	if strings.Contains(w.Body.String(), "up-m1") {
		t.Fatalf("客户不应看到上游名，body=%s", w.Body)
	}
	if !strings.Contains(w.Body.String(), `"model":"m1"`) {
		t.Fatalf("客户应看到外部名，body=%s", w.Body)
	}
	// 计费锚外部名（usage_logs.model_name 来自请求）
	var mn string
	_ = e.f.db.Raw("SELECT model_name FROM usage_logs ORDER BY id DESC LIMIT 1").Scan(&mn)
	if mn != "m1" {
		t.Fatalf("计费应锚外部名，got %q", mn)
	}
}

// 映射全链路（流式）：SSE 每块的 model 字段同样改写回外部名，usage 解析不受影响
func TestModelMappingChatStream(t *testing.T) {
	e := newTestEnv(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"x\",\"model\":\"up-m1\",\"choices\":[{\"delta\":{\"content\":\"he\"}}]}\n\n" +
			"data: {\"id\":\"x\",\"model\":\"up-m1\",\"choices\":[{\"delta\":{}}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2}}\n\n" +
			"data: [DONE]\n\n"))
	}))
	defer up.Close()
	e.seedMappedChannel(t, 1, "ch", up.URL)

	w := e.post(chatBody("q", `,"stream":true`))
	if w.Code != http.StatusOK {
		t.Fatalf("应 200，got %d body=%s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "up-m1") {
		t.Fatalf("流式客户不应看到上游名，body=%s", w.Body)
	}
	if !strings.Contains(w.Body.String(), `"model":"m1"`) {
		t.Fatalf("流式客户应看到外部名，body=%s", w.Body)
	}
	cost, _, status := e.usageRow(t, e.lastUsage(t))
	if status != 200 || cost != CalcCost(5, 2, 2000000, 8000000) {
		t.Fatalf("usage 解析应不受改写影响，got cost=%d status=%d", cost, status)
	}
}

// 无映射渠道：响应原样透传（零改写零开销路径）
func TestNoMappingPassthrough(t *testing.T) {
	e := newTestEnv(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"x","model":"m1-echo","choices":[{"message":{"content":"hi"}}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`))
	}))
	defer up.Close()
	e.seedUpstreamChannel(t, 1, "ch", up.URL, nil, 10)

	w := e.post(chatBody("q", ""))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"model":"m1-echo"`) {
		t.Fatalf("无映射应原样透传，got %d body=%s", w.Code, w.Body)
	}
}

// 映射 + 缓存：缓存写入的已是改写后的 body，回放不泄漏上游名
func TestModelMappingCacheNoLeak(t *testing.T) {
	e := newTestEnv(t)
	e.h.CacheTTL = time.Minute
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"x","model":"up-m1","choices":[{"message":{"content":"hi"}}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`))
	}))
	defer up.Close()
	e.seedMappedChannel(t, 1, "ch", up.URL)

	if w := e.post(chatBody("q", "")); w.Code != http.StatusOK {
		t.Fatalf("首次应 200，got %d", w.Code)
	}
	w2 := e.post(chatBody("q", ""))
	if w2.Header().Get("X-Tg-Cache") != "hit" {
		t.Fatal("第二次应命中缓存")
	}
	if strings.Contains(w2.Body.String(), "up-m1") {
		t.Fatalf("缓存回放不应泄漏上游名，body=%s", w2.Body)
	}
}

// swapModelBytes 覆盖带空格的 "model": "x" 形态（部分厂商/代理返回 pretty-print JSON）
func TestSwapModelBytesSpacedForm(t *testing.T) {
	in := []byte(`{"id": "x", "model": "up-m1", "choices": []}`)
	out := swapModelBytes(in, [2]string{"up-m1", "pub-m1"})
	if strings.Contains(string(out), `"model": "up-m1"`) || !strings.Contains(string(out), `"model": "pub-m1"`) {
		t.Fatalf("带空格形态应被改写，got %s", out)
	}
	// 紧凑形态不回归
	in2 := []byte(`{"model":"up-m1"}`)
	out2 := swapModelBytes(in2, [2]string{"up-m1", "pub-m1"})
	if string(out2) != `{"model":"pub-m1"}` {
		t.Fatalf("紧凑形态改写错误，got %s", out2)
	}
}
