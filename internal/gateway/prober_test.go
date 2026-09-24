package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// disableViaSQL 直接置系统禁用状态（模拟熔断结果）
func (e *testEnv) disableViaSQL(t *testing.T, id int64, autoDisabled bool) {
	t.Helper()
	flag := int64(0)
	if autoDisabled {
		flag = time.Now().Unix()
	}
	mustExec(t, e.f, "UPDATE channels SET status = 0, auto_disabled_at = ? WHERE id = ?", flag, id)
}

// 系统熔断禁用的渠道探测成功 → 自动启用、清标记、清熔断计数
func TestAutoProbeRecoversChannel(t *testing.T) {
	e := newTestEnv(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(okBody))
	}))
	defer up.Close()
	e.seedUpstreamChannel(t, 1, "ch", up.URL, nil, 10)
	e.disableViaSQL(t, 1, true)

	n, err := AutoProbeOnce(e.h)
	if err != nil || n != 1 {
		t.Fatalf("应恢复 1 个渠道，got n=%d err=%v", n, err)
	}
	var row struct{ Status, AutoDisabledAt int64 }
	if err := e.f.db.Raw("SELECT status, auto_disabled_at FROM channels WHERE id = 1").Scan(&row).Error; err != nil || row.Status != 1 || row.AutoDisabledAt != 0 {
		t.Fatalf("渠道应已启用且清掉系统标记，got status=%d auto_disabled_at=%d err=%v", row.Status, row.AutoDisabledAt, err)
	}
}

// 人工禁用（auto_disabled_at=0）的渠道永不被探测：上游零命中、状态不变
func TestAutoProbeSkipsManualDisabled(t *testing.T) {
	e := newTestEnv(t)
	var hits atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(okBody))
	}))
	defer up.Close()
	e.seedUpstreamChannel(t, 1, "ch", up.URL, nil, 10)
	e.disableViaSQL(t, 1, false)

	n, err := AutoProbeOnce(e.h)
	if err != nil || n != 0 {
		t.Fatalf("人工禁用不应被探测，got n=%d err=%v", n, err)
	}
	if hits.Load() != 0 {
		t.Fatalf("人工禁用渠道不应收到探测请求，got %d", hits.Load())
	}
	var status int64
	_ = e.f.db.Raw("SELECT status FROM channels WHERE id = 1").Scan(&status)
	if status != 0 {
		t.Fatalf("人工禁用状态不应被改变，got %d", status)
	}
}

// 探测失败（上游 5xx）→ 保持禁用，只更新测试时间戳
func TestAutoProbeFailedStaysDisabled(t *testing.T) {
	e := newTestEnv(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer up.Close()
	e.seedUpstreamChannel(t, 1, "ch", up.URL, nil, 10)
	e.disableViaSQL(t, 1, true)

	n, err := AutoProbeOnce(e.h)
	if err != nil || n != 0 {
		t.Fatalf("探测失败不应恢复，got n=%d err=%v", n, err)
	}
	var status int64
	_ = e.f.db.Raw("SELECT status FROM channels WHERE id = 1").Scan(&status)
	if status != 0 {
		t.Fatalf("探测失败应保持禁用，got %d", status)
	}
}

// 全链路：连续失败触发熔断（写 auto_disabled_at）→ 上游恢复 → AutoProbeOnce 自动拉起。
// 验证 DisableChannel 打的标记与 prober 的恢复条件闭环
func TestBreakerThenAutoProbeRecovers(t *testing.T) {
	e := newTestEnv(t)
	e.h.Breaker = NewBreaker(2) // 连续 2 次失败熔断
	var healthy atomic.Bool
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !healthy.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(okBody))
	}))
	defer up.Close()
	e.seedUpstreamChannel(t, 1, "ch", up.URL, nil, 10)

	// 两笔请求：第一笔 502；第二笔触发熔断（异步禁用）
	for i := 0; i < 2; i++ {
		w := e.post(chatBody("q", ""))
		if w.Code != http.StatusBadGateway {
			t.Fatalf("第 %d 笔应 502，got %d", i+1, w.Code)
		}
	}
	var flag int64
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_ = e.f.db.Raw("SELECT auto_disabled_at FROM channels WHERE id = 1").Scan(&flag).Error
		if flag > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if flag == 0 {
		t.Fatal("熔断禁用应写入 auto_disabled_at 标记")
	}

	// 上游恢复健康 → 探测自动拉起
	healthy.Store(true)
	n, err := AutoProbeOnce(e.h)
	if err != nil || n != 1 {
		t.Fatalf("上游恢复后应自动拉起，got n=%d err=%v", n, err)
	}
	var status int64
	_ = e.f.db.Raw("SELECT status FROM channels WHERE id = 1").Scan(&status)
	if status != 1 {
		t.Fatalf("渠道应已自动启用，got %d", status)
	}
	// 恢复后熔断计数已清零：再失败 1 次不应再次禁用
	healthy.Store(false)
	if w := e.post(chatBody("q", "")); w.Code != http.StatusBadGateway {
		t.Fatalf("恢复后请求应 502，got %d", w.Code)
	}
	_ = e.f.db.Raw("SELECT status FROM channels WHERE id = 1").Scan(&status)
	if status != 1 {
		t.Fatal("清零熔断计数后单次失败不应再次禁用")
	}
}

