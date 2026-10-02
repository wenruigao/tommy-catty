package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestUserRateLimiter_Allow(t *testing.T) {
	rl := NewUserRateLimiter(RateLimitConfig{RequestsPerMinute: 3, Burst: 3})
	defer rl.Stop()

	// 前 3 次应通过
	for i := 0; i < 3; i++ {
		allowed, _ := rl.Allow("user1")
		if !allowed {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}

	// 第 4 次应被拒绝
	allowed, retryAfter := rl.Allow("user1")
	if allowed {
		t.Error("4th request should be denied")
	}
	if retryAfter <= 0 {
		t.Error("retryAfter should be positive")
	}
}

func TestUserRateLimiter_PerUser(t *testing.T) {
	rl := NewUserRateLimiter(RateLimitConfig{RequestsPerMinute: 2, Burst: 2})
	defer rl.Stop()

	rl.Allow("user1")
	rl.Allow("user1")

	// user1 已耗尽
	if allowed, _ := rl.Allow("user1"); allowed {
		t.Error("user1 should be limited")
	}

	// user2 不受影响
	if allowed, _ := rl.Allow("user2"); !allowed {
		t.Error("user2 should be allowed")
	}
}

func TestUserRateLimiter_Refill(t *testing.T) {
	// 60 次/分钟 = 1 次/秒
	rl := NewUserRateLimiter(RateLimitConfig{RequestsPerMinute: 60, Burst: 1})
	defer rl.Stop()

	// 消耗唯一的令牌
	if allowed, _ := rl.Allow("u"); !allowed {
		t.Fatal("first request should be allowed")
	}
	if allowed, _ := rl.Allow("u"); allowed {
		t.Fatal("second request should be denied")
	}

	// 等待令牌补充（1秒补充1个）
	time.Sleep(1100 * time.Millisecond)
	if allowed, _ := rl.Allow("u"); !allowed {
		t.Error("request after refill should be allowed")
	}
}

func TestUserRateLimiter_NilWhenDisabled(t *testing.T) {
	rl := NewUserRateLimiter(RateLimitConfig{RequestsPerMinute: 0})
	if rl != nil {
		t.Error("limiter should be nil when RequestsPerMinute <= 0")
	}
}

func TestUserRateLimiter_NilAllow(t *testing.T) {
	var rl *UserRateLimiter // nil
	allowed, _ := rl.Allow("anyone")
	if !allowed {
		t.Error("nil limiter should always allow")
	}
}

func TestRateLimitMiddleware_Pass(t *testing.T) {
	rl := NewUserRateLimiter(RateLimitConfig{RequestsPerMinute: 10, Burst: 10})
	defer rl.Stop()

	handler := RateLimitMiddleware(rl)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// 模拟已认证请求
	req := httptest.NewRequest("POST", "/api/v1/chat", nil)
	ctx := context.WithValue(req.Context(), userIDKey, "user1")
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestRateLimitMiddleware_429(t *testing.T) {
	rl := NewUserRateLimiter(RateLimitConfig{RequestsPerMinute: 2, Burst: 2})
	defer rl.Stop()

	handler := RateLimitMiddleware(rl)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	makeReq := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/v1/chat", nil)
		ctx := context.WithValue(req.Context(), userIDKey, "user1")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req.WithContext(ctx))
		return w
	}

	makeReq() // 1
	makeReq() // 2
	w := makeReq() // 3 → 429

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429, got %d", w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Error("429 should include Retry-After header")
	}
}

func TestRateLimitMiddleware_NilLimiter_Passthrough(t *testing.T) {
	handler := RateLimitMiddleware(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	ctx := context.WithValue(req.Context(), userIDKey, "user1")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req.WithContext(ctx))

	if w.Code != http.StatusOK {
		t.Errorf("nil limiter should passthrough, got %d", w.Code)
	}
}

func TestRateLimitMiddleware_NoUserID_Passthrough(t *testing.T) {
	rl := NewUserRateLimiter(RateLimitConfig{RequestsPerMinute: 1, Burst: 1})
	defer rl.Stop()

	handler := RateLimitMiddleware(rl)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// 无 userID 的请求（不应到达此处，但防御性直通）
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("no-userID request should passthrough, got %d", w.Code)
	}
}
