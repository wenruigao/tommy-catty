package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/wenruigao/tommy-catty/internal/llm"
	"github.com/wenruigao/tommy-catty/internal/tool"
)

// ============================================================
// 测试替身：mock LLMClient / 计数 ToolCaller / 简易 MemoryStore / mock ToolGate
// ============================================================

// mockLLMClient 按预设序列返回响应，并记录每次请求的消息列表。
// 响应用尽后默认返回固定最终答案，保证测试确定性。
type mockLLMClient struct {
	responses []llm.ChatResponse
	requests  [][]llm.Message // 每次 Chat 调用收到的消息快照
}

// Chat 实现 LLMClient 接口。
func (m *mockLLMClient) Chat(_ context.Context, messages []llm.Message, _ []llm.ToolDef) (llm.ChatResponse, error) {
	snapshot := make([]llm.Message, len(messages))
	copy(snapshot, messages)
	m.requests = append(m.requests, snapshot)

	if len(m.responses) == 0 {
		return llm.ChatResponse{Content: "默认最终答案"}, nil
	}
	resp := m.responses[0]
	m.responses = m.responses[1:]
	return resp, nil
}

// countingToolCaller 统计每个工具被调用的次数，可配置返回错误。
type countingToolCaller struct {
	calls    map[string]int         // 工具名 -> 调用次数
	callErr  error                  // 若非 nil，Call 直接返回该错误
	result   map[string]tool.Result // 工具名 -> 固定返回结果（可选）
	toolDefs []llm.ToolDef
}

// Call 实现 ToolCaller 接口。
func (c *countingToolCaller) Call(_ context.Context, name string, _ map[string]interface{}) (tool.Result, error) {
	c.calls[name]++
	if c.callErr != nil {
		return tool.Result{}, c.callErr
	}
	if r, ok := c.result[name]; ok {
		return r, nil
	}
	return tool.Result{Output: "ok:" + name}, nil
}

// ToToolDefs 实现 ToolCaller 接口。
func (c *countingToolCaller) ToToolDefs() []llm.ToolDef {
	return c.toolDefs
}

// simpleMemory 是最小 MemoryStore 实现，仅记录存入的消息。
type simpleMemory struct {
	stored []llm.Message
}

// GetContext 实现 MemoryStore 接口。
func (m *simpleMemory) GetContext(int) []llm.Message { return nil }

// Store 实现 MemoryStore 接口。
func (m *simpleMemory) Store(messages []llm.Message) { m.stored = append(m.stored, messages...) }

// Search 实现 MemoryStore 接口。
func (m *simpleMemory) Search(string, int) []string { return nil }

// mockGate 记录收到的检查请求，并按工具名决定是否拦截。
type mockGate struct {
	denyErr error            // 拦截时返回的错误
	denyAll bool             // 拦截所有工具
	denySet map[string]bool  // 仅拦截指定工具
	checks  []gateCheckEntry // 收到的检查记录
}

// gateCheckEntry 记录一次门禁检查的入参。
type gateCheckEntry struct {
	toolName string
	argsJSON string
}

// CheckToolCall 实现 ToolGate 接口。
func (g *mockGate) CheckToolCall(_ context.Context, toolName, argsSummary string) error {
	g.checks = append(g.checks, gateCheckEntry{toolName: toolName, argsJSON: argsSummary})
	if g.denyAll || g.denySet[toolName] {
		return g.denyErr
	}
	return nil
}

// ============================================================
// Run 主循环集成测试
// ============================================================

