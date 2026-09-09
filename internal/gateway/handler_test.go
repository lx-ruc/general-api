package gateway

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"token-gateway/internal/auth"
	"token-gateway/internal/config"
	"token-gateway/internal/coord"
	"token-gateway/internal/crypto"
	"token-gateway/internal/database"
	"token-gateway/internal/metrics"
	"token-gateway/internal/middleware"
)

// ---------------- 共享测试底座 ----------------

type dbFixture struct {
	db *gorm.DB
}

func databaseCfg(path string) config.Database {
	return config.Database{Driver: "sqlite", Path: path}
}

// newTestDB 临时 SQLite + 全量 schema（含 channel_keys / cache_hit）
func newTestDB(t *testing.T) *dbFixture {
	t.Helper()
	db, err := database.Open(databaseCfg(t.TempDir() + "/test.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := database.Migrate(db); err != nil {
		t.Fatalf("迁移测试库失败: %v", err)
	}
	t.Cleanup(func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() })
	return &dbFixture{db: db}
}

type testEnv struct {
	f       *dbFixture
	h       *Handler
	cd      *coord.Mem
	m       *metrics.Metrics
	engine  *gin.Engine
	apiKey  string
	userID  int64
	orgID   int64
	upCount atomic.Int64 // 上游 mock 命中计数（按需在各测试里另行统计）
}

// newTestEnv 建好 org/user/api_key/grant/model，注册 /v1 全链路（含 APIKeyAuth）
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	f := newTestDB(t)
	now := time.Now().Unix()
	apiKey := "sk-test-key-0001"

	mustExec(t, f, `INSERT INTO orgs (name, quota_limit, status, created_at, updated_at)
		VALUES ('t-org', 100000000, 1, ?, ?)`, now, now)
	mustExec(t, f, `INSERT INTO users (org_id, username, password_hash, role, status, created_at, updated_at)
		VALUES (1, 'tester', 'x', 'member', 1, ?, ?)`, now, now)
	mustExec(t, f, `INSERT INTO api_keys (org_id, user_id, key_prefix, key_hash, status, created_at)
		VALUES (1, 1, 'sk-test', ?, 1, ?)`, auth.HashAPIKey(apiKey), now)
	mustExec(t, f, `INSERT INTO models (name, input_price, output_price, status, created_at, updated_at)
		VALUES ('m1', 2000000, 8000000, 1, ?, ?)`, now, now)
	mustExec(t, f, `INSERT INTO user_model_grants (user_id, model_name, created_at) VALUES (1, 'm1', ?)`, now)

	cfg := &config.Config{}
	cfg.Gateway = config.Gateway{
		MaxBodyMB:                10,
		PerKeyRPM:                10000,
		UpstreamFirstByteTimeout: config.Duration{Duration: 10 * time.Second},
		ChannelBreakerThreshold:  0, // 测试中关闭熔断，避免跨用例干扰
		QueueWaitTimeout:         config.Duration{Duration: 2 * time.Second},
		KeyCooldown:              config.Duration{Duration: time.Minute},
	}
	cipher, _ := crypto.NewCipher("")
	cd := coord.NewMem(1000)
	m := metrics.New()
	h := NewHandler(f.db, cipher, cfg, middleware.NewRateLimiter(10000, 20), m, cd)

	engine := gin.New()
	v1 := engine.Group("/v1", middleware.APIKeyAuth(f.db))
	v1.POST("/chat/completions", h.ChatCompletions)
	return &testEnv{f: f, h: h, cd: cd, m: m, engine: engine, apiKey: apiKey, userID: 1, orgID: 1}
}

// seedUpstreamChannel 渠道指向 mock 上游；poolKeys 非空走 Key 池，否则 legacy 单 key
func (e *testEnv) seedUpstreamChannel(t *testing.T, id int64, name, upstreamURL string, poolKeys []string, priority int) {
	t.Helper()
	now := time.Now().Unix()
	if len(poolKeys) > 0 {
		mustExec(t, e.f, `INSERT INTO channels (id,name,base_url,path,weight,priority,status,created_at,updated_at)
			VALUES (?,?,?, '/v1/chat/completions', 1, ?, 1, ?, ?)`, id, name, upstreamURL, priority, now, now)
		for i, k := range poolKeys {
			mustExec(t, e.f, `INSERT INTO channel_keys (channel_id,key_enc,weight,status,created_at,updated_at)
				VALUES (?,?,1,1,?,?)`, id, k, now+int64(i), now+int64(i))
		}
	} else {
		mustExec(t, e.f, `INSERT INTO channels (id,name,base_url,path,upstream_key_enc,weight,priority,status,created_at,updated_at)
			VALUES (?,?,?, '/v1/chat/completions', 'legacy-key', 1, ?, 1, ?, ?)`, id, name, upstreamURL, priority, now, now)
	}
	mustExec(t, e.f, `INSERT INTO channel_abilities (channel_id, model_name) VALUES (?, 'm1')`, id)
}

