package coord

import (
	"container/list"
	"context"
	"sync"
	"time"
)

// Mem 进程内存实现（单机/未配置 Redis）。
// 闸门与冷却为 per-node：多实例部署时实际并发 ≈ max × 节点数（见 docs/高并发设计.md 的取舍声明）。
type Mem struct {
	mu       sync.Mutex
	cooldown map[string]time.Time // scope → 冷却截止

	slotsMu sync.Mutex
	slots   map[string]chan struct{}

	cache *memCache
}

// NewMem maxCacheItems 内存 LRU 条数上限（<=0 视为不限，仅靠 TTL）
func NewMem(maxCacheItems int) *Mem {
	return &Mem{
		cooldown: map[string]time.Time{},
		slots:    map[string]chan struct{}{},
		cache:    newMemCache(maxCacheItems),
	}
}

func (m *Mem) IsCooling(scope string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	until, ok := m.cooldown[scope]
	if !ok {
		return false
	}
	if time.Now().After(until) {
		delete(m.cooldown, scope) // 惰性清理
		return false
	}
	return true
}

// SetCooldown 设置冷却；已有更长的冷却在期时只保留更长的——
// 避免并发请求的短 429 冷却覆盖 401/403 的长效冷却（与 Redis 实现 keep-longer 语义一致）
func (m *Mem) SetCooldown(scope string, d time.Duration) {
	if d <= 0 {
		return
	}
	m.mu.Lock()
	if until, ok := m.cooldown[scope]; !ok || time.Now().Add(d).After(until) {
		m.cooldown[scope] = time.Now().Add(d)
	}
	m.mu.Unlock()
}

func (m *Mem) AcquireSlot(ctx context.Context, scope string, max int) (func(), bool) {
	if max <= 0 {
		return func() {}, true // 不限流
	}
	m.slotsMu.Lock()
	ch, ok := m.slots[scope]
	if !ok || cap(ch) != max { // 配置变更后重建（容量不同）
		ch = make(chan struct{}, max)
		m.slots[scope] = ch
	}
	m.slotsMu.Unlock()

	select {
	case ch <- struct{}{}:
		var once sync.Once
		return func() {
			once.Do(func() { <-ch }) // 幂等 release，与 Redis ZREM 语义对齐
		}, true
	case <-ctx.Done():
		return func() {}, false
	}
}

func (m *Mem) CacheGet(key string) ([]byte, bool) { return m.cache.get(key) }

func (m *Mem) CacheSet(key string, val []byte, ttl time.Duration) { m.cache.set(key, val, ttl) }

// ---- 内存 LRU 缓存（TTL + 条数上限）----

type cacheEntry struct {
	key    string
	val    []byte
	expire time.Time
}

type memCache struct {
	mu       sync.Mutex
	maxItems int
	ll       *list.List               // front=最近使用
	items    map[string]*list.Element // key → element
}

func newMemCache(maxItems int) *memCache {
	return &memCache{maxItems: maxItems, ll: list.New(), items: map[string]*list.Element{}}
}

func (c *memCache) get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return nil, false
	}
	e := el.Value.(*cacheEntry)
	if time.Now().After(e.expire) {
		c.ll.Remove(el)
		delete(c.items, key)
		return nil, false
	}
	c.ll.MoveToFront(el)
	return e.val, true
}

func (c *memCache) set(key string, val []byte, ttl time.Duration) {
	if ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok { // 已存在：覆盖并前移
		e := el.Value.(*cacheEntry)
		e.val, e.expire = val, time.Now().Add(ttl)
		c.ll.MoveToFront(el)
		return
	}
	if c.maxItems > 0 {
		for c.ll.Len() >= c.maxItems { // 淘汰最久未用
			back := c.ll.Back()
			if back == nil {
				break
			}
			c.ll.Remove(back)
			delete(c.items, back.Value.(*cacheEntry).key)
		}
	}
	el := c.ll.PushFront(&cacheEntry{key: key, val: val, expire: time.Now().Add(ttl)})
	c.items[key] = el
}
