package coord

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

func TestMemCooldown(t *testing.T) {
	m := NewMem(0)
	if m.IsCooling("ck:1:2") {
		t.Fatal("未设置冷却却返回 cooling")
	}
	m.SetCooldown("ck:1:2", 40*time.Millisecond)
	if !m.IsCooling("ck:1:2") {
		t.Fatal("设置冷却后应处于冷却期")
	}
	time.Sleep(50 * time.Millisecond)
	if m.IsCooling("ck:1:2") {
		t.Fatal("冷却到期后应自动恢复（惰性过期）")
	}
}

// SetCooldown keep-longer：短冷却不得截断在期的长冷却；长冷却可以延长短冷却
func TestMemCooldownKeepLonger(t *testing.T) {
	m := NewMem(0)
	m.SetCooldown("k:1", 500*time.Millisecond)
	m.SetCooldown("k:1", 30*time.Millisecond) // 并发 429 的短冷却：不应缩短
	if !m.IsCooling("k:1") {
		t.Fatal("短冷却不应清除在期冷却")
	}
	time.Sleep(60 * time.Millisecond) // 30ms 已过，500ms 未到
	if !m.IsCooling("k:1") {
		t.Fatal("短冷却不应截断长冷却（500ms 仍应在期）")
	}

	m2 := NewMem(0)
	m2.SetCooldown("k:2", 30*time.Millisecond)
	m2.SetCooldown("k:2", 500*time.Millisecond) // 更长的冷却到达：应延长
	time.Sleep(60 * time.Millisecond)
	if !m2.IsCooling("k:2") {
		t.Fatal("更长的冷却应覆盖短冷却")
	}
}

// Backoff 指数退避：连击翻倍、封顶 max、冷却到期连击归零重新起步
func TestMemBackoffStaircase(t *testing.T) {
	m := NewMem(0)
	base, cap := 10*time.Minute, 24*time.Hour
	want := []time.Duration{
		10 * time.Minute, 20 * time.Minute, 40 * time.Minute, 80 * time.Minute,
		160 * time.Minute, 320 * time.Minute, 640 * time.Minute, 1280 * time.Minute,
		24 * time.Hour, 24 * time.Hour, // 第 9 次起封顶
	}
	for i, w := range want {
		if got := m.Backoff("k:bk", base, cap); got != w {
			t.Fatalf("第 %d 次退避应为 %v，got %v", i+1, w, got)
		}
	}
	if !m.IsCooling("k:bk") {
		t.Fatal("退避后应处于冷却期")
	}
	// 普通冷却重置连击：SetCooldown 覆盖（keep-longer 成立，须比剩余退避长）后
	// 下一次 Backoff 回到起步档
	mr := NewMem(0)
	mr.Backoff("k:rs", base, cap)            // 10min
	mr.SetCooldown("k:rs", 12*time.Hour)     // 更长 → 覆盖且连击归零
	if got := mr.Backoff("k:rs", base, cap); got != 10*time.Minute {
		t.Fatalf("普通冷却后连击应归零，got %v", got)
	}
}

// 冷却到期（探测成功无人续期）→ 连击自然归零，下一次从起步档重来
func TestMemBackoffResetsAfterExpiry(t *testing.T) {
	m := NewMem(0)
	if got := m.Backoff("k:exp", 30*time.Millisecond, time.Minute); got != 30*time.Millisecond {
		t.Fatalf("首次应为 base，got %v", got)
	}
	time.Sleep(40 * time.Millisecond) // 30ms 冷却过期
	if got := m.Backoff("k:exp", 30*time.Millisecond, time.Minute); got != 30*time.Millisecond {
		t.Fatalf("到期后应回到起步档而非翻倍，got %v", got)
	}
	time.Sleep(40 * time.Millisecond)
	if m.IsCooling("k:exp") {
		t.Fatal("起步档到期后不应再冷却")
	}
}

