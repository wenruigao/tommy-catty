package llm

import (
	"context"
	"io"
	"strings"
	"testing"
)

// collectChunks 读取通道中的所有数据块
func collectChunks(ch <-chan StreamChunk) []StreamChunk {
	var chunks []StreamChunk
	for c := range ch {
		chunks = append(chunks, c)
	}
	return chunks
}

func TestParseSSEStream_ToolCallIDFallback(t *testing.T) {
	// 模拟 MiMo 流式响应：tool_calls 不携带 id 字段
	sse := `data: {"id":"chatcmpl-1","model":"mimo","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"type":"function","function":{"name":"web_search","arguments":"{\"query\":\"go\"}"}}]}}]}` + "\n" +
		`data: [DONE]` + "\n"

	p := &GenericProvider{}
	ch := make(chan StreamChunk, 8)
	go p.parseSSEStream(context.Background(), io.NopCloser(strings.NewReader(sse)), ch)

	chunks := collectChunks(ch)
	if len(chunks) != 2 {
		t.Fatalf("chunk count = %d, want 2 (tool call + DONE)", len(chunks))
	}

	tc := chunks[0].ToolCallDelta
	if tc == nil {
		t.Fatal("第一个 chunk 应包含 ToolCallDelta")
	}
	if tc.ID != "call_0" {
		t.Errorf("ToolCallDelta.ID = %q, want 兜底 ID %q", tc.ID, "call_0")
	}
	if tc.Name != "web_search" {
		t.Errorf("ToolCallDelta.Name = %q, want %q", tc.Name, "web_search")
	}
	if tc.Arguments != `{"query":"go"}` {
		t.Errorf("ToolCallDelta.Arguments = %q", tc.Arguments)
	}
	if !chunks[1].Done {
		t.Error("[DONE] 应产生 Done=true 的结束块")
	}
}

func TestParseSSEStream_ToolCallWithID(t *testing.T) {
	// 标准 OpenAI 流式响应：tool_calls 携带 id，不应被兜底覆盖
	sse := `data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"tool_calls":[{"id":"call_abc","type":"function","function":{"name":"file_read","arguments":"{}"}}]}}]}` + "\n" +
		`data: [DONE]` + "\n"

	p := &GenericProvider{}
	ch := make(chan StreamChunk, 8)
	go p.parseSSEStream(context.Background(), io.NopCloser(strings.NewReader(sse)), ch)

	chunks := collectChunks(ch)
	if len(chunks) < 1 || chunks[0].ToolCallDelta == nil {
		t.Fatal("第一个 chunk 应包含 ToolCallDelta")
	}
	if chunks[0].ToolCallDelta.ID != "call_abc" {
		t.Errorf("ToolCallDelta.ID = %q, want %q（不应被兜底覆盖）", chunks[0].ToolCallDelta.ID, "call_abc")
	}
}

func TestParseSSEStream_MultipleToolCalls(t *testing.T) {
	// 单个 SSE chunk 中携带两个并发 tool call（按 index 区分），两个都应被解析
	sse := `data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"web_search","arguments":"{\"query\":\"go\"}"}},{"index":1,"id":"call_b","type":"function","function":{"name":"file_read","arguments":"{\"path\":\"/tmp/a.txt\"}"}}]}}]}` + "\n" +
		`data: [DONE]` + "\n"

	p := &GenericProvider{}
	ch := make(chan StreamChunk, 8)
	go p.parseSSEStream(context.Background(), io.NopCloser(strings.NewReader(sse)), ch)

	chunks := collectChunks(ch)
	if len(chunks) != 2 {
		t.Fatalf("chunk count = %d, want 2 (tool calls + DONE)", len(chunks))
	}

	deltas := chunks[0].ToolCallDeltas
	if len(deltas) != 2 {
		t.Fatalf("ToolCallDeltas count = %d, want 2", len(deltas))
	}
	if deltas[0].ID != "call_a" || deltas[0].Name != "web_search" {
		t.Errorf("ToolCallDeltas[0] = %+v, want ID=call_a Name=web_search", deltas[0])
	}
	if deltas[1].ID != "call_b" || deltas[1].Name != "file_read" {
		t.Errorf("ToolCallDeltas[1] = %+v, want ID=call_b Name=file_read", deltas[1])
	}

	// 兼容字段应指向第一个工具调用
	if chunks[0].ToolCallDelta == nil || chunks[0].ToolCallDelta.ID != "call_a" {
		t.Errorf("ToolCallDelta = %+v, want 指向第一个工具调用 call_a", chunks[0].ToolCallDelta)
	}
}

func TestParseSSEStream_MultipleToolCallsIDFallback(t *testing.T) {
	// 多个 tool call 均不携带 id 时，按 index 生成 call_0/call_1；
	// 跨 chunk 的后续片段通过 index 归并，兜底 ID 保持稳定
	sse := `data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"type":"function","function":{"name":"web_search","arguments":"{\"query\":"}},{"index":1,"type":"function","function":{"name":"file_read","arguments":"{\"path\":"}}]}}]}` + "\n" +
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"\"/tmp/a.txt\"}"}}]}}]}` + "\n" +
		`data: [DONE]` + "\n"

	p := &GenericProvider{}
	ch := make(chan StreamChunk, 8)
	go p.parseSSEStream(context.Background(), io.NopCloser(strings.NewReader(sse)), ch)

	chunks := collectChunks(ch)
	if len(chunks) != 3 {
		t.Fatalf("chunk count = %d, want 3 (两个 tool call chunk + DONE)", len(chunks))
	}

	deltas := chunks[0].ToolCallDeltas
	if len(deltas) != 2 {
		t.Fatalf("第一个 chunk 的 ToolCallDeltas count = %d, want 2", len(deltas))
	}
	if deltas[0].ID != "call_0" {
		t.Errorf("ToolCallDeltas[0].ID = %q, want 兜底 ID %q", deltas[0].ID, "call_0")
	}
	if deltas[1].ID != "call_1" {
		t.Errorf("ToolCallDeltas[1].ID = %q, want 兜底 ID %q", deltas[1].ID, "call_1")
	}

	// 第二个 chunk 仅含 index=1 的增量片段，兜底 ID 应仍为 call_1（按 index 而非切片位置）
	deltas2 := chunks[1].ToolCallDeltas
	if len(deltas2) != 1 {
		t.Fatalf("第二个 chunk 的 ToolCallDeltas count = %d, want 1", len(deltas2))
	}
	if deltas2[0].ID != "call_1" {
		t.Errorf("跨 chunk 增量片段 ID = %q, want %q（按 index 归并保持稳定）", deltas2[0].ID, "call_1")
	}
}
