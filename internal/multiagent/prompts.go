package multiagent

import (
	"fmt"
	"strings"
)

// planPromptTemplate 任务分解 Prompt 模板。
const planPromptTemplate = `你是一个任务编排器。请将以下复杂任务分解为可独立执行的子任务。

可用角色：
{{ROLES}}

原始任务：{{GOAL}}

请输出 JSON 格式的执行计划：
{
  "strategy": "parallel|sequential|mixed",
  "subtasks": [
    {
      "id": "t1",
      "role": "角色名",
      "goal": "子任务目标描述（要足够详细，包含必要的上下文和具体要求）",
      "depends_on": []
    }
  ]
}

规则：
- 每个子任务必须指定一个可用角色
- 有数据依赖的子任务必须声明 depends_on（依赖的子任务 ID 列表）
- 无依赖的子任务可以并行执行
- 子任务数量不超过 {{MAX_SUBTASKS}} 个
- 每个子任务的 goal 必须自包含，不依赖其他子任务的执行过程（仅依赖其结果）
- 只输出 JSON，不要输出其他内容`

// summaryPromptTemplate 结果汇总 Prompt 模板。
const summaryPromptTemplate = `你是一个任务编排器。以下是各专职 Agent 完成子任务的结果。
请基于这些结果，生成一份完整的最终答案。

原始目标：{{GOAL}}

子任务结果：
{{RESULTS}}

请整合以上结果，生成连贯、完整的最终答案。
要求：
- 保留关键数据和引用来源
- 如果某些子任务失败，说明哪些部分信息可能不完整
- 直接输出最终答案，不要输出"根据子任务结果"等编排相关描述`

// buildPlanPrompt 构建任务分解 Prompt。
func buildPlanPrompt(roles map[string]*RoleDef, goal string, maxSubTasks int) string {
	var roleLines strings.Builder
	for name, role := range roles {
		tools := strings.Join(role.Tools, ", ")
		roleLines.WriteString(fmt.Sprintf("- %s: %s（可用工具: %s）\n", name, role.Description, tools))
	}

	prompt := strings.ReplaceAll(planPromptTemplate, "{{ROLES}}", roleLines.String())
	prompt = strings.ReplaceAll(prompt, "{{GOAL}}", goal)
	prompt = strings.ReplaceAll(prompt, "{{MAX_SUBTASKS}}", fmt.Sprintf("%d", maxSubTasks))
	return prompt
}

// buildSummaryPrompt 构建结果汇总 Prompt。
func buildSummaryPrompt(goal string, results map[string]*SubTaskResult) string {
	var sb strings.Builder
	for id, r := range results {
		sb.WriteString(fmt.Sprintf("### %s (%s)\n状态: %s\n", id, r.Role, r.Status))
		if r.Error != "" {
			sb.WriteString(fmt.Sprintf("错误: %s\n", r.Error))
		}
		if r.Output != "" {
			output := r.Output
			if len(output) > 3000 {
				output = output[:3000] + "\n...（已截断）"
			}
			sb.WriteString(fmt.Sprintf("结果:\n%s\n", output))
		}
		sb.WriteString("\n")
	}

	prompt := strings.ReplaceAll(summaryPromptTemplate, "{{GOAL}}", goal)
	prompt = strings.ReplaceAll(prompt, "{{RESULTS}}", sb.String())
	return prompt
}