// 配额标记：MarkQuotaCooling 置位（IsQuotaCooling ⊆ IsCooling）；
// ClearCooldown 立即解除冷却与标记，且连击归零（下次退避回起步档）
func TestMemQuotaCoolingAndClear(t *testing.T) {
	m := NewMem(0)
	d := m.Backoff("ck:1:2", time.Hour, 24*time.Hour)
	m.MarkQuotaCooling("ck:1:2", d)
	if !m.IsCooling("ck:1:2") || !m.IsQuotaCooling("ck:1:2") {
		t.Fatal("配额冷却后 IsCooling/IsQuotaCooling 均应为真")
	}
	m.ClearCooldown("ck:1:2")
	if m.IsCooling("ck:1:2") || m.IsQuotaCooling("ck:1:2") {
		t.Fatal("清除后不应再冷却（含配额标记）")
	}
	// 连击随清除归零：下次退避从起步档而非翻倍档开始
	if got := m.Backoff("ck:1:2", time.Hour, 24*time.Hour); got != time.Hour {
		t.Fatalf("清除后连击应归零，下次退避应为起步档，got %v", got)
	}
	// 未标记过的 scope 恒不构成配额冷却
	m.SetCooldown("ck:1:3", time.Minute)
	if m.IsQuotaCooling("ck:1:3") {
		t.Fatal("普通冷却不应被误判为配额冷却")
	}
	// 清除不存在的 scope 是无害 no-op
	m.ClearCooldown("ck:9:9")
}

func TestMemSlotQueueAndTimeout(t *testing.T) {
	m := NewMem(0)
	ctx := context.Background()

	rel1, ok := m.AcquireSlot(ctx, "ch:1", 1)
	if !ok {
		t.Fatal("首个名额应获取成功")
	}
	// 满员：带超时的排队应失败
	qctx, cancel := context.WithTimeout(ctx, 40*time.Millisecond)
	defer cancel()
	if _, ok := m.AcquireSlot(qctx, "ch:1", 1); ok {
		t.Fatal("满员时排队超时不应获取成功")
	}
	rel1()
	// 释放后可再获取
	if _, ok := m.AcquireSlot(ctx, "ch:1", 1); !ok {
		t.Fatal("释放后应可再次获取")
	}
}

func TestMemSlotReleaseIdempotent(t *testing.T) {
	m := NewMem(0)
	rel, ok := m.AcquireSlot(context.Background(), "ch:1", 1)
	if !ok {
		t.Fatal("应获取成功")
	}
	rel()
	rel() // 幂等：重复 release 不得panic/重复归还
	rel()
	// 名额恰好回到 1：两次获取应一成一败
	if _, ok := m.AcquireSlot(context.Background(), "ch:1", 1); !ok {
		t.Fatal("release 幂等后名额应恰好为 1")
	}
	qctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, ok := m.AcquireSlot(qctx, "ch:1", 1); ok {
		t.Fatal("重复 release 不应多归还名额")
	}
}

func TestMemSlotUnlimited(t *testing.T) {
	m := NewMem(0)
	for i := 0; i < 100; i++ {
		if _, ok := m.AcquireSlot(context.Background(), "ch:9", 0); !ok {
			t.Fatalf("max<=0 应不限流，第 %d 次被拒", i+1)
		}
	}
}

func TestMemCacheLRUEviction(t *testing.T) {
	m := NewMem(2)
	m.CacheSet("k1", []byte("v1"), time.Minute)
	m.CacheSet("k2", []byte("v2"), time.Minute)
	if _, ok := m.CacheGet("k1"); !ok {
		t.Fatal("k1 应命中")
	}
	m.CacheSet("k3", []byte("v3"), time.Minute) // 淘汰最久未用的 k2
	if _, ok := m.CacheGet("k2"); ok {
		t.Fatal("k2 应被 LRU 淘汰")
	}
	if v, ok := m.CacheGet("k1"); !ok || string(v) != "v1" {
		t.Fatal("k1 最近使用过，不应被淘汰")
	}
	if v, ok := m.CacheGet("k3"); !ok || string(v) != "v3" {
		t.Fatal("k3 应命中")
	}
}