func (e *testEnv) post(body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.engine.ServeHTTP(w, req)
	return w
}

func chatBody(prompt string, extra string) string {
	return fmt.Sprintf(`{"model":"m1","messages":[{"role":"user","content":%q}]%s}`, prompt, extra)
}

const okBody = `{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`

func (e *testEnv) usageRow(t *testing.T, id int64) (cost, cacheHit, status int64) {
	t.Helper()
	var row struct{ Cost, CacheHit, Status int64 }
	if err := e.f.db.Raw("SELECT cost, cache_hit, status FROM usage_logs WHERE id = ?", id).
		Scan(&row).Error; err != nil || row.Status == 0 {
		t.Fatalf("读取 usage_log 失败: %v %+v", err, row)
	}
	return row.Cost, row.CacheHit, row.Status
}

func (e *testEnv) lastUsage(t *testing.T) (id int64) {
	t.Helper()
	if err := e.f.db.Raw("SELECT id FROM usage_logs ORDER BY id DESC LIMIT 1").Scan(&id).Error; err != nil || id == 0 {
		t.Fatalf("读取 usage_log id 失败: %v", err)
	}
	return id
}

// ---------------- 数据面编排 ----------------

// 过期密钥在鉴权层即被拒：401 且带 expired 语义，不触达渠道
func TestExpiredKeyRejected401(t *testing.T) {
	e := newTestEnv(t)
	now := time.Now().Unix()
	expiredKey := "sk-expired-key-01"
	mustExec(t, e.f, `INSERT INTO api_keys (org_id, user_id, key_prefix, key_hash, status, expired_at, created_at)
		VALUES (1, 1, 'sk-exp', ?, 1, ?, ?)`, auth.HashAPIKey(expiredKey), now-3600, now)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatBody("hi", "")))
	req.Header.Set("Authorization", "Bearer "+expiredKey)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.engine.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("过期密钥应 401，得 %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "expired") {
		t.Fatalf("错误信息应含 expired，得 %s", w.Body.String())
	}
	// 即将过期（未来时刻）不受影响
	futureKey := "sk-future-key-001"
	mustExec(t, e.f, `INSERT INTO api_keys (org_id, user_id, key_prefix, key_hash, status, expired_at, created_at)
		VALUES (1, 1, 'sk-fut', ?, 1, ?, ?)`, auth.HashAPIKey(futureKey), now+3600, now)
	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatBody("hi", "")))
	req2.Header.Set("Authorization", "Bearer "+futureKey)
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	e.engine.ServeHTTP(w2, req2)
	if w2.Code == http.StatusUnauthorized {
		t.Fatalf("未过期密钥不应 401: %s", w2.Body.String())
	}
}

// 成本中心快照不变性：结算落 usage_logs.cost_center_id；key 改派后历史行不动、新行归新中心
func TestCostCenterSnapshot(t *testing.T) {
	e := newTestEnv(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(okBody))
	}))
	defer up.Close()
	e.seedUpstreamChannel(t, 1, "ch", up.URL, nil, 1)

	// 两个中心；key 初始归中心 1
	now := time.Now().Unix()
	mustExec(t, e.f, `INSERT INTO cost_centers (id, org_id, name, status, created_at, updated_at)
		VALUES (1, 1, 'A', 1, ?, ?), (2, 1, 'B', 1, ?, ?)`, now, now, now, now)
	mustExec(t, e.f, `UPDATE api_keys SET cost_center_id = 1 WHERE id = 1`)

	if w := e.post(chatBody("q", "")); w.Code != http.StatusOK {
		t.Fatalf("首请求应 200，得 %d: %s", w.Code, w.Body.String())
	}
	var cc1 int64
	_ = e.f.db.Raw(`SELECT COALESCE(cost_center_id,0) FROM usage_logs WHERE id = ?`, e.lastUsage(t)).Scan(&cc1).Error
	if cc1 != 1 {
		t.Fatalf("首笔应快照中心 1，得 %d", cc1)
	}

	// 改派中心 2 → 历史行不动，新行归 2
	mustExec(t, e.f, `UPDATE api_keys SET cost_center_id = 2 WHERE id = 1`)
	if w := e.post(chatBody("q", "")); w.Code != http.StatusOK {
		t.Fatalf("次请求应 200，得 %d", w.Code)
	}
	var cc2 int64
	_ = e.f.db.Raw(`SELECT COALESCE(cost_center_id,0) FROM usage_logs WHERE id = ?`, e.lastUsage(t)).Scan(&cc2).Error
	if cc2 != 2 {
		t.Fatalf("次笔应快照中心 2，得 %d", cc2)
	}
	_ = e.f.db.Raw(`SELECT COALESCE(cost_center_id,0) FROM usage_logs WHERE id = ?`, e.lastUsage(t)-1).Scan(&cc1).Error
	if cc1 != 1 {
		t.Fatalf("改派不得改写历史快照，得 %d", cc1)
	}
}

