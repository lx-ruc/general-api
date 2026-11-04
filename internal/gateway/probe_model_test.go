package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
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

// 视频渠道探测：GET 不存在的任务 id（绝不 POST 真创建请求——会真的建任务计费）；
// 404 = 鉴权通过端点正确 → 健康。回归旧逻辑：chat body POST 打任务端点必 400 被体检误杀
func TestProbeVideoChannelGet(t *testing.T) {
	e := newTestEnv(t)
	var mu sync.Mutex
	var gotMethod, gotPath, gotBody string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotMethod, gotPath, gotBody = r.Method, r.URL.Path, string(b)
		mu.Unlock()
		// Ark 对不存在的任务 id 回 404（鉴权已通过）
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"task not found"}}`))
	}))
	defer up.Close()
	now := time.Now().Unix()
	mustExec(t, e.f, `INSERT INTO models (name, type, status, created_at, updated_at)
		VALUES ('seedance-1', 'video', 1, ?, ?)`, now, now)
	mustExec(t, e.f, `INSERT INTO channels (id,name,base_url,path,upstream_key_enc,weight,priority,status,created_at,updated_at)
		VALUES (1,'video-ch',?, '/contents/generations/tasks', 'legacy-key', 1, 10, 1, ?, ?)`, up.URL, now, now)
	mustExec(t, e.f, `INSERT INTO channel_abilities (channel_id, model_name) VALUES (1, 'seedance-1')`)

	res := ProbeChannel(e.h.DB, e.h.Cipher, e.h.Client, e.h.Coord, e.h.KeyCooldown, 1)
	if !res.OK || res.Err != "" {
		t.Fatalf("视频渠道 404 探测应判健康，got ok=%v err=%s", res.OK, res.Err)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotMethod != http.MethodGet || gotBody != "" {
		t.Fatalf("视频探测应为 GET 无 body，got %s %q", gotMethod, gotBody)
	}
	if gotPath != "/contents/generations/tasks/cgt-probe-0000000000000000" {
		t.Fatalf("视频探测应打到任务端点+假任务 id，got %s", gotPath)
	}
}

// 视频 Key 失效（401）：走既有禁 Key 分类（可换池内下一把），不放行为健康
func TestProbeVideoKeyRejected(t *testing.T) {
	e := newTestEnv(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	defer up.Close()
	now := time.Now().Unix()
	mustExec(t, e.f, `INSERT INTO models (name, type, status, created_at, updated_at)
		VALUES ('seedance-1', 'video', 1, ?, ?)`, now, now)
	mustExec(t, e.f, `INSERT INTO channels (id,name,base_url,path,upstream_key_enc,weight,priority,status,created_at,updated_at)
		VALUES (1,'video-ch',?, '/contents/generations/tasks', 'legacy-key', 1, 10, 1, ?, ?)`, up.URL, now, now)
	mustExec(t, e.f, `INSERT INTO channel_abilities (channel_id, model_name) VALUES (1, 'seedance-1')`)

	res := ProbeChannel(e.h.DB, e.h.Cipher, e.h.Client, e.h.Coord, e.h.KeyCooldown, 1)
	if res.OK {
		t.Fatal("401 Key 失效不得判健康")
	}
	if res.Status != http.StatusUnauthorized {
		t.Fatalf("应保留上游 401 状态，got %d", res.Status)
	}
}
