package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"token-gateway/internal/auth"
	"token-gateway/internal/model"
)

// 视频模型测试底料：models 行 type=video + 渠道 path=/contents/generations/tasks + 授权
func (e *testEnv) seedVideo(t *testing.T, upstreamURL string, modelOutPrice int64) {
	t.Helper()
	now := time.Now().Unix()
	mustExec(t, e.f, `INSERT INTO models (name, type, input_price, output_price, cost_output_price, status, created_at, updated_at)
		VALUES ('vid-1', 'video', 0, ?, 1000000, 1, ?, ?)`, modelOutPrice, now, now)
	mustExec(t, e.f, `INSERT INTO channels (id,name,base_url,path,upstream_key_enc,weight,priority,status,created_at,updated_at)
		VALUES (1,'video-ch',?, '/contents/generations/tasks', 'legacy-key', 1, 10, 1, ?, ?)`, upstreamURL, now, now)
	mustExec(t, e.f, `INSERT INTO channel_abilities (channel_id, model_name) VALUES (1, 'vid-1')`)
	mustExec(t, e.f, `INSERT INTO user_model_grants (user_id, model_name, created_at) VALUES (1, 'vid-1', ?)`, now)
}

func (e *testEnv) postVideo(body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/videos/generations/tasks", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.engine.ServeHTTP(w, req)
	return w
}

func (e *testEnv) getVideoTask(taskID, apiKey string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/v1/videos/generations/tasks/"+taskID, nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	w := httptest.NewRecorder()
	e.engine.ServeHTTP(w, req)
	return w
}