// 429 → Key 冷却 → 同请求降级到低优先级渠道；下个请求冷却生效不再打 429 渠道
func Test429CooldownThenFallback(t *testing.T) {
	e := newTestEnv(t)
	up1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.upCount.Add(1)
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	defer up1.Close()
	up2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(okBody))
	}))
	defer up2.Close()
	e.seedUpstreamChannel(t, 1, "ch429", up1.URL, nil, 10)
	e.seedUpstreamChannel(t, 2, "chok", up2.URL, nil, 1)

	w := e.post(chatBody("q", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("应降级到渠道 2 成功，got %d body=%s", w.Code, w.Body)
	}
	if got := w.Header().Get("X-Tg-Channel-Id"); got != "2" {
		t.Fatalf("应由渠道 2 应答，got X-Tg-Channel-Id=%q", got)
	}
	if e.m.Upstream429.Value() != 1 || e.m.KeyCooldown.Value() != 1 {
		t.Fatalf("429/冷却指标错误: %d %d", e.m.Upstream429.Value(), e.m.KeyCooldown.Value())
	}
	if !e.cd.IsCooling("ck:1:legacy") {
		t.Fatal("渠道 1 的 key 应进入冷却")
	}

	// 第二次请求：渠道 1 被冷却过滤，直接命中渠道 2
	w = e.post(chatBody("q", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("第二次请求应直接走渠道 2，got %d", w.Code)
	}
	if e.upCount.Load() != 1 {
		t.Fatalf("冷却后不应再打渠道 1，upstream hits=%d", e.upCount.Load())
	}
}

// 全部候选都 429 → 最终回 429（而非 502）并带 Retry-After
func TestAll429Returns429WithRetryAfter(t *testing.T) {
	e := newTestEnv(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer up.Close()
	e.seedUpstreamChannel(t, 1, "ch", up.URL, nil, 10)

	w := e.post(chatBody("q", ""))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("全 429 应回 429，got %d body=%s", w.Code, w.Body)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("应携带 Retry-After")
	}
	if !strings.Contains(w.Body.String(), "upstream_busy") {
		t.Fatalf("错误类型应为 upstream_busy: %s", w.Body)
	}
	if _, _, status := e.usageRow(t, e.lastUsage(t)); status != 429 {
		t.Fatalf("usage_log 应记 429，got %d", status)
	}
}

// 上游 401 → 池内该 Key 禁用并同渠道切备用 Key：命中坏 Key 的请求本身也应成功（3.6 主 Key 报错自动切备用）
func Test401AutoDisablesPoolKey(t *testing.T) {
	e := newTestEnv(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer bad-key" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
			return
		}
		_, _ = w.Write([]byte(okBody))
	}))
	defer up.Close()
	e.seedUpstreamChannel(t, 1, "ch", up.URL, []string{"bad-key", "good-key"}, 10)

	// 无论先选中哪把 key，请求都应成功：好 key 直接 200；坏 key 401 → 同渠道换好 key → 200
	for i := 0; i < 20; i++ {
		if w := e.post(chatBody("q", "")); w.Code != http.StatusOK {
			t.Fatalf("第 %d 笔请求应全部 200（坏 key 应就地切备用），got %d body=%s", i, w.Code, w.Body)
		}
	}

	// 坏 key 已被异步禁用落库
	var cnt int64
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_ = e.f.db.Raw("SELECT COUNT(*) FROM channel_keys WHERE key_enc = 'bad-key' AND status = 0").Scan(&cnt).Error
		if cnt == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if cnt != 1 {
		t.Fatal("坏 key 应被异步禁用")
	}
	if e.m.KeyDisabled.Value() < 1 {
		t.Fatalf("key_disabled 指标应 ≥1，got %d", e.m.KeyDisabled.Value())
	}
}

