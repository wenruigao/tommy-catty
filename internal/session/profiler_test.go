package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tommy-cat/agent/internal/llm"
)

func profilerHistory() []llm.Message {
	return []llm.Message{
		{Role: "user", Content: "帮我查一下天气，记住我以后都要简洁回答"},
		{Role: "assistant", Content: "好的，今天晴天。"},
		{Role: "user", Content: "再帮我搜索 Go 泛型的资料"},
		{Role: "assistant", Content: "已找到相关资料。"},
	}
}

// TestUserProfiler_Generate 验证达到更新间隔后生成 user.md。
func TestUserProfiler_Generate(t *testing.T) {
	dir := t.TempDir()
	var calls int
	chatFn := func(_ context.Context, _ []llm.Message) (string, error) {
		calls++
		return "# 用户画像\n- 偏好简洁回答", nil
	}
	p := NewUserProfiler(dir, 2, chatFn)

	// 第 1 次 Run：未到间隔，不生成
	p.OnRunComplete(context.Background(), "u1", profilerHistory())
	if calls != 0 {
		t.Fatalf("未到间隔不应调用 LLM，实际调用 %d 次", calls)
	}

	// 第 2 次 Run：达到间隔，生成画像
	p.OnRunComplete(context.Background(), "u1", profilerHistory())
	if calls != 1 {
		t.Fatalf("达到间隔应调用 LLM 一次，实际 %d 次", calls)
	}

	data, err := os.ReadFile(filepath.Join(dir, "u1", "user.md"))
	if err != nil {
		t.Fatalf("应生成 user.md: %v", err)
	}
	if !strings.Contains(string(data), "偏好简洁回答") {
		t.Errorf("user.md 内容不正确: %q", string(data))
	}
}

// TestUserProfiler_MergeOldProfile 验证更新画像时旧内容会并入 prompt，
// 且新内容覆盖写入文件。
func TestUserProfiler_MergeOldProfile(t *testing.T) {
	dir := t.TempDir()
	profileDir := filepath.Join(dir, "u1")
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profileDir, "user.md"), []byte("旧偏好：喜欢表格输出"), 0o644); err != nil {
		t.Fatal(err)
	}

	var gotPrompt string
	chatFn := func(_ context.Context, messages []llm.Message) (string, error) {
		for _, m := range messages {
			gotPrompt += m.Content + "\n"
		}
		return "新画像：喜欢表格输出 + 简洁回答", nil
	}
	p := NewUserProfiler(dir, 1, chatFn)

	p.OnRunComplete(context.Background(), "u1", profilerHistory())

	if !strings.Contains(gotPrompt, "旧偏好：喜欢表格输出") {
		t.Errorf("prompt 应包含旧画像以便合并，得到:\n%s", gotPrompt)
	}
	if !strings.Contains(gotPrompt, "保留仍然有效") {
		t.Errorf("prompt 应要求保留旧偏好，得到:\n%s", gotPrompt)
	}

	data, err := os.ReadFile(filepath.Join(profileDir, "user.md"))
	if err != nil {
		t.Fatalf("user.md 应被更新: %v", err)
	}
	if !strings.Contains(string(data), "新画像") {
		t.Errorf("user.md 应写入新画像，得到: %q", string(data))
	}
}

// TestUserProfiler_FailureSilent 验证 LLM 失败时静默处理且不破坏旧画像。
func TestUserProfiler_FailureSilent(t *testing.T) {
	dir := t.TempDir()
	chatFn := func(_ context.Context, _ []llm.Message) (string, error) {
		return "", context.DeadlineExceeded
	}
	p := NewUserProfiler(dir, 1, chatFn)

	// 不应 panic，也不应生成文件
	p.OnRunComplete(context.Background(), "u1", profilerHistory())
	if _, err := os.Stat(filepath.Join(dir, "u1", "user.md")); !os.IsNotExist(err) {
		t.Error("LLM 失败时不应生成 user.md")
	}
}

// TestUserProfiler_Disabled 验证 chatFn 为 nil 时画像生成被禁用。
func TestUserProfiler_Disabled(t *testing.T) {
	p := NewUserProfiler(t.TempDir(), 1, nil)
	p.OnRunComplete(context.Background(), "u1", profilerHistory()) // 不应 panic
}

// TestUserProfiler_PerUserInterval 验证不同用户的计数相互独立。
func TestUserProfiler_PerUserInterval(t *testing.T) {
	dir := t.TempDir()
	var calls int
	chatFn := func(_ context.Context, _ []llm.Message) (string, error) {
		calls++
		return "画像", nil
	}
	p := NewUserProfiler(dir, 2, chatFn)

	p.OnRunComplete(context.Background(), "u1", profilerHistory())
	p.OnRunComplete(context.Background(), "u2", profilerHistory())
	if calls != 0 {
		t.Fatalf("两个用户各 1 次 Run 均未到间隔，实际调用 %d 次", calls)
	}
	p.OnRunComplete(context.Background(), "u2", profilerHistory())
	if calls != 1 {
		t.Fatalf("u2 达到间隔应触发一次生成，实际 %d 次", calls)
	}
}