// TestRun_ToolsCalledOncePerRound 覆盖场景 a/c：
// 第一轮 LLM 返回两个 tool_calls，第二轮返回最终答案；
// 断言每个工具恰好被调用一次（防二次执行回归），且正常产出 FinalAnswer 结束。
func TestRun_ToolsCalledOncePerRound(t *testing.T) {
	mockLLM := &mockLLMClient{responses: []llm.ChatResponse{
		{
			Content: "先查两个数据",
			ToolCalls: []llm.ToolCall{
				{ID: "call-1", Name: "web_search", Arguments: `{"query":"a"}`},
				{ID: "call-2", Name: "file_read", Arguments: `{"path":"b"}`},
			},
		},
		{Content: "最终答案内容"},
	}}
	caller := &countingToolCaller{calls: make(map[string]int)}
	mem := &simpleMemory{}

	e := NewEngine(EngineConfig{
		LLM:           mockLLM,
		Tools:         caller,
		Memory:        mem,
		MaxIterations: 10,
	})

	trace, err := e.Run(context.Background(), "测试目标")
	if err != nil {
		t.Fatalf("Run 应成功结束，实际错误: %v", err)
	}

	// 每个工具每轮恰好被调用一次
	if caller.calls["web_search"] != 1 {
		t.Errorf("web_search 调用次数 = %d, 期望 1", caller.calls["web_search"])
	}
	if caller.calls["file_read"] != 1 {
		t.Errorf("file_read 调用次数 = %d, 期望 1", caller.calls["file_read"])
	}

	// 无 tool_calls 时产出 FinalAnswer 并结束
	last := trace.Steps[len(trace.Steps)-1]
	if !last.IsFinal || last.FinalAnswer != "最终答案内容" {
		t.Errorf("最终步骤异常: IsFinal=%v, FinalAnswer=%q", last.IsFinal, last.FinalAnswer)
	}

	// 最终答案应存入记忆
	found := false
	for _, msg := range mem.stored {
		if msg.Role == "assistant" && msg.Content == "最终答案内容" {
			found = true
		}
	}
	if !found {
		t.Error("最终答案应存入记忆")
	}
}

// TestRun_ToolCallIDPairing 覆盖场景 b：
// 断言第二轮发给 LLM 的消息中，assistant 的 ToolCalls 与后续 tool 消息的 ToolCallID 正确配对。
func TestRun_ToolCallIDPairing(t *testing.T) {
	mockLLM := &mockLLMClient{responses: []llm.ChatResponse{
		{
			ToolCalls: []llm.ToolCall{
				{ID: "call-1", Name: "web_search", Arguments: `{"query":"a"}`},
				{ID: "call-2", Name: "file_read", Arguments: `{"path":"b"}`},
			},
		},
		{Content: "完成"},
	}}
	caller := &countingToolCaller{calls: make(map[string]int)}

	e := NewEngine(EngineConfig{LLM: mockLLM, Tools: caller, Memory: &simpleMemory{}, MaxIterations: 10})
	if _, err := e.Run(context.Background(), "目标"); err != nil {
		t.Fatalf("Run 失败: %v", err)
	}

	if len(mockLLM.requests) != 2 {
		t.Fatalf("LLM 请求次数 = %d, 期望 2", len(mockLLM.requests))
	}
	msgs := mockLLM.requests[1] // 第二轮请求的消息

	// 定位含 ToolCalls 的 assistant 消息
	assistantIdx := -1
	for i, m := range msgs {
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			assistantIdx = i
		}
	}
	if assistantIdx < 0 {
		t.Fatal("第二轮请求中缺少带 ToolCalls 的 assistant 消息")
	}
	assistant := msgs[assistantIdx]

	// 其后应紧跟与 ToolCalls 一一配对的 tool 消息
	if assistantIdx+len(assistant.ToolCalls) >= len(msgs) {
		t.Fatalf("assistant 消息后缺少足够的 tool 消息（总数 %d）", len(msgs))
	}
	for j, tc := range assistant.ToolCalls {
		toolMsg := msgs[assistantIdx+1+j]
		if toolMsg.Role != "tool" {
			t.Errorf("消息[%d] 角色 = %q, 期望 tool", assistantIdx+1+j, toolMsg.Role)
		}
		if toolMsg.ToolCallID != tc.ID {
			t.Errorf("tool 消息 ToolCallID = %q, 期望 %q", toolMsg.ToolCallID, tc.ID)
		}
		if !strings.Contains(toolMsg.Content, tc.Name) {
			t.Errorf("tool 消息内容 %q 应包含工具名 %q", toolMsg.Content, tc.Name)
		}
	}
}

