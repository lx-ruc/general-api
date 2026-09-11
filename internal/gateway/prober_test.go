package gateway

import (
	"net/http"
	"net/http/httptest"
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