// ---- 定时渠道体检 ----

// 连续失败达阈值 → 自动禁用（写系统标记，可被自动恢复拉起）+ 指标累计
func TestChannelTestDisablesAfterThreshold(t *testing.T) {
	e := newTestEnv(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer up.Close()
	e.seedUpstreamChannel(t, 1, "ch", up.URL, nil, 10)

	failures := map[int64]int{}
	// 前两轮失败但未达阈值 3：渠道保持启用
	for i := 0; i < 2; i++ {
		n, err := ChannelTestOnce(e.h, 3, failures)
		if err != nil || n != 0 {
			t.Fatalf("第 %d 轮不应禁用，got n=%d err=%v", i+1, n, err)
		}
	}
	var status int64
	_ = e.f.db.Raw("SELECT status FROM channels WHERE id = 1").Scan(&status)
	if status != 1 {
		t.Fatal("未达阈值不应禁用")
	}
	// 第三轮达阈值 → 禁用 + 系统标记
	if n, err := ChannelTestOnce(e.h, 3, failures); err != nil || n != 1 {
		t.Fatalf("第三轮应禁用 1 个渠道，got n=%d err=%v", n, err)
	}
	var row struct{ Status, AutoDisabledAt int64; Remark string }
	if err := e.f.db.Raw("SELECT status, auto_disabled_at, remark FROM channels WHERE id = 1").Scan(&row).Error; err != nil ||
		row.Status != 0 || row.AutoDisabledAt == 0 || !strings.Contains(row.Remark, "定时体检") {
		t.Fatalf("应禁用并写系统标记，got %+v err=%v", row, err)
	}
	if e.m.ChannelProbeResult.With("fail").Value() != 3 {
		t.Fatalf("fail 指标应累计 3，got %d", e.m.ChannelProbeResult.With("fail").Value())
	}
}

// 中途成功清零连续失败计数：fail→fail→ok→fail→fail（阈值 3）不触发禁用
func TestChannelTestSuccessResetsCounter(t *testing.T) {
	e := newTestEnv(t)
	var healthy atomic.Bool
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !healthy.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(okBody))
	}))
	defer up.Close()
	e.seedUpstreamChannel(t, 1, "ch", up.URL, nil, 10)

	failures := map[int64]int{}
	_, _ = ChannelTestOnce(e.h, 3, failures)
	_, _ = ChannelTestOnce(e.h, 3, failures)
	healthy.Store(true)
	if n, _ := ChannelTestOnce(e.h, 3, failures); n != 0 {
		t.Fatal("成功轮不应禁用")
	}
	healthy.Store(false)
	_, _ = ChannelTestOnce(e.h, 3, failures)
	n, _ := ChannelTestOnce(e.h, 3, failures)
	if n != 0 {
		t.Fatal("计数清零后再失败两轮（<3）不应禁用")
	}
	var status int64
	_ = e.f.db.Raw("SELECT status FROM channels WHERE id = 1").Scan(&status)
	if status != 1 {
		t.Fatal("渠道应保持启用")
	}
}

// interval=0 体检循环直接返回（不启动）；含达阈值渠道与自动恢复的联动冒烟：
// 体检禁用（写系统标记）→ 上游恢复 → AutoProbeOnce 能拉起
func TestChannelTestLoopDisabledAndRecoveryHandoff(t *testing.T) {
	e := newTestEnv(t)
	// interval=0：直接返回不阻塞
	ChannelTestLoop(0, 3, e.h)

	var healthy atomic.Bool
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !healthy.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(okBody))
	}))
	defer up.Close()
	e.seedUpstreamChannel(t, 1, "ch", up.URL, nil, 10)

	// 体检禁用
	failures := map[int64]int{}
	for i := 0; i < 3; i++ {
		_, _ = ChannelTestOnce(e.h, 3, failures)
	}
	var status int64
	_ = e.f.db.Raw("SELECT status FROM channels WHERE id = 1").Scan(&status)
	if status != 0 {
		t.Fatal("体检达阈值应禁用")
	}
	// 上游恢复 → 自动探测拉起（体检与自动恢复闭环）
	healthy.Store(true)
	if n, err := AutoProbeOnce(e.h); err != nil || n != 1 {
		t.Fatalf("体检禁用的渠道应能被自动恢复拉起，got n=%d err=%v", n, err)
	}
	_ = e.f.db.Raw("SELECT status FROM channels WHERE id = 1").Scan(&status)
	if status != 1 {
		t.Fatal("渠道应已自动恢复启用")
	}
}

// ---- 配额类 429 的探测语义（管理台「测试」/体检共用内核）----