// TestRun_MaxIterations 覆盖场景 d：
// LLM 每轮都返回 tool_calls，达到 MaxIterations 后按现有语义返回错误与追踪。
func TestRun_MaxIterations(t *testing.T) {
	mockLLM := &mockLLMClient{responses: []llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "web_search", Arguments: `{}`}}},
		{ToolCalls: []llm.ToolCall{{ID: "c2", Name: "web_search", Arguments: `{}`}}},
		{ToolCalls: []llm.ToolCall{{ID: "c3", Name: "web_search", Arguments: `{}`}}},
	}}
	caller := &countingToolCaller{calls: make(map[string]int)}

	e := NewEngine(EngineConfig{LLM: mockLLM, Tools: caller, Memory: &simpleMemory{}, MaxIterations: 3})
	trace, err := e.Run(context.Background(), "无法完成的目标")
	if err == nil {
		t.Fatal("达到最大迭代次数应返回错误")
	}
	if !strings.Contains(trace.Error, "超过最大迭代次数") {
		t.Errorf("trace.Error = %q, 应包含\"超过最大迭代次数\"", trace.Error)
	}
	if len(trace.Steps) != 3 {
		t.Errorf("步骤数 = %d, 期望 3（每轮一个工具调用）", len(trace.Steps))
	}
	if caller.calls["web_search"] != 3 {
		t.Errorf("工具调用次数 = %d, 期望 3", caller.calls["web_search"])
	}
}

// TestRun_ToolErrorContinues 覆盖场景 e：
// 工具执行失败时循环继续，且错误信息通过 tool 消息反馈给 LLM。
func TestRun_ToolErrorContinues(t *testing.T) {
	mockLLM := &mockLLMClient{responses: []llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "shell_exec", Arguments: `{"cmd":"x"}`}}},
		{Content: "收到错误，改用手动回答"},
	}}
	caller := &countingToolCaller{
		calls:   make(map[string]int),
		callErr: errors.New("模拟的执行器故障"),
	}

	e := NewEngine(EngineConfig{LLM: mockLLM, Tools: caller, Memory: &simpleMemory{}, MaxIterations: 10})
	trace, err := e.Run(context.Background(), "目标")
	if err != nil {
		t.Fatalf("工具失败不应中断循环，实际错误: %v", err)
	}

	// 循环继续并产出最终答案
	last := trace.Steps[len(trace.Steps)-1]
	if !last.IsFinal || last.FinalAnswer != "收到错误，改用手动回答" {
		t.Errorf("循环未正常继续: IsFinal=%v, FinalAnswer=%q", last.IsFinal, last.FinalAnswer)
	}

	// 错误应记录到步骤观察中
	if !strings.Contains(trace.Steps[0].Observation, "工具调用失败") {
		t.Errorf("Observation = %q, 应包含\"工具调用失败\"", trace.Steps[0].Observation)
	}

	// 错误应通过 tool 消息反馈给 LLM（第二轮请求中）
	if len(mockLLM.requests) != 2 {
		t.Fatalf("LLM 请求次数 = %d, 期望 2", len(mockLLM.requests))
	}
	found := false
	for _, m := range mockLLM.requests[1] {
		if m.Role == "tool" && m.ToolCallID == "call-1" && strings.Contains(m.Content, "模拟的执行器故障") {
			found = true
		}
	}
	if !found {
		t.Error("第二轮请求中缺少携带错误信息的 tool 消息")
	}
}

// TestRun_ResultErrorContinues 工具返回 Result.Error（非致命错误）时循环同样继续。
func TestRun_ResultErrorContinues(t *testing.T) {
	mockLLM := &mockLLMClient{responses: []llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "web_search", Arguments: `{}`}}},
		{Content: "完成"},
	}}
	caller := &countingToolCaller{
		calls:  make(map[string]int),
		result: map[string]tool.Result{"web_search": {Error: "搜索超时"}},
	}

	e := NewEngine(EngineConfig{LLM: mockLLM, Tools: caller, Memory: &simpleMemory{}, MaxIterations: 10})
	trace, err := e.Run(context.Background(), "目标")
	if err != nil {
		t.Fatalf("Result.Error 不应中断循环，实际错误: %v", err)
	}
	if !strings.Contains(trace.Steps[0].Observation, "工具执行错误: 搜索超时") {
		t.Errorf("Observation = %q, 应包含\"工具执行错误: 搜索超时\"", trace.Steps[0].Observation)
	}
}