// 上游 5xx：同渠道剩余 key 一并跳过（渠道级故障换 key 无意义）
func Test5xxSkipsWholeChannel(t *testing.T) {
	e := newTestEnv(t)
	var hits atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer up.Close()
	e.seedUpstreamChannel(t, 1, "ch", up.URL, []string{"k1", "k2", "k3"}, 10)

	w := e.post(chatBody("q", ""))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("5xx 耗尽应回 502，got %d", w.Code)
	}
	if hits.Load() != 1 {
		t.Fatalf("5xx 应只打渠道一次（跳过剩余 key），hits=%d", hits.Load())
	}
}

// 并发闸门=1：并发请求在上游侧被串行化（排队削峰而非立即失败）
func TestConcurrencyGateSerializes(t *testing.T) {
	e := newTestEnv(t)
	e.h.MaxConcurrency = 1
	var inFlight, maxInFlight atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := inFlight.Add(1)
		for {
			m := maxInFlight.Load()
			if cur <= m || maxInFlight.CompareAndSwap(m, cur) {
				break
			}
		}
		time.Sleep(120 * time.Millisecond)
		inFlight.Add(-1)
		_, _ = w.Write([]byte(okBody))
	}))
	defer up.Close()
	e.seedUpstreamChannel(t, 1, "ch", up.URL, nil, 10)

	var wg sync.WaitGroup
	codes := make([]int, 3)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w := e.post(chatBody(fmt.Sprintf("q%d", i), ""))
			codes[i] = w.Code
		}(i)
	}
	wg.Wait()
	for i, c := range codes {
		if c != http.StatusOK {
			t.Fatalf("请求 %d 应排队后成功，got %d", i, c)
		}
	}
	if maxInFlight.Load() != 1 {
		t.Fatalf("上游并发应被限制为 1，max=%d", maxInFlight.Load())
	}
}

// 精确缓存：相同请求第二次 X-Tg-Cache: hit、body 相同、上游只打一次、第二笔 cost=0
func TestExactCacheHit(t *testing.T) {
	e := newTestEnv(t)
	e.h.CacheTTL = time.Minute
	var hits atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(okBody))
	}))
	defer up.Close()
	e.seedUpstreamChannel(t, 1, "ch", up.URL, nil, 10)

	body := chatBody("same-question", `,"temperature":0.7`)
	w1 := e.post(body)
	if w1.Code != http.StatusOK || w1.Header().Get("X-Tg-Cache") != "" {
		t.Fatalf("首请求应 miss: code=%d cache=%q", w1.Code, w1.Header().Get("X-Tg-Cache"))
	}
	id1 := e.lastUsage(t)

	w2 := e.post(body)
	if w2.Code != http.StatusOK {
		t.Fatalf("缓存命中应 200，got %d", w2.Code)
	}
	if w2.Header().Get("X-Tg-Cache") != "hit" {
		t.Fatal("第二次请求应标记 X-Tg-Cache: hit")
	}
	if !bytes.Equal(w1.Body.Bytes(), w2.Body.Bytes()) {
		t.Fatal("缓存命中应回放相同 body")
	}
	if hits.Load() != 1 {
		t.Fatalf("上游只应被打一次，hits=%d", hits.Load())
	}

	// 计费：第一笔 cost = ceil((7*2M + 3*8M)/1M) = 38；第二笔缓存 cost=0 cache_hit=1
	if cost, _, _ := e.usageRow(t, id1); cost != 38 {
		t.Fatalf("第一笔 cost 应为 38，got %d", cost)
	}
	id2 := e.lastUsage(t)
	if id2 == id1 {
		t.Fatal("应产生第二笔 usage_log")
	}
	if cost, hit, _ := e.usageRow(t, id2); cost != 0 || hit != 1 {
		t.Fatalf("缓存命中笔应为 cost=0 cache_hit=1，got cost=%d hit=%d", cost, hit)
	}
	var used int64
	_ = e.f.db.Raw("SELECT quota_used FROM users WHERE id = 1").Scan(&used).Error
	if used != 38 {
		t.Fatalf("用户仅应被扣 38 点，got %d", used)
	}

	// 不同 prompt → 不同缓存 key → 仍打上游
	w3 := e.post(chatBody("another-question", `,"temperature":0.7`))
	if w3.Header().Get("X-Tg-Cache") == "hit" || hits.Load() != 2 {
		t.Fatalf("不同请求不应命中缓存，cache=%q hits=%d", w3.Header().Get("X-Tg-Cache"), hits.Load())
	}
}

