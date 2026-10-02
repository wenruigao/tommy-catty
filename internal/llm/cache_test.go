package llm

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSemanticCacheHitMiss(t *testing.T) {
	cache := NewSemanticCache(10, time.Minute, 0)
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
	cache := NewSemanticCache(10, 1*time.Millisecond, 0)
	req := ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "x"}}}
	cache.Put(req, ChatResponse{Content: "y"})
	time.Sleep(5 * time.Millisecond)
	_, hit := cache.Get(req)
	if hit {
		t.Error("expected miss after TTL expiry")
	}
}

func TestSemanticCacheNormalization(t *testing.T) {
	cache := NewSemanticCache(10, time.Minute, 0)
	req1 := ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hello   world"}}}
	req2 := ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hello world"}}}
	cache.Put(req1, ChatResponse{Content: "cached"})
	// 不同空白应命中同一缓存
	_, hit := cache.Get(req2)
	if !hit {
		t.Error("normalized requests should hit same cache entry")
	}
}

// ============================================================
// 字节预算淘汰测试
// ============================================================

func TestSemanticCache_ByteBudget_EvictsOldest(t *testing.T) {
	// 每条约 64(key) + 100(content) + 64(overhead) ≈ 228 字节
	// 设预算 500 字节，第 3 条写入时应淘汰第 1 条
	cache := NewSemanticCache(100, time.Minute, 500)

	for i := 0; i < 3; i++ {
		req := ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: fmt.Sprintf("req-%d", i)}}}
		resp := ChatResponse{Content: strings.Repeat("x", 100)}
		cache.Put(req, resp)
		time.Sleep(time.Millisecond) // 确保时间戳不同
	}

	_, _, size, bytes, evictions := cache.Stats()
	if evictions == 0 {
		t.Error("expected at least one eviction due to byte budget")
	}
	if bytes > 500 {
		t.Errorf("bytes %d exceeds budget 500", bytes)
	}
	// 第 1 条应已被淘汰
	req0 := ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "req-0"}}}
	if _, hit := cache.Get(req0); hit {
		t.Error("oldest entry should have been evicted")
	}
	_ = size
}

func TestSemanticCache_ByteBudget_NoLimitWhenZero(t *testing.T) {
	cache := NewSemanticCache(100, time.Minute, 0) // 不限字节
	for i := 0; i < 50; i++ {
		req := ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: fmt.Sprintf("r%d", i)}}}
		cache.Put(req, ChatResponse{Content: strings.Repeat("y", 200)})
	}
	_, _, size, _, evictions := cache.Stats()
	if size != 50 {
		t.Errorf("expected 50 entries, got %d", size)
	}
	if evictions != 0 {
		t.Errorf("expected 0 evictions with no byte limit, got %d", evictions)
	}
}

func TestSemanticCache_ByteBudget_OverwriteSameKey(t *testing.T) {
	cache := NewSemanticCache(100, time.Minute, 1000)
	req := ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "same"}}}

	cache.Put(req, ChatResponse{Content: strings.Repeat("a", 100)})
	_, _, _, bytes1, _ := cache.Stats()

	// 覆盖同一键：字节应先扣旧再加新，不触发淘汰
	cache.Put(req, ChatResponse{Content: strings.Repeat("b", 100)})
	_, _, size, bytes2, evictions := cache.Stats()

	if size != 1 {
		t.Errorf("expected 1 entry after overwrite, got %d", size)
	}
	if bytes2 != bytes1 {
		t.Errorf("bytes should stay same after same-size overwrite: %d vs %d", bytes1, bytes2)
	}
	if evictions != 0 {
		t.Errorf("overwrite should not evict, got %d", evictions)
	}
}

func TestSemanticCache_ByteBudget_LargeEntryEvictsMultiple(t *testing.T) {
	// 预算 600 字节，先写 2 条小条目（各约 228B），再写 1 条大条目（约 464B）
	// 大条目写入时应淘汰足够多的旧条目以腾出空间
	cache := NewSemanticCache(100, time.Minute, 600)

	small := ChatResponse{Content: strings.Repeat("s", 100)}
	for i := 0; i < 2; i++ {
		req := ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: fmt.Sprintf("small-%d", i)}}}
		cache.Put(req, small)
		time.Sleep(time.Millisecond)
	}

	big := ChatResponse{Content: strings.Repeat("B", 300)}
	reqBig := ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "big"}}}
	cache.Put(reqBig, big)

	_, _, _, bytes, _ := cache.Stats()
	if bytes > 600 {
		t.Errorf("bytes %d exceeds budget 600 after large entry", bytes)
	}
	// 大条目应存在
	if _, hit := cache.Get(reqBig); !hit {
		t.Error("large entry should be in cache")
	}
}

func TestSemanticCache_Clear_ResetsBytes(t *testing.T) {
	cache := NewSemanticCache(100, time.Minute, 10000)
	req := ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "x"}}}
	cache.Put(req, ChatResponse{Content: "hello"})

	cache.Clear()
	_, _, size, bytes, _ := cache.Stats()
	if size != 0 || bytes != 0 {
		t.Errorf("after Clear: size=%d bytes=%d, want 0/0", size, bytes)
	}
}

func TestEstimateSize(t *testing.T) {
	key := strings.Repeat("k", 64)
	resp := ChatResponse{
		Content: "hello world",
		Model:   "gpt-4",
		ToolCalls: []ToolCall{
			{ID: "tc1", Name: "search", Arguments: `{"q":"test"}`},
		},
	}
	size := estimateSize(key, resp)
	// 64(key) + 11(content) + 5(model) + 64(overhead) + 3(id) + 6(name) + 12(args) = 165
	expected := 64 + 11 + 5 + 64 + 3 + 6 + 12
	if size != expected {
		t.Errorf("estimateSize = %d, want %d", size, expected)
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
