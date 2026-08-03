package llm

import (
	"testing"
)

func TestParseOpenAIResponse_Empty(t *testing.T) {
	resp := openAIResponse{}
	result := parseOpenAIResponse(resp)
	if result.Content != "" {
		t.Errorf("Content should be empty, got %q", result.Content)
	}
	if len(result.ToolCalls) != 0 {
		t.Errorf("ToolCalls should be empty, got %d", len(result.ToolCalls))
	}
}

func TestParseOpenAIResponse_WithContent(t *testing.T) {
	resp := openAIResponse{
		Model: "deepseek-chat",
		Choices: []openAIChoice{
			{
				Index: 0,
				Message: openAIMessage{
					Role:    "assistant",
					Content: "Hello, world!",
				},
				FinishReason: "stop",
			},
		},
		Usage: Usage{
			PromptTokens:     10,
			CompletionTokens: 5,
			TotalTokens:      15,
		},
	}
	result := parseOpenAIResponse(resp)
	if result.Content != "Hello, world!" {
		t.Errorf("Content = %q, want %q", result.Content, "Hello, world!")
	}
	if result.Model != "deepseek-chat" {
		t.Errorf("Model = %q, want %q", result.Model, "deepseek-chat")
	}
	if result.FinishReason != "stop" {
		t.Errorf("FinishReason = %q, want %q", result.FinishReason, "stop")
	}
	if result.Usage.TotalTokens != 15 {
		t.Errorf("Usage.TotalTokens = %d, want 15", result.Usage.TotalTokens)
	}
}

func TestParseOpenAIResponse_WithToolCalls(t *testing.T) {
	resp := openAIResponse{
		Model: "deepseek-chat",
		Choices: []openAIChoice{
			{
				Index: 0,
				Message: openAIMessage{
					Role:    "assistant",
					Content: "",
					ToolCalls: []openAIToolCall{
						{
							ID:   "call_123",
							Type: "function",
							Function: openAIFuncCall{
								Name:      "web_search",
								Arguments: `{"query":"test"}`,
							},
						},
					},
				},
				FinishReason: "tool_calls",
			},
		},
	}
	result := parseOpenAIResponse(resp)
	if len(result.ToolCalls) != 1 {
		t.Fatalf("ToolCalls length = %d, want 1", len(result.ToolCalls))
	}
	if result.ToolCalls[0].ID != "call_123" {
		t.Errorf("ToolCall ID = %q, want %q", result.ToolCalls[0].ID, "call_123")
	}
	if result.ToolCalls[0].Name != "web_search" {
		t.Errorf("ToolCall Name = %q, want %q", result.ToolCalls[0].Name, "web_search")
	}
	if result.ToolCalls[0].Arguments != `{"query":"test"}` {
		t.Errorf("ToolCall Arguments = %q", result.ToolCalls[0].Arguments)
	}
}

func TestParseOpenAIResponse_MultipleToolCalls(t *testing.T) {
	resp := openAIResponse{
		Model: "gpt-4",
		Choices: []openAIChoice{
			{
				Index: 0,
				Message: openAIMessage{
					Role: "assistant",
					ToolCalls: []openAIToolCall{
						{
							ID:   "call_1",
							Type: "function",
							Function: openAIFuncCall{
								Name:      "search",
								Arguments: `{"q":"a"}`,
							},
						},
						{
							ID:   "call_2",
							Type: "function",
							Function: openAIFuncCall{
								Name:      "fetch",
								Arguments: `{"url":"b"}`,
							},
						},
					},
				},
			},
		},
	}
	result := parseOpenAIResponse(resp)
	if len(result.ToolCalls) != 2 {
		t.Fatalf("ToolCalls length = %d, want 2", len(result.ToolCalls))
	}
	if result.ToolCalls[0].Name != "search" || result.ToolCalls[1].Name != "fetch" {
		t.Error("ToolCall order incorrect")
	}
}

func TestParseOpenAIResponse_FirstChoiceOnly(t *testing.T) {
	resp := openAIResponse{
		Choices: []openAIChoice{
			{
				Index: 0,
				Message: openAIMessage{
					Content: "first",
				},
			},
			{
				Index: 1,
				Message: openAIMessage{
					Content: "second",
				},
			},
		},
	}
	result := parseOpenAIResponse(resp)
	if result.Content != "first" {
		t.Errorf("should use first choice, got %q", result.Content)
	}
}

