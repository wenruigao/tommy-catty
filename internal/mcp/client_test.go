package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wenruigao/tommy-catty/internal/tool"
)

// ============================================================
// mockTransport — 测试用模拟传输层
// ============================================================

type mockTransport struct {
	responses map[string]*JSONRPCResponse
	errors    map[string]error
	closed    bool
	notified  []string
}

func newMockTransport() *mockTransport {
	return &mockTransport{
		responses: make(map[string]*JSONRPCResponse),
		errors:    make(map[string]error),
	}
}

func (m *mockTransport) Send(_ context.Context, req *JSONRPCRequest) (*JSONRPCResponse, error) {
	if err, ok := m.errors[req.Method]; ok {
		return nil, err
	}
	if resp, ok := m.responses[req.Method]; ok {
		return resp, nil
	}
	return &JSONRPCResponse{JSONRPC: "2.0", ID: req.ID}, nil
}

func (m *mockTransport) Notify(_ context.Context, method string, _ interface{}) error {
	m.notified = append(m.notified, method)
	return nil
}

func (m *mockTransport) Close() error {
	m.closed = true
	return nil
}

// newTestClient 创建带模拟传输层的已连接测试客户端
func newTestClient(cfg ClientConfig, mt *mockTransport) *Client {
	c := NewClient(cfg)
	c.transport = mt
	c.connected = true
	return c
}

// ============================================================
// Client.CallTool 测试
// ============================================================

func TestClient_CallTool_Success(t *testing.T) {
	resultJSON, _ := json.Marshal(&ToolCallResult{
		Content: []ToolContent{{Type: "text", Text: "hello"}},
	})
	mt := newMockTransport()
	mt.responses["tools/call"] = &JSONRPCResponse{JSONRPC: "2.0", Result: resultJSON}
	c := newTestClient(ClientConfig{Name: "test"}, mt)

	result, err := c.CallTool(context.Background(), "echo", map[string]interface{}{"msg": "hi"})
	if err != nil {
		t.Fatalf("CallTool error: %v", err)
	}
	if len(result.Content) != 1 || result.Content[0].Text != "hello" {
		t.Errorf("unexpected result: %+v", result)
	}
}

func TestClient_CallTool_NotConnected(t *testing.T) {
	c := NewClient(ClientConfig{Name: "test"})
	_, err := c.CallTool(context.Background(), "echo", nil)
	if err == nil {
		t.Error("expected error when not connected")
	}
}

func TestClient_CallTool_RPCError(t *testing.T) {
	mt := newMockTransport()
	mt.responses["tools/call"] = &JSONRPCResponse{
		JSONRPC: "2.0",
		Error:   &JSONRPCError{Code: -1, Message: "tool not found"},
	}
	c := newTestClient(ClientConfig{Name: "test"}, mt)

	_, err := c.CallTool(context.Background(), "missing", nil)
	if err == nil {
		t.Error("expected error on RPC error response")
	}
}

func TestClient_CallTool_SendError(t *testing.T) {
	mt := newMockTransport()
	mt.errors["tools/call"] = fmt.Errorf("connection lost")
	c := newTestClient(ClientConfig{Name: "test"}, mt)

	_, err := c.CallTool(context.Background(), "echo", nil)
	if err == nil {
		t.Error("expected error on send failure")
	}
}

func TestClient_CallTool_InvalidJSON(t *testing.T) {
	mt := newMockTransport()
	mt.responses["tools/call"] = &JSONRPCResponse{
		JSONRPC: "2.0",
		Result:  json.RawMessage(`{invalid}`),
	}
	c := newTestClient(ClientConfig{Name: "test"}, mt)

	_, err := c.CallTool(context.Background(), "echo", nil)
	if err == nil {
		t.Error("expected error on invalid JSON result")
	}
}

func TestClient_CallTool_IsError(t *testing.T) {
	resultJSON, _ := json.Marshal(&ToolCallResult{
		Content: []ToolContent{{Type: "text", Text: "fail"}},
		IsError: true,
	})
	mt := newMockTransport()
	mt.responses["tools/call"] = &JSONRPCResponse{JSONRPC: "2.0", Result: resultJSON}
	c := newTestClient(ClientConfig{Name: "test"}, mt)

	result, err := c.CallTool(context.Background(), "bad", nil)
	if err != nil {
		t.Fatalf("CallTool should not return Go error for tool-level error: %v", err)
	}
	if !result.IsError {
		t.Error("result.IsError should be true")
	}
}

