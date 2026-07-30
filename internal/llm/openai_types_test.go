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