// 创建任务：Ark 原生透传（出站路径/body 原样）、回 {"id"} 原文、
// 创建不计量（NoUsage=1 cost=0）、video_tasks 登记归属/渠道/价格快照
func TestVideoCreatePassthrough(t *testing.T) {
	e := newTestEnv(t)
	var mu sync.Mutex
	var gotPath, gotMethod, gotBody string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotPath, gotMethod, gotBody = r.URL.Path, r.Method, string(b)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"id":"cgt-20260101-abc"}`))
	}))
	defer up.Close()
	e.seedVideo(t, up.URL, 30000000)

	body := `{"model":"vid-1","content":[{"type":"text","text":"小猫跳舞 --ratio 1:1 --dur 5"}]}`
	w := e.postVideo(body)
	if w.Code != http.StatusOK {
		t.Fatalf("应 200，got %d body=%s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), `"id":"cgt-20260101-abc"`) {
		t.Fatalf("客户端应收上游原文 {\"id\"}, got %s", w.Body)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotPath != "/contents/generations/tasks" || gotMethod != http.MethodPost {
		t.Fatalf("出站应为 POST /contents/generations/tasks，got %s %s", gotMethod, gotPath)
	}
	// relay 经 map 重组转发（键序会变），按语义断言：content 数组与参数原样到达
	if !strings.Contains(gotBody, `"model":"vid-1"`) ||
		!strings.Contains(gotBody, `"text":"小猫跳舞 --ratio 1:1 --dur 5"`) {
		t.Fatalf("上游应收到原样 body（语义），got %s", gotBody)
	}
	// 创建不计量：cost=0 NoUsage=1
	var ul struct{ Cost, NoUsage, Status int64 }
	if err := e.f.db.Raw(`SELECT cost, no_usage, status FROM usage_logs ORDER BY id DESC LIMIT 1`).
		Scan(&ul).Error; err != nil || ul.Status == 0 {
		t.Fatalf("读取 usage_log 失败: %v %+v", err, ul)
	}
	if ul.Cost != 0 || ul.NoUsage != 1 {
		t.Fatalf("创建应不计量 NoUsage=1 cost=0，got cost=%d no_usage=%d", ul.Cost, ul.NoUsage)
	}
	// 登记快照：归属/渠道/价格（output_price 已含客户覆盖价语义——此处为模型默认价）
	var vt struct {
		TaskID      string
		OrgID       int64
		UserID      int64
		ChannelID   int64
		ModelName   string
		Billed      int64
		OutputPrice int64
	}
	if err := e.f.db.Raw(`SELECT task_id, org_id, user_id, channel_id, model_name, billed, output_price
		FROM video_tasks WHERE task_id = 'cgt-20260101-abc'`).Scan(&vt).Error; err != nil || vt.TaskID == "" {
		t.Fatalf("video_tasks 应已登记: %v %+v", err, vt)
	}
	if vt.OrgID != 1 || vt.UserID != 1 || vt.ChannelID != 1 || vt.ModelName != "vid-1" ||
		vt.Billed != 0 || vt.OutputPrice != 30000000 {
		t.Fatalf("登记快照不符: %+v", vt)
	}
}

// endpointURL 幂等守卫：渠道 path 已是任务端点时不得拼出双重路径；
// 反向：视频模型挂在 path=/chat/completions 的聊天渠道上也能改写到任务端点
func TestVideoCreateOnChatChannelPathRewrite(t *testing.T) {
	if got := endpointURL("https://x.example", "/contents/generations/tasks", videoTaskPath); got != "https://x.example/contents/generations/tasks" {
		t.Fatalf("path 已含任务端点应原样保留，got %s", got)
	}
	if got := endpointURL("https://x.example/v1", "/chat/completions", videoTaskPath); got != "https://x.example/v1/contents/generations/tasks" {
		t.Fatalf("chat 渠道路径应整体替换为任务端点，got %s", got)
	}
	// embeddings 语义回归：path=/embeddings 与 fixed 同值仍正确
	if got := endpointURL("https://x.example/v1", "/embeddings", "/embeddings"); got != "https://x.example/v1/embeddings" {
		t.Fatalf("embeddings 改写语义不应变化，got %s", got)
	}
}

// 类型 gate 双向：视频模型打 chat 端点 → 400 带指引；chat 模型打视频端点 → 400
func TestVideoTypeGate(t *testing.T) {
	e := newTestEnv(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("类型 gate 应在网关拦截，不应触达上游")
	}))
	defer up.Close()
	e.seedVideo(t, up.URL, 30000000)

	w := e.post(`{"model":"vid-1","messages":[{"role":"user","content":"hi"}]}`)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "/v1/videos/generations/tasks") {
		t.Fatalf("视频模型打 chat 端点应 400 带指引，got %d body=%s", w.Code, w.Body)
	}
	w2 := e.postVideo(`{"model":"m1","content":[{"type":"text","text":"x"}]}`)
	if w2.Code != http.StatusBadRequest || !strings.Contains(w2.Body.String(), "not a video generation model") {
		t.Fatalf("chat 模型打视频端点应 400，got %d body=%s", w2.Code, w2.Body)
	}
	// 缺 content → 400（spec.require）
	w3 := e.postVideo(`{"model":"vid-1"}`)
	if w3.Code != http.StatusBadRequest || !strings.Contains(w3.Body.String(), "missing required parameter: content") {
		t.Fatalf("缺 content 应 400，got %d body=%s", w3.Code, w3.Body)
	}
}

// 查询结算一次：queued 不落流水不结算；succeeded+usage 首查结算
// （1,000,000 tokens × 30M 点/M = 30,000,000 点，双记账）；
// 重复查询响应不变且不重复扣费（billed=1）
func TestVideoQuerySettleOnce(t *testing.T) {
	e := newTestEnv(t)
	now := time.Now().Unix()
	mustExec(t, e.f, `INSERT INTO models (name, type, input_price, output_price, status, created_at, updated_at)
		VALUES ('vid-1', 'video', 0, 30000000, 1, ?, ?)`, now, now)
	mustExec(t, e.f, `INSERT INTO channels (id,name,base_url,path,upstream_key_enc,weight,priority,status,created_at,updated_at)
		VALUES (1,'video-ch',?, '/contents/generations/tasks', 'legacy-key', 1, 10, 1, ?, ?)`, "http://up.invalid", now, now)
	mustExec(t, e.f, `INSERT INTO channel_abilities (channel_id, model_name) VALUES (1, 'vid-1')`)
	mustExec(t, e.f, `INSERT INTO video_tasks (task_id, org_id, user_id, api_key_id, channel_id, model_name,
		output_price, cost_output_price, create_request_id, created_at, updated_at)
		VALUES ('cgt-t1', 1, 1, 1, 1, 'vid-1', 30000000, 1000000, 'req-1', ?, ?)`, now, now)

	var stage int32 // 0=queued 1=succeeded
	var mu sync.Mutex
	var paths []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.Path)
		st := atomic.LoadInt32(&stage)
		mu.Unlock()
		if st == 0 {
			_, _ = w.Write([]byte(`{"id":"cgt-t1","status":"queued"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"cgt-t1","status":"succeeded","content":{"video_url":"https://v.example/1.mp4"},"usage":{"completion_tokens":1000000,"total_tokens":1000000}}`))
	}))
	defer up.Close()
	// 渠道指向 mock（覆盖 up.invalid）
	mustExec(t, e.f, `UPDATE channels SET base_url = ? WHERE id = 1`, up.URL)

	w1 := e.getVideoTask("cgt-t1", e.apiKey)
	if w1.Code != http.StatusOK || !strings.Contains(w1.Body.String(), `"queued"`) {
		t.Fatalf("首查应透传 queued，got %d body=%s", w1.Code, w1.Body)
	}
	if n := e.usageCount(t); n != 0 {
		t.Fatalf("queued 阶段不应落 usage_logs，got %d 行", n)
	}
	if billed := e.videoBilled(t, "cgt-t1"); billed != 0 {
		t.Fatalf("queued 阶段不应结算")
	}

	atomic.StoreInt32(&stage, 1)
	w2 := e.getVideoTask("cgt-t1", e.apiKey)
	if w2.Code != http.StatusOK || !strings.Contains(w2.Body.String(), `"succeeded"`) {
		t.Fatalf("succeeded 应透传，got %d body=%s", w2.Code, w2.Body)
	}
	var used1 int64
	_ = e.f.db.Raw(`SELECT quota_used FROM users WHERE id = 1`).Scan(&used1).Error
	if used1 != 30000000 {
		t.Fatalf("结算应为 1M×30M/M=30,000,000 点，got users.quota_used=%d", used1)
	}
	var settleRow struct {
		RequestID        string
		CompletionTokens int64
		OutputPrice      int64
		VendorCost       int64
		Cost             int64
	}
	if err := e.f.db.Raw(`SELECT request_id, completion_tokens, output_price, vendor_cost, cost FROM usage_logs
		WHERE request_id = 'cgt-t1'`).Scan(&settleRow).Error; err != nil || settleRow.Cost == 0 {
		t.Fatalf("结算流水缺失: %v %+v", err, settleRow)
	}
	if settleRow.CompletionTokens != 1000000 || settleRow.OutputPrice != 30000000 ||
		settleRow.Cost != 30000000 || settleRow.VendorCost != 1000000 {
		t.Fatalf("结算流水不符: %+v", settleRow)
	}

	// 重复查询：响应不变、不重复扣费
	w3 := e.getVideoTask("cgt-t1", e.apiKey)
	if w3.Code != http.StatusOK || !strings.Contains(w3.Body.String(), `"succeeded"`) {
		t.Fatalf("重复查询应照常透传，got %d", w3.Code)
	}
	var used2 int64
	_ = e.f.db.Raw(`SELECT quota_used FROM users WHERE id = 1`).Scan(&used2).Error
	if used2 != used1 {
		t.Fatalf("重复查询不得重复扣费，%d → %d", used1, used2)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 3 {
		t.Fatalf("三次查询应三次打上游，got %v", paths)
	}
	for _, p := range paths {
		if p != "GET /contents/generations/tasks/cgt-t1" {
			t.Fatalf("查询出站路径不符: %s", p)
		}
	}
}

// org 隔离：B 客户 key 查 A 客户任务 → 404 且上游零命中（不可区分，防探测）
func TestVideoQueryOrgIsolation(t *testing.T) {
	e := newTestEnv(t)
	now := time.Now().Unix()
	// A 客户（org 1）的任务挂在 mock 渠道上
	var hits int
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte(`{"id":"cgt-a","status":"running"}`))
	}))
	defer up.Close()
	mustExec(t, e.f, `INSERT INTO channels (id,name,base_url,path,upstream_key_enc,weight,priority,status,created_at,updated_at)
		VALUES (1,'video-ch',?, '/contents/generations/tasks', 'legacy-key', 1, 10, 1, ?, ?)`, up.URL, now, now)
	mustExec(t, e.f, `INSERT INTO video_tasks (task_id, org_id, user_id, api_key_id, channel_id, model_name,
		output_price, create_request_id, created_at, updated_at)
		VALUES ('cgt-a', 1, 1, 1, 1, 'vid-1', 30000000, 'r', ?, ?)`, now, now)
	// B 客户（org 2）+ 独立 key
	otherKey := "sk-other-org-key"
	mustExec(t, e.f, `INSERT INTO orgs (id, name, quota_limit, status, created_at, updated_at)
		VALUES (2, 'org-b', 100000000, 1, ?, ?)`, now, now)
	mustExec(t, e.f, `INSERT INTO users (id, org_id, username, password_hash, role, status, created_at, updated_at)
		VALUES (2, 2, 'b-user', 'x', 'member', 1, ?, ?)`, now, now)
	mustExec(t, e.f, `INSERT INTO api_keys (org_id, user_id, key_prefix, key_hash, status, created_at)
		VALUES (2, 2, 'sk-oth', ?, 1, ?)`, auth.HashAPIKey(otherKey), now)

	w := e.getVideoTask("cgt-a", otherKey)
	if w.Code != http.StatusNotFound {
		t.Fatalf("跨客户查询应 404，got %d body=%s", w.Code, w.Body)
	}
	if hits != 0 {
		t.Fatalf("跨客户查询不得触达上游，hits=%d", hits)
	}
	// 任务不存在同响应
	w2 := e.getVideoTask("cgt-not-exist", e.apiKey)
	if w2.Code != http.StatusNotFound {
		t.Fatalf("不存在的任务应 404，got %d", w2.Code)
	}
}

// 渠道被禁用仍可查询转发（钱已花，查询只读+结算）
func TestVideoQueryDisabledChannel(t *testing.T) {
	e := newTestEnv(t)
	now := time.Now().Unix()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"cgt-x","status":"queued"}`))
	}))
	defer up.Close()
	mustExec(t, e.f, `INSERT INTO channels (id,name,base_url,path,upstream_key_enc,weight,priority,status,created_at,updated_at)
		VALUES (1,'video-ch',?, '/contents/generations/tasks', 'legacy-key', 1, 10, 0, ?, ?)`, up.URL, now, now)
	mustExec(t, e.f, `INSERT INTO video_tasks (task_id, org_id, user_id, api_key_id, channel_id, model_name,
		output_price, create_request_id, created_at, updated_at)
		VALUES ('cgt-x', 1, 1, 1, 1, 'vid-1', 30000000, 'r', ?, ?)`, now, now)

	w := e.getVideoTask("cgt-x", e.apiKey)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"queued"`) {
		t.Fatalf("禁用渠道的任务查询应照常透传，got %d body=%s", w.Code, w.Body)
	}
}

// 上游非 2xx（任务过期 404 等）原样透传，不结算
func TestVideoQueryUpstreamErrorPassthrough(t *testing.T) {
	e := newTestEnv(t)
	now := time.Now().Unix()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"task not found"}}`))
	}))
	defer up.Close()
	mustExec(t, e.f, `INSERT INTO channels (id,name,base_url,path,upstream_key_enc,weight,priority,status,created_at,updated_at)
		VALUES (1,'video-ch',?, '/contents/generations/tasks', 'legacy-key', 1, 10, 1, ?, ?)`, up.URL, now, now)
	mustExec(t, e.f, `INSERT INTO video_tasks (task_id, org_id, user_id, api_key_id, channel_id, model_name,
		output_price, create_request_id, created_at, updated_at)
		VALUES ('cgt-e', 1, 1, 1, 1, 'vid-1', 30000000, 'r', ?, ?)`, now, now)

	w := e.getVideoTask("cgt-e", e.apiKey)
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "task not found") {
		t.Fatalf("上游 404 应原样透传，got %d body=%s", w.Code, w.Body)
	}
	if e.videoBilled(t, "cgt-e") != 0 {
		t.Fatal("上游错误不得结算")
	}
}

