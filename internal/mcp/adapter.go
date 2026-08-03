package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tommy-cat/agent/internal/tool"
)

// ============================================================
// Tool Adapter — 将 MCP 远程工具适配为本地 Tool 接口
// ============================================================

// MCPToolAdapter 将 MCP Server 的远程工具适配为本地 tool.Tool 接口。
// 对引擎层完全透明：MCP 工具和内置工具使用方式完全一致。
type MCPToolAdapter struct {
	client     *Client
	definition ToolDefinition
	fullName   string // 带前缀的完整工具名
}

// NewMCPToolAdapter 创建工具适配器
func NewMCPToolAdapter(client *Client, def ToolDefinition) *MCPToolAdapter {
	return &MCPToolAdapter{
		client:     client,
		definition: def,
		fullName:   client.ToolFullName(def.Name),
	}
}

// Name 返回带前缀的工具名（避免多 server 冲突）
func (a *MCPToolAdapter) Name() string {
	return a.fullName
}

// Description 返回工具描述（标注来源为 MCP）
func (a *MCPToolAdapter) Description() string {
	desc := a.definition.Description
	if desc == "" {
		desc = fmt.Sprintf("MCP tool from %s", a.client.cfg.Name)
	}
	return desc
}

// Parameters 将 MCP 的 inputSchema 转换为本地 JSONSchema
func (a *MCPToolAdapter) Parameters() tool.JSONSchema {
	return parseInputSchema(a.definition.InputSchema)
}

// Execute 调用远程 MCP 工具
func (a *MCPToolAdapter) Execute(ctx context.Context, args map[string]interface{}) (tool.Result, error) {
	// 去除前缀，使用远程工具原始名称调用
	remoteName := a.client.StripPrefix(a.fullName)

	result, err := a.client.CallTool(ctx, remoteName, args)
	if err != nil {
		return tool.Result{}, err
	}

	// 将 MCP ToolCallResult 转换为本地 tool.Result
	return convertToolResult(result), nil
}

// convertToolResult 将 MCP 工具返回结果转换为本地格式
func convertToolResult(result *ToolCallResult) tool.Result {
	if result.IsError {
		// 工具执行错误
		errText := extractText(result.Content)
		return tool.Result{
			Error: errText,
		}
	}

	// 正常输出
	output := extractText(result.Content)
	return tool.Result{
		Output: output,
		Metadata: map[string]interface{}{
			"source": "mcp",
		},
	}
}

// extractText 从 MCP 内容列表中提取文本
func extractText(contents []ToolContent) string {
	var parts []string
	for _, c := range contents {
		switch c.Type {
		case "text":
			parts = append(parts, c.Text)
		case "image":
			parts = append(parts, fmt.Sprintf("[image: %s, %d bytes]", c.MimeType, len(c.Data)))
		case "resource":
			parts = append(parts, fmt.Sprintf("[resource: %s]", c.URI))
		default:
			if c.Text != "" {
				parts = append(parts, c.Text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

// parseInputSchema 将 MCP 的 inputSchema (JSON Schema) 转换为本地 tool.JSONSchema
func parseInputSchema(raw json.RawMessage) tool.JSONSchema {
	schema := tool.JSONSchema{
		Type:       "object",
		Properties: make(map[string]tool.Property),
	}

	if len(raw) == 0 {
		return schema
	}

	// 解析通用 JSON Schema 结构
	var rawSchema struct {
		Type       string                     `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}

	if err := json.Unmarshal(raw, &rawSchema); err != nil {
		return schema
	}

	if rawSchema.Type != "" {
		schema.Type = rawSchema.Type
	}
	schema.Required = rawSchema.Required

	// 解析每个属性
	for name, propRaw := range rawSchema.Properties {
		var prop struct {
			Type        string      `json:"type"`
			Description string      `json:"description"`
			Enum        []string    `json:"enum"`
			Default     interface{} `json:"default"`
		}
		if err := json.Unmarshal(propRaw, &prop); err != nil {
			// 解析失败时使用 string 类型
			prop.Type = "string"
		}
		if prop.Type == "" {
			prop.Type = "string"
		}
		schema.Properties[name] = tool.Property{
			Type:        prop.Type,
			Description: prop.Description,
			Enum:        prop.Enum,
			Default:     prop.Default,
		}
	}

	return schema
}

// ============================================================
// Manager — 管理多个 MCP Server 连接
// ============================================================

// Manager 管理所有 MCP Server 连接，负责连接、发现和注册工具
type Manager struct {
	clients []*Client
}

// NewManager 创建 MCP 管理器
func NewManager() *Manager {
	return &Manager{}
}

// ConnectAll 连接所有配置的 MCP Server。
// 单个 server 连接失败不阻塞其他 server，返回所有失败的错误列表。
func (m *Manager) ConnectAll(ctx context.Context, configs []ClientConfig) []error {
	var errs []error
	for _, cfg := range configs {
		client := NewClient(cfg)
		if err := client.Connect(ctx); err != nil {
			errs = append(errs, err)
			continue
		}
		m.clients = append(m.clients, client)
	}
	return errs
}

// RegisterTools 将所有已连接 MCP Server 的工具注册到本地 Registry。
// 风险等级从 ClientConfig.DefaultRiskLevel 读取，默认 L1（低写入）。
func (m *Manager) RegisterTools(registry *tool.Registry) int {
	count := 0
	for _, client := range m.clients {
		riskLevel := tool.RiskLowWrite // 默认 L1
		if client.cfg.DefaultRiskLevel > 0 {
			riskLevel = tool.RiskLevel(client.cfg.DefaultRiskLevel)
		}
		for _, toolDef := range client.Tools() {
			adapter := NewMCPToolAdapter(client, toolDef)
			registry.Register(adapter, riskLevel, 60*time.Second)
			count++
		}
	}
	return count
}

// Clients 返回所有已连接的客户端
func (m *Manager) Clients() []*Client {
	return m.clients
}

// CloseAll 关闭所有连接
func (m *Manager) CloseAll() {
	for _, client := range m.clients {
		client.Close()
	}
}
