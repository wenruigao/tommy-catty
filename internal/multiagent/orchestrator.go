package multiagent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/tommy-cat/agent/internal/engine"
	"github.com/tommy-cat/agent/internal/llm"
	"github.com/tommy-cat/agent/internal/tool"
	"github.com/tommy-cat/agent/internal/trace"
)

// Orchestrator 编排多个 Worker Agent 协作完成复杂任务。
type Orchestrator struct {
	llm      engine.LLMClient
	roles    map[string]*RoleDef
	toolReg  *tool.Registry
	cfg      OrchestratorConfig
	toolGate engine.ToolGate // 继承主会话的安全门禁
	tracer   *trace.Tracer
}

// NewOrchestrator 创建编排器。
func NewOrchestrator(
	llmClient engine.LLMClient,
	roles map[string]*RoleDef,
	toolReg *tool.Registry,
	cfg OrchestratorConfig,
	toolGate engine.ToolGate,
	tracer *trace.Tracer,
) *Orchestrator {
	if cfg.MaxWorkers <= 0 {
		cfg.MaxWorkers = 5
	}
	if cfg.MaxSubTasks <= 0 {
		cfg.MaxSubTasks = 10
	}
	if cfg.WorkerTimeout <= 0 {
		cfg.WorkerTimeout = 120 * time.Second
	}
	return &Orchestrator{
		llm:      llmClient,
		roles:    roles,
		toolReg:  toolReg,
		cfg:      cfg,
		toolGate: toolGate,
		tracer:   tracer,
	}
}

// Execute 接收用户目标，分解任务并编排 Worker 执行，返回最终结果。
func (o *Orchestrator) Execute(ctx context.Context, goal string) (*OrchestratorResult, error) {
	start := time.Now()
	result := &OrchestratorResult{
		Results: make(map[string]*SubTaskResult),
	}

	// ★ Step 1: 任务分解
	plan, planTokens, err := o.decompose(ctx, goal)
	if err != nil {
		return nil, fmt.Errorf("任务分解失败: %w", err)
	}
	result.Plan = plan
	result.TotalTokens += planTokens

	// ★ Step 2: 调度执行
	bb := NewBlackboard()
	if err := o.schedule(ctx, plan, bb, result); err != nil {
		return nil, fmt.Errorf("任务调度失败: %w", err)
	}

	// ★ Step 3: 结果汇总
	summary, summaryTokens, err := o.summarize(ctx, goal, bb.AllResults())
	if err != nil {
		return nil, fmt.Errorf("结果汇总失败: %w", err)
	}
	result.FinalAnswer = summary
	result.TotalTokens += summaryTokens
	result.Duration = time.Since(start)

	return result, nil
}

// decompose 使用 LLM 将目标分解为执行计划。
func (o *Orchestrator) decompose(ctx context.Context, goal string) (*Plan, int, error) {
	prompt := buildPlanPrompt(o.roles, goal, o.cfg.MaxSubTasks)

	msgs := []llm.Message{
		{Role: "system", Content: "你是一个任务编排器，负责将复杂任务分解为可独立执行的子任务。只输出 JSON，不要输出其他内容。"},
		{Role: "user", Content: prompt},
	}

	resp, err := o.llm.Chat(ctx, msgs, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("LLM 任务分解调用失败: %w", err)
	}

	plan, err := parsePlan(resp.Content, goal)
	if err != nil {
		return nil, resp.Usage.TotalTokens, fmt.Errorf("执行计划解析失败: %w", err)
	}

	// 校验角色引用
	for _, st := range plan.SubTasks {
		if _, ok := o.roles[st.Role]; !ok {
			return nil, resp.Usage.TotalTokens, fmt.Errorf("子任务 %q 引用了不存在的角色 %q", st.ID, st.Role)
		}
	}

	return plan, resp.Usage.TotalTokens, nil
}