// 满容时覆盖已有 key 走"前移+覆写"早退路径，不得挤掉其他条目；
// 已过期但未被惰性清理的条目同理（items 命中即覆写，不产生多余逐出）
func TestMemCacheOverwriteAtCapacity(t *testing.T) {
	m := NewMem(2)
	m.CacheSet("k1", []byte("v1"), time.Minute)
	m.CacheSet("k2", []byte("v2"), time.Minute)
	m.CacheSet("k1", []byte("v1b"), time.Minute) // 满容覆盖：不得逐出 k2
	if v, ok := m.CacheGet("k2"); !ok || string(v) != "v2" {
		t.Fatal("满容覆盖 k1 不应逐出 k2")
	}
	if v, ok := m.CacheGet("k1"); !ok || string(v) != "v1b" {
		t.Fatal("覆盖后应返回新值")
	}
	m.CacheSet("k3", []byte("v3"), time.Minute) // k2 现为最久未用，被逐出
	if _, ok := m.CacheGet("k2"); ok {
		t.Fatal("k2 应在 k3 入缓后被逐出")
	}

	// 过期条目占位时再 set：items 仍命中 → 覆写复活，长度不增
	m2 := NewMem(1)
	m2.CacheSet("e1", []byte("old"), 30*time.Millisecond)
	time.Sleep(40 * time.Millisecond)
	m2.CacheSet("e1", []byte("new"), time.Minute)
	if v, ok := m2.CacheGet("e1"); !ok || string(v) != "new" {
		t.Fatal("过期条目覆写后应立即以新值命中")
	}
}

// 并发 get/set/TTL 过期混合锤击：任何未持锁的 map/list 访问都会被 -race 抓出；
// 断言只有命中语义（不 panic），不断言具体哪次命中（并发下顺序不定）
func TestMemCacheConcurrentHammer(t *testing.T) {
	m := NewMem(4)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 3000; i++ {
				k := fmt.Sprintf("k%d", (g*7+i)%6) // 键集有交集也有竞争
				switch i % 3 {
				case 0:
					m.CacheGet(k)
				case 1:
					m.CacheSet(k, []byte{byte(i)}, time.Duration(i%5)*10*time.Millisecond) // TTL 0~40ms 混入过期
				default:
					m.CacheSet(k, []byte{byte(g)}, time.Minute)
					m.CacheGet(k)
				}
			}
		}(g)
	}
	wg.Wait()
	if _, ok := m.CacheGet("k0"); !ok { // 锤击后写入仍在：最后一批 1 分钟 TTL 的键可命中
		m.CacheSet("k0", []byte("post"), time.Minute) // 不作断言依据，仅保证不 panic
	}
}

func TestMemCacheTTLExpiry(t *testing.T) {
	m := NewMem(0)
	m.CacheSet("k", []byte("v"), 40*time.Millisecond)
	if v, ok := m.CacheGet("k"); !ok || string(v) != "v" {
		t.Fatal("TTL 内应命中")
	}
	time.Sleep(50 * time.Millisecond)
	if _, ok := m.CacheGet("k"); ok {
		t.Fatal("TTL 过期后应 miss")
	}
}

