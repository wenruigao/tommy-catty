// wiring_test.go 验证语义缓存与 Token 计量在网关中的接线行为。
package llm

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeCountingProvider 记录调用次数的模拟供应商。
type fakeCountingProvider struct {
	calls int
	resp  ChatResponse
}

func (f *fakeCountingProvider) Name() string   { return "fake" }
func (f *fakeCountingProvider) Model() string  { return "fake-model" }
func (f *fakeCountingProvider) MaxTokens() int { return 4096 }
func (f *fakeCountingProvider) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	f.calls++
	return f.resp, nil
}
func (f *fakeCountingProvider) ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, error) {
	return nil, errors.New("not implemented")
}

func newFakeGateway(resp ChatResponse) (*Gateway, *fakeCountingProvider) {
	fp := &fakeCountingProvider{resp: resp}
	gw := NewGateway()
	gw.Register(fp)
	gw.SetDefault("fake")
	return gw, fp
}

// TestCacheKey_IncludesTools 验证缓存键包含工具列表（不同工具集不得互相命中）。
func TestCacheKey_IncludesTools(t *testing.T) {
	base := ChatRequest{Messages: []Message{{Role: "user", Content: "hello"}}}
	withA := base
	withA.Tools = []ToolDef{{Name: "tool_a"}}
	withB := base
	withB.Tools = []ToolDef{{Name: "tool_b"}}
	withA2 := base
	withA2.Tools = []ToolDef{{Name: "tool_a"}}

	if cacheKey(base) == cacheKey(withA) {
		t.Error("有工具与无工具的缓存键应不同")
	}
	if cacheKey(withA) == cacheKey(withB) {
		t.Error("不同工具集的缓存键应不同")
	}
	if cacheKey(withA) != cacheKey(withA2) {
		t.Error("相同工具集的缓存键应一致")
	}
}

// TestGateway_CacheHitAndMeter 验证网关命中语义缓存时不再调用供应商，且计量如实记录。
func TestGateway_CacheHitAndMeter(t *testing.T) {
	gw, fp := newFakeGateway(ChatResponse{
		Content:      "hi",
		Model:        "fake-model",
		FinishReason: "stop",
		Usage:        Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	})
	gw.SetCache(NewSemanticCache(10, time.Minute))
	gw.SetMeter(NewMeter(0))

	req := ChatRequest{Messages: []Message{{Role: "user", Content: "hello"}}}
	if _, err := gw.Chat(context.Background(), req); err != nil {
		t.Fatalf("first chat: %v", err)
	}
	resp2, err := gw.Chat(context.Background(), req)
	if err != nil {
		t.Fatalf("second chat: %v", err)
	}
	if resp2.Content != "hi" {
		t.Errorf("cached content mismatch: %q", resp2.Content)
	}
	if fp.calls != 1 {
		t.Errorf("expected 1 provider call (second cached), got %d", fp.calls)
	}
	if got := gw.Meter().Summary().TotalTokens; got != 15 {
		t.Errorf("meter total tokens: got %d, want 15", got)
	}
	hits, _, _ := gw.Cache().Stats()
	if hits != 1 {
		t.Errorf("cache hits: got %d, want 1", hits)
	}
}

// TestGateway_ToolCallResponseNotCached 验证工具调用响应不进缓存（避免副作用重放）。
func TestGateway_ToolCallResponseNotCached(t *testing.T) {
	gw, fp := newFakeGateway(ChatResponse{
		Model:        "fake-model",
		FinishReason: "tool_calls",
		ToolCalls:    []ToolCall{{Name: "shell_exec"}},
	})
	gw.SetCache(NewSemanticCache(10, time.Minute))

	req := ChatRequest{Messages: []Message{{Role: "user", Content: "run it"}}}
	for i := 0; i < 2; i++ {
		if _, err := gw.Chat(context.Background(), req); err != nil {
			t.Fatalf("chat #%d: %v", i+1, err)
		}
	}
	if fp.calls != 2 {
		t.Errorf("tool-call responses must not be cached, expected 2 calls, got %d", fp.calls)
	}
}

// TestGateway_BudgetExceeded 验证超出日预算后拒绝新调用。
func TestGateway_BudgetExceeded(t *testing.T) {
	gw, _ := newFakeGateway(ChatResponse{Content: "hi"})
	m := NewMeter(100)
	m.Record(TokenRecord{Category: UsageExecution, TotalTokens: 150})
	gw.SetMeter(m)

	_, err := gw.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "hello"}},
	})
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("expected ErrBudgetExceeded, got %v", err)
	}
}

// TestNewGatewayFromConfig_CacheAndMeter 验证配置驱动的缓存/计量初始化。
func TestNewGatewayFromConfig_CacheAndMeter(t *testing.T) {
	gw := NewGatewayFromConfig(GatewayConfig{
		Cache: &CacheYAMLConfig{Enabled: true, Capacity: 10, TTL: "1m"},
		Meter: &MeterYAMLConfig{DailyTokenLimit: 5000},
	})
	if gw.Cache() == nil {
		t.Fatal("cache should be created when enabled")
	}
	if gw.Meter() == nil {
		t.Fatal("meter should always be created")
	}
	if _, limit, _ := gw.Meter().CheckBudget(); limit != 5000 {
		t.Errorf("daily limit: got %d, want 5000", limit)
	}

	gw2 := NewGatewayFromConfig(GatewayConfig{})
	if gw2.Cache() != nil {
		t.Error("cache should be disabled by default")
	}
	if gw2.Meter() == nil {
		t.Error("meter should still be enabled by default (aggregation only)")
	}
}
