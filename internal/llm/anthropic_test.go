package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// captureAnthropicRequest 启动一个 httptest 服务器，捕获请求头与请求体后返回固定响应
func captureAnthropicRequest(t *testing.T, status int, respBody string) (*AnthropicProvider, *http.Header, *anthropicRequest) {
	t.Helper()
	var gotHeader http.Header
	var gotReq anthropicRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Clone()
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Errorf("解码请求体失败: %v", err)
		}
		w.WriteHeader(status)
		fmt.Fprint(w, respBody)
	}))
	t.Cleanup(srv.Close)

	p := NewAnthropicProvider(ProviderConfig{
		Name:    "claude",
		BaseURL: srv.URL,
		APIKey:  "sk-ant-test",
		Model:   "claude-sonnet-4-5",
	})
	return p, &gotHeader, &gotReq
}

func TestAnthropicChat_RequestStructure(t *testing.T) {
	p, header, gotReq := captureAnthropicRequest(t, http.StatusOK, `{
		"model": "claude-sonnet-4-5",
		"content": [{"type": "text", "text": "好的"}],
		"stop_reason": "end_turn",
		"usage": {"input_tokens": 10, "output_tokens": 5}
	}`)

	resp, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{
			{Role: RoleSystem, Content: "你是助手"},
			{Role: RoleSystem, Content: "保持简洁"},
			{Role: RoleUser, Content: "查天气"},
			{Role: RoleAssistant, Content: "我来查", ToolCalls: []ToolCall{
				{ID: "toolu_abc", Name: "web_search", Arguments: `{"query":"北京天气"}`},
			}},
			{Role: RoleTool, ToolCallID: "toolu_abc", Content: "晴，25度"},
			{Role: RoleUser, Content: "谢谢"},
		},
		Tools: []ToolDef{{
			Name:        "web_search",
			Description: "网络搜索",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]interface{}{"type": "string"},
				},
			},
		}},
		MaxTokens: 1024,
	})
	if err != nil {
		t.Fatalf("Chat 出错: %v", err)
	}
	if resp.Content != "好的" {
		t.Errorf("Content = %q, want %q", resp.Content, "好的")
	}

	// ── 请求头断言 ──
	if got := header.Get("x-api-key"); got != "sk-ant-test" {
		t.Errorf("x-api-key = %q, want %q", got, "sk-ant-test")
	}
	if got := header.Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("anthropic-version = %q, want %q", got, "2023-06-01")
	}
	if got := header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}

	// ── system 消息进入顶层字段，messages 中无 system 角色 ──
	if gotReq.System != "你是助手\n\n保持简洁" {
		t.Errorf("system = %q, want 两条 system 消息合并", gotReq.System)
	}
	for i, m := range gotReq.Messages {
		if m.Role == RoleSystem {
			t.Errorf("messages[%d] 不应包含 system 角色", i)
		}
	}

	// ── assistant ToolCalls 转为 tool_use 块 ──
	// 消息序列：user(查天气) / assistant(text+tool_use) / user(tool_result + 谢谢)
	// 相邻同角色消息合并后应为 3 条
	if len(gotReq.Messages) != 3 {
		t.Fatalf("messages 条数 = %d, want 3: %+v", len(gotReq.Messages), gotReq.Messages)
	}
	assistantMsg := gotReq.Messages[1]
	if assistantMsg.Role != RoleAssistant {
		t.Fatalf("messages[1].Role = %q, want assistant", assistantMsg.Role)
	}
	if len(assistantMsg.Content) != 2 {
		t.Fatalf("assistant content 块数 = %d, want 2 (text + tool_use)", len(assistantMsg.Content))
	}
	toolUse := assistantMsg.Content[1]
	if toolUse.Type != "tool_use" || toolUse.ID != "toolu_abc" || toolUse.Name != "web_search" {
		t.Errorf("tool_use 块 = %+v, want id=toolu_abc name=web_search", toolUse)
	}
	if toolUse.Input["query"] != "北京天气" {
		t.Errorf("tool_use.input = %v, want query=北京天气", toolUse.Input)
	}

	// ── tool 消息转为 user 消息内的 tool_result 块 ──
	// 后续的普通 user 消息（"谢谢"）因角色相邻合并进同一条 user 消息
	toolResultMsg := gotReq.Messages[2]
	if toolResultMsg.Role != RoleUser {
		t.Fatalf("messages[2].Role = %q, want user（tool_result 所在消息）", toolResultMsg.Role)
	}
	if len(toolResultMsg.Content) != 2 || toolResultMsg.Content[0].Type != "tool_result" {
		t.Fatalf("tool_result 消息块 = %+v, want tool_result + text 两块", toolResultMsg.Content)
	}
	tr := toolResultMsg.Content[0]
	if tr.ToolUseID != "toolu_abc" {
		t.Errorf("tool_result.tool_use_id = %q, want %q", tr.ToolUseID, "toolu_abc")
	}
	if tr.Content != "晴，25度" {
		t.Errorf("tool_result.content = %q, want %q", tr.Content, "晴，25度")
	}

	// ── 工具定义映射为 input_schema ──
	if len(gotReq.Tools) != 1 {
		t.Fatalf("tools 数量 = %d, want 1", len(gotReq.Tools))
	}
	tool := gotReq.Tools[0]
	if tool.Name != "web_search" || tool.Description != "网络搜索" {
		t.Errorf("tool = %+v, want name=web_search", tool)
	}
	if tool.InputSchema["type"] != "object" {
		t.Errorf("input_schema = %v, want 透传 parameters", tool.InputSchema)
	}

	// ── max_tokens 使用请求指定值 ──
	if gotReq.MaxTokens != 1024 {
		t.Errorf("max_tokens = %d, want 1024", gotReq.MaxTokens)
	}
}