// schedule 按依赖关系调度 Worker 执行。
// 使用拓扑排序确定执行顺序，无依赖的任务并行执行。
// 并发受两层控制：全局 MaxWorkers 信号量 + 每角色 MaxConcurrent 计数。
func (o *Orchestrator) schedule(ctx context.Context, plan *Plan, bb *Blackboard, result *OrchestratorResult) error {
	// 构建依赖图
	dependents := make(map[string][]string) // taskID → 依赖它的任务列表
	inDegree := make(map[string]int)        // taskID → 入度
	taskMap := make(map[string]SubTask)

	for _, st := range plan.SubTasks {
		taskMap[st.ID] = st
		inDegree[st.ID] = len(st.DependsOn)
		for _, dep := range st.DependsOn {
			dependents[dep] = append(dependents[dep], st.ID)
		}
	}

	// 收集初始可执行任务（入度为 0）
	var ready []string
	for _, st := range plan.SubTasks {
		if inDegree[st.ID] == 0 {
			ready = append(ready, st.ID)
		}
	}

	// 并发控制
	sem := make(chan struct{}, o.cfg.MaxWorkers) // 全局并发上限
	activePerRole := make(map[string]int)        // 每角色当前活跃 Worker 数
	var wg sync.WaitGroup
	var mu sync.Mutex
	var schedErr error
	completed := make(map[string]bool)

	for len(ready) > 0 {
		// 取出当前所有就绪任务
		batch := ready
		ready = nil

		var deferred []string // 因角色并发限制暂不启动的任务

		for _, taskID := range batch {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			mu.Lock()
			if schedErr != nil {
				mu.Unlock()
				break
			}

			st := taskMap[taskID]
			roleLimit := o.roles[st.Role].EffectiveMaxConcurrent()

			// 每角色并发限制检查：超限则推迟到下一轮
			if activePerRole[st.Role] >= roleLimit {
				mu.Unlock()
				deferred = append(deferred, taskID)
				continue
			}
			activePerRole[st.Role]++
			mu.Unlock()

			sem <- struct{}{} // 获取全局信号量
			wg.Add(1)

			go func(st SubTask) {
				defer func() {
					<-sem
					mu.Lock()
					activePerRole[st.Role]--
					mu.Unlock()
					wg.Done()
				}()

				// 收集上游上下文
				upstreamCtx := bb.GatherContext(st.DependsOn)

				// 创建 Worker 并执行
				workerID := fmt.Sprintf("worker-%s-%s", st.Role, st.ID)
				worker, err := NewWorker(
					o.roles[st.Role], workerID, o.llm,
					o.toolReg, o.toolGate, o.tracer,
				)
				if err != nil {
					log.Printf("  ⚠️  multiagent: Worker %s 创建失败: %v", workerID, err)
					sr := &SubTaskResult{
						SubTaskID: st.ID,
						Role:      st.Role,
						Status:    "failed",
						Error:     err.Error(),
					}
					mu.Lock()
					bb.Put(st.ID, sr)
					result.Results[st.ID] = sr
					mu.Unlock()
					return
				}

				// 带超时的上下文
				workerCtx, cancel := context.WithTimeout(ctx, o.cfg.WorkerTimeout)
				defer cancel()

				sr := worker.Execute(workerCtx, st, upstreamCtx)

				// 超时检测
				if workerCtx.Err() == context.DeadlineExceeded && sr.Status != "failed" {
					sr.Status = "timeout"
					sr.Error = "Worker 执行超时"
				}

				mu.Lock()
				bb.Put(st.ID, sr)
				result.Results[st.ID] = sr
				completed[st.ID] = true
				mu.Unlock()

				// 检查是否有新的就绪任务
				mu.Lock()
				for _, depID := range dependents[st.ID] {
					inDegree[depID]--
					if inDegree[depID] == 0 {
						ready = append(ready, depID)
					}
				}
				mu.Unlock()
			}(st)
		}

		// 等待当前批次完成
		wg.Wait()

		// 被推迟的任务重新加入就绪队列
		ready = append(ready, deferred...)

		mu.Lock()
		if schedErr != nil {
			mu.Unlock()
			return schedErr
		}
		mu.Unlock()
	}

	return nil
}

