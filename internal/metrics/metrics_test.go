package metrics

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// 回归：histogram 的 _count 与 le="+Inf" 必须等于观察次数本身。
// 曾把「累积语义」的桶计数逐桶求和当总数——一次落在全部桶内的快请求会让
// _count 膨胀 len(buckets) 倍（10 桶 → Prometheus rate(_count) 算出的 QPS 虚高 10 倍）。
func TestHistogramCountMatchesObservations(t *testing.T) {
	m := New()
	for i := 0; i < 3; i++ {
		m.Latency.Observe(0.003)
	}
	// 超出最大桶（30s）的观察：不落入任何显式桶，但必须计入 _count 与 +Inf
	m.Latency.Observe(45)

	w := httptest.NewRecorder()
	m.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	body := w.Body.String()

	if !strings.Contains(body, "tg_gateway_latency_seconds_count 4\n") {
		t.Fatalf("_count 应为观察次数 4（当前实现把累积桶求和，快请求会重复计数）:\n%s", body)
	}
	if !strings.Contains(body, `tg_gateway_latency_seconds_bucket{le="+Inf"} 4`) {
		t.Fatalf("le=+Inf 应为 4（含超出最大桶的观察）:\n%s", body)
	}
	// 桶仍为累积语义：0.003s 的 3 次观察全部落在首桶；45s 不落任何显式桶
	if !strings.Contains(body, `tg_gateway_latency_seconds_bucket{le="0.01"} 3`) {
		t.Fatalf("首桶（le=0.01）应为 3:\n%s", body)
	}
	if !strings.Contains(body, `tg_gateway_latency_seconds_bucket{le="30"} 3`) {
		t.Fatalf("最大显式桶（le=30）应为 3（45s 的观察不落入）:\n%s", body)
	}
}
