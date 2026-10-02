package server

import (
	"net/http"
	"strconv"
	"sync"
	"time"
)

// RateLimitConfig HTTP 层 per-user 请求限流配置。
type RateLimitConfig struct {
	// RequestsPerMinute 每用户每分钟最大请求数（默认 10；<= 0 不限流）。
	RequestsPerMinute int `yaml:"requests_per_minute"`
	// Burst 突发容量（默认等于 RequestsPerMinute；允许短时间内的并发峰值）。
	Burst int `yaml:"burst"`
}

// DefaultRateLimitConfig 返回默认限流配置（10 次/分钟）。
func DefaultRateLimitConfig() RateLimitConfig {
	return RateLimitConfig{RequestsPerMinute: 10, Burst: 10}
}

// UserRateLimiter 管理所有用户的令牌桶，带自动过期清理。
type UserRateLimiter struct {
	mu       sync.Mutex
	buckets  map[string]*tokenBucket
	rate     float64 // 每秒补充令牌数
	burst    int     // 桶容量
	stopCh   chan struct{}
	stopped  bool
}

// tokenBucket 单用户令牌桶。
type tokenBucket struct {
	tokens   float64
	lastTime time.Time
}

// NewUserRateLimiter 创建 per-user 限流器。
// requestsPerMinute <= 0 时返回 nil（不限流）。
func NewUserRateLimiter(cfg RateLimitConfig) *UserRateLimiter {
	if cfg.RequestsPerMinute <= 0 {
		return nil
	}
	burst := cfg.Burst
	if burst <= 0 {
		burst = cfg.RequestsPerMinute
	}
	rl := &UserRateLimiter{
		buckets: make(map[string]*tokenBucket),
		rate:    float64(cfg.RequestsPerMinute) / 60.0,
		burst:   burst,
		stopCh:  make(chan struct{}),
	}
	go rl.cleanup()
	return rl
}

// Allow 检查指定用户是否允许本次请求。
func (rl *UserRateLimiter) Allow(userID string) (allowed bool, retryAfter time.Duration) {
	if rl == nil {
		return true, 0
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	b, ok := rl.buckets[userID]
	if !ok {
		b = &tokenBucket{tokens: float64(rl.burst), lastTime: now}
		rl.buckets[userID] = b
	}

	// 补充令牌
	elapsed := now.Sub(b.lastTime).Seconds()
	b.tokens += elapsed * rl.rate
	if b.tokens > float64(rl.burst) {
		b.tokens = float64(rl.burst)
	}
	b.lastTime = now

	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}

	// 计算需要等待多久才能有 1 个令牌
	deficit := 1 - b.tokens
	retryAfter = time.Duration(deficit/rl.rate*1000) * time.Millisecond
	return false, retryAfter
}

// Stop 停止后台清理 goroutine。
func (rl *UserRateLimiter) Stop() {
	if rl == nil {
		return
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if !rl.stopped {
		rl.stopped = true
		close(rl.stopCh)
	}
}

// cleanup 定期清理长时间不活跃的用户桶（防止内存泄漏）。
func (rl *UserRateLimiter) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			rl.mu.Lock()
			now := time.Now()
			for uid, b := range rl.buckets {
				// 超过 10 分钟无请求则清除
				if now.Sub(b.lastTime) > 10*time.Minute {
					delete(rl.buckets, uid)
				}
			}
			rl.mu.Unlock()
		case <-rl.stopCh:
			return
		}
	}
}

// RateLimitMiddleware 创建 per-user HTTP 限流中间件。
// 必须在 AuthMiddleware 之后使用（依赖 context 中的 userID）。
// limiter 为 nil 时中间件直通（不限流）。
func RateLimitMiddleware(limiter *UserRateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if limiter == nil {
				next.ServeHTTP(w, r)
				return
			}

			userID := UserIDFromContext(r.Context())
			if userID == "" {
				// 未认证请求交给下游处理（不应到达此处）
				next.ServeHTTP(w, r)
				return
			}

			allowed, retryAfter := limiter.Allow(userID)
			if !allowed {
				w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusTooManyRequests)
				w.Write([]byte(`{"error":"rate limit exceeded, please try again later"}`))
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
