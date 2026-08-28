package multiagent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tommy-cat/agent/internal/llm"
	"github.com/tommy-cat/agent/internal/tool"
)

// ============================================================
// 测试替身
// ============================================================

// mockLLMClient 按预设序列返回响应。
type mockLLMClient struct {
	responses []llm.ChatResponse
	requests  [][]llm.Message
}

func (m *mockLLMClient) Chat(_ context.Context, messages []llm.Message, _ []llm.ToolDef) (llm.ChatResponse, error) {
	snapshot := make([]llm.Message, len(messages))
	copy(snapshot, messages)
	m.requests = append(m.requests, snapshot)

	if len(m.responses) == 0 {
		return llm.ChatResponse{Content: "默认答案"}, nil
	}
	resp := m.responses[0]
	m.responses = m.responses[1:]
	return resp, nil
}

// dummyTool 用于测试的简单工具实现。
type dummyTool struct{ name string }

func (d *dummyTool) Name() string                { return d.name }
func (d *dummyTool) Description() string         { return "测试工具 " + d.name }
func (d *dummyTool) Parameters() tool.JSONSchema { return tool.JSONSchema{Type: "object"} }
func (d *dummyTool) Execute(_ context.Context, _ map[string]interface{}) (tool.Result, error) {
	return tool.Result{Output: "ok"}, nil
}

// newTestRegistry 创建包含指定工具名的测试注册表。
func newTestRegistry(names ...string) *tool.Registry {
	reg := tool.NewRegistry()
	for _, n := range names {
		reg.Register(&dummyTool{name: n}, tool.RiskReadOnly, 10*time.Second)
	}
	return reg
}

// testRole 快速创建测试用角色。
func testRole(name string, tools ...string) *RoleDef {
	return &RoleDef{
		Name:         name,
		Description:  "测试角色 " + name,
		SystemPrompt: "你是" + name,
		Tools:        tools,
	}
}

// ============================================================
// RoleDef 测试
// ============================================================