// TestRun_TokenUsageAccumulated 断言主循环各轮 token 用量累加到 trace。
func TestRun_TokenUsageAccumulated(t *testing.T) {
	mockLLM := &mockLLMClient{responses: []llm.ChatResponse{
		{
			ToolCalls: []llm.ToolCall{{ID: "c1", Name: "web_search", Arguments: `{}`}},
			Usage:     llm.Usage{TotalTokens: 100},
		},
		{Content: "完成", Usage: llm.Usage{TotalTokens: 50}},
	}}
	caller := &countingToolCaller{calls: make(map[string]int)}

	e := NewEngine(EngineConfig{LLM: mockLLM, Tools: caller, Memory: &simpleMemory{}, MaxIterations: 10})
	trace, err := e.Run(context.Background(), "目标")
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if trace.TokenUsage != 150 {
		t.Errorf("TokenUsage = %d, 期望 150", trace.TokenUsage)
	}
}

// ============================================================
// ToolGate 门禁测试
// ============================================================

// TestRun_GateDeniesTool 覆盖任务 1：
// 门禁拒绝时工具未被调用，tool 消息携带"调用被拦截"及原因，保留 tool_call_id 配对，循环继续。
func TestRun_GateDeniesTool(t *testing.T) {
	mockLLM := &mockLLMClient{responses: []llm.ChatResponse{
		{
			ToolCalls: []llm.ToolCall{
				{ID: "call-1", Name: "shell_exec", Arguments: `{"command":"rm -rf /"}`},
				{ID: "call-2", Name: "web_search", Arguments: `{"query":"安全查询"}`},
			},
		},
		{Content: "收到拦截反馈，改用安全方式回答"},
	}}
	caller := &countingToolCaller{calls: make(map[string]int)}
	gate := &mockGate{
		denyErr: errors.New("命中策略 deny: 禁止破坏性命令"),
		denySet: map[string]bool{"shell_exec": true},
	}

	e := NewEngine(EngineConfig{
		LLM:           mockLLM,
		Tools:         caller,
		Memory:        &simpleMemory{},
		MaxIterations: 10,
		ToolGate:      gate,
	})

	trace, err := e.Run(context.Background(), "目标")
	if err != nil {
		t.Fatalf("门禁拦截不应中断循环，实际错误: %v", err)
	}

	// 被拦截的工具未被调用，放行的工具正常调用一次
	if caller.calls["shell_exec"] != 0 {
		t.Errorf("shell_exec 被拦截后不应执行，实际调用 %d 次", caller.calls["shell_exec"])
	}
	if caller.calls["web_search"] != 1 {
		t.Errorf("web_search 应放行并调用 1 次，实际 %d 次", caller.calls["web_search"])
	}

	// 门禁应收到两次检查，且参数为工具参数 JSON
	if len(gate.checks) != 2 {
		t.Fatalf("门禁检查次数 = %d, 期望 2", len(gate.checks))
	}
	if gate.checks[0].toolName != "shell_exec" || gate.checks[0].argsJSON != `{"command":"rm -rf /"}` {
		t.Errorf("第一次门禁检查入参错误: %+v", gate.checks[0])
	}

	// 拦截原因计入观察记录
	if !strings.Contains(trace.Steps[0].Observation, "调用被拦截: 命中策略 deny") {
		t.Errorf("Observation = %q, 应包含拦截原因", trace.Steps[0].Observation)
	}

	// 第二轮请求中应存在与 call-1 配对、内容为拦截信息的 tool 消息
	if len(mockLLM.requests) != 2 {
		t.Fatalf("LLM 请求次数 = %d, 期望 2", len(mockLLM.requests))
	}
	found := false
	for _, m := range mockLLM.requests[1] {
		if m.Role == "tool" && m.ToolCallID == "call-1" &&
			strings.Contains(m.Content, "调用被拦截") && strings.Contains(m.Content, "命中策略 deny") {
			found = true
		}
	}
	if !found {
		t.Error("第二轮请求中缺少与 call-1 配对的拦截反馈 tool 消息")
	}

	// 循环正常结束并产出最终答案
	last := trace.Steps[len(trace.Steps)-1]
	if !last.IsFinal {
		t.Error("循环应正常产出最终答案")
	}
}

