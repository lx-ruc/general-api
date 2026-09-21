package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

type visitor struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// RateLimiter 内存令牌桶（按 key 隔离），带过期清理
type RateLimiter struct {
	mu        sync.Mutex
	visitors  map[string]*visitor
	rate      rate.Limit
	burst     int
	unlimited bool // 构造时 perMinute<=0：不限流
}

// NewRateLimiter perMinute 每分钟配额；burst 允许的瞬时突发。
// perMinute<=0 = 不限流（Allow 恒 true）——与本仓库其他阈值语义一致（0=关闭），
// 避免配 0 想关限流的部署被静默替换成 60 RPM
func NewRateLimiter(perMinute, burst int) *RateLimiter {
	if perMinute <= 0 {
		return &RateLimiter{unlimited: true}
	}
	if burst <= 0 {
		burst = 1
	}
	r := &RateLimiter{
		visitors: make(map[string]*visitor),
		rate:     rate.Limit(float64(perMinute) / 60.0),
		burst:    burst,
	}
	go r.janitor()
	return r
}

func (r *RateLimiter) Allow(key string) bool {
	if r.unlimited {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.visitors[key]
	if !ok {
		v = &visitor{limiter: rate.NewLimiter(r.rate, r.burst)}
		r.visitors[key] = v
	}
	v.lastSeen = time.Now()
	return v.limiter.Allow()
}

func (r *RateLimiter) janitor() {
	for range time.Tick(5 * time.Minute) {
		r.mu.Lock()
		for k, v := range r.visitors {
			if time.Since(v.lastSeen) > 10*time.Minute {
				delete(r.visitors, k)
			}
		}
		r.mu.Unlock()
	}
}

// Middleware gin 中间件形式；keyFn 从请求上下文取限流 key
func (r *RateLimiter) Middleware(keyFn func(c *gin.Context) string, failType, failMsg string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !r.Allow(keyFn(c)) {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": gin.H{"message": failMsg, "type": failType, "code": failType},
			})
			return
		}
		c.Next()
	}
}