func TestAnthropicChat_MaxTokensDefault(t *testing.T) {
	p, _, gotReq := captureAnthropicRequest(t, http.StatusOK, `{
		"model": "claude-sonnet-4-5",
		"content": [{"type": "text", "text": "hi"}],
		"stop_reason": "end_turn",
		"usage": {"input_tokens": 1, "output_tokens": 1}
	}`)

	// 请求未指定 MaxTokens：Anthropic 要求 max_tokens 必填，应使用兜底值
	_, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat 出错: %v", err)
	}
	if gotReq.MaxTokens != anthropicDefaultMaxTokens {
		t.Errorf("max_tokens = %d, want 兜底值 %d", gotReq.MaxTokens, anthropicDefaultMaxTokens)
	}
}

func TestAnthropicChat_CustomHeaders(t *testing.T) {
	var gotHeader http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Clone()
		fmt.Fprint(w, `{"model":"m","content":[],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer srv.Close()

	p := NewAnthropicProvider(ProviderConfig{
		Name:    "claude",
		BaseURL: srv.URL,
		APIKey:  "k",
		Headers: map[string]string{"X-Custom": "v1"},
	})
	if _, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}); err != nil {
		t.Fatalf("Chat 出错: %v", err)
	}
	if got := gotHeader.Get("X-Custom"); got != "v1" {
		t.Errorf("自定义头 X-Custom = %q, want %q", got, "v1")
	}
}

func TestParseAnthropicResponse_TextAndToolUse(t *testing.T) {
	resp := parseAnthropicResponse(anthropicResponse{
		Model: "claude-sonnet-4-5",
		Content: []anthropicContentBlock{
			{Type: "text", Text: "我先搜索。"},
			{Type: "tool_use", ID: "toolu_1", Name: "web_search", Input: map[string]interface{}{"query": "天气"}},
			{Type: "tool_use", Name: "file_read", Input: map[string]interface{}{"path": "/tmp/a.txt"}},
		},
		StopReason: "tool_use",
		Usage:      anthropicUsage{InputTokens: 100, OutputTokens: 20, CacheReadInputTokens: 60},
	})

	if resp.Content != "我先搜索。" {
		t.Errorf("Content = %q, want %q", resp.Content, "我先搜索。")
	}
	if resp.FinishReason != "tool_calls" {
		t.Errorf("FinishReason = %q, want tool_calls", resp.FinishReason)
	}
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("ToolCalls 数量 = %d, want 2", len(resp.ToolCalls))
	}
	// 第一个工具调用
	if resp.ToolCalls[0].ID != "toolu_1" || resp.ToolCalls[0].Name != "web_search" {
		t.Errorf("ToolCalls[0] = %+v, want id=toolu_1 name=web_search", resp.ToolCalls[0])
	}
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(resp.ToolCalls[0].Arguments), &args); err != nil {
		t.Fatalf("Arguments 应为合法 JSON: %v", err)
	}
	if args["query"] != "天气" {
		t.Errorf("Arguments = %q, want 含 query=天气", resp.ToolCalls[0].Arguments)
	}
	// 第二个工具调用 ID 为空，应兜底为 toolu_<index>
	if resp.ToolCalls[1].ID != "toolu_2" {
		t.Errorf("ToolCalls[1].ID = %q, want 兜底 ID %q", resp.ToolCalls[1].ID, "toolu_2")
	}

	// usage 映射：cache_read_input_tokens → CachedTokens
	if resp.Usage.PromptTokens != 100 || resp.Usage.CompletionTokens != 20 || resp.Usage.TotalTokens != 120 {
		t.Errorf("Usage = %+v, want 100/20/120", resp.Usage)
	}
	if resp.Usage.PromptDetails.CachedTokens != 60 {
		t.Errorf("CachedTokens = %d, want 60", resp.Usage.PromptDetails.CachedTokens)
	}
}

func TestMapAnthropicStopReason(t *testing.T) {
	cases := map[string]string{
		"end_turn":   "stop",
		"tool_use":   "tool_calls",
		"max_tokens": "length",
	}
	for in, want := range cases {
		if got := mapAnthropicStopReason(in); got != want {
			t.Errorf("mapAnthropicStopReason(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAnthropicChatStream(t *testing.T) {
	sse := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":1}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"你好"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"，世界"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"","name":"web_search"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"query\":"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"天气\"}"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":1}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":20}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sse)
	}))
	defer srv.Close()

	p := NewAnthropicProvider(ProviderConfig{
		Name:    "claude",
		BaseURL: srv.URL,
		APIKey:  "k",
		Model:   "claude-sonnet-4-5",
	})

	ch, err := p.ChatStream(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatStream 出错: %v", err)
	}

	var text strings.Builder
	var lastTool *ToolCall
	done := false
	for chunk := range ch {
		text.WriteString(chunk.Delta)
		if len(chunk.ToolCallDeltas) > 0 {
			tc := chunk.ToolCallDeltas[0]
			lastTool = &tc
		}
		if chunk.Done {
			done = true
		}
	}

	// 文本 delta 拼接
	if text.String() != "你好，世界" {
		t.Errorf("文本 delta 拼接 = %q, want %q", text.String(), "你好，世界")
	}
	// tool call 归并：ID 兜底为 toolu_<index>，参数跨 chunk 累积
	if lastTool == nil {
		t.Fatal("应收到工具调用 delta")
	}
	if lastTool.ID != "toolu_1" {
		t.Errorf("工具调用 ID = %q, want 兜底 ID %q", lastTool.ID, "toolu_1")
	}
	if lastTool.Name != "web_search" {
		t.Errorf("工具调用 Name = %q, want web_search", lastTool.Name)
	}
	if lastTool.Arguments != `{"query":"天气"}` {
		t.Errorf("归并后的 Arguments = %q, want %q", lastTool.Arguments, `{"query":"天气"}`)
	}
	if !done {
		t.Error("message_stop 事件应产生 Done chunk")
	}
}

func TestAnthropicChat_Unauthorized(t *testing.T) {
	p, _, _ := captureAnthropicRequest(t, http.StatusUnauthorized,
		`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)

	_, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("401 响应应返回错误")
	}
	// 错误应包含状态码与响应摘要
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("错误信息 = %q, want 含状态码 401", err.Error())
	}
	if !strings.Contains(err.Error(), "invalid x-api-key") {
		t.Errorf("错误信息 = %q, want 含响应摘要", err.Error())
	}
	// 应归类为结构化 APIError，供重试机制分类
	if _, ok := err.(*APIError); !ok {
		t.Errorf("错误类型 = %T, want *APIError", err)
	}
}

