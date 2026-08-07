package engine

import (
	"context"
	"testing"

	"github.com/tommy-cat/agent/internal/llm"
	"github.com/tommy-cat/agent/internal/tool"
	"github.com/tommy-cat/agent/internal/trace"
)

// scriptedLLM 按顺序返回预置响应：第一次触发工具调用，第二次给出最终答案。
type scriptedLLM struct {
	responses []llm.ChatResponse
	idx       int
}

func (m *scriptedLLM) Chat(_ context.Context, _ []llm.Message, _ []llm.ToolDef) (llm.ChatResponse, error) {
	if m.idx < len(m.responses) {
		r := m.responses[m.idx]
		m.idx++
		return r, nil
	}
	return llm.ChatResponse{Content: "done"}, nil
}

// echoTools 总是成功执行工具的最小 ToolCaller 实现。
type echoTools struct{}

func (f *echoTools) Call(_ context.Context, name string, _ map[string]interface{}) (tool.Result, error) {
	return tool.Result{Output: "ok:" + name}, nil
}

func (f *echoTools) ToToolDefs() []llm.ToolDef { return nil }

// TestRun_RecordsTraceSpans 回归 P0 缺陷：Engine 曾不持有 Tracer，
// trace.Tracer.StartSpan 在生产代码中零调用，/trace 与 JSONL 导出永远为空。
// 修复后 Run 应记录 task / llm.chat / tool.* 三类 span。
func TestRun_RecordsTraceSpans(t *testing.T) {
	tracer := trace.NewTracer()
	llmClient := &scriptedLLM{responses: []llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{{ID: "tc-1", Name: "shell_exec", Arguments: `{"cmd":"ls"}`}}},
		{Content: "final"},
	}}

	eng := NewEngine(EngineConfig{
		LLM:           llmClient,
		Tools:         &echoTools{},
		MaxIterations: 5,
		Tracer:        tracer,
	})

	result, err := eng.Run(context.Background(), "测试任务")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	spans := tracer.GetSpans()
	if len(spans) == 0 {
		t.Fatal("配置 Tracer 后 Run 应记录 span，实际为空（追踪未接线）")
	}
	names := make(map[string]bool)
	for _, s := range spans {
		names[s.Name] = true
		if s.TraceID != result.TaskID {
			t.Errorf("span TraceID = %s, want %s", s.TraceID, result.TaskID)
		}
		if s.EndTime.Before(s.StartTime) {
			t.Errorf("span %s 结束时间早于开始时间", s.Name)
		}
	}
	for _, want := range []string{"task", "llm.chat", "tool.shell_exec"} {
		if !names[want] {
			t.Errorf("缺少 span %q，实际记录: %v", want, names)
		}
	}
}

// TestRun_NilTracer 验证未配置追踪器时 Run 正常执行不 panic。
func TestRun_NilTracer(t *testing.T) {
	eng := NewEngine(EngineConfig{
		LLM:   &scriptedLLM{responses: []llm.ChatResponse{{Content: "final"}}},
		Tools: &echoTools{},
	})
	result, err := eng.Run(context.Background(), "hello")
	if err != nil {
		t.Fatalf("未配置 Tracer 时 Run 应成功: %v", err)
	}
	if len(result.Steps) == 0 {
		t.Error("Run 应至少产生一个最终答案步骤")
	}
}
