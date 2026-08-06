// Package skill 提供引擎执行追踪与 Skill 生成数据格式之间的转换。
package skill

import (
	"encoding/json"
	"strings"

	"github.com/tommy-cat/agent/internal/engine"
)

// traceDataView 与 traceData 结构对应，用于把引擎执行追踪
// 序列化为生成器可解析的 JSON 格式。
type traceDataView struct {
	TraceID     string          `json:"trace_id"`
	TaskSummary string          `json:"task_summary"`
	Steps       []traceStepView `json:"steps"`
	Tools       []string        `json:"tools"`
}

// traceStepView 与 traceStep 结构对应。
type traceStepView struct {
	Order    int    `json:"order"`
	Action   string `json:"action"`
	ToolName string `json:"tool_name"`
	Input    string `json:"input"`
	Output   string `json:"output"`
	Success  bool   `json:"success"`
}

// BuildTraceJSON 把引擎执行追踪转换为 Skill 生成器期望的 JSON。
// 供 CLI / HTTP 两种模式复用，避免转换逻辑散落各入口。
func BuildTraceJSON(result *engine.ExecutionTrace) (string, error) {
	data := traceDataView{
		TraceID:     result.TaskID,
		TaskSummary: result.Goal,
	}
	toolSet := make(map[string]bool)
	for i, step := range result.Steps {
		ts := traceStepView{Order: i + 1, Success: true}
		switch {
		case step.Action != "":
			ts.Action = "call_tool"
			ts.ToolName = step.Action
			if input, err := json.Marshal(step.ActionInput); err == nil {
				ts.Input = string(input)
			}
			ts.Output = step.Observation
			// 执行错误/调用被拦截的步骤标记为失败
			if strings.HasPrefix(step.Observation, "工具执行错误") ||
				strings.HasPrefix(step.Observation, "工具调用失败") ||
				strings.HasPrefix(step.Observation, "调用被拦截") {
				ts.Success = false
			}
			toolSet[step.Action] = true
		case step.IsFinal:
			ts.Action = "final_answer"
			ts.Output = step.FinalAnswer
		default:
			ts.Action = "think"
			ts.Output = step.Thought
		}
		data.Steps = append(data.Steps, ts)
	}
	for name := range toolSet {
		data.Tools = append(data.Tools, name)
	}
	out, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	return string(out), nil
}