func TestParseOpenAIResponse_EmptyToolCallIDFallback(t *testing.T) {
	resp := openAIResponse{
		Model: "mimo-v2.5-pro",
		Choices: []openAIChoice{
			{
				Index: 0,
				Message: openAIMessage{
					Role: "assistant",
					ToolCalls: []openAIToolCall{
						{
							// 模拟 MiMo 不返回 tool call ID 的情况
							ID:   "",
							Type: "function",
							Function: openAIFuncCall{
								Name:      "web_search",
								Arguments: `{"query":"a"}`,
							},
						},
						{
							ID:   "",
							Type: "function",
							Function: openAIFuncCall{
								Name:      "file_read",
								Arguments: `{"path":"b"}`,
							},
						},
					},
				},
				FinishReason: "tool_calls",
			},
		},
	}
	result := parseOpenAIResponse(resp)
	if len(result.ToolCalls) != 2 {
		t.Fatalf("ToolCalls length = %d, want 2", len(result.ToolCalls))
	}
	if result.ToolCalls[0].ID != "call_0" {
		t.Errorf("ToolCalls[0].ID = %q, want %q", result.ToolCalls[0].ID, "call_0")
	}
	if result.ToolCalls[1].ID != "call_1" {
		t.Errorf("ToolCalls[1].ID = %q, want %q", result.ToolCalls[1].ID, "call_1")
	}
	if result.ToolCalls[0].Name != "web_search" || result.ToolCalls[1].Name != "file_read" {
		t.Error("兜底 ID 不应影响工具名称解析")
	}
}

func TestToOpenAIMessages_AssistantWithToolCalls(t *testing.T) {
	msgs := []Message{
		{
			Role:    RoleAssistant,
			Content: "",
			ToolCalls: []ToolCall{
				{ID: "call_0", Name: "web_search", Arguments: `{"query":"golang"}`},
				{ID: "call_1", Name: "file_read", Arguments: `{"path":"/tmp/a.txt"}`},
			},
		},
	}
	result := toOpenAIMessages(msgs)
	if len(result) != 1 {
		t.Fatalf("messages length = %d, want 1", len(result))
	}
	m := result[0]
	if m.Role != RoleAssistant {
		t.Errorf("Role = %q, want %q", m.Role, RoleAssistant)
	}
	if len(m.ToolCalls) != 2 {
		t.Fatalf("ToolCalls length = %d, want 2", len(m.ToolCalls))
	}

	// 逐一断言嵌套结构 {id, type, function:{name, arguments}}
	tc0 := m.ToolCalls[0]
	if tc0.ID != "call_0" {
		t.Errorf("ToolCalls[0].ID = %q, want %q", tc0.ID, "call_0")
	}
	if tc0.Type != "function" {
		t.Errorf("ToolCalls[0].Type = %q, want %q", tc0.Type, "function")
	}
	if tc0.Function.Name != "web_search" {
		t.Errorf("ToolCalls[0].Function.Name = %q, want %q", tc0.Function.Name, "web_search")
	}
	if tc0.Function.Arguments != `{"query":"golang"}` {
		t.Errorf("ToolCalls[0].Function.Arguments = %q", tc0.Function.Arguments)
	}

	tc1 := m.ToolCalls[1]
	if tc1.ID != "call_1" {
		t.Errorf("ToolCalls[1].ID = %q, want %q", tc1.ID, "call_1")
	}
	if tc1.Type != "function" {
		t.Errorf("ToolCalls[1].Type = %q, want %q", tc1.Type, "function")
	}
	if tc1.Function.Name != "file_read" {
		t.Errorf("ToolCalls[1].Function.Name = %q, want %q", tc1.Function.Name, "file_read")
	}
	if tc1.Function.Arguments != `{"path":"/tmp/a.txt"}` {
		t.Errorf("ToolCalls[1].Function.Arguments = %q", tc1.Function.Arguments)
	}
}

func TestToOpenAIMessages_ToolMessage(t *testing.T) {
	msgs := []Message{
		{
			Role:       RoleTool,
			Content:    "搜索结果……",
			ToolCallID: "call_0",
		},
	}
	result := toOpenAIMessages(msgs)
	if len(result) != 1 {
		t.Fatalf("messages length = %d, want 1", len(result))
	}
	if result[0].Role != RoleTool {
		t.Errorf("Role = %q, want %q", result[0].Role, RoleTool)
	}
	if result[0].ToolCallID != "call_0" {
		t.Errorf("ToolCallID = %q, want %q", result[0].ToolCallID, "call_0")
	}
	if result[0].Content != "搜索结果……" {
		t.Errorf("Content = %q", result[0].Content)
	}
	if len(result[0].ToolCalls) != 0 {
		t.Errorf("role=tool 消息不应携带 ToolCalls, got %d", len(result[0].ToolCalls))
	}
}

func TestToOpenAIMessages_PlainMessages(t *testing.T) {
	msgs := []Message{
		{Role: RoleSystem, Content: "你是助手"},
		{Role: RoleUser, Content: "你好"},
		{Role: RoleAssistant, Content: "你好，有什么可以帮你？"},
	}
	result := toOpenAIMessages(msgs)
	if len(result) != 3 {
		t.Fatalf("messages length = %d, want 3", len(result))
	}
	for i, want := range msgs {
		if result[i].Role != want.Role {
			t.Errorf("messages[%d].Role = %q, want %q", i, result[i].Role, want.Role)
		}
		if result[i].Content != want.Content {
			t.Errorf("messages[%d].Content = %q, want %q", i, result[i].Content, want.Content)
		}
		if len(result[i].ToolCalls) != 0 {
			t.Errorf("messages[%d] 不应携带 ToolCalls", i)
		}
		if result[i].ToolCallID != "" {
			t.Errorf("messages[%d] 不应携带 ToolCallID", i)
		}
	}
}