// 补偿扫描：未结算任务被扫上游并结算；再扫 no-op（幂等）；failed 不再扫；超龄跳过
func TestVideoSweep(t *testing.T) {
	e := newTestEnv(t)
	now := time.Now().Unix()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/contents/generations/tasks/cgt-s1":
			_, _ = w.Write([]byte(`{"id":"cgt-s1","status":"succeeded","usage":{"completion_tokens":500000}}`))
		case "/contents/generations/tasks/cgt-s2":
			_, _ = w.Write([]byte(`{"id":"cgt-s2","status":"failed","error":{"message":"content policy"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer up.Close()
	mustExec(t, e.f, `INSERT INTO channels (id,name,base_url,path,upstream_key_enc,weight,priority,status,created_at,updated_at)
		VALUES (1,'video-ch',?, '/contents/generations/tasks', 'legacy-key', 1, 10, 1, ?, ?)`, up.URL, now, now)
	// s1: 未查询过（status=''）→ 扫描结算；s2: failed → 不扫；
	// s3: 超龄（created_at 在放弃线之前）→ 不扫
	mustExec(t, e.f, `INSERT INTO video_tasks (task_id, org_id, user_id, api_key_id, channel_id, model_name,
		output_price, cost_output_price, create_request_id, created_at, updated_at)
		VALUES ('cgt-s1', 1, 1, 1, 1, 'vid-1', 2000000, 1000000, 'r', ?, ?)`, now, now)
	mustExec(t, e.f, `INSERT INTO video_tasks (task_id, org_id, user_id, api_key_id, channel_id, model_name,
		output_price, create_request_id, created_at, updated_at)
		VALUES ('cgt-s2', 1, 1, 1, 1, 'vid-1', 2000000, 'r', ?, ?)`, now, now)
	mustExec(t, e.f, `UPDATE video_tasks SET status = 'failed' WHERE task_id = 'cgt-s2'`)
	mustExec(t, e.f, `INSERT INTO video_tasks (task_id, org_id, user_id, api_key_id, channel_id, model_name,
		output_price, create_request_id, created_at, updated_at)
		VALUES ('cgt-s3', 1, 1, 1, 1, 'vid-1', 2000000, 'r', ?, ?)`, now-25*3600, now-25*3600)

	VideoTaskSweepOnce(e.h)
	if e.videoBilled(t, "cgt-s1") != 1 {
		t.Fatal("s1 应被扫描结算")
	}
	var used int64
	_ = e.f.db.Raw(`SELECT quota_used FROM users WHERE id = 1`).Scan(&used).Error
	if used != 1000000 { // 500,000 × 2M/M = 1,000,000
		t.Fatalf("扫描结算金额不符，got %d", used)
	}
	if e.videoBilled(t, "cgt-s3") != 0 {
		t.Fatal("超龄任务不应被扫")
	}

	// 再扫一轮：s1 已结算 no-op（不重复扣费）
	VideoTaskSweepOnce(e.h)
	var used2 int64
	_ = e.f.db.Raw(`SELECT quota_used FROM users WHERE id = 1`).Scan(&used2).Error
	if used2 != used {
		t.Fatalf("重复扫描不得重复扣费，%d → %d", used, used2)
	}
}

// 并发结算幂等（双网关语义）：两个 goroutine 同时 settleVideoTask 仅一次记账
func TestSettleVideoConcurrent(t *testing.T) {
	e := newTestEnv(t)
	now := time.Now().Unix()
	mustExec(t, e.f, `INSERT INTO video_tasks (task_id, org_id, user_id, api_key_id, channel_id, model_name,
		output_price, create_request_id, created_at, updated_at)
		VALUES ('cgt-c', 1, 1, 1, 1, 'vid-1', 30000000, 'r', ?, ?)`, now, now)

	var wg sync.WaitGroup
	const n = 8
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			vt := videoTaskOf(t, e.f.db, "cgt-c")
			if err := e.h.settleVideoTask(vt, "succeeded", 1000000, 0, ""); err != nil {
				t.Errorf("并发结算不应报错: %v", err)
			}
		}()
	}
	wg.Wait()
	var used int64
	_ = e.f.db.Raw(`SELECT quota_used FROM users WHERE id = 1`).Scan(&used).Error
	if used != 30000000 {
		t.Fatalf("并发结算应仅记一次账（30,000,000），got %d", used)
	}
	var rows int64
	_ = e.f.db.Raw(`SELECT COUNT(*) FROM usage_logs WHERE request_id = 'cgt-c'`).Scan(&rows).Error
	if rows != 1 {
		t.Fatalf("结算流水应恰一行，got %d", rows)
	}
}

// 模型映射不泄漏：渠道配了上游名改写时，创建出站改写、查询响应改写回对外名
func TestVideoModelMapping(t *testing.T) {
	e := newTestEnv(t)
	now := time.Now().Unix()
	var mu sync.Mutex
	var gotCreateModel, gotQueryPath string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.Method == http.MethodPost {
			var req struct {
				Model string `json:"model"`
			}
			_ = json.Unmarshal(b, &req)
			mu.Lock()
			gotCreateModel = req.Model
			mu.Unlock()
			_, _ = w.Write([]byte(`{"id":"cgt-m"}`))
			return
		}
		mu.Lock()
		gotQueryPath = r.URL.Path
		mu.Unlock()
		// 上游按其模型名应答（真实厂商行为）
		_, _ = w.Write([]byte(`{"id":"cgt-m","model":"ep-2026-video","status":"succeeded","usage":{"completion_tokens":100000}}`))
	}))
	defer up.Close()
	mustExec(t, e.f, `INSERT INTO models (name, type, input_price, output_price, status, created_at, updated_at)
		VALUES ('vid-1', 'video', 0, 2000000, 1, ?, ?)`, now, now)
	mustExec(t, e.f, `INSERT INTO channels (id,name,base_url,path,upstream_key_enc,weight,priority,status,created_at,updated_at)
		VALUES (1,'video-ch',?, '/contents/generations/tasks', 'legacy-key', 1, 10, 1, ?, ?)`, up.URL, now, now)
	mustExec(t, e.f, `INSERT INTO channel_abilities (channel_id, model_name, upstream_model_name)
		VALUES (1, 'vid-1', 'ep-2026-video')`)
	mustExec(t, e.f, `INSERT INTO user_model_grants (user_id, model_name, created_at) VALUES (1, 'vid-1', ?)`, now)

	w := e.postVideo(`{"model":"vid-1","content":[{"type":"text","text":"x"}]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("创建应 200，got %d body=%s", w.Code, w.Body)
	}
	mu.Lock()
	if gotCreateModel != "ep-2026-video" {
		t.Fatalf("创建出站应改写为上游名，got %s", gotCreateModel)
	}
	mu.Unlock()
	w2 := e.getVideoTask("cgt-m", e.apiKey)
	if w2.Code != http.StatusOK {
		t.Fatalf("查询应 200，got %d", w2.Code)
	}
	if strings.Contains(w2.Body.String(), "ep-2026-video") {
		t.Fatalf("查询响应不得泄漏上游名: %s", w2.Body)
	}
	if !strings.Contains(w2.Body.String(), `"model":"vid-1"`) && !strings.Contains(w2.Body.String(), `"status":"succeeded"`) {
		t.Fatalf("查询响应应透传并改写模型名: %s", w2.Body)
	}
	_ = gotQueryPath
}

// ---------------- 小工具 ----------------

func (e *testEnv) usageCount(t *testing.T) int {
	t.Helper()
	var n int64
	if err := e.f.db.Raw(`SELECT COUNT(*) FROM usage_logs`).Scan(&n).Error; err != nil {
		t.Fatalf("统计 usage_logs 失败: %v", err)
	}
	return int(n)
}

func (e *testEnv) videoBilled(t *testing.T, taskID string) int {
	t.Helper()
	var billed int64
	if err := e.f.db.Raw(`SELECT billed FROM video_tasks WHERE task_id = ?`, taskID).Scan(&billed).Error; err != nil {
		t.Fatalf("读取 video_tasks 失败: %v", err)
	}
	return int(billed)
}

// videoTaskOf 按任务 id 载入登记行（并发结算用）
func videoTaskOf(t *testing.T, db *gorm.DB, taskID string) *model.VideoTask {
	t.Helper()
	var vt model.VideoTask
	if err := db.Where("task_id = ?", taskID).First(&vt).Error; err != nil {
		t.Fatalf("载入 video_task 失败: %v", err)
	}
	return &vt
}
