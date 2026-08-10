package session

import (
	"context"
	"testing"
	"time"

	"github.com/tommy-cat/agent/internal/llm"
	"github.com/tommy-cat/agent/internal/memory"
	"github.com/tommy-cat/agent/internal/memstore"
)

// TestSessionPrewarmInjectsHistory 验证会话创建时预热最近长期记忆进入
// 工作记忆（角色正确、参与上下文），且预热内容不会被重复持久化。
func TestSessionPrewarmInjectsHistory(t *testing.T) {
	store := memstore.NewFileStore(t.TempDir(), t.TempDir(), 0)
	defer store.Close()
	ctx := context.Background()

	if err := store.SaveMemory(ctx, memory.MemoryEntry{ID: "h1", UserID: "u1", Content: "我喜欢喝咖啡", Tags: []string{"user"}, Timestamp: time.Now().Add(-2 * time.Hour)}); err != nil {
		t.Fatalf("SaveMemory(h1): %v", err)
	}
	if err := store.SaveMemory(ctx, memory.MemoryEntry{ID: "h2", UserID: "u1", Content: "好的已记下", Tags: []string{"assistant"}, Timestamp: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatalf("SaveMemory(h2): %v", err)
	}

	s := NewUserSession("u1", SessionDeps{MemStore: store, PrewarmCount: 10, MemorySize: 20})

	msgs := s.memory.GetContext(10)
	if len(msgs) != 2 {
		t.Fatalf("预热应注入 2 条历史，实际 %d: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != "user" || msgs[1].Role != "assistant" {
		t.Fatalf("预热角色顺序错误: %+v", msgs)
	}

	// 引擎回写包含历史内容时，预热内容不重复落盘（内容去重），仅新消息落盘
	s.memory.Store([]llm.Message{
		{Role: "user", Content: "我喜欢喝咖啡"},
		{Role: "user", Content: "这是本轮新消息"},
	})
	entries, err := store.RecentMemories(ctx, "u1", 100)
	if err != nil {
		t.Fatalf("RecentMemories: %v", err)
	}
	if len(entries) != 3 { // 2 条历史 + 1 条新消息
		t.Fatalf("长期记忆应为 3 条（预热不重复落盘），实际 %d", len(entries))
	}
}