func TestRoleDef_Validate(t *testing.T) {
	tests := []struct {
		name    string
		role    RoleDef
		wantErr bool
	}{
		{
			name:    "合法角色",
			role:    RoleDef{Name: "r1", Description: "desc", SystemPrompt: "prompt", Tools: []string{"t1"}},
			wantErr: false,
		},
		{
			name:    "空名称",
			role:    RoleDef{Description: "desc", SystemPrompt: "prompt", Tools: []string{"t1"}},
			wantErr: true,
		},
		{
			name:    "空描述",
			role:    RoleDef{Name: "r1", SystemPrompt: "prompt", Tools: []string{"t1"}},
			wantErr: true,
		},
		{
			name:    "空提示词",
			role:    RoleDef{Name: "r1", Description: "desc", Tools: []string{"t1"}},
			wantErr: true,
		},
		{
			name:    "空工具集",
			role:    RoleDef{Name: "r1", Description: "desc", SystemPrompt: "prompt"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.role.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateRoles(t *testing.T) {
	t.Run("空角色集", func(t *testing.T) {
		if err := ValidateRoles(nil); err == nil {
			t.Error("期望错误，得到 nil")
		}
	})

	t.Run("名称不一致", func(t *testing.T) {
		roles := map[string]*RoleDef{
			"key1": {Name: "other", Description: "d", SystemPrompt: "p", Tools: []string{"t"}},
		}
		if err := ValidateRoles(roles); err == nil {
			t.Error("期望键名与 Name 不一致的错误")
		}
	})

	t.Run("合法角色集", func(t *testing.T) {
		roles := map[string]*RoleDef{
			"r1": testRole("r1", "t1"),
			"r2": testRole("r2", "t2"),
		}
		if err := ValidateRoles(roles); err != nil {
			t.Errorf("不应报错: %v", err)
		}
	})
}

func TestRoleDef_EffectiveValues(t *testing.T) {
	r := &RoleDef{Name: "r", Description: "d", SystemPrompt: "p", Tools: []string{"t"}}
	if got := r.EffectiveMaxIterations(); got != DefaultWorkerIterations {
		t.Errorf("默认迭代次数 = %d, 期望 %d", got, DefaultWorkerIterations)
	}
	if got := r.EffectiveMaxConcurrent(); got != DefaultMaxConcurrent {
		t.Errorf("默认并发数 = %d, 期望 %d", got, DefaultMaxConcurrent)
	}

	r.MaxIterations = 30
	r.MaxConcurrent = 5
	if got := r.EffectiveMaxIterations(); got != 30 {
		t.Errorf("自定义迭代次数 = %d, 期望 30", got)
	}
	if got := r.EffectiveMaxConcurrent(); got != 5 {
		t.Errorf("自定义并发数 = %d, 期望 5", got)
	}
}

// ============================================================
// Blackboard 测试
// ============================================================

func TestBlackboard_PutGet(t *testing.T) {
	bb := NewBlackboard()

	r := &SubTaskResult{SubTaskID: "t1", Role: "r1", Status: "success", Output: "hello"}
	bb.Put("t1", r)

	got, ok := bb.Get("t1")
	if !ok || got.Output != "hello" {
		t.Errorf("Get(t1) = %v, %v; 期望 hello, true", got, ok)
	}

	_, ok = bb.Get("nonexistent")
	if ok {
		t.Error("Get(nonexistent) 应返回 false")
	}
}

func TestBlackboard_GatherContext(t *testing.T) {
	bb := NewBlackboard()
	bb.Put("t1", &SubTaskResult{SubTaskID: "t1", Role: "r1", Status: "success", Output: "结果1"})
	bb.Put("t2", &SubTaskResult{SubTaskID: "t2", Role: "r2", Status: "failed", Error: "出错"})

	ctx := bb.GatherContext([]string{"t1", "t2", "t3"})
	if !strings.Contains(ctx, "结果1") {
		t.Error("应包含 t1 的结果")
	}
	if !strings.Contains(ctx, "出错") {
		t.Error("应包含 t2 的错误")
	}
	if !strings.Contains(ctx, "未完成") {
		t.Error("应标注 t3 为未完成")
	}

	// 空依赖返回空
	if got := bb.GatherContext(nil); got != "" {
		t.Errorf("空依赖应返回空串，得到 %q", got)
	}
}

func TestBlackboard_ConcurrentAccess(t *testing.T) {
	bb := NewBlackboard()
	var wg sync.WaitGroup

	// 并发写入不同 key
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			bb.Put(fmt.Sprintf("t%d", id), &SubTaskResult{
				SubTaskID: fmt.Sprintf("t%d", id),
				Status:    "success",
			})
		}(i)
	}
	wg.Wait()

	all := bb.AllResults()
	if len(all) != 100 {
		t.Errorf("结果数 = %d, 期望 100", len(all))
	}
}

// ============================================================
// Plan 解析测试
// ============================================================

func TestParsePlan_Valid(t *testing.T) {
	input := `{
		"strategy": "mixed",
		"subtasks": [
			{"id": "t1", "role": "researcher", "goal": "搜索信息", "depends_on": []},
			{"id": "t2", "role": "writer", "goal": "撰写报告", "depends_on": ["t1"]}
		]
	}`

	plan, err := parsePlan(input, "测试目标")
	if err != nil {
		t.Fatalf("parsePlan 失败: %v", err)
	}
	if plan.Strategy != "mixed" {
		t.Errorf("Strategy = %q, 期望 mixed", plan.Strategy)
	}
	if len(plan.SubTasks) != 2 {
		t.Fatalf("SubTasks 数 = %d, 期望 2", len(plan.SubTasks))
	}
	if plan.SubTasks[1].DependsOn[0] != "t1" {
		t.Error("t2 应依赖 t1")
	}
}

func TestParsePlan_WithSurroundingText(t *testing.T) {
	input := `好的，这是执行计划：
{"strategy":"parallel","subtasks":[{"id":"t1","role":"r","goal":"g","depends_on":[]}]}
希望这能帮到你。`

	plan, err := parsePlan(input, "目标")
	if err != nil {
		t.Fatalf("parsePlan 失败: %v", err)
	}
	if len(plan.SubTasks) != 1 {
		t.Errorf("SubTasks 数 = %d, 期望 1", len(plan.SubTasks))
	}
}

func TestParsePlan_Errors(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"无 JSON", "这不是 JSON"},
		{"空子任务", `{"strategy":"p","subtasks":[]}`},
		{"重复 ID", `{"subtasks":[{"id":"t1","role":"r","goal":"g"},{"id":"t1","role":"r","goal":"g"}]}`},
		{"依赖不存在", `{"subtasks":[{"id":"t1","role":"r","goal":"g","depends_on":["t99"]}]}`},
		{"字段缺失", `{"subtasks":[{"id":"t1","role":"r"}]}`},
		{"依赖成环", `{"subtasks":[{"id":"t1","role":"r","goal":"g","depends_on":["t2"]},{"id":"t2","role":"r","goal":"g","depends_on":["t1"]}]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parsePlan(tt.input, "目标")
			if err == nil {
				t.Error("期望错误，得到 nil")
			}
		})
	}
}

func TestParsePlan_DefaultStrategy(t *testing.T) {
	input := `{"subtasks":[{"id":"t1","role":"r","goal":"g"}]}`
	plan, err := parsePlan(input, "目标")
	if err != nil {
		t.Fatalf("parsePlan 失败: %v", err)
	}
	if plan.Strategy != "mixed" {
		t.Errorf("默认 Strategy = %q, 期望 mixed", plan.Strategy)
	}
}

// ============================================================
// detectCycle 测试
// ============================================================

func TestDetectCycle(t *testing.T) {
	t.Run("无环", func(t *testing.T) {
		tasks := []SubTask{
			{ID: "t1"},
			{ID: "t2", DependsOn: []string{"t1"}},
			{ID: "t3", DependsOn: []string{"t1"}},
		}
		if err := detectCycle(tasks); err != nil {
			t.Errorf("不应检测到环: %v", err)
		}
	})

	t.Run("有环", func(t *testing.T) {
		tasks := []SubTask{
			{ID: "t1", DependsOn: []string{"t3"}},
			{ID: "t2", DependsOn: []string{"t1"}},
			{ID: "t3", DependsOn: []string{"t2"}},
		}
		if err := detectCycle(tasks); err == nil {
			t.Error("应检测到环")
		}
	})
}

// ============================================================
// buildToolSubset 测试
// ============================================================

func TestBuildToolSubset(t *testing.T) {
	reg := newTestRegistry("web_search", "web_fetch", "shell_exec", "code_run")

	t.Run("正常子集", func(t *testing.T) {
		sub, err := buildToolSubset(reg, []string{"web_search", "web_fetch"})
		if err != nil {
			t.Fatalf("buildToolSubset 失败: %v", err)
		}
		defs := sub.ToToolDefs()
		if len(defs) != 2 {
			t.Errorf("工具数 = %d, 期望 2", len(defs))
		}
	})

	t.Run("不存在的工具", func(t *testing.T) {
		_, err := buildToolSubset(reg, []string{"nonexistent"})
		if err == nil {
			t.Error("期望错误，得到 nil")
		}
	})
}

// ============================================================
// DelegateTaskTool 测试
// ============================================================

func TestDelegateTaskTool_Metadata(t *testing.T) {
	orch := NewOrchestrator(&mockLLMClient{}, nil, tool.NewRegistry(), DefaultOrchestratorConfig(), nil, nil)
	dt := NewDelegateTaskTool(orch)

	if dt.Name() != "delegate_task" {
		t.Errorf("Name() = %q, 期望 delegate_task", dt.Name())
	}
	if dt.Description() == "" {
		t.Error("Description() 不应为空")
	}
	schema := dt.Parameters()
	if _, ok := schema.Properties["goal"]; !ok {
		t.Error("Parameters 应包含 goal 属性")
	}
}

func TestDelegateTaskTool_EmptyGoal(t *testing.T) {
	orch := NewOrchestrator(&mockLLMClient{}, nil, tool.NewRegistry(), DefaultOrchestratorConfig(), nil, nil)
	dt := NewDelegateTaskTool(orch)

	result, err := dt.Execute(context.Background(), map[string]interface{}{})
	if err != nil {
		t.Fatalf("不应返回 error: %v", err)
	}
	if result.Error == "" {
		t.Error("空 goal 应返回错误")
	}
}

// ============================================================
// Orchestrator 端到端测试
// ============================================================

func TestOrchestrator_Execute(t *testing.T) {
	// Mock LLM：第一次返回任务分解 JSON，第二次返回汇总结果
	planJSON := `{
		"strategy": "parallel",
		"subtasks": [
			{"id": "t1", "role": "researcher", "goal": "搜索 AI 最新动态", "depends_on": []},
			{"id": "t2", "role": "writer", "goal": "撰写摘要", "depends_on": ["t1"]}
		]
	}`
	mockLLM := &mockLLMClient{
		responses: []llm.ChatResponse{
			{Content: planJSON, Usage: llm.Usage{TotalTokens: 100}},
			// Worker t1 的 LLM 调用（最终答案）
			{Content: "AI 最新动态：大模型持续发展。", Usage: llm.Usage{TotalTokens: 50}},
			// Worker t2 的 LLM 调用（最终答案）
			{Content: "摘要：AI 领域持续进步。", Usage: llm.Usage{TotalTokens: 30}},
			// 汇总 LLM 调用
			{Content: "最终报告：AI 领域在 2025 年持续发展，大模型能力不断提升。", Usage: llm.Usage{TotalTokens: 80}},
		},
	}

	roles := map[string]*RoleDef{
		"researcher": testRole("researcher", "web_search"),
		"writer":     testRole("writer", "file_write"),
	}
	reg := newTestRegistry("web_search", "file_write")

	orch := NewOrchestrator(mockLLM, roles, reg, DefaultOrchestratorConfig(), nil, nil)
	result, err := orch.Execute(context.Background(), "调研 AI 动态并撰写报告")
	if err != nil {
		t.Fatalf("Execute 失败: %v", err)
	}

	if result.FinalAnswer == "" {
		t.Error("FinalAnswer 不应为空")
	}
	if result.Plan == nil || len(result.Plan.SubTasks) != 2 {
		t.Error("Plan 应包含 2 个子任务")
	}
	if result.TotalTokens <= 0 {
		t.Error("TotalTokens 应大于 0")
	}
}

func TestOrchestrator_DecomposeError(t *testing.T) {
	// LLM 返回无效 JSON
	mockLLM := &mockLLMClient{
		responses: []llm.ChatResponse{
			{Content: "这不是有效的 JSON"},
		},
	}
	roles := map[string]*RoleDef{"r": testRole("r", "t")}
	reg := newTestRegistry("t")

	orch := NewOrchestrator(mockLLM, roles, reg, DefaultOrchestratorConfig(), nil, nil)
	_, err := orch.Execute(context.Background(), "测试")
	if err == nil {
		t.Error("期望分解失败")
	}
}

func TestOrchestrator_InvalidRoleReference(t *testing.T) {
	// LLM 返回引用不存在角色的计划
	planJSON := `{"subtasks":[{"id":"t1","role":"nonexistent","goal":"g"}]}`
	mockLLM := &mockLLMClient{
		responses: []llm.ChatResponse{
			{Content: planJSON},
		},
	}
	roles := map[string]*RoleDef{"r": testRole("r", "t")}
	reg := newTestRegistry("t")

	orch := NewOrchestrator(mockLLM, roles, reg, DefaultOrchestratorConfig(), nil, nil)
	_, err := orch.Execute(context.Background(), "测试")
	if err == nil {
		t.Error("期望角色引用错误")
	}
}

// ============================================================
// Prompt 构建测试
// ============================================================

func TestBuildPlanPrompt(t *testing.T) {
	roles := map[string]*RoleDef{
		"researcher": testRole("researcher", "web_search", "web_fetch"),
	}
	prompt := buildPlanPrompt(roles, "调研 AI", 5)

	if !strings.Contains(prompt, "researcher") {
		t.Error("Prompt 应包含角色名")
	}
	if !strings.Contains(prompt, "调研 AI") {
		t.Error("Prompt 应包含目标")
	}
	if !strings.Contains(prompt, "5") {
		t.Error("Prompt 应包含最大子任务数")
	}
}

func TestBuildSummaryPrompt(t *testing.T) {
	results := map[string]*SubTaskResult{
		"t1": {SubTaskID: "t1", Role: "r1", Status: "success", Output: "研究结果"},
		"t2": {SubTaskID: "t2", Role: "r2", Status: "failed", Error: "超时"},
	}
	prompt := buildSummaryPrompt("原始目标", results)

	if !strings.Contains(prompt, "原始目标") {
		t.Error("Prompt 应包含原始目标")
	}
	if !strings.Contains(prompt, "研究结果") {
		t.Error("Prompt 应包含成功结果")
	}
	if !strings.Contains(prompt, "超时") {
		t.Error("Prompt 应包含失败信息")
	}
}

// ============================================================
// buildExecutionSummary 测试
// ============================================================

func TestBuildExecutionSummary(t *testing.T) {
	result := &OrchestratorResult{
		Plan: &Plan{
			Strategy: "mixed",
			SubTasks: []SubTask{{ID: "t1"}, {ID: "t2"}},
		},
		Results: map[string]*SubTaskResult{
			"t1": {Status: "success"},
			"t2": {Status: "failed"},
		},
		TotalTokens: 500,
		Duration:    3 * time.Second,
	}

	summary := buildExecutionSummary(result)
	if !strings.Contains(summary, "mixed") {
		t.Error("摘要应包含策略")
	}
	if !strings.Contains(summary, "成功 1") {
		t.Error("摘要应包含成功数")
	}
	if !strings.Contains(summary, "失败 1") {
		t.Error("摘要应包含失败数")
	}

	// nil 安全
	if got := buildExecutionSummary(nil); got != "" {
		t.Errorf("nil 输入应返回空串，得到 %q", got)
	}
}

// ============================================================
// Worker 测试
// ============================================================

func TestWorker_Execute(t *testing.T) {
	mockLLM := &mockLLMClient{
		responses: []llm.ChatResponse{
			{Content: "研究完成", Usage: llm.Usage{TotalTokens: 50}},
		},
	}

	reg := newTestRegistry("web_search")
	role := testRole("researcher", "web_search")

	worker, err := NewWorker(role, "w1", mockLLM, reg, nil, nil)
	if err != nil {
		t.Fatalf("NewWorker 失败: %v", err)
	}

	result := worker.Execute(context.Background(), SubTask{
		ID:   "t1",
		Role: "researcher",
		Goal: "搜索信息",
	}, "")

	if result.Status != "success" {
		t.Errorf("Status = %q, 期望 success", result.Status)
	}
	if result.SubTaskID != "t1" {
		t.Errorf("SubTaskID = %q, 期望 t1", result.SubTaskID)
	}
	if result.Role != "researcher" {
		t.Errorf("Role = %q, 期望 researcher", result.Role)
	}
}

func TestWorker_InvalidToolSubset(t *testing.T) {
	reg := newTestRegistry("web_search")
	role := testRole("bad", "nonexistent_tool")

	_, err := NewWorker(role, "w1", &mockLLMClient{}, reg, nil, nil)
	if err == nil {
		t.Error("期望工具子集构建失败")
	}
}

func TestWorker_WithUpstreamContext(t *testing.T) {
	mockLLM := &mockLLMClient{
		responses: []llm.ChatResponse{
			{Content: "基于上游结果完成", Usage: llm.Usage{TotalTokens: 30}},
		},
	}

	reg := newTestRegistry("web_search")
	role := testRole("writer", "web_search")
	worker, _ := NewWorker(role, "w1", mockLLM, reg, nil, nil)

	result := worker.Execute(context.Background(), SubTask{
		ID:   "t2",
		Goal: "撰写报告",
	}, "上游结果：AI 发展迅速。")

	if result.Status != "success" {
		t.Errorf("Status = %q, 期望 success", result.Status)
	}

	// 验证上游上下文被注入到 LLM 请求中
	if len(mockLLM.requests) == 0 {
		t.Fatal("应有 LLM 请求")
	}
	lastReq := mockLLM.requests[len(mockLLM.requests)-1]
	found := false
	for _, msg := range lastReq {
		if msg.Role == "user" && strings.Contains(msg.Content, "上游结果") {
			found = true
			break
		}
	}
	if !found {
		t.Error("LLM 请求应包含上游上下文")
	}
}

// ============================================================
// DefaultOrchestratorConfig 测试
// ============================================================

func TestDefaultOrchestratorConfig(t *testing.T) {
	cfg := DefaultOrchestratorConfig()
	if cfg.MaxWorkers != 5 {
		t.Errorf("MaxWorkers = %d, 期望 5", cfg.MaxWorkers)
	}
	if cfg.MaxSubTasks != 10 {
		t.Errorf("MaxSubTasks = %d, 期望 10", cfg.MaxSubTasks)
	}
	if cfg.WorkerTimeout != 120*time.Second {
		t.Errorf("WorkerTimeout = %v, 期望 120s", cfg.WorkerTimeout)
	}
}

// ============================================================
// JSON 序列化测试（确保结构体可正确序列化）
// ============================================================

func TestSubTaskResult_JSON(t *testing.T) {
	r := &SubTaskResult{
		SubTaskID:  "t1",
		Role:       "researcher",
		Status:     "success",
		Output:     "结果",
		TokenUsage: 100,
		Duration:   2 * time.Second,
		Steps:      3,
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("JSON 序列化失败: %v", err)
	}
	if !strings.Contains(string(data), "researcher") {
		t.Error("JSON 应包含角色名")
	}
}