// 探测命中配额类 429 → 显式分类（Quota=true、错误文案说配额耗尽而非裸 429），
// 且只给探测用的那把 Key 标记配额冷却（其它 Key 不连坐）
func TestProbeQuotaClassifiedAndCoolsOnlyProbedKey(t *testing.T) {
	e := newTestEnv(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"SetLimitExceeded","message":"has reached the set usage limit"}}`))
	}))
	defer up.Close()
	e.seedUpstreamChannel(t, 1, "ch", up.URL, []string{"k1", "k2"}, 10)

	res := ProbeChannel(e.h.DB, e.h.Cipher, e.h.Client, e.h.Coord, e.h.KeyCooldown, 1)
	if res.OK || !res.Quota {
		t.Fatalf("配额 429 应分类为 Quota（非裸 429/故障），got ok=%v quota=%v err=%s", res.OK, res.Quota, res.Err)
	}
	if !strings.Contains(res.Err, "配额耗尽") || !strings.Contains(res.Err, "SetLimitExceeded") {
		t.Fatalf("错误文案应明确说上游配额耗尽并带错误码，got %q", res.Err)
	}
	if !e.cd.IsCooling("ck:1:1") || !e.cd.IsQuotaCooling("ck:1:1") {
		t.Fatal("被探测的 Key 应进入配额冷却")
	}
	if e.cd.IsCooling("ck:1:2") {
		t.Fatal("未探测的 Key 不应连坐冷却")
	}
}

// 探测路径发现配额死 Key 也发站内通知（异步落库）：低流量渠道不依赖业务流量也能告警到管理员。
// 用渠道 2 避开其它用例已触发的节流窗口（notifyLast 按 ck:渠道:Key 全局节流）
func TestProbeQuotaNotifiesPlatformAdmins(t *testing.T) {
	e := newTestEnv(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"SetLimitExceeded"}}`))
	}))
	defer up.Close()
	e.seedUpstreamChannel(t, 2, "ch2", up.URL, []string{"k1"}, 10)
	mustExec(t, e.f, `INSERT INTO users (id, org_id, username, password_hash, role, status, created_at, updated_at)
		VALUES (9, NULL, 'root', 'x', 'platform_admin', 1, 0, 0)`)

	_ = ProbeChannel(e.h.DB, e.h.Cipher, e.h.Client, e.h.Coord, e.h.KeyCooldown, 2)

	var cnt int64
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_ = e.f.db.Raw(`SELECT COUNT(*) FROM notifications WHERE user_id = 9 AND type = 'key_quota_cooling'`).Scan(&cnt).Error
		if cnt > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if cnt == 0 {
		t.Fatal("探测发现配额耗尽应站内通知系统管理员")
	}
}

// 探测成功 → 清除该 Key 冷却：厂商侧限额恢复后点一次「测试」，Key 立即回池
func TestProbeSuccessClearsCooldown(t *testing.T) {
	e := newTestEnv(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(okBody))
	}))
	defer up.Close()
	e.seedUpstreamChannel(t, 1, "ch", up.URL, []string{"k1"}, 10)

	// 预置配额冷却（模拟此前配额耗尽）
	d := e.cd.Backoff("ck:1:1", time.Minute, 24*time.Hour)
	e.cd.MarkQuotaCooling("ck:1:1", d)
	res := ProbeChannel(e.h.DB, e.h.Cipher, e.h.Client, e.h.Coord, e.h.KeyCooldown, 1)
	if !res.OK {
		t.Fatalf("探测应成功，got err=%s", res.Err)
	}
	if e.cd.IsCooling("ck:1:1") || e.cd.IsQuotaCooling("ck:1:1") {
		t.Fatal("探测成功应清除该 Key 的冷却（含配额标记）")
	}
}

// 体检遇到配额耗尽：不计连续失败、不禁用渠道——配额是 Key/账户级问题，
// 渠道本身与其它 Key 健康（否则限额恢复前渠道被整条拉黑）
func TestChannelTestQuotaNeverDisables(t *testing.T) {
	e := newTestEnv(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"SetLimitExceeded"}}`))
	}))
	defer up.Close()
	e.seedUpstreamChannel(t, 1, "ch", up.URL, []string{"k1"}, 10)

	failures := map[int64]int{}
	for i := 0; i < 5; i++ { // 远超阈值 3
		if n, err := ChannelTestOnce(e.h, 3, failures); err != nil || n != 0 {
			t.Fatalf("配额耗尽任何一轮都不应禁用渠道，第 %d 轮 got n=%d err=%v", i+1, n, err)
		}
	}
	var row struct{ Status, LastTestOk int64 }
	_ = e.f.db.Raw("SELECT status, last_test_ok FROM channels WHERE id = 1").Scan(&row)
	if row.Status != 1 {
		t.Fatalf("配额耗尽不应禁用渠道，got status=%d", row.Status)
	}
	if row.LastTestOk != 0 {
		t.Fatalf("测试结果仍应如实记失败（last_test_ok=0），got %d", row.LastTestOk)
	}
}
