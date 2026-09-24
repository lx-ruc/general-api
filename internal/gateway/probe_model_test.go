package gateway

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// 探活选模：渠道带 embedding 模型时必须挑 chat 模型发 chat 请求
// （否则 embedding-3 按 chat 协议探测必然 400，体检会误杀健康渠道）
func TestProbePicksChatModelOverEmbedding(t *testing.T) {
	e := newTestEnv(t)
	var mu sync.Mutex
	gotPath := ""
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath = r.URL.Path
		mu.Unlock()
		_, _ = w.Write([]byte(okBody))
	}))
	defer up.Close()
	e.seedUpstreamChannel(t, 1, "ch", up.URL, []string{"k1"}, 10)
	// 字母序 embedding-3 在前；另挂一个 chat 模型
	mustExec(t, e.f, `INSERT INTO channel_abilities (channel_id, model_name) VALUES (1, 'embedding-3')`)

	res := ProbeChannel(e.h.DB, e.h.Cipher, e.h.Client, e.h.Coord, e.h.KeyCooldown, 1)
	if !res.OK || res.Err != "" {
		t.Fatalf("健康渠道应探测成功，got ok=%v err=%s", res.OK, res.Err)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("应挑 chat 模型探测 chat 端点，got path=%s", gotPath)
	}
}

// 纯 embedding 渠道：改发 embeddings 请求（路径与请求体都换）
func TestProbeEmbeddingsOnlyChannel(t *testing.T) {
	e := newTestEnv(t)
	var mu sync.Mutex
	gotPath, gotBody := "", ""
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 512)
		n, _ := r.Body.Read(buf)
		mu.Lock()
		gotPath, gotBody = r.URL.Path, string(buf[:n])
		mu.Unlock()
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1]}],"usage":{"prompt_tokens":1}}`))
	}))
	defer up.Close()
	e.seedUpstreamChannel(t, 1, "ch", up.URL, []string{"k1"}, 10)
	mustExec(t, e.f, `DELETE FROM channel_abilities WHERE channel_id = 1`)
	mustExec(t, e.f, `INSERT INTO channel_abilities (channel_id, model_name) VALUES (1, 'text-embedding-v4')`)

	res := ProbeChannel(e.h.DB, e.h.Cipher, e.h.Client, e.h.Coord, e.h.KeyCooldown, 1)
	if !res.OK || res.Err != "" {
		t.Fatalf("纯 embedding 渠道应探测成功，got ok=%v err=%s", res.OK, res.Err)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotPath != "/v1/embeddings" {
		t.Fatalf("embedding 渠道应探测 /v1/embeddings，got %s", gotPath)
	}
	if gotBody != `{"model":"text-embedding-v4","input":"ping"}` {
		t.Fatalf("embedding 探测请求体不符，got %s", gotBody)
	}
}
