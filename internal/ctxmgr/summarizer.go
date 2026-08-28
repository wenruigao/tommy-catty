package ctxmgr

import (
	"context"
	"fmt"

	"github.com/wenruigao/tommy-catty/internal/llm"
)

// ChatFunc 定义调用 LLM 的函数签名（与 engine.LLMClient.Chat 兼容）。
type ChatFunc func(ctx context.Context, messages []llm.Message, tools []llm.ToolDef) (llm.ChatResponse, error)

// LLMSummarizer 使用 LLM 生成对话摘要的实现。
type LLMSummarizer struct {
	chatFn ChatFunc

	// OnUsage 可选的 token 用量回调。
	// Summarize 成功时以本次 LLM 调用的 prompt/completion token 数调用；
	// 为 nil 时不做计量。
	OnUsage func(promptTokens, completionTokens int)
}

// NewLLMSummarizer 创建基于 LLM 的摘要生成器。
// chatFn 为 LLM 调用函数（通常从 Gateway 或 engine.LLMClient 适配）。
func NewLLMSummarizer(chatFn ChatFunc) *LLMSummarizer {
	return &LLMSummarizer{chatFn: chatFn}
}

// Summarize 调用 LLM 对给定文本生成简洁的中文摘要。
// chatFn 未设置、LLM 调用失败或返回空内容时返回错误。
func (s *LLMSummarizer) Summarize(ctx context.Context, text string, maxTokens int) (string, error) {
	if s.chatFn == nil {
		return "", fmt.Errorf("summarizer: chat function not set")
	}

	prompt := fmt.Sprintf(`请对以下对话历史生成简洁的中文摘要，保留关键信息（任务目标、重要结论、工具执行结果）。
摘要不超过 %d 个 token，直接输出摘要内容，不要添加标题或前缀。

%s`, maxTokens, text)

	msgs := []llm.Message{
		{Role: "system", Content: "你是一个对话摘要助手，擅长提取关键信息并生成简洁摘要。"},
		{Role: "user", Content: prompt},
	}

	resp, err := s.chatFn(ctx, msgs, nil)
	if err != nil {
		return "", fmt.Errorf("summarizer: LLM call failed: %w", err)
	}

	if resp.Content == "" {
		return "", fmt.Errorf("summarizer: empty response from LLM")
	}

	// 摘要成功，上报本次调用的 token 用量（未设置回调则不计）
	if s.OnUsage != nil {
		s.OnUsage(resp.Usage.PromptTokens, resp.Usage.CompletionTokens)
	}

	return resp.Content, nil
}