// TestRedisCoord 真实 Redis 集成测试：TG_TEST_REDIS_ADDR=redis:6379 go test 才执行
func TestRedisCoord(t *testing.T) {
	addr := os.Getenv("TG_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("未设置 TG_TEST_REDIS_ADDR，跳过 Redis 协调器测试")
	}
	rc, err := NewRedis(RedisConfig{Addr: addr})
	if err != nil {
		t.Fatalf("连接 Redis 失败: %v", err)
	}
	defer rc.Close()

	// 冷却
	if rc.IsCooling("t:cd") {
		t.Fatal("未设置冷却却返回 cooling")
	}
	rc.SetCooldown("t:cd", 50*time.Millisecond)
	if !rc.IsCooling("t:cd") {
		t.Fatal("设置冷却后应处于冷却期")
	}
	time.Sleep(60 * time.Millisecond)
	if rc.IsCooling("t:cd") {
		t.Fatal("冷却到期后应自动恢复")
	}

	// 冷却 keep-longer：短冷却不截断长冷却
	rc.SetCooldown("t:cd2", 500*time.Millisecond)
	rc.SetCooldown("t:cd2", 40*time.Millisecond)
	time.Sleep(60 * time.Millisecond)
	if !rc.IsCooling("t:cd2") {
		t.Fatal("短冷却不应截断长冷却（keep-longer）")
	}
	rc.SetCooldown("t:cd3", 40*time.Millisecond)
	rc.SetCooldown("t:cd3", 500*time.Millisecond)
	time.Sleep(60 * time.Millisecond)
	if !rc.IsCooling("t:cd3") {
		t.Fatal("长冷却应覆盖短冷却")
	}

	// 秒级窗口边界：1500ms 剩余时到达的 1200ms 冷却不得覆盖（TTL 秒精度会把
	// 剩余截断成 1，令 1200>1 误判"更长"而缩短冷却——必须用 PTTL 毫秒比较）
	rc.SetCooldown("t:cd4", 1500*time.Millisecond)
	rc.SetCooldown("t:cd4", 1200*time.Millisecond)
	if !rc.IsCooling("t:cd4") {
		t.Fatal("覆盖后应仍在冷却期")
	}
	time.Sleep(1000 * time.Millisecond) // 1200ms 已过，1500ms 未到
	if !rc.IsCooling("t:cd4") {
		t.Fatal("短冷却不得截断长冷却（秒级边界）")
	}

	// 指数退避：连击翻倍、封顶 max、到期归零；普通冷却覆盖后连击重置
	if d := rc.Backoff("t:bk1", 50*time.Millisecond, time.Hour); d != 50*time.Millisecond {
		t.Fatalf("首次退避应为 base，got %v", d)
	}
	if d := rc.Backoff("t:bk1", 50*time.Millisecond, time.Hour); d != 100*time.Millisecond {
		t.Fatalf("第二次应翻倍，got %v", d)
	}
	if d := rc.Backoff("t:bk1", 50*time.Millisecond, 120*time.Millisecond); d != 120*time.Millisecond {
		t.Fatalf("应封顶 max，got %v", d)
	}
	if d := rc.Backoff("t:bk2", 40*time.Millisecond, time.Minute); d != 40*time.Millisecond {
		t.Fatalf("首次退避应为 base，got %v", d)
	}
	time.Sleep(50 * time.Millisecond) // 40ms 冷却过期
	if d := rc.Backoff("t:bk2", 40*time.Millisecond, time.Minute); d != 40*time.Millisecond {
		t.Fatalf("到期后应回到起步档，got %v", d)
	}
	rc.SetCooldown("t:bk3", 30*time.Millisecond) // 值 "0"：不继承连击
	time.Sleep(35 * time.Millisecond)
	if d := rc.Backoff("t:bk3", 30*time.Millisecond, time.Minute); d != 30*time.Millisecond {
		t.Fatalf("普通冷却过期后应从起步档开始，got %v", d)
	}

	// 配额标记与清除：标记后 IsQuotaCooling ⊆ IsCooling；清除立即生效且连击归零
	if d := rc.Backoff("t:qc", time.Hour, 24*time.Hour); d != time.Hour {
		t.Fatalf("首次退避应为 base，got %v", d)
	}
	rc.MarkQuotaCooling("t:qc", time.Hour)
	if !rc.IsCooling("t:qc") || !rc.IsQuotaCooling("t:qc") {
		t.Fatal("配额冷却后 IsCooling/IsQuotaCooling 均应为真")
	}
	rc.ClearCooldown("t:qc")
	if rc.IsCooling("t:qc") || rc.IsQuotaCooling("t:qc") {
		t.Fatal("清除后不应再冷却（含配额标记）")
	}
	if d := rc.Backoff("t:qc", time.Hour, 24*time.Hour); d != time.Hour {
		t.Fatalf("清除后连击应归零（DEL 连击键），下次退避应为起步档，got %v", d)
	}
	rc.SetCooldown("t:nc", time.Minute)
	if rc.IsQuotaCooling("t:nc") {
		t.Fatal("普通冷却不应被误判为配额冷却")
	}

	// 闸门（scope 带唯一后缀：防上一轮未释放的租约残留让本轮排队等 45s）
	ctx := context.Background()
	slotScope := fmt.Sprintf("t:slot:%d", time.Now().UnixNano())
	rel, ok := rc.AcquireSlot(ctx, slotScope, 1)
	if !ok {
		t.Fatal("首个名额应获取成功")
	}
	qctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	if _, ok := rc.AcquireSlot(qctx, slotScope, 1); ok {
		t.Fatal("满员时排队超时不应获取成功")
	}
	rel()
	rel() // 幂等
	rel2, ok := rc.AcquireSlot(ctx, slotScope, 1)
	if !ok {
		t.Fatal("释放后应可再次获取")
	}
	rel2() // 收尾释放，避免污染下一轮

	// 缓存
	rc.CacheSet("t:ck", []byte("v"), time.Minute)
	if v, ok := rc.CacheGet("t:ck"); !ok || string(v) != "v" {
		t.Fatalf("缓存应命中，got %q %v", v, ok)
	}
}