// ============================================================
// Client 状态方法测试
// ============================================================

func TestClient_Tools_ReturnsCopy(t *testing.T) {
	c := NewClient(ClientConfig{Name: "test"})
	c.tools = []ToolDefinition{{Name: "tool1"}, {Name: "tool2"}}

	tools := c.Tools()
	if len(tools) != 2 {
		t.Fatalf("Tools() len = %d, want 2", len(tools))
	}
	tools[0].Name = "modified"
	if c.tools[0].Name == "modified" {
		t.Error("Tools() should return a copy, not a reference")
	}
}

func TestClient_IsConnected(t *testing.T) {
	c := NewClient(ClientConfig{Name: "test"})
	if c.IsConnected() {
		t.Error("new client should not be connected")
	}
	c.connected = true
	if !c.IsConnected() {
		t.Error("client should be connected")
	}
}

func TestClient_Close(t *testing.T) {
	mt := newMockTransport()
	c := newTestClient(ClientConfig{Name: "test"}, mt)

	c.Close()
	if !mt.closed {
		t.Error("transport should be closed")
	}
	if c.IsConnected() {
		t.Error("client should not be connected after close")
	}
}

func TestClient_Close_NoTransport(t *testing.T) {
	c := NewClient(ClientConfig{Name: "test"})
	if err := c.Close(); err != nil {
		t.Errorf("Close without transport should not error: %v", err)
	}
}

func TestClient_ServerInfo(t *testing.T) {
	c := NewClient(ClientConfig{Name: "test"})
	c.serverInfo = Implementation{Name: "srv", Version: "2.0"}
	info := c.ServerInfo()
	if info.Name != "srv" || info.Version != "2.0" {
		t.Errorf("ServerInfo() = %+v", info)
	}
}

func TestNewClient_DefaultTimeout(t *testing.T) {
	c := NewClient(ClientConfig{Name: "test"})
	if c.cfg.Timeout != 30*time.Second {
		t.Errorf("default timeout = %v, want 30s", c.cfg.Timeout)
	}
}

// ============================================================
// Client.createTransport 错误路径测试
// ============================================================

func TestClient_createTransport_Errors(t *testing.T) {
	tests := []struct {
		name string
		cfg  ClientConfig
	}{
		{"stdio without command", ClientConfig{Transport: "stdio"}},
		{"sse without url", ClientConfig{Transport: "sse"}},
		{"http without url", ClientConfig{Transport: "http"}},
		{"unsupported transport", ClientConfig{Transport: "grpc"}},
		{"default (stdio) without command", ClientConfig{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewClient(tt.cfg)
			_, err := c.createTransport()
			if err == nil {
				t.Error("expected error")
			}
		})
	}
}

// ============================================================
// Manager 测试
// ============================================================

func TestManager_NewManager(t *testing.T) {
	m := NewManager()
	if m == nil {
		t.Fatal("NewManager returned nil")
	}
	if len(m.Clients()) != 0 {
		t.Error("new manager should have no clients")
	}
}

func TestManager_ConnectAll_Empty(t *testing.T) {
	m := NewManager()
	errs := m.ConnectAll(context.Background(), nil)
	if len(errs) != 0 {
		t.Errorf("expected no errors, got %v", errs)
	}
}

func TestManager_ConnectAll_AllFail(t *testing.T) {
	m := NewManager()
	errs := m.ConnectAll(context.Background(), []ClientConfig{
		{Name: "bad1", Transport: "stdio"},
		{Name: "bad2", Transport: "grpc"},
	})
	if len(errs) != 2 {
		t.Errorf("expected 2 errors, got %d", len(errs))
	}
	if len(m.Clients()) != 0 {
		t.Error("failed clients should not be added")
	}
}

func TestManager_RegisterTools(t *testing.T) {
	m := NewManager()
	c := NewClient(ClientConfig{Name: "srv", DefaultRiskLevel: 2})
	c.tools = []ToolDefinition{
		{Name: "tool1", Description: "desc1"},
		{Name: "tool2", Description: "desc2"},
	}
	c.connected = true
	m.clients = append(m.clients, c)

	reg := tool.NewRegistry()
	count := m.RegisterTools(reg)
	if count != 2 {
		t.Errorf("RegisterTools count = %d, want 2", count)
	}
	if len(reg.List()) != 2 {
		t.Errorf("registry should have 2 tools, got %d", len(reg.List()))
	}
}

