// Package llm 提供大语言模型的统一网关接口，支持多供应商路由、重试和流式输出。
package llm

import "context"

// 消息角色常量
const (
	RoleSystem    = "system"    // 系统提示
	RoleUser      = "user"      // 用户输入
	RoleAssistant = "assistant" // 模型回复
	RoleTool      = "tool"      // 工具调用结果
)

// Message 表示对话中的一条消息
type Message struct {
	Role    string `json:"role"`    // 角色: system, user, assistant, tool
	Content string `json:"content"` // 消息内容
}

// ToolCall 表示模型返回的工具调用请求
type ToolCall struct {
	ID        string `json:"id"`        // 工具调用唯一标识
	Name      string `json:"name"`      // 工具名称
	Arguments string `json:"arguments"` // JSON 格式的参数
}

// ToolDef 定义可供模型调用的工具
type ToolDef struct {
	Name        string                 `json:"name"`        // 工具名称
	Description string                 `json:"description"` // 工具描述
	Parameters  map[string]interface{} `json:"parameters"`  // JSON Schema 参数定义
}

// ChatRequest 表示一次聊天补全请求
type ChatRequest struct {
	Model       string    `json:"model"`                 // 模型名称，用于路由到对应供应商
	Messages    []Message `json:"messages"`              // 对话消息列表
	Tools       []ToolDef `json:"tools,omitempty"`       // 可用工具定义
	Temperature float64   `json:"temperature,omitempty"` // 采样温度
	MaxTokens   int       `json:"max_tokens,omitempty"`  // 最大生成 token 数
	Stream      bool      `json:"stream,omitempty"`      // 是否启用流式输出
}

// Usage 表示 token 用量统计
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`     // 输入 token 数
	CompletionTokens int `json:"completion_tokens"` // 输出 token 数
	TotalTokens      int `json:"total_tokens"`      // 总 token 数
}

// ChatResponse 表示聊天补全的完整响应
type ChatResponse struct {
	Content      string     `json:"content"`                 // 模型生成的文本内容
	ToolCalls    []ToolCall `json:"tool_calls,omitempty"`    // 工具调用列表
	Model        string     `json:"model"`                   // 实际使用的模型
	Usage        Usage      `json:"usage"`                   // token 用量
	FinishReason string     `json:"finish_reason"`           // 结束原因: stop, tool_calls, length
}

// StreamChunk 表示流式输出中的一个数据块
type StreamChunk struct {
	Delta         string    `json:"delta"`                    // 增量文本内容
	ToolCallDelta *ToolCall `json:"tool_call_delta,omitempty"` // 增量工具调用信息
	Done          bool      `json:"done"`                     // 是否为最后一个块
}

// LLMProvider 定义大语言模型供应商的统一接口
type LLMProvider interface {
	// Chat 发送聊天请求并返回完整响应
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)

	// ChatStream 发送聊天请求并返回流式数据通道
	ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, error)

	// Name 返回供应商名称标识
	Name() string

	// MaxTokens 返回该供应商支持的最大 token 数
	MaxTokens() int
}
