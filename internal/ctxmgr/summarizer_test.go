package ctxmgr

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/wenruigao/tommy-catty/internal/llm"
)

// mockChatFn 返回一个产出固定内容的 ChatFunc。
func mockChatFn(content string, err error) ChatFunc {
	return func(ctx context.Context, messages []llm.Message, tools []llm.ToolDef) (llm.ChatResponse, error) {
		return llm.ChatResponse{Content: content}, err
	}
}

func TestLLMSummarizer_Summarize_OK(t *testing.T) {
	s := NewLLMSummarizer(mockChatFn("这是对话摘要。", nil))
	got, err := s.Summarize(context.Background(), "user: 你好\nassistant: 你好！", 100)
	if err != nil {
		t.Fatalf("Summarize 返回错误: %v", err)
	}
	if got != "这是对话摘要。" {
		t.Errorf("摘要内容 = %q, want %q", got, "这是对话摘要。")
	}
}

func TestLLMSummarizer_Summarize_PromptContainsText(t *testing.T) {
	var gotMsgs []llm.Message
	chatFn := func(ctx context.Context, messages []llm.Message, tools []llm.ToolDef) (llm.ChatResponse, error) {
		gotMsgs = messages
		return llm.ChatResponse{Content: "摘要"}, nil
	}
	s := NewLLMSummarizer(chatFn)
	if _, err := s.Summarize(context.Background(), "需要被摘要的原文", 100); err != nil {
		t.Fatalf("Summarize 返回错误: %v", err)
	}
	if len(gotMsgs) != 2 {
		t.Fatalf("应发送 2 条消息（system+user），实际 %d 条", len(gotMsgs))
	}
	if !strings.Contains(gotMsgs[1].Content, "需要被摘要的原文") {
		t.Errorf("user 消息应包含待摘要原文，实际: %q", gotMsgs[1].Content)
	}
}

func TestLLMSummarizer_Summarize_ChatFnError(t *testing.T) {
	s := NewLLMSummarizer(mockChatFn("", errors.New("网络错误")))
	_, err := s.Summarize(context.Background(), "text", 100)
	if err == nil {
		t.Fatal("chatFn 返回错误时 Summarize 应返回错误")
	}
	if !strings.Contains(err.Error(), "网络错误") {
		t.Errorf("错误应包装底层原因，实际: %v", err)
	}
}

func TestLLMSummarizer_Summarize_NilChatFn(t *testing.T) {
	s := NewLLMSummarizer(nil)
	_, err := s.Summarize(context.Background(), "text", 100)
	if err == nil {
		t.Fatal("chatFn 为 nil 时 Summarize 应返回错误")
	}
}

func TestLLMSummarizer_Summarize_EmptyResponse(t *testing.T) {
	s := NewLLMSummarizer(mockChatFn("", nil))
	_, err := s.Summarize(context.Background(), "text", 100)
	if err == nil {
		t.Fatal("LLM 返回空内容时 Summarize 应返回错误")
	}
}

// mockChatFnWithUsage 返回一个产出固定内容并携带 token 用量的 ChatFunc。
func mockChatFnWithUsage(content string, promptTokens, completionTokens int) ChatFunc {
	return func(ctx context.Context, messages []llm.Message, tools []llm.ToolDef) (llm.ChatResponse, error) {
		return llm.ChatResponse{
			Content: content,
			Usage:   llm.Usage{PromptTokens: promptTokens, CompletionTokens: completionTokens},
		}, nil
	}
}

func TestLLMSummarizer_OnUsage_CalledWithUsage(t *testing.T) {
	s := NewLLMSummarizer(mockChatFnWithUsage("摘要", 120, 30))
	var gotPrompt, gotCompletion, calls int
	s.OnUsage = func(promptTokens, completionTokens int) {
		gotPrompt, gotCompletion = promptTokens, completionTokens
		calls++
	}
	if _, err := s.Summarize(context.Background(), "text", 100); err != nil {
		t.Fatalf("Summarize 返回错误: %v", err)
	}
	if calls != 1 {
		t.Fatalf("OnUsage 应被调用 1 次，实际 %d 次", calls)
	}
	if gotPrompt != 120 || gotCompletion != 30 {
		t.Errorf("OnUsage 参数 = (%d, %d), want (120, 30)", gotPrompt, gotCompletion)
	}
}

func TestLLMSummarizer_OnUsage_NilCallback(t *testing.T) {
	// OnUsage 为 nil 时不应 panic
	s := NewLLMSummarizer(mockChatFnWithUsage("摘要", 10, 5))
	if _, err := s.Summarize(context.Background(), "text", 100); err != nil {
		t.Fatalf("Summarize 返回错误: %v", err)
	}
}

func TestLLMSummarizer_OnUsage_NotCalledOnError(t *testing.T) {
	s := NewLLMSummarizer(mockChatFn("", errors.New("网络错误")))
	calls := 0
	s.OnUsage = func(promptTokens, completionTokens int) { calls++ }
	if _, err := s.Summarize(context.Background(), "text", 100); err == nil {
		t.Fatal("chatFn 返回错误时 Summarize 应返回错误")
	}
	if calls != 0 {
		t.Errorf("Summarize 失败时 OnUsage 不应被调用，实际 %d 次", calls)
	}
}
