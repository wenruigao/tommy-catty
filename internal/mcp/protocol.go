// Package mcp 实现 Model Context Protocol (MCP) 客户端，
// 支持通过 stdio 和 SSE 两种传输方式连接 MCP Server，
// 并将远程工具透明适配为本地 Tool 接口。
package mcp

import "encoding/json"

// ============================================================
// JSON-RPC 2.0 基础类型
// ============================================================

// JSONRPCRequest JSON-RPC 2.0 请求
type JSONRPCRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id,omitempty"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

// JSONRPCResponse JSON-RPC 2.0 响应
type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

// JSONRPCError JSON-RPC 2.0 错误
type JSONRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *JSONRPCError) Error() string {
	return e.Message
}

// ============================================================
// MCP 协议类型
// ============================================================

// InitializeParams 初始化请求参数
type InitializeParams struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    ClientCaps     `json:"capabilities"`
	ClientInfo      Implementation `json:"clientInfo"`
}

// ClientCaps 客户端能力声明
type ClientCaps struct {
	Roots    *RootsCap    `json:"roots,omitempty"`
	Sampling *SamplingCap `json:"sampling,omitempty"`
}

// RootsCap Roots 能力
type RootsCap struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// SamplingCap Sampling 能力
type SamplingCap struct{}

// Implementation 实现信息
type Implementation struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// InitializeResult 初始化响应
type InitializeResult struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    ServerCaps     `json:"capabilities"`
	ServerInfo      Implementation `json:"serverInfo"`
}

// ServerCaps 服务端能力
type ServerCaps struct {
	Tools     *ToolsCap     `json:"tools,omitempty"`
	Resources *ResourcesCap `json:"resources,omitempty"`
	Prompts   *PromptsCap   `json:"prompts,omitempty"`
}

// ToolsCap 工具能力
type ToolsCap struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// ResourcesCap 资源能力
type ResourcesCap struct {
	Subscribe   bool `json:"subscribe,omitempty"`
	ListChanged bool `json:"listChanged,omitempty"`
}

// PromptsCap 提示词能力
type PromptsCap struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// ============================================================
// MCP 工具定义
// ============================================================

// ToolDefinition MCP Server 暴露的工具定义
type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// ToolsListResult tools/list 响应
type ToolsListResult struct {
	Tools      []ToolDefinition `json:"tools"`
	NextCursor string           `json:"nextCursor,omitempty"`
}

// ToolCallParams tools/call 请求参数
type ToolCallParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments,omitempty"`
}

// ToolCallResult tools/call 响应
type ToolCallResult struct {
	Content []ToolContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

// ToolContent 工具返回内容
type ToolContent struct {
	Type     string `json:"type"`               // "text", "image", "resource"
	Text     string `json:"text,omitempty"`     // type=text 时的文本内容
	MimeType string `json:"mimeType,omitempty"` // type=image 时的 MIME 类型
	Data     string `json:"data,omitempty"`     // type=image 时的 base64 数据
	URI      string `json:"uri,omitempty"`      // type=resource 时的 URI
}

// ============================================================
// MCP 资源定义（可选支持）
// ============================================================

// ResourceDefinition MCP Server 暴露的资源
type ResourceDefinition struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

// ResourcesListResult resources/list 响应
type ResourcesListResult struct {
	Resources  []ResourceDefinition `json:"resources"`
	NextCursor string               `json:"nextCursor,omitempty"`
}

// ============================================================
// 协议版本
// ============================================================

const (
	// ProtocolVersion 支持的 MCP 协议版本
	ProtocolVersion = "2024-11-05"

	// ClientName 客户端标识
	ClientName = "tommy-cat-agent"

	// ClientVersion 客户端版本
	ClientVersion = "0.1.0"
)
