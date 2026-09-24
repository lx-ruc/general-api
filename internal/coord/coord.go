// Package coord 高并发协调器：Key 冷却、渠道并发闸门、精确缓存。
// 两种实现：内存（默认，单机零依赖）与 Redis（多实例全局共享）。
// Redis 故障时 fail-open（不冷却/放行/缓存 miss），绝不拖死数据面。
package coord

import (
	"context"
	"time"
)

// Coordinator 协调器接口
//
// scope 约定：
//   - Key 冷却：ck:{channelID}:{keyID}（单 Key 兼容模式用 ck:{channelID}:legacy）
//   - 并发闸门：ch:{channelID}
type Coordinator interface {
	// IsCooling scope 是否处于冷却期
	IsCooling(scope string) bool
	// SetCooldown 设置/刷新冷却
	SetCooldown(scope string, d time.Duration)
	// Backoff 指数退避冷却：按 scope 记连续触发次数，第 n 次冷却 = base×2^(n-1)，
	// 封顶 max，返回本次实际冷却时长（供日志与 Retry-After）。连击计数与冷却同
	// 生命周期：冷却到期即归零——配额恢复后探测成功无人续期、自然重新起步，
	// 到期后无人探测同样归零（退档无害）。用于配额类 429 这类可恢复的持久故障。
	Backoff(scope string, base, max time.Duration) time.Duration

	// MarkQuotaCooling 标记该 scope 当前的冷却是配额耗尽类（区别于普通限流冷却）。
	// 上游厂商侧限额（火山 SetLimitExceeded / OpenAI insufficient_quota）重试不可能
	// 恢复，网关据此在全部候选 Key 均因配额冷却时对客户端回 402 而非 429。
	// 紧跟在 Backoff 之后调用，ttl 传 Backoff 返回的本次冷却时长
	MarkQuotaCooling(scope string, ttl time.Duration)
	// IsQuotaCooling scope 是否处于配额耗尽冷却（恒为 IsCooling 的子集）
	IsQuotaCooling(scope string) bool
	// ClearCooldown 立即解除 scope 的冷却（含配额标记）。管理台用：
	// 厂商侧限额恢复/排障后不必等指数退避自然到期
	ClearCooldown(scope string)

	// AcquireSlot 获取并发闸门名额（内含有界等待）。
	// 返回 release（幂等，可安全多次调用）与是否获得；max<=0 视为不限流，直接放行。
	AcquireSlot(ctx context.Context, scope string, max int) (release func(), acquired bool)

	// CacheGet / CacheSet 精确缓存（val 为完整上游响应 body）
	CacheGet(key string) ([]byte, bool)
	CacheSet(key string, val []byte, ttl time.Duration)
}

// Nop 空实现（闸门/冷却/缓存全关）
type Nop struct{}

func (Nop) IsCooling(string) bool                     { return false }
func (Nop) SetCooldown(string, time.Duration)         {}
func (Nop) Backoff(string, time.Duration, time.Duration) time.Duration { return 0 }
func (Nop) MarkQuotaCooling(string, time.Duration)    {}
func (Nop) IsQuotaCooling(string) bool                { return false }
func (Nop) ClearCooldown(string)                      {}
func (Nop) AcquireSlot(context.Context, string, int) (func(), bool) { return func() {}, true }
func (Nop) CacheGet(string) ([]byte, bool)            { return nil, false }
func (Nop) CacheSet(string, []byte, time.Duration)    {}
