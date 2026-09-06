package gateway

import (
	"log/slog"
	"sync"
	"time"
)

// Breaker 渠道熔断：连续失败 N 次自动禁用渠道（改 DB status=0），
// 成功一次即清零。多实例下各自计数（触发更快，可接受）；管理员手动启用即恢复。
type Breaker struct {
	mu        sync.Mutex
	fails     map[int64]int
	threshold int
}

func NewBreaker(threshold int) *Breaker {
	return &Breaker{fails: map[int64]int{}, threshold: threshold}
}

// Enabled 是否启用熔断（threshold<=0 关闭）
func (b *Breaker) Enabled() bool { return b.threshold > 0 }

// RecordSuccess 渠道请求成功，清零连续失败计数
func (b *Breaker) RecordSuccess(channelID int64) {
	b.mu.Lock()
	delete(b.fails, channelID)
	b.mu.Unlock()
}

// RecordFailure 渠道请求失败（网络错误或 5xx）；返回是否达到阈值应禁用
func (b *Breaker) RecordFailure(channelID int64) (shouldDisable bool) {
	if !b.Enabled() {
		return false
	}
	b.mu.Lock()
	b.fails[channelID]++
	n := b.fails[channelID]
	b.mu.Unlock()
	return n >= b.threshold
}

// Disable 执行禁用（写库 + 日志）；供达到阈值时调用
func (h *Handler) DisableChannel(channelID int64, name string) {
	res := h.DB.Exec("UPDATE channels SET status = 0, remark = ?, updated_at = ? WHERE id = ? AND status = 1",
		"熔断：连续失败自动禁用（"+time.Now().Format("2006-01-02 15:04")+"），修复后请手动启用并测试",
		time.Now().Unix(), channelID)
	if res.Error != nil {
		slog.Warn("熔断禁用写库失败", "channel", name, "err", res.Error)
		return
	}
	if res.RowsAffected > 0 {
		slog.Warn("渠道熔断：连续失败已自动禁用", "channel_id", channelID, "channel", name,
			"threshold", h.Breaker.threshold)
		if h.Metrics != nil {
			h.Metrics.ChannelDisabled.Inc()
		}
	}
}
