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
func (Nop) AcquireSlot(context.Context, string, int) (func(), bool) { return func() {}, true }
func (Nop) CacheGet(string) ([]byte, bool)            { return nil, false }
func (Nop) CacheSet(string, []byte, time.Duration)    {}
