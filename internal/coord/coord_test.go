package coord

import (
	"context"
	"os"
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

	// 闸门
	ctx := context.Background()
	rel, ok := rc.AcquireSlot(ctx, "t:slot", 1)
	if !ok {
		t.Fatal("首个名额应获取成功")
	}
	qctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	if _, ok := rc.AcquireSlot(qctx, "t:slot", 1); ok {
		t.Fatal("满员时排队超时不应获取成功")
	}
	rel()
	rel() // 幂等
	if _, ok := rc.AcquireSlot(ctx, "t:slot", 1); !ok {
		t.Fatal("释放后应可再次获取")
	}

	// 缓存
	rc.CacheSet("t:ck", []byte("v"), time.Minute)
	if v, ok := rc.CacheGet("t:ck"); !ok || string(v) != "v" {
		t.Fatalf("缓存应命中，got %q %v", v, ok)
	}
}