func TestManager_RegisterTools_DefaultRisk(t *testing.T) {
	m := NewManager()
	c := NewClient(ClientConfig{Name: "srv"})
	c.tools = []ToolDefinition{{Name: "t1"}}
	c.connected = true
	m.clients = append(m.clients, c)

	reg := tool.NewRegistry()
	m.RegisterTools(reg)
	meta, ok := reg.Get("srv_t1")
	if !ok {
		t.Fatal("tool srv_t1 not found in registry")
	}
	if meta.RiskLevel != tool.RiskLowWrite {
		t.Errorf("default risk = %d, want RiskLowWrite(%d)", meta.RiskLevel, tool.RiskLowWrite)
	}
}

func TestManager_RegisterTools_NoClients(t *testing.T) {
	m := NewManager()
	reg := tool.NewRegistry()
	count := m.RegisterTools(reg)
	if count != 0 {
		t.Errorf("expected 0, got %d", count)
	}
}

func TestManager_CloseAll(t *testing.T) {
	m := NewManager()
	mt1 := newMockTransport()
	mt2 := newMockTransport()
	m.clients = []*Client{
		newTestClient(ClientConfig{Name: "s1"}, mt1),
		newTestClient(ClientConfig{Name: "s2"}, mt2),
	}

	m.CloseAll()
	if !mt1.closed || !mt2.closed {
		t.Error("all transports should be closed")
	}
}

func TestManager_Clients(t *testing.T) {
	m := NewManager()
	c1 := NewClient(ClientConfig{Name: "a"})
	c2 := NewClient(ClientConfig{Name: "b"})
	m.clients = []*Client{c1, c2}

	clients := m.Clients()
	if len(clients) != 2 {
		t.Errorf("Clients() len = %d, want 2", len(clients))
	}
}

// ============================================================
// SSE Transport 测试（httptest）
// ============================================================

func TestSSETransport_Send_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/message" {
			t.Errorf("expected /message, got %s", r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q", ct)
		}
		var req JSONRPCRequest
		json.NewDecoder(r.Body).Decode(&req)
		json.NewEncoder(w).Encode(JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  json.RawMessage(`{"ok":true}`),
		})
	}))
	defer server.Close()

	tr := NewSSETransport(SSEConfig{URL: server.URL, Timeout: 5 * time.Second})
	resp, err := tr.Send(context.Background(), &JSONRPCRequest{Method: "test"})
	if err != nil {
		t.Fatalf("Send error: %v", err)
	}
	if resp == nil {
		t.Fatal("response is nil")
	}
}

func TestSSETransport_Send_CustomHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Token") != "abc123" {
			t.Error("custom header not forwarded")
		}
		json.NewEncoder(w).Encode(JSONRPCResponse{JSONRPC: "2.0"})
	}))
	defer server.Close()

	tr := NewSSETransport(SSEConfig{
		URL:     server.URL,
		Headers: map[string]string{"X-Token": "abc123"},
	})
	_, err := tr.Send(context.Background(), &JSONRPCRequest{Method: "test"})
	if err != nil {
		t.Fatalf("Send error: %v", err)
	}
}

func TestSSETransport_Send_NonOKStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	tr := NewSSETransport(SSEConfig{URL: server.URL})
	_, err := tr.Send(context.Background(), &JSONRPCRequest{Method: "test"})
	if err == nil {
		t.Error("expected error on 500")
	}
}

func TestSSETransport_Send_ConnectionRefused(t *testing.T) {
	tr := NewSSETransport(SSEConfig{URL: "http://127.0.0.1:1"})
	_, err := tr.Send(context.Background(), &JSONRPCRequest{Method: "test"})
	if err == nil {
		t.Error("expected error on connection refused")
	}
}

func TestSSETransport_Send_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`not json`))
	}))
	defer server.Close()

	tr := NewSSETransport(SSEConfig{URL: server.URL})
	_, err := tr.Send(context.Background(), &JSONRPCRequest{Method: "test"})
	if err == nil {
		t.Error("expected error on invalid JSON response")
	}
}

