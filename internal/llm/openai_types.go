package llm

import "fmt"

// ========== OpenAI 兼容格式的 API 数据结构 ==========
// 这些类型定义了 OpenAI Chat Completions API 的请求/响应格式，
// 所有兼容该协议的模型服务（DeepSeek、通义千问、OpenAI、Ollama、vLLM 等）
// 均使用相同的数据结构进行通信。

// openAIRequest 是 OpenAI 兼容格式的请求结构
type openAIRequest struct {
	Model       string             `json:"model"`
	Messages    []openAIMessageReq `json:"messages"`
	Tools       []openAITool       `json:"tools,omitempty"`
	Temperature float64            `json:"temperature,omitempty"`
	MaxTokens   int                `json:"max_tokens,omitempty"`
	Stream      bool               `json:"stream,omitempty"`
}

// openAIMessageReq 是请求中的消息格式（支持 tool_calls 嵌套结构）
type openAIMessageReq struct {
	Role       string              `json:"role"`
	Content    string              `json:"content"`
	ToolCallID string              `json:"tool_call_id,omitempty"`
	ToolCalls  []openAIToolCallReq `json:"tool_calls,omitempty"`
}

// openAIToolCallReq 是请求中的工具调用格式（与响应格式略有不同）
type openAIToolCallReq struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Function openAIFuncCall `json:"function"`
}

// toOpenAIMessages 将内部 Message 列表转换为 OpenAI 请求格式。
// 关键转换：ToolCalls 从扁平结构转为嵌套的 {id, type, function} 结构。
func toOpenAIMessages(msgs []Message) []openAIMessageReq {
	result := make([]openAIMessageReq, 0, len(msgs))
	for _, m := range msgs {
		apiMsg := openAIMessageReq{
			Role:       m.Role,
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
		}
		if len(m.ToolCalls) > 0 {
			apiMsg.ToolCalls = make([]openAIToolCallReq, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				apiMsg.ToolCalls = append(apiMsg.ToolCalls, openAIToolCallReq{
					ID:   tc.ID,
					Type: "function",
					Function: openAIFuncCall{
						Name:      tc.Name,
						Arguments: tc.Arguments,
					},
				})
			}
		}
		result = append(result, apiMsg)
	}
	return result
}

// openAITool 是 OpenAI 格式的工具定义
type openAITool struct {
	Type     string         `json:"type"`
	Function openAIFunction `json:"function"`
}

// openAIFunction 是 OpenAI 格式的函数定义
type openAIFunction struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters,omitempty"`
}

// openAIResponse 是 OpenAI 兼容格式的完整响应
type openAIResponse struct {
	ID      string         `json:"id"`
	Model   string         `json:"model"`
	Choices []openAIChoice `json:"choices"`
	Usage   Usage          `json:"usage"`
}

// openAIChoice 是响应中的选项
type openAIChoice struct {
	Index        int           `json:"index"`
	Message      openAIMessage `json:"message"`
	FinishReason string        `json:"finish_reason"`
}

// openAIMessage 是响应中的消息
type openAIMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	ToolCalls []openAIToolCall `json:"tool_calls,omitempty"`
}

// openAIToolCall 是响应中的工具调用
type openAIToolCall struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Function openAIFuncCall `json:"function"`
	// Index 是流式响应中 tool_calls 元素的序号。
	// OpenAI 流式协议按 index 归并跨 chunk 的增量片段，
	// 部分供应商可能省略该字段，故用指针区分"未提供"。
	Index *int `json:"index"`
}

// openAIFuncCall 是工具调用的函数信息
type openAIFuncCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// openAIStreamChunk 是流式响应的数据块
type openAIStreamChunk struct {
	ID      string               `json:"id"`
	Model   string               `json:"model"`
	Choices []openAIStreamChoice `json:"choices"`
}

// openAIStreamChoice 是流式响应中的选项
type openAIStreamChoice struct {
	Index        int         `json:"index"`
	Delta        openAIDelta `json:"delta"`
	FinishReason string      `json:"finish_reason"`
}

// openAIDelta 是流式响应中的增量内容
type openAIDelta struct {
	Role      string           `json:"role,omitempty"`
	Content   string           `json:"content,omitempty"`
	ToolCalls []openAIToolCall `json:"tool_calls,omitempty"`
}

// parseOpenAIResponse 将 OpenAI 格式响应转换为统一的 ChatResponse
func parseOpenAIResponse(resp openAIResponse) ChatResponse {
	result := ChatResponse{
		Model: resp.Model,
		Usage: resp.Usage,
	}

	if len(resp.Choices) > 0 {
		choice := resp.Choices[0]
		result.Content = choice.Message.Content
		result.FinishReason = choice.FinishReason

		// 解析工具调用
		if len(choice.Message.ToolCalls) > 0 {
			result.ToolCalls = make([]ToolCall, 0, len(choice.Message.ToolCalls))
			for i, tc := range choice.Message.ToolCalls {
				id := tc.ID
				if id == "" {
					// 某些 API（如 MiMo）不返回 tool call ID，生成备用 ID
					id = fmt.Sprintf("call_%d", i)
				}
				result.ToolCalls = append(result.ToolCalls, ToolCall{
					ID:        id,
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				})
			}
		}
	}

	return result
}
