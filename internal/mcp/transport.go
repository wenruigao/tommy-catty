package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ============================================================
// Transport 传输层抽象
// ============================================================

// Transport 定义 MCP 通信传输层接口
type Transport interface {
	// Send 发送 JSON-RPC 请求并等待响应
	Send(ctx context.Context, req *JSONRPCRequest) (*JSONRPCResponse, error)
	// Notify 发送通知（无需响应）
	Notify(ctx context.Context, method string, params interface{}) error
	// Close 关闭传输连接
	Close() error
}

// ============================================================
// Stdio Transport — 通过子进程 stdin/stdout 通信
// ============================================================

// StdioTransport 通过启动子进程并使用 stdin/stdout 进行 JSON-RPC 通信
type StdioTransport struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	mu     sync.Mutex
	nextID int
}

// StdioConfig stdio 传输配置
type StdioConfig struct {
	// Command 要执行的命令（如 "npx", "python", "node"）
	Command string
	// Args 命令参数（如 ["-y", "@modelcontextprotocol/server-filesystem", "/path"]）
	Args []string
	// Env 额外环境变量
	Env []string
	// WorkDir 工作目录
	WorkDir string
}

// NewStdioTransport 创建 stdio 传输并启动子进程
func NewStdioTransport(cfg StdioConfig) (*StdioTransport, error) {
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Env = append(os.Environ(), cfg.Env...)
	if cfg.WorkDir != "" {
		cmd.Dir = cfg.WorkDir
	}
	// 丢弃子进程的 stderr（避免干扰）
	cmd.Stderr = os.Stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp stdio: stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp stdio: stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp stdio: start process %q: %w", cfg.Command, err)
	}

	return &StdioTransport{
		cmd:    cmd,
		stdin:  stdin,
		stdout: bufio.NewReader(stdout),
		nextID: 1,
	}, nil
}

// Send 发送请求并读取响应
func (t *StdioTransport) Send(ctx context.Context, req *JSONRPCRequest) (*JSONRPCResponse, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	req.JSONRPC = "2.0"
	if req.ID == nil {
		req.ID = t.nextID
		t.nextID++
	}

	// 序列化并写入 stdin
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("mcp stdio: marshal request: %w", err)
	}
	data = append(data, '\n')

	if _, err := t.stdin.Write(data); err != nil {
		return nil, fmt.Errorf("mcp stdio: write: %w", err)
	}

	// 读取响应（带超时）
	type readResult struct {
		resp *JSONRPCResponse
		err  error
	}
	ch := make(chan readResult, 1)

	go func() {
		line, err := t.stdout.ReadBytes('\n')
		if err != nil {
			ch <- readResult{nil, fmt.Errorf("mcp stdio: read: %w", err)}
			return
		}
		var resp JSONRPCResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			ch <- readResult{nil, fmt.Errorf("mcp stdio: unmarshal response: %w", err)}
			return
		}
		ch <- readResult{&resp, nil}
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-ch:
		return result.resp, result.err
	}
}

// Notify 发送通知
func (t *StdioTransport) Notify(_ context.Context, method string, params interface{}) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	req := &JSONRPCRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = t.stdin.Write(data)
	return err
}

// Close 关闭子进程
func (t *StdioTransport) Close() error {
	t.stdin.Close()
	// 给子进程一点时间优雅退出
	done := make(chan error, 1)
	go func() { done <- t.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.cmd.Process.Kill()
	}
	return nil
}

// ============================================================
// SSE Transport — 通过 HTTP SSE 通信
// ============================================================

// SSETransport 通过 HTTP Server-Sent Events 进行 MCP 通信
type SSETransport struct {
	baseURL    string
	httpClient *http.Client
	headers    map[string]string
	nextID     int
	mu         sync.Mutex
}

// SSEConfig SSE 传输配置
type SSEConfig struct {
	// URL MCP Server 的 SSE 端点（如 "http://localhost:3000/sse"）
	URL string
	// Headers 额外请求头（如认证 token）
	Headers map[string]string
	// Timeout 请求超时
	Timeout time.Duration
}

// NewSSETransport 创建 SSE 传输
func NewSSETransport(cfg SSEConfig) *SSETransport {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	return &SSETransport{
		baseURL: strings.TrimSuffix(cfg.URL, "/"),
		httpClient: &http.Client{
			Timeout: timeout,
		},
		headers: cfg.Headers,
		nextID:  1,
	}
}

// Send 通过 HTTP POST 发送请求
func (t *SSETransport) Send(ctx context.Context, req *JSONRPCRequest) (*JSONRPCResponse, error) {
	t.mu.Lock()
	req.JSONRPC = "2.0"
	if req.ID == nil {
		req.ID = t.nextID
		t.nextID++
	}
	t.mu.Unlock()

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("mcp sse: marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+"/message", strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("mcp sse: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	for k, v := range t.headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := t.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("mcp sse: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mcp sse: unexpected status %d", resp.StatusCode)
	}

	var rpcResp JSONRPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return nil, fmt.Errorf("mcp sse: decode response: %w", err)
	}

	return &rpcResp, nil
}

// Notify 发送通知
func (t *SSETransport) Notify(ctx context.Context, method string, params interface{}) error {
	req := &JSONRPCRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
	_, err := t.Send(ctx, req)
	return err
}

// Close 关闭传输
func (t *SSETransport) Close() error {
	return nil
}