func TestAnthropicChatStream_Non200(t *testing.T) {
	p, _, _ := captureAnthropicRequest(t, http.StatusUnauthorized, `{"error":"unauthorized"}`)

	_, err := p.ChatStream(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("401 响应应返回错误")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("错误信息 = %q, want 含状态码 401", err.Error())
	}
}

func TestAnthropicProvider_DefaultBaseURL(t *testing.T) {
	p := NewAnthropicProvider(ProviderConfig{Name: "claude", Model: "claude-sonnet-4-5"})
	if p.cfg.BaseURL != anthropicDefaultBaseURL {
		t.Errorf("BaseURL = %q, want 默认端点 %q", p.cfg.BaseURL, anthropicDefaultBaseURL)
	}
}

func TestNewGatewayFromConfig_AnthropicProtocol(t *testing.T) {
	gw := NewGatewayFromConfig(GatewayConfig{
		Providers: map[string]ProviderConfig{
			"claude":   {Protocol: "anthropic", Model: "claude-sonnet-4-5"},
			"deepseek": {Model: "deepseek-chat"},
		},
		DefaultProvider: "deepseek",
	})

	p, err := gw.getProvider("claude")
	if err != nil {
		t.Fatalf("getProvider 出错: %v", err)
	}
	if _, ok := p.(*AnthropicProvider); !ok {
		t.Errorf("protocol=anthropic 的供应商类型 = %T, want *AnthropicProvider", p)
	}

	p2, err := gw.getProvider("deepseek")
	if err != nil {
		t.Fatalf("getProvider 出错: %v", err)
	}
	if _, ok := p2.(*GenericProvider); !ok {
		t.Errorf("默认协议的供应商类型 = %T, want *GenericProvider", p2)
	}
}
