// Package metrics 零依赖的 Prometheus 文本格式指标（数据面专用，标签基数固定）
package metrics

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// Counter 单调计数器
type Counter struct {
	v atomic.Uint64
}

func (c *Counter) Inc()      { c.v.Add(1) }
func (c *Counter) Value() uint64 { return c.v.Load() }

// Gauge 可增减值
type Gauge struct {
	v atomic.Int64
}

func (g *Gauge) Inc()   { g.v.Add(1) }
func (g *Gauge) Dec()   { g.v.Add(-1) }
func (g *Gauge) Value() int64 { return g.v.Load() }

// counterVec 带固定小标签集的计数器组
type counterVec struct {
	name, help string
	children   sync.Map // label value → *Counter
}

func (m *counterVec) With(label string) *Counter {
	v, _ := m.children.LoadOrStore(label, &Counter{})
	return v.(*Counter)
}

// Histogram 延迟直方图（秒）
type Histogram struct {
	name, help string
	buckets    []float64 // 上界
	counts     []uint64
	sum        atomic.Uint64 // 微秒累加，导出时转秒
	mu         sync.Mutex
}

func NewHistogram(name, help string, buckets []float64) *Histogram {
	return &Histogram{name: name, help: help, buckets: buckets, counts: make([]uint64, len(buckets))}
}

func (h *Histogram) Observe(seconds float64) {
	us := uint64(seconds * 1e6)
	h.sum.Add(us)
	h.mu.Lock()
	for i, b := range h.buckets {
		if seconds <= b {
			h.counts[i]++
		}
	}
	h.mu.Unlock()
}

// Metrics 全局指标集合
type Metrics struct {
	// 请求数按状态码（200/401/403/429/5xx…基数有限）
	Requests *counterVec
	// 网关侧端到端延迟
	Latency *Histogram
	// 当前进行中的 SSE 流数
	ActiveStreams Gauge
	// 上游错误（网络失败/5xx）
	UpstreamErrors Counter
	// 计费结算失败
	SettleErrors Counter
	// 渠道熔断自动禁用次数
	ChannelDisabled Counter
	// 上游 429（触发 Key 冷却 + 换 Key/渠道重试）
	Upstream429 Counter
	// Key 进入冷却次数
	KeyCooldown Counter
	// Key 因上游 401/403 被自动禁用次数
	KeyDisabled Counter
	// 渠道闸门排队等待时长（秒）
	QueueWait *Histogram
	// 闸门排队超时次数
	QueueTimeouts Counter
	// 精确缓存命中次数
	CacheHits Counter
	// 协调器 Redis 故障次数（fail-open）
	CoordRedisErrors Counter
	// 预算告警档位触发次数（按主体 org/user）
	AlertTriggers *counterVec
	// 预算告警检查被 60s 节流跳过的次数
	AlertThrottled Counter
}

func New() *Metrics {
	return &Metrics{
		Requests: &counterVec{name: "tg_gateway_requests_total", help: "数据面请求数（按响应状态码）"},
		Latency: NewHistogram("tg_gateway_latency_seconds", "数据面请求端到端延迟（秒）",
			[]float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30}),
		QueueWait: NewHistogram("tg_gateway_queue_wait_seconds", "渠道闸门排队等待时长（秒）",
			[]float64{0.005, 0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10}),
		AlertTriggers: &counterVec{name: "tg_alert_triggers_total", help: "预算告警档位触发次数（按主体）"},
	}
}

// Handler GET /metrics
func (m *Metrics) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b strings.Builder
		// requests vec
		writeVec(&b, m.Requests, "code")
		// latency + queue wait histograms
		writeHistogram(&b, m.Latency)
		writeHistogram(&b, m.QueueWait)
		// simple counters / gauges
		writeSimple(&b, "tg_gateway_active_streams", "当前进行中的 SSE 流数", "gauge", fmt.Sprint(m.ActiveStreams.Value()))
		writeSimple(&b, "tg_gateway_upstream_errors_total", "上游错误次数（网络失败/5xx）", "counter", fmt.Sprint(m.UpstreamErrors.Value()))
		writeSimple(&b, "tg_gateway_settle_errors_total", "计费结算失败次数", "counter", fmt.Sprint(m.SettleErrors.Value()))
		writeSimple(&b, "tg_gateway_channels_disabled_total", "渠道熔断自动禁用次数", "counter", fmt.Sprint(m.ChannelDisabled.Value()))
		writeSimple(&b, "tg_gateway_upstream_429_total", "上游 429 次数（触发 Key 冷却重试）", "counter", fmt.Sprint(m.Upstream429.Value()))
		writeSimple(&b, "tg_gateway_key_cooldown_total", "Key 进入冷却次数", "counter", fmt.Sprint(m.KeyCooldown.Value()))
		writeSimple(&b, "tg_gateway_key_disabled_total", "Key 因上游 401/403 自动禁用次数", "counter", fmt.Sprint(m.KeyDisabled.Value()))
		writeSimple(&b, "tg_gateway_queue_timeouts_total", "渠道闸门排队超时次数", "counter", fmt.Sprint(m.QueueTimeouts.Value()))
		writeSimple(&b, "tg_gateway_cache_hits_total", "精确缓存命中次数", "counter", fmt.Sprint(m.CacheHits.Value()))
		writeSimple(&b, "tg_coord_redis_errors_total", "协调器 Redis 故障次数（fail-open）", "counter", fmt.Sprint(m.CoordRedisErrors.Value()))
		writeVec(&b, m.AlertTriggers, "subject")
		writeSimple(&b, "tg_alert_throttled_total", "预算告警检查被节流跳过次数（60s/主体）", "counter", fmt.Sprint(m.AlertThrottled.Value()))
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte(b.String()))
	}
}

func writeSimple(b *strings.Builder, name, help, typ, val string) {
	b.WriteString(fmt.Sprintf("# HELP %s %s\n# TYPE %s %s\n%s %s\n", name, help, name, typ, name, val))
}

func writeVec(b *strings.Builder, v *counterVec, label string) {
	if v == nil {
		return
	}
	b.WriteString(fmt.Sprintf("# HELP %s %s\n# TYPE %s counter\n", v.name, v.help, v.name))
	type kv struct{ k string; v uint64 }
	var rows []kv
	v.children.Range(func(key, val any) bool {
		rows = append(rows, kv{key.(string), val.(*Counter).Value()})
		return true
	})
	sort.Slice(rows, func(i, j int) bool { return rows[i].k < rows[j].k })
	for _, r := range rows {
		b.WriteString(fmt.Sprintf("%s{%s=%q} %d\n", v.name, label, r.k, r.v))
	}
}

func writeHistogram(b *strings.Builder, h *Histogram) {
	if h == nil {
		return
	}
	b.WriteString(fmt.Sprintf("# HELP %s %s\n# TYPE %s histogram\n", h.name, h.help, h.name))
	h.mu.Lock()
	var totalCount uint64
	for i, c := range h.counts {
		totalCount += c
		b.WriteString(fmt.Sprintf("%s_bucket{le=%q} %d\n", h.name, trimFloat(h.buckets[i]), c))
	}
	sumUs := h.sum.Load()
	h.mu.Unlock()
	b.WriteString(fmt.Sprintf("%s_bucket{le=\"+Inf\"} %d\n", h.name, totalCount))
	b.WriteString(fmt.Sprintf("%s_sum %s\n", h.name, trimFloat(float64(sumUs)/1e6)))
	b.WriteString(fmt.Sprintf("%s_count %d\n", h.name, totalCount))
}

func trimFloat(f float64) string {
	s := fmt.Sprintf("%g", f)
	return s
}