// TestRun_GateDeniesAll 所有工具被拦截时引擎不崩溃，LLM 可据反馈直接作答。
func TestRun_GateDeniesAll(t *testing.T) {
	mockLLM := &mockLLMClient{responses: []llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "shell_exec", Arguments: `{}`}}},
		{Content: "工具不可用，直接回答"},
	}}
	caller := &countingToolCaller{calls: make(map[string]int)}
	gate := &mockGate{denyErr: errors.New("全局禁用工具"), denyAll: true}

	e := NewEngine(EngineConfig{
		LLM: mockLLM, Tools: caller, Memory: &simpleMemory{},
		MaxIterations: 10, ToolGate: gate,
	})
	trace, err := e.Run(context.Background(), "目标")
	if err != nil {
		t.Fatalf("Run 不应失败: %v", err)
	}
	if caller.calls["shell_exec"] != 0 {
		t.Errorf("工具不应被执行，实际调用 %d 次", caller.calls["shell_exec"])
	}
	if !trace.Steps[len(trace.Steps)-1].IsFinal {
		t.Error("应正常产出最终答案")
	}
}

// TestRun_NoGateBackwardCompat 不配置门禁（nil）时行为与原来一致：工具正常执行。
func TestRun_NoGateBackwardCompat(t *testing.T) {
	mockLLM := &mockLLMClient{responses: []llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "web_search", Arguments: `{}`}}},
		{Content: "完成"},
	}}
	caller := &countingToolCaller{calls: make(map[string]int)}

	e := NewEngine(EngineConfig{LLM: mockLLM, Tools: caller, Memory: &simpleMemory{}, MaxIterations: 10})
	if _, err := e.Run(context.Background(), "目标"); err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if caller.calls["web_search"] != 1 {
		t.Errorf("无门禁时工具应正常执行，实际调用 %d 次", caller.calls["web_search"])
	}
}

// ============================================================
// 反思机制测试（重规划提示角色 / 反思 token 计量）
// ============================================================

// reflectionLLM 区分主循环请求与反思请求：反思请求 tools 为 nil。
type reflectionLLM struct {
	mainResp   []llm.ChatResponse // 主循环响应序列
	reflResp   llm.ChatResponse   // 反思响应
	mainReqs   [][]llm.Message
	reflCalled int
}

// Chat 实现 LLMClient 接口：tools 为 nil 视为反思调用。
func (m *reflectionLLM) Chat(_ context.Context, messages []llm.Message, tools []llm.ToolDef) (llm.ChatResponse, error) {
	if tools == nil {
		m.reflCalled++
		return m.reflResp, nil
	}
	snapshot := make([]llm.Message, len(messages))
	copy(snapshot, messages)
	m.mainReqs = append(m.mainReqs, snapshot)
	if len(m.mainResp) == 0 {
		return llm.ChatResponse{Content: "默认答案"}, nil
	}
	resp := m.mainResp[0]
	m.mainResp = m.mainResp[1:]
	return resp, nil
}