// summarize 使用 LLM 汇总所有子任务结果。
func (o *Orchestrator) summarize(ctx context.Context, goal string, results map[string]*SubTaskResult) (string, int, error) {
	prompt := buildSummaryPrompt(goal, results)

	msgs := []llm.Message{
		{Role: "system", Content: "你是一个任务编排器，负责将多个子任务的结果整合为连贯的最终答案。"},
		{Role: "user", Content: prompt},
	}

	resp, err := o.llm.Chat(ctx, msgs, nil)
	if err != nil {
		return "", 0, fmt.Errorf("LLM 汇总调用失败: %w", err)
	}

	return resp.Content, resp.Usage.TotalTokens, nil
}

// parsePlan 解析 LLM 输出的执行计划 JSON。
func parsePlan(content string, goal string) (*Plan, error) {
	// 提取 JSON 块（LLM 可能在 JSON 前后添加说明文字）
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("未找到有效的 JSON 块")
	}
	jsonStr := content[start : end+1]

	var raw struct {
		Strategy string `json:"strategy"`
		SubTasks []struct {
			ID        string   `json:"id"`
			Role      string   `json:"role"`
			Goal      string   `json:"goal"`
			DependsOn []string `json:"depends_on"`
		} `json:"subtasks"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &raw); err != nil {
		return nil, fmt.Errorf("JSON 解析失败: %w", err)
	}

	if len(raw.SubTasks) == 0 {
		return nil, fmt.Errorf("执行计划中无子任务")
	}

	plan := &Plan{
		Goal:     goal,
		Strategy: raw.Strategy,
		SubTasks: make([]SubTask, 0, len(raw.SubTasks)),
	}

	ids := make(map[string]bool)
	for _, st := range raw.SubTasks {
		if st.ID == "" || st.Role == "" || st.Goal == "" {
			return nil, fmt.Errorf("子任务字段不完整（id/role/goal 均必填）")
		}
		if ids[st.ID] {
			return nil, fmt.Errorf("子任务 ID %q 重复", st.ID)
		}
		ids[st.ID] = true
		plan.SubTasks = append(plan.SubTasks, SubTask{
			ID:        st.ID,
			Role:      st.Role,
			Goal:      st.Goal,
			DependsOn: st.DependsOn,
		})
	}

	// 校验依赖引用
	for _, st := range plan.SubTasks {
		for _, dep := range st.DependsOn {
			if !ids[dep] {
				return nil, fmt.Errorf("子任务 %q 依赖了不存在的任务 %q", st.ID, dep)
			}
		}
	}

	// 校验无环（拓扑排序检测）
	if err := detectCycle(plan.SubTasks); err != nil {
		return nil, err
	}

	if plan.Strategy == "" {
		plan.Strategy = "mixed"
	}

	return plan, nil
}

// detectCycle 检测子任务依赖图中是否存在环。
func detectCycle(tasks []SubTask) error {
	inDegree := make(map[string]int)
	dependents := make(map[string][]string)
	for _, t := range tasks {
		inDegree[t.ID] = len(t.DependsOn)
		for _, dep := range t.DependsOn {
			dependents[dep] = append(dependents[dep], t.ID)
		}
	}

	// Kahn 算法
	var queue []string
	for _, t := range tasks {
		if inDegree[t.ID] == 0 {
			queue = append(queue, t.ID)
		}
	}

	visited := 0
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		visited++
		for _, dep := range dependents[node] {
			inDegree[dep]--
			if inDegree[dep] == 0 {
				queue = append(queue, dep)
			}
		}
	}

	if visited != len(tasks) {
		return fmt.Errorf("子任务依赖图中存在环")
	}
	return nil
}
