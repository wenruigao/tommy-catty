package memory

import (
	"context"
	"testing"

	"github.com/wenruigao/tommy-catty/internal/llm"
)

// TestCombinedStoreSkipsExistingContent 验证已存在于工作记忆中的内容
// （含会话预热回放与历史消息）不会被 CombinedMemory.Store 重复落盘。
func TestCombinedStoreSkipsExistingContent(t *testing.T) {
	ctx := context.Background()
	working := NewWorkingMemory(10)
	cm := NewCombinedMemory(working, nil)

	// 模拟会话创建时注入的预热条目
	if err := working.Store(ctx, MemoryEntry{ID: "p1", Content: "我喜欢喝咖啡", Tags: []string{"user", PrewarmTag}}); err != nil {
		t.Fatalf("working.Store: %v", err)
	}

	cm.Store([]llm.Message{
		{Role: "user", Content: "我喜欢喝咖啡"}, // 已在工作记忆中，应跳过
		{Role: "user", Content: "这是新消息"},
	})

	entries, err := working.GetRecent(ctx, 10)
	if err != nil {
		t.Fatalf("GetRecent: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("工作记忆应为 2 条（原 1 条 + 新 1 条），实际 %d", len(entries))
	}
	for _, e := range entries {
		if e.ID != "p1" && e.Content != "这是新消息" {
			t.Fatalf("出现非预期条目: %+v", e)
		}
	}
}
