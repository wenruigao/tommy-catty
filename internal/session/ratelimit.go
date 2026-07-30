package session

import (
	"sync"
	"time"
)

// RateLimitConfig 限流配置。
type RateLimitConfig struct {
	// RequestsPerMinute 每分钟最大请求数（0 表示不限流）
	RequestsPerMinute int
}

// RateLimiter 基于滑动窗口的 per-user 限流器。
type RateLimiter struct {
	maxPerMinute int
	mu           sync.Mutex
	timestamps   []time.Time // 最近一分钟内的请求时间戳
}

// NewRateLimiter 创建限流器实例。
func NewRateLimiter(cfg RateLimitConfig) *RateLimiter {
	return &RateLimiter{
		maxPerMinute: cfg.RequestsPerMinute,
		timestamps:   make([]time.Time, 0, 64),
	}
}

// Allow 检查当前请求是否被允许（未超限）。
// 如果允许，记录本次请求时间戳。
func (rl *RateLimiter) Allow() bool {
	if rl.maxPerMinute <= 0 {
		return true // 未配置限流
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	windowStart := now.Add(-time.Minute)

	// 清理窗口外的旧时间戳
	valid := rl.timestamps[:0]
	for _, ts := range rl.timestamps {
		if ts.After(windowStart) {
			valid = append(valid, ts)
		}
	}
	rl.timestamps = valid

	// 判断是否超限
	if len(rl.timestamps) >= rl.maxPerMinute {
		return false
	}

	rl.timestamps = append(rl.timestamps, now)
	return true
}