func TestSSETransport_Notify(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(JSONRPCResponse{JSONRPC: "2.0"})
	}))
	defer server.Close()

	tr := NewSSETransport(SSEConfig{URL: server.URL})
	if err := tr.Notify(context.Background(), "notifications/initialized", nil); err != nil {
		t.Errorf("Notify error: %v", err)
	}
}

func TestSSETransport_Close(t *testing.T) {
	tr := NewSSETransport(SSEConfig{URL: "http://localhost:1"})
	if err := tr.Close(); err != nil {
		t.Errorf("Close error: %v", err)
	}
}

func TestSSETransport_URLTrailingSlash(t *testing.T) {
	tr := NewSSETransport(SSEConfig{URL: "http://localhost:3000/sse/"})
	if tr.baseURL != "http://localhost:3000/sse" {
		t.Errorf("baseURL = %q, want trailing slash removed", tr.baseURL)
	}
}

func TestSSETransport_DefaultTimeout(t *testing.T) {
	tr := NewSSETransport(SSEConfig{URL: "http://localhost"})
	if tr.httpClient.Timeout != 60*time.Second {
		t.Errorf("default timeout = %v, want 60s", tr.httpClient.Timeout)
	}
}

func TestSSETransport_IDIncrement(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(JSONRPCResponse{JSONRPC: "2.0"})
	}))
	defer server.Close()

	tr := NewSSETransport(SSEConfig{URL: server.URL})
	tr.Send(context.Background(), &JSONRPCRequest{Method: "a"})
	tr.Send(context.Background(), &JSONRPCRequest{Method: "b"})
	if tr.nextID != 3 {
		t.Errorf("nextID = %d, want 3", tr.nextID)
	}
}

func TestSSETransport_ContextCancelled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		json.NewEncoder(w).Encode(JSONRPCResponse{JSONRPC: "2.0"})
	}))
	defer server.Close()

	tr := NewSSETransport(SSEConfig{URL: server.URL, Timeout: 5 * time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := tr.Send(ctx, &JSONRPCRequest{Method: "test"})
	if err == nil {
		t.Error("expected error on cancelled context")
	}
}

// ============================================================
// MCPToolAdapter.Execute 集成测试（通过 mock transport）
// ============================================================

func TestMCPToolAdapter_Execute_Success(t *testing.T) {
	resultJSON, _ := json.Marshal(&ToolCallResult{
		Content: []ToolContent{{Type: "text", Text: "result data"}},
	})
	mt := newMockTransport()
	mt.responses["tools/call"] = &JSONRPCResponse{JSONRPC: "2.0", Result: resultJSON}

	c := newTestClient(ClientConfig{Name: "srv"}, mt)
	adapter := NewMCPToolAdapter(c, ToolDefinition{Name: "fetch", Description: "fetch data"})

	res, err := adapter.Execute(context.Background(), map[string]interface{}{"url": "http://x"})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if res.Output != "result data" {
		t.Errorf("Output = %q, want 'result data'", res.Output)
	}
	if res.Metadata["source"] != "mcp" {
		t.Error("Metadata should contain source=mcp")
	}
}

func TestMCPToolAdapter_Execute_Error(t *testing.T) {
	mt := newMockTransport()
	mt.errors["tools/call"] = fmt.Errorf("timeout")

	c := newTestClient(ClientConfig{Name: "srv"}, mt)
	adapter := NewMCPToolAdapter(c, ToolDefinition{Name: "slow"})

	_, err := adapter.Execute(context.Background(), nil)
	if err == nil {
		t.Error("expected error")
	}
}

func TestMCPToolAdapter_Execute_ToolError(t *testing.T) {
	resultJSON, _ := json.Marshal(&ToolCallResult{
		Content: []ToolContent{{Type: "text", Text: "not found"}},
		IsError: true,
	})
	mt := newMockTransport()
	mt.responses["tools/call"] = &JSONRPCResponse{JSONRPC: "2.0", Result: resultJSON}

	c := newTestClient(ClientConfig{Name: "srv"}, mt)
	adapter := NewMCPToolAdapter(c, ToolDefinition{Name: "lookup"})

	res, err := adapter.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute should not return Go error for tool error: %v", err)
	}
	if res.Error != "not found" {
		t.Errorf("Error = %q, want 'not found'", res.Error)
	}
}
