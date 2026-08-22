package multiagent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/tommy-cat/agent/internal/engine"
	"github.com/tommy-cat/agent/internal/llm"
	"github.com/tommy-cat/agent/internal/tool"
	"github.com/tommy-cat/agent/internal/trace"
)

// Worker 是一个受限的 Agent 实例，拥有特定角色和工具子集。
// 内部复用 engine.Engine 驱动 ReAct 循环。
type Worker struct {
	role   *RoleDef
	id     string // 实例标识（用于追踪）
	llm    engine.LLMClient
	tools  engine.ToolCaller // 受限工具子集
	toolGate engine.ToolGate   // 继承主会话的安全门禁
	tracer *trace.Tracer
}

// NewWorker 创建一个 Worker 实例。
// globalReg 为全局工具注册表，Worker 从中按角色白名单提取子集。
func NewWorker(role *RoleDef, id string, llmClient engine.LLMClient,
	globalReg *tool.Registry, toolGate engine.ToolGate, tracer *trace.Tracer) (*Worker, error) {

	subReg, err := buildToolSubset(globalReg, role.Tools)
	if err != nil {
		return nil, fmt.Errorf("角色 %q 工具子集构建失败: %w", role.Name, err)
	}

	return &Worker{
		role:     role,
		id:       id,
		llm:      llmClient,
		tools:    subReg,
		toolGate: toolGate,
		tracer:   tracer,
	}, nil
}

// Execute 执行子任务，返回结果。
func (w *Worker) Execute(ctx context.Context, subTask SubTask, upstreamContext string) *SubTaskResult {
	start := time.Now()

	// 构建 Worker 的 Engine
	mem := &workerMemory{messages: make([]llm.Message, 0, 16)}
	eng := engine.NewEngine(engine.EngineConfig{
		LLM:           w.llm,
		Tools:         w.tools,
		Memory:        mem,
		MaxIterations: w.role.EffectiveMaxIterations(),
		SystemPrompt:  w.role.SystemPrompt,
		ToolGate:      w.toolGate,
		Tracer:        w.tracer,
	})

	// 将上游上下文拼接到目标中
	goal := subTask.Goal
	if upstreamContext != "" {
		goal = upstreamContext + "\n\n你的任务：" + subTask.Goal
	}

	// 执行 ReAct 循环
	traceResult, err := eng.Run(ctx, goal)

	result := &SubTaskResult{
		SubTaskID: subTask.ID,
		Role:      w.role.Name,
		Duration:  time.Since(start),
	}

	if traceResult != nil {
		result.TokenUsage = traceResult.TokenUsage
		result.Steps = len(traceResult.Steps)
		// 提取最终答案
		for i := len(traceResult.Steps) - 1; i >= 0; i-- {
			if traceResult.Steps[i].IsFinal {
				result.Output = traceResult.Steps[i].FinalAnswer
				break
			}
		}
	}

	if err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		// 如果有部分输出但执行失败，保留输出供汇总参考
		if result.Output == "" && traceResult != nil && traceResult.Error != "" {
			result.Output = fmt.Sprintf("[执行失败] %s", traceResult.Error)
		}
	} else {
		result.Status = "success"
	}

	return result
}

// Role 返回 Worker 的角色定义。
func (w *Worker) Role() *RoleDef {
	return w.role
}

// buildToolSubset 从全局注册表中提取指定工具的子集注册表。
func buildToolSubset(globalReg *tool.Registry, toolNames []string) (*tool.Registry, error) {
	sub := tool.NewRegistry()
	for _, name := range toolNames {
		meta, ok := globalReg.Get(name)
		if !ok {
			return nil, fmt.Errorf("工具 %q 在全局注册表中不存在", name)
		}
		sub.Register(meta.Tool, meta.RiskLevel, meta.Timeout)
	}
	return sub, nil
}

// workerMemory 是 Worker 使用的轻量级内存存储，
// 实现 engine.MemoryStore 接口。Worker 无需跨任务记忆，
// 仅在单次执行内维护对话上下文。
type workerMemory struct {
	mu       sync.Mutex
	messages []llm.Message
}

func (m *workerMemory) GetContext(limit int) []llm.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit <= 0 || limit > len(m.messages) {
		limit = len(m.messages)
	}
	cp := make([]llm.Message, limit)
	copy(cp, m.messages[len(m.messages)-limit:])
	return cp
}

func (m *workerMemory) Store(messages []llm.Message) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = append(m.messages, messages...)
}

func (m *workerMemory) Search(query string, topK int) []string {
	return nil // Worker 不支持语义搜索
}