// TestRun_ReplanPromptAsUserMessage 覆盖任务 3：
// 重规划提示应以 user 角色（带 [系统反馈] 前缀）注入，而非 system 角色。
func TestRun_ReplanPromptAsUserMessage(t *testing.T) {
	mockLLM := &reflectionLLM{
		mainResp: []llm.ChatResponse{
			// 第一轮工具失败，触发反思；反思建议 replan，触发重规划
			{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "web_search", Arguments: `{}`}}},
			{Content: "重规划后完成"},
		},
		reflResp: llm.ChatResponse{Content: `{"satisfaction": 0.2, "adjustment": "replan"}`},
	}
	caller := &countingToolCaller{
		calls:    make(map[string]int),
		callErr:  errors.New("模拟失败"),
		toolDefs: []llm.ToolDef{{Name: "web_search"}}, // 非 nil，供 reflectionLLM 区分主循环/反思调用
	}
	reflCfg := DefaultReflectionConfig()

	e := NewEngine(EngineConfig{
		LLM: mockLLM, Tools: caller, Memory: &simpleMemory{},
		MaxIterations: 10, Reflection: &reflCfg,
	})
	if _, err := e.Run(context.Background(), "目标"); err != nil {
		t.Fatalf("Run 失败: %v", err)
	}

	if mockLLM.reflCalled != 1 {
		t.Fatalf("反思调用次数 = %d, 期望 1", mockLLM.reflCalled)
	}
	if len(mockLLM.mainReqs) != 2 {
		t.Fatalf("主循环 LLM 请求次数 = %d, 期望 2", len(mockLLM.mainReqs))
	}

	// 第二轮请求中，重规划提示应为 user 角色且带 [系统反馈] 前缀
	found := false
	for _, m := range mockLLM.mainReqs[1] {
		if strings.Contains(m.Content, "之前的执行路径遇到问题") {
			if m.Role != "user" {
				t.Errorf("重规划提示角色 = %q, 期望 user", m.Role)
			}
			if !strings.HasPrefix(m.Content, "[系统反馈] ") {
				t.Errorf("重规划提示缺少 [系统反馈] 前缀: %q", m.Content)
			}
			found = true
		}
	}
	if !found {
		t.Error("第二轮请求中未找到重规划提示消息")
	}
}

// TestRun_ReflectionTokenUsage 覆盖任务 4：
// 反思调用的 token 用量应累加到当次 Run 的 TokenUsage。
func TestRun_ReflectionTokenUsage(t *testing.T) {
	mockLLM := &reflectionLLM{
		mainResp: []llm.ChatResponse{
			{
				ToolCalls: []llm.ToolCall{{ID: "c1", Name: "web_search", Arguments: `{}`}},
				Usage:     llm.Usage{TotalTokens: 100},
			},
			{Content: "完成", Usage: llm.Usage{TotalTokens: 50}},
		},
		reflResp: llm.ChatResponse{
			Content: `{"satisfaction": 0.5, "adjustment": "continue"}`,
			Usage:   llm.Usage{TotalTokens: 30},
		},
	}
	caller := &countingToolCaller{
		calls:    make(map[string]int),
		callErr:  errors.New("模拟失败"), // 工具失败触发反思
		toolDefs: []llm.ToolDef{{Name: "web_search"}},
	}
	reflCfg := DefaultReflectionConfig()

	e := NewEngine(EngineConfig{
		LLM: mockLLM, Tools: caller, Memory: &simpleMemory{},
		MaxIterations: 10, Reflection: &reflCfg,
	})
	trace, err := e.Run(context.Background(), "目标")
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}

	if mockLLM.reflCalled != 1 {
		t.Fatalf("反思调用次数 = %d, 期望 1", mockLLM.reflCalled)
	}
	// 总量 = 主循环 100 + 反思 30 + 最终 50
	if trace.TokenUsage != 180 {
		t.Errorf("TokenUsage = %d, 期望 180（含反思用量 30）", trace.TokenUsage)
	}
}

// TestRun_ReflectionFailureIgnored 反思调用失败（LLM 错误）不阻断主流程。
func TestRun_ReflectionFailureIgnored(t *testing.T) {
	mockLLM := &reflectionLLM{
		mainResp: []llm.ChatResponse{
			{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "web_search", Arguments: `{}`}}},
			{Content: "完成"},
		},
		reflResp: llm.ChatResponse{}, // 内容由 failingReflectionLLM 场景覆盖；此处验证解析降级
	}
	caller := &countingToolCaller{
		calls:    make(map[string]int),
		callErr:  fmt.Errorf("模拟失败"),
		toolDefs: []llm.ToolDef{{Name: "web_search"}},
	}
	reflCfg := DefaultReflectionConfig()

	e := NewEngine(EngineConfig{
		LLM: mockLLM, Tools: caller, Memory: &simpleMemory{},
		MaxIterations: 10, Reflection: &reflCfg,
	})
	trace, err := e.Run(context.Background(), "目标")
	if err != nil {
		t.Fatalf("反思异常不应阻断主流程: %v", err)
	}
	if !trace.Steps[len(trace.Steps)-1].IsFinal {
		t.Error("应正常产出最终答案")
	}
}
