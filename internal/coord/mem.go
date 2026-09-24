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
	cooldown map[string]cdEntry // scope → 冷却截止 + Backoff 连击计数

	slotsMu sync.Mutex
	slots   map[string]chan struct{}

	cache *memCache
}

// cdEntry 冷却条目：hits 为 Backoff 连击次数（SetCooldown 不动它），
// 冷却到期（惰性清理或下次 Backoff 发现过期）即连同计数一起消失
type cdEntry struct {
	until time.Time
	hits  int
}

// NewMem maxCacheItems 内存 LRU 条数上限（<=0 视为不限，仅靠 TTL）
func NewMem(maxCacheItems int) *Mem {
	return &Mem{
		cooldown: map[string]cdEntry{},
		slots:    map[string]chan struct{}{},
		cache:    newMemCache(maxCacheItems),
	}
}

func (m *Mem) IsCooling(scope string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.cooldown[scope]
	if !ok {
		return false
	}
	if time.Now().After(e.until) {
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
	if e, ok := m.cooldown[scope]; !ok || time.Now().Add(d).After(e.until) {
		m.cooldown[scope] = cdEntry{until: time.Now().Add(d)} // hits 归零：普通冷却不继承退避连击
	}
	m.mu.Unlock()
}

// Backoff 指数退避冷却：base×2^(hits-1) 封顶 max。退避档单调递增且 base 不小于任何
// 普通冷却时长，直接覆盖写不会截断在期冷却
func (m *Mem) Backoff(scope string, base, max time.Duration) time.Duration {
	if base <= 0 {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	e, ok := m.cooldown[scope]
	if !ok || now.After(e.until) {
		e = cdEntry{} // 冷却已到期：连击归零重新起步
	}
	e.hits++
	d := base << min(e.hits-1, 30) // 移位上限防溢出（base<<30 必然已超任何 max）
	if d > max {
		d = max
	}
	e.until = now.Add(d)
	m.cooldown[scope] = e
	return d
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
