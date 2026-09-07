package coord

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// RedisCoord 多实例全局协调实现。
//
//   - 冷却：SET NX EX（tg:cd:{scope}）
//   - 闸门：ZSET lease（tg:slot:{scope}），member=请求 uuid、score=租约到期 ms；
//     拿到名额后心跳续租（lease/3），进程崩溃最迟 lease 时长自动回收名额；
//     排队者按 50→200ms 退避轮询，ctx 取消立即退出
//   - 缓存：GET / SETEX（tg:cache:{key}）
//
// 任何 Redis 错误 → fail-open（不冷却/放行/缓存 miss）+ 限频错误日志 + onError 计数。
type RedisCoord struct {
	client  *redis.Client
	lease   time.Duration
	onError func()
	lastLog atomic.Int64 // 上次错误日志的 unix ms（限频 1 次/分钟）
}

// RedisConfig Redis 连接与租约参数（Lease 为 0 用默认 45s）
type RedisConfig struct {
	Addr     string
	Password string
	DB       int
	Lease    time.Duration
	OnError  func() // Redis 故障回调（接 metrics 计数；可为 nil）
}

const defaultLease = 45 * time.Second

func NewRedis(cfg RedisConfig) (*RedisCoord, error) {
	lease := cfg.Lease
	if lease <= 0 {
		lease = defaultLease
	}
	client := redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
		DialTimeout:  3 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, err
	}
	return &RedisCoord{client: client, lease: lease, onError: cfg.OnError}, nil
}

// Close 关闭连接
func (r *RedisCoord) Close() error { return r.client.Close() }

func (r *RedisCoord) err(ctx context.Context, op string, e error) bool {
	if e == nil || e == context.Canceled || e == context.DeadlineExceeded {
		return false
	}
	if r.onError != nil {
		r.onError()
	}
	if now := time.Now().UnixMilli(); now-r.lastLog.Load() >= 60_000 {
		r.lastLog.Store(now)
		slog.Error("coord redis 故障，fail-open", "op", op, "err", e)
	}
	return true
}

func withTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 3*time.Second)
}

// ---- 冷却 ----

func (r *RedisCoord) IsCooling(scope string) bool {
	ctx, cancel := withTimeout()
	defer cancel()
	n, err := r.client.Exists(ctx, "tg:cd:"+scope).Result()
	if r.err(ctx, "is_cooling", err) {
		return false // fail-open
	}
	return n > 0
}

func (r *RedisCoord) SetCooldown(scope string, d time.Duration) {
	if d <= 0 {
		return
	}
	ctx, cancel := withTimeout()
	defer cancel()
	err := r.client.Set(ctx, "tg:cd:"+scope, "1", d).Err()
	r.err(ctx, "set_cooldown", err)
}

// ---- 并发闸门 ----

// acquireLua 原子占坑：清过期成员 → 未满则 ZADD 租约
var acquireLua = redis.NewScript(`
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', ARGV[1])
if redis.call('ZCARD', KEYS[1]) < tonumber(ARGV[2]) then
  redis.call('ZADD', KEYS[1], ARGV[3], ARGV[4])
  redis.call('PEXPIRE', KEYS[1], ARGV[5])
  return 1
end
return 0`)

// renewLua 续租：member 仍在才更新 score（XX），不在返回 0
var renewLua = redis.NewScript(`
if redis.call('ZADD', KEYS[1], 'XX', 'CH', ARGV[1], ARGV[2]) == 1 then
  return 1
end
return 0`)

var releaseLua = redis.NewScript(`return redis.call('ZREM', KEYS[1], ARGV[1])`)

func (r *RedisCoord) AcquireSlot(ctx context.Context, scope string, max int) (func(), bool) {
	if max <= 0 {
		return func() {}, true // 不限流
	}
	slotKey := "tg:slot:" + scope
	member := uuid.NewString()
	zsetTTL := (4 * r.lease).Milliseconds()

	backoff := 50 * time.Millisecond
	for {
		nowMs := time.Now().UnixMilli()
		expireMs := nowMs + r.lease.Milliseconds()
		res, err := acquireLua.Run(ctx, r.client, []string{slotKey},
			nowMs, max, expireMs, member, zsetTTL).Int()
		if err != nil {
			r.err(ctx, "acquire_slot", err)
			return func() {}, true // fail-open：闸门全开
		}
		if res == 1 {
			stop := make(chan struct{})
			go r.heartbeat(slotKey, member, stop)
			var once sync.Once
			return func() {
				once.Do(func() {
					close(stop)
					rctx, cancel := withTimeout()
					defer cancel()
					_ = releaseLua.Run(rctx, r.client, []string{slotKey}, member).Err()
				})
			}, true
		}
		// 满员：退避轮询（ctx 取消立即退出）
		select {
		case <-ctx.Done():
			return func() {}, false
		case <-time.After(backoff):
		}
		if backoff < 200*time.Millisecond {
			backoff += 50 * time.Millisecond
		}
	}
}

// heartbeat 持有期间续租；SSE 时长无界，靠续租保住名额，崩溃后最迟 lease 自动回收
func (r *RedisCoord) heartbeat(slotKey, member string, stop chan struct{}) {
	ticker := time.NewTicker(r.lease / 3)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			ctx, cancel := withTimeout()
			expireMs := time.Now().Add(r.lease).UnixMilli()
			_, err := renewLua.Run(ctx, r.client, []string{slotKey}, expireMs, member).Int()
			cancel()
			if r.err(ctx, "renew", err) {
				return // 续租失败放弃（最坏 lease 后名额自动回收，短暂超发）
			}
		}
	}
}

// ---- 缓存 ----

func (r *RedisCoord) CacheGet(key string) ([]byte, bool) {
	ctx, cancel := withTimeout()
	defer cancel()
	val, err := r.client.Get(ctx, "tg:cache:"+key).Bytes()
	if err == redis.Nil {
		return nil, false
	}
	if r.err(ctx, "cache_get", err) {
		return nil, false // fail-open：当 miss
	}
	return val, true
}

func (r *RedisCoord) CacheSet(key string, val []byte, ttl time.Duration) {
	if ttl <= 0 {
		return
	}
	ctx, cancel := withTimeout()
	defer cancel()
	err := r.client.Set(ctx, "tg:cache:"+key, val, ttl).Err()
	r.err(ctx, "cache_set", err)
}
