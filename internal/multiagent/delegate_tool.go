package multiagent

import (
	"context"
	"fmt"

	"github.com/wenruigao/tommy-catty/internal/tool"
)

// DelegateTaskTool 实现 tool.Tool 接口，供主 Agent 将复杂任务
// 委派给 Orchestrator 进行多 Agent 协作执行。
type DelegateTaskTool struct {
	orchestrator *Orchestrator
}

// NewDelegateTaskTool 创建委派任务工具。
func NewDelegateTaskTool(orch *Orchestrator) *DelegateTaskTool {
	return &DelegateTaskTool{orchestrator: orch}
}

// Name 返回工具名称。
func (t *DelegateTaskTool) Name() string {
	return "delegate_task"
}

// Description 返回工具描述。
func (t *DelegateTaskTool) Description() string {
	return "将复杂任务分解并委派给多个专职 Agent 协作执行。适用于需要多种能力配合的任务（如调研+分析+写作）。" +
		"系统会自动分解任务、分配给合适的角色并行/串行执行，最终汇总结果。"
}

// Parameters 返回工具的 JSON Schema 参数定义。
func (t *DelegateTaskTool) Parameters() tool.JSONSchema {
	return tool.JSONSchema{
		Type: "object",
		Properties: map[string]tool.Property{
			"goal": {
				Type:        "string",
				Description: "要委派的完整任务描述（必须足够详细，包含所有必要的上下文信息）",
			},
		},
		Required: []string{"goal"},
	}
}

// Execute 执行委派任务。
func (t *DelegateTaskTool) Execute(ctx context.Context, args map[string]interface{}) (tool.Result, error) {
	goal, _ := args["goal"].(string)
	if goal == "" {
		return tool.Result{Error: "goal 参数不能为空"}, nil
	}

	result, err := t.orchestrator.Execute(ctx, goal)
	if err != nil {
		return tool.Result{Error: fmt.Sprintf("多 Agent 协作执行失败: %v", err)}, nil
	}

	// 构建输出：包含最终答案和执行摘要
	output := result.FinalAnswer
	if output == "" {
		output = "[编排器未生成最终答案]"
	}

	// 附加执行摘要
	summary := buildExecutionSummary(result)

	return tool.Result{
		Output: output + "\n\n---\n" + summary,
		Metadata: map[string]interface{}{
			"total_tokens":  result.TotalTokens,
			"duration_ms":   result.Duration.Milliseconds(),
			"subtask_count": len(result.Plan.SubTasks),
		},
	}, nil
}

// buildExecutionSummary 构建执行摘要（附加在输出末尾）。
func buildExecutionSummary(result *OrchestratorResult) string {
	if result == nil || result.Plan == nil {
		return ""
	}

	var successCount, failCount int
	for _, r := range result.Results {
		if r.Status == "success" {
			successCount++
		} else {
			failCount++
		}
	}

	return fmt.Sprintf(
		"[多 Agent 执行摘要] 策略: %s | 子任务: %d 个（成功 %d，失败 %d）| 总耗时: %s | Token: %d",
		result.Plan.Strategy,
		len(result.Plan.SubTasks),
		successCount,
		failCount,
		result.Duration.Round(100*1e6), // 精确到 100ms
		result.TotalTokens,
	)
}
