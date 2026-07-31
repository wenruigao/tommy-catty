package llm

import (
	"testing"
	"time"
)

func TestSemanticCacheHitMiss(t *testing.T) {
	cache := NewSemanticCache(10, time.Minute)
	req := ChatRequest{Model: "test", Messages: []Message{{Role: "user", Content: "hello world"}}}

	// Miss
	_, hit := cache.Get(req)
	if hit {
		t.Error("expected miss on empty cache")
	}

	// Put and hit
	resp := ChatResponse{Content: "hi there"}
	cache.Put(req, resp)
	got, hit := cache.Get(req)
	if !hit {
		t.Error("expected hit after put")
	}
	if got.Content != "hi there" {
		t.Errorf("unexpected cached content: %q", got.Content)
	}
}

func TestSemanticCacheTTL(t *testing.T) {
	cache := NewSemanticCache(10, 1*time.Millisecond)
	req := ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "x"}}}
	cache.Put(req, ChatResponse{Content: "y"})
	time.Sleep(5 * time.Millisecond)
	_, hit := cache.Get(req)
	if hit {
		t.Error("expected miss after TTL expiry")
	}
}

func TestSemanticCacheNormalization(t *testing.T) {
	cache := NewSemanticCache(10, time.Minute)
	req1 := ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hello   world"}}}
	req2 := ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hello world"}}}
	cache.Put(req1, ChatResponse{Content: "cached"})
	// 不同空白应命中同一缓存
	_, hit := cache.Get(req2)
	if !hit {
		t.Error("normalized requests should hit same cache entry")
	}
}

func TestMeter(t *testing.T) {
	m := NewMeter(1000)
	m.Record(TokenRecord{Category: UsageExecution, TotalTokens: 500, PromptTokens: 300, CompletionTokens: 200})
	m.Record(TokenRecord{Category: UsagePlanning, TotalTokens: 600, PromptTokens: 400, CompletionTokens: 200})

	used, limit, exceeded := m.CheckBudget()
	if used != 1100 || limit != 1000 || !exceeded {
		t.Errorf("expected exceeded: used=%d limit=%d exceeded=%v", used, limit, exceeded)
	}

	s := m.Summary()
	if s.TotalTokens != 1100 {
		t.Errorf("expected total 1100, got %d", s.TotalTokens)
	}
	if s.ByCategory["execution"] != 500 {
		t.Errorf("expected execution=500, got %d", s.ByCategory["execution"])
	}
}
