package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"
)

// ============================================================
// MCP Client — 连接 MCP Server，发现并调用远程工具
// ============================================================

// ClientConfig MCP 客户端配置
type ClientConfig struct {
	// Name 服务器标识名称（用于日志和工具前缀）
	Name string

	// Transport 传输方式: "stdio" 或 "sse"
	Transport string

	// Stdio 传输配置（Transport="stdio" 时使用）
	Command string   // 启动命令
	Args    []string // 命令参数
	Env     []string // 额外环境变量
	WorkDir string   // 工作目录

	// SSE 传输配置（Transport="sse" 时使用）
	URL     string            // SSE 端点 URL
	Headers map[string]string // 额外请求头

	// ToolPrefix 工具名前缀（避免多 server 工具名冲突）
	// 如设置为 "fs_"，则远程工具 "read_file" 注册为 "fs_read_file"
	ToolPrefix string

	// DefaultRiskLevel 工具风险等级（默认 L1 低写入）
	// 可通过配置设置为更高风险等级以配合安全策略
	DefaultRiskLevel int

	// Timeout 请求超时
	Timeout time.Duration
}

// Client MCP 客户端实例
type Client struct {
	cfg        ClientConfig
	transport  Transport
	tools      []ToolDefinition
	serverInfo Implementation
	mu         sync.RWMutex
	connected  bool
}

// NewClient 创建 MCP 客户端（不立即连接）
func NewClient(cfg ClientConfig) *Client {
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	return &Client{cfg: cfg}
}

// Connect 建立连接并完成 MCP 握手
func (c *Client) Connect(ctx context.Context) error {
	// 创建传输层
	transport, err := c.createTransport()
	if err != nil {
		return fmt.Errorf("mcp [%s]: create transport: %w", c.cfg.Name, err)
	}
	c.transport = transport

	// 执行 initialize 握手
	initParams := &InitializeParams{
		ProtocolVersion: ProtocolVersion,
		Capabilities: ClientCaps{
			Roots: &RootsCap{ListChanged: true},
		},
		ClientInfo: Implementation{
			Name:    ClientName,
			Version: ClientVersion,
		},
	}

	resp, err := c.transport.Send(ctx, &JSONRPCRequest{
		Method: "initialize",
		Params: initParams,
	})
	if err != nil {
		c.transport.Close()
		return fmt.Errorf("mcp [%s]: initialize: %w", c.cfg.Name, err)
	}
	if resp.Error != nil {
		c.transport.Close()
		return fmt.Errorf("mcp [%s]: initialize error: %s", c.cfg.Name, resp.Error.Message)
	}

	// 解析初始化结果
	var initResult InitializeResult
	if err := json.Unmarshal(resp.Result, &initResult); err != nil {
		c.transport.Close()
		return fmt.Errorf("mcp [%s]: parse initialize result: %w", c.cfg.Name, err)
	}
	c.serverInfo = initResult.ServerInfo

	// 发送 initialized 通知
	c.transport.Notify(ctx, "notifications/initialized", nil)

	// 发现工具
	if err := c.discoverTools(ctx); err != nil {
		c.transport.Close()
		return fmt.Errorf("mcp [%s]: discover tools: %w", c.cfg.Name, err)
	}

	c.mu.Lock()
	c.connected = true
	c.mu.Unlock()

	log.Printf("[MCP] Connected to %q (%s %s), discovered %d tools",
		c.cfg.Name, c.serverInfo.Name, c.serverInfo.Version, len(c.tools))

	return nil
}

// discoverTools 调用 tools/list 发现远程工具
func (c *Client) discoverTools(ctx context.Context) error {
	resp, err := c.transport.Send(ctx, &JSONRPCRequest{
		Method: "tools/list",
	})
	if err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("tools/list error: %s", resp.Error.Message)
	}

	var result ToolsListResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return fmt.Errorf("parse tools/list: %w", err)
	}

	c.mu.Lock()
	c.tools = result.Tools
	c.mu.Unlock()

	return nil
}

// Tools 返回发现的所有远程工具定义
func (c *Client) Tools() []ToolDefinition {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make([]ToolDefinition, len(c.tools))
	copy(result, c.tools)
	return result
}

// CallTool 调用远程工具
func (c *Client) CallTool(ctx context.Context, name string, args map[string]interface{}) (*ToolCallResult, error) {
	c.mu.RLock()
	connected := c.connected
	c.mu.RUnlock()

	if !connected {
		return nil, fmt.Errorf("mcp [%s]: not connected", c.cfg.Name)
	}

	params := &ToolCallParams{
		Name:      name,
		Arguments: args,
	}

	resp, err := c.transport.Send(ctx, &JSONRPCRequest{
		Method: "tools/call",
		Params: params,
	})
	if err != nil {
		return nil, fmt.Errorf("mcp [%s]: call tool %q: %w", c.cfg.Name, name, err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("mcp [%s]: tool %q error: %s", c.cfg.Name, name, resp.Error.Message)
	}

	var result ToolCallResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("mcp [%s]: parse tool result: %w", c.cfg.Name, err)
	}

	return &result, nil
}

// ServerInfo 返回服务器信息
func (c *Client) ServerInfo() Implementation {
	return c.serverInfo
}

// IsConnected 返回连接状态
func (c *Client) IsConnected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.connected
}

// Close 关闭连接
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.transport != nil {
		c.connected = false
		return c.transport.Close()
	}
	return nil
}

// createTransport 根据配置创建传输层
func (c *Client) createTransport() (Transport, error) {
	switch c.cfg.Transport {
	case "stdio", "":
		if c.cfg.Command == "" {
			return nil, fmt.Errorf("stdio transport requires 'command' field")
		}
		return NewStdioTransport(StdioConfig{
			Command: c.cfg.Command,
			Args:    c.cfg.Args,
			Env:     c.cfg.Env,
			WorkDir: c.cfg.WorkDir,
		})
	case "sse", "http":
		if c.cfg.URL == "" {
			return nil, fmt.Errorf("sse transport requires 'url' field")
		}
		return NewSSETransport(SSEConfig{
			URL:     c.cfg.URL,
			Headers: c.cfg.Headers,
			Timeout: c.cfg.Timeout,
		}), nil
	default:
		return nil, fmt.Errorf("unsupported transport: %q", c.cfg.Transport)
	}
}

// ToolFullName 返回带前缀的完整工具名
func (c *Client) ToolFullName(remoteName string) string {
	if c.cfg.ToolPrefix != "" {
		return c.cfg.ToolPrefix + remoteName
	}
	return c.cfg.Name + "_" + remoteName
}

// StripPrefix 从完整工具名中去除前缀，返回远程工具名
func (c *Client) StripPrefix(fullName string) string {
	prefix := c.cfg.ToolPrefix
	if prefix == "" {
		prefix = c.cfg.Name + "_"
	}
	if len(fullName) > len(prefix) && fullName[:len(prefix)] == prefix {
		return fullName[len(prefix):]
	}
	return fullName
}
