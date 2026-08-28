package session

import (
	"context"
	"testing"
	"time"

	"github.com/wenruigao/tommy-catty/internal/ctxmgr"
	"github.com/wenruigao/tommy-catty/internal/llm"
	"github.com/wenruigao/tommy-catty/internal/tool"
)

// mockLLM 是一个简单的 mock LLM，直接返回固定回复（无工具调用）。
type mockLLM struct{}

func (m *mockLLM) Chat(_ context.Context, messages []llm.Message, _ []llm.ToolDef) (llm.ChatResponse, error) {
	// 返回最后一条 user 消息的回显
	lastUser := ""
	for _, msg := range messages {
		if msg.Role == "user" {
			lastUser = msg.Content
		}
	}
	return llm.ChatResponse{
		Content: "echo: " + lastUser,
		Usage:   llm.Usage{TotalTokens: 10},
	}, nil
}

func testDeps() SessionDeps {
	registry := tool.NewRegistry()
	return SessionDeps{
		LLM:           &mockLLM{},
		Tools:         registry,
		MaxIterations: 5,
		SystemPrompt:  "test",
		MemorySize:    50,
		CtxConfig:     ctxmgr.DefaultConfig(),
		RateLimit:     RateLimitConfig{RequestsPerMinute: 0}, // 不限流
	}
}

// TestMemoryIsolation 验证两个用户的记忆互不可见。
func TestMemoryIsolation(t *testing.T) {
	sm := NewSessionManager(DefaultManagerConfig(), testDeps())
	defer sm.Shutdown()

	alice := sm.GetOrCreate("alice")
	bob := sm.GetOrCreate("bob")

	ctx := context.Background()

	// Alice 执行一次对话
	_, err := alice.Run(ctx, "hello from alice")
	if err != nil {
		t.Fatalf("alice.Run failed: %v", err)
	}

	// Bob 执行一次对话
	_, err = bob.Run(ctx, "hello from bob")
	if err != nil {
		t.Fatalf("bob.Run failed: %v", err)
	}

	// Alice 的历史应包含 "alice" 相关内容，不包含 "bob"
	aliceHistory := alice.GetHistory(20)
	for _, msg := range aliceHistory {
		if msg.Content == "hello from bob" || msg.Content == "echo: hello from bob" {
			t.Error("MEMORY LEAK: alice can see bob's messages")
		}
	}

	// Bob 的历史应包含 "bob" 相关内容，不包含 "alice"
	bobHistory := bob.GetHistory(20)
	for _, msg := range bobHistory {
		if msg.Content == "hello from alice" || msg.Content == "echo: hello from alice" {
			t.Error("MEMORY LEAK: bob can see alice's messages")
		}
	}

	// 验证各自能看到自己的消息
	aliceFound := false
	for _, msg := range aliceHistory {
		if msg.Content == "hello from alice" {
			aliceFound = true
		}
	}
	if !aliceFound {
		t.Error("alice cannot see her own message")
	}
}

// TestSessionManagerLifecycle 验证 session 的创建、获取和删除。
func TestSessionManagerLifecycle(t *testing.T) {
	sm := NewSessionManager(DefaultManagerConfig(), testDeps())
	defer sm.Shutdown()

	// 初始无会话
	if sm.ActiveCount() != 0 {
		t.Fatalf("expected 0 sessions, got %d", sm.ActiveCount())
	}

	// 创建
	s := sm.GetOrCreate("user1")
	if s == nil {
		t.Fatal("GetOrCreate returned nil")
	}
	if sm.ActiveCount() != 1 {
		t.Fatalf("expected 1 session, got %d", sm.ActiveCount())
	}

	// 重复获取返回同一实例
	s2 := sm.GetOrCreate("user1")
	if s2 != s {
		t.Error("GetOrCreate returned different instance for same user")
	}

	// 删除
	sm.Remove("user1")
	if sm.ActiveCount() != 0 {
		t.Fatalf("expected 0 sessions after remove, got %d", sm.ActiveCount())
	}

	// 删除后 Get 返回 false
	_, ok := sm.Get("user1")
	if ok {
		t.Error("Get should return false after Remove")
	}
}

// TestRateLimiter 验证限流器基本行为。
func TestRateLimiter(t *testing.T) {
	rl := NewRateLimiter(RateLimitConfig{RequestsPerMinute: 3})

	// 前 3 次应允许
	for i := 0; i < 3; i++ {
		if !rl.Allow() {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}

	// 第 4 次应被拒绝
	if rl.Allow() {
		t.Error("4th request should be rate limited")
	}
}

// TestRateLimiterDisabled 验证不限流配置。
func TestRateLimiterDisabled(t *testing.T) {
	rl := NewRateLimiter(RateLimitConfig{RequestsPerMinute: 0})

	for i := 0; i < 100; i++ {
		if !rl.Allow() {
			t.Fatalf("request %d should be allowed when rate limit is disabled", i+1)
		}
	}
}

// TestSessionTTLExpiry 验证过期淘汰。
func TestSessionTTLExpiry(t *testing.T) {
	cfg := ManagerConfig{
		MaxSessions:     10,
		SessionTTL:      50 * time.Millisecond, // 极短 TTL 用于测试
		CleanupInterval: 10 * time.Millisecond,
	}
	sm := NewSessionManager(cfg, testDeps())
	defer sm.Shutdown()

	sm.GetOrCreate("ephemeral")
	if sm.ActiveCount() != 1 {
		t.Fatalf("expected 1 session, got %d", sm.ActiveCount())
	}

	// 等待 TTL 过期 + cleanup 执行
	time.Sleep(100 * time.Millisecond)

	if sm.ActiveCount() != 0 {
		t.Fatalf("expected 0 sessions after TTL expiry, got %d", sm.ActiveCount())
	}
}

// TestClearMemory 验证清空记忆。
func TestClearMemory(t *testing.T) {
	sm := NewSessionManager(DefaultManagerConfig(), testDeps())
	defer sm.Shutdown()

	s := sm.GetOrCreate("user1")
	ctx := context.Background()

	_, _ = s.Run(ctx, "remember this")

	// 清空前应有历史
	before := s.GetHistory(10)
	if len(before) == 0 {
		t.Fatal("expected history before clear")
	}

	// 清空
	s.ClearMemory()

	// 清空后应为空
	after := s.GetHistory(10)
	if len(after) != 0 {
		t.Errorf("expected empty history after clear, got %d messages", len(after))
	}
}