// SSE 流式：正常透传+计量；完成后闸门名额已释放（紧随其后的请求立即可用）
func TestSSEStreamReleasesSlot(t *testing.T) {
	e := newTestEnv(t)
	e.h.MaxConcurrency = 1
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"he\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"llo\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer up.Close()
	e.seedUpstreamChannel(t, 1, "ch", up.URL, nil, 10)

	w := e.post(chatBody("q", `,"stream":true`))
	if w.Code != http.StatusOK {
		t.Fatalf("SSE 应 200，got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "llo") || !strings.Contains(w.Body.String(), "[DONE]") {
		t.Fatalf("SSE body 应透传: %q", w.Body.String())
	}
	// 流式 usage 计量（模型价 2M/8M → cost = 5*2 + 2*8 = 26）
	id := e.lastUsage(t)
	var row struct {
		Cost             int64
		IsStream         int64
		PromptTokens     int64
		CompletionTokens int64
	}
	_ = e.f.db.Raw("SELECT cost, is_stream, prompt_tokens, completion_tokens FROM usage_logs WHERE id = ?", id).Scan(&row).Error
	if row.Cost != 26 || row.IsStream != 1 || row.PromptTokens != 5 || row.CompletionTokens != 2 {
		t.Fatalf("流式计量错误: %+v", row)
	}

	// 名额已释放：下一个请求无需等待即可成功
	w2 := e.post(chatBody("q2", `,"stream":true`))
	if w2.Code != http.StatusOK {
		t.Fatalf("SSE 完成后名额应释放，got %d", w2.Code)
	}
}

// 渠道在但没配 Key（无池、无 legacy 密文）→ 503 channel_key_missing，而非误报 429 限流
func TestNoKeyReturnsChannelKeyMissing(t *testing.T) {
	e := newTestEnv(t)
	now := time.Now().Unix()
	mustExec(t, e.f, `INSERT INTO channels (id,name,base_url,path,weight,priority,status,created_at,updated_at)
		VALUES (1,'nokey','http://up.example','/v1/chat/completions',1,0,1,?,?)`, now, now)
	mustExec(t, e.f, `INSERT INTO channel_abilities (channel_id, model_name) VALUES (1, 'm1')`)

	w := e.post(chatBody("q", ""))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("无 Key 应回 503，got %d body=%s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), "channel_key_missing") {
		t.Fatalf("错误类型应为 channel_key_missing: %s", w.Body)
	}
	if strings.Contains(w.Body.String(), "upstream_busy") {
		t.Fatalf("不应误报限流: %s", w.Body)
	}
}

// Key 池全部禁用（status=0）→ 同样 503 channel_key_missing
func TestAllKeysDisabledReturnsChannelKeyMissing(t *testing.T) {
	e := newTestEnv(t)
	now := time.Now().Unix()
	mustExec(t, e.f, `INSERT INTO channels (id,name,base_url,path,weight,priority,status,created_at,updated_at)
		VALUES (1,'disabled','http://up.example','/v1/chat/completions',1,0,1,?,?)`, now, now)
	mustExec(t, e.f, `INSERT INTO channel_keys (channel_id,key_enc,weight,status,created_at,updated_at)
		VALUES (1,'k1',1,0,?,?)`, now, now)
	mustExec(t, e.f, `INSERT INTO channel_abilities (channel_id, model_name) VALUES (1, 'm1')`)

	w := e.post(chatBody("q", ""))
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "channel_key_missing") {
		t.Fatalf("Key 全禁用应回 503 channel_key_missing，got %d body=%s", w.Code, w.Body)
	}
}

// 模型存在且已授权，但没有任何启用渠道提供 → 503 no_available_channel
func TestNoChannelReturnsNoAvailableChannel(t *testing.T) {
	e := newTestEnv(t)
	now := time.Now().Unix()
	mustExec(t, e.f, `INSERT INTO models (name, input_price, output_price, status, created_at, updated_at)
		VALUES ('m2', 2000000, 8000000, 1, ?, ?)`, now, now)
	mustExec(t, e.f, `INSERT INTO user_model_grants (user_id, model_name, created_at) VALUES (1, 'm2', ?)`, now)

	w := e.post(`{"model":"m2","messages":[{"role":"user","content":"q"}]}`)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("无渠道应回 503，got %d body=%s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), "no_available_channel") {
		t.Fatalf("错误类型应为 no_available_channel: %s", w.Body)
	}
}
