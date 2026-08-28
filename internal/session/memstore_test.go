package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/wenruigao/tommy-catty/internal/llm"
	"github.com/wenruigao/tommy-catty/internal/memory"
	"github.com/wenruigao/tommy-catty/internal/memstore"
)

// TestLongTermPersistenceAcrossSessions 验证长期记忆在"会话重启"
// （新建共享同一 Store 的 CombinedMemory）后仍可检索，且多用户隔离。
func TestLongTermPersistenceAcrossSessions(t *testing.T) {
	store := memstore.NewFileStore(t.TempDir(), t.TempDir(), 0)
	defer store.Close()

	first := memory.NewCombinedMemory(memory.NewWorkingMemory(10), memstore.NewMemoryAdapter(store, "u1"))
	first.Store([]llm.Message{
		{Role: "user", Content: "记住我喜欢喝咖啡"},
		{Role: "assistant", Content: "好的，已记下咖啡偏好"},
	})

	// 模拟重启后的新会话（工作记忆为空，长期记忆共享同一后端）
	second := memory.NewCombinedMemory(memory.NewWorkingMemory(10), memstore.NewMemoryAdapter(store, "u1"))
	results := second.Search("咖啡", 5)
	if len(results) == 0 {
		t.Fatal("新会话应能从长期记忆检索到咖啡相关内容")
	}

	// 其他用户不可见
	other := memory.NewCombinedMemory(memory.NewWorkingMemory(10), memstore.NewMemoryAdapter(store, "u2"))
	if got := other.Search("咖啡", 5); len(got) != 0 {
		t.Fatalf("u2 不应看到 u1 的记忆，实际: %v", got)
	}
}

// TestProfilerViaStore 验证 profiler 注入存储后端后画像经后端落盘（无需本地目录）。
func TestProfilerViaStore(t *testing.T) {
	store := memstore.NewFileStore(t.TempDir(), t.TempDir(), 0)
	defer store.Close()

	profiler := NewUserProfiler("", 1, func(ctx context.Context, messages []llm.Message) (string, error) {
		return "# 画像\n用户喜欢简洁回答", nil
	})
	profiler.SetStore(store)

	history := []llm.Message{
		{Role: "user", Content: "帮我查一下天气"},
		{Role: "assistant", Content: "今天晴"},
	}
	profiler.OnRunComplete(context.Background(), "u1", history)

	profile, err := store.LoadProfile(context.Background(), "u1")
	if err != nil || profile == "" {
		t.Fatalf("画像应经存储后端落盘: %q err=%v", profile, err)
	}
}

// TestLoadUserProfileViaFallback 验证后端读取失败/为空时回退本地文件。
func TestLoadUserProfileViaFallback(t *testing.T) {
	dir := t.TempDir()
	userID := "u-fallback"
	path := userProfilePath(dir, userID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("准备目录失败: %v", err)
	}
	if err := os.WriteFile(path, []byte("本地画像内容"), 0o644); err != nil {
		t.Fatalf("准备本地画像失败: %v", err)
	}

	// store 无该用户画像 → 回退本地文件
	store := memstore.NewFileStore(t.TempDir(), t.TempDir(), 0)
	defer store.Close()
	if got := loadUserProfileVia(store, dir, userID); got != "本地画像内容" {
		t.Fatalf("应回退本地文件，实际: %q", got)
	}
	// store 为 nil → 直接本地文件
	if got := loadUserProfileVia(nil, dir, userID); got != "本地画像内容" {
		t.Fatalf("nil store 应读本地文件，实际: %q", got)
	}
}
