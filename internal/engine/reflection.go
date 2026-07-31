// reflection.go 实现 Agent 自我反思（Self-Reflection）与重规划触发机制。
// 参考 Reflexion (Shinn et al., 2023)：执行后自评结果质量，不满意则调整或重新规划。
package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/tommy-cat/agent/internal/llm"
)

// ReflectionConfig 反思机制配置。
type ReflectionConfig struct {
	// Enabled 是否启用反思（默认 true）
	Enabled bool
	// IntervalSteps 每隔多少步执行一次阶段性反思（默认 5）
	IntervalSteps int
	// SatisfactionThreshold 低于此分数触发调整（默认 0.6）
	SatisfactionThreshold float64
	// MaxReplans 最大重规划次数（默认 2）
	MaxReplans int
	// DeviationThreshold 累积偏差超过此值触发重规划（默认 1.5）
	DeviationThreshold float64
}

// DefaultReflectionConfig 返回默认反思配置。
func DefaultReflectionConfig() ReflectionConfig {
	return ReflectionConfig{
		Enabled:               true,
		IntervalSteps:         5,
		SatisfactionThreshold: 0.6,
		MaxReplans:            2,
		DeviationThreshold:    1.5,
	}
}

// ReflectionResult 反思输出结构。
type ReflectionResult struct {
	// Satisfaction 满意度 0-1
	Satisfaction float64
	// Issues 发现的问题列表
	Issues []string
	// Adjustment 调整决策：continue | revise | replan
	Adjustment string
}

// ReplanState 跟踪重规划相关状态（ReAct 循环内局部变量）。
type ReplanState struct {
	// DeviationScore 累积偏差分数
	DeviationScore float64
	// ReplanCount 已执行的重规划次数
	ReplanCount int
	// ConsecutiveFailures 连续失败计数
	ConsecutiveFailures int
	// SuccessfulObservations 成功的观察结果（重规划时保留）
	SuccessfulObservations []string
}

// shouldReflect 判断当前步是否应触发反思。
func shouldReflect(cfg ReflectionConfig, stepIndex int, toolFailed bool) bool {
	if !cfg.Enabled {
		return false
	}
	if toolFailed {
		return true
	}
	if cfg.IntervalSteps > 0 && stepIndex > 0 && stepIndex%cfg.IntervalSteps == 0 {
		return true
	}
	return false
}

// buildReflectionPrompt 构建反思 Prompt。
func buildReflectionPrompt(goal string, recentSteps []StepResult) string {
	var sb strings.Builder
	sb.WriteString("请基于以下执行轨迹进行自我评估：\n\n")
	sb.WriteString(fmt.Sprintf("原始目标：%s\n\n", goal))
	sb.WriteString("最近执行步骤：\n")
	for i, step := range recentSteps {
		sb.WriteString(fmt.Sprintf("%d. 工具=%s", i+1, step.Action))
		if step.Observation != "" {
			obs := step.Observation
			if len(obs) > 200 {
				obs = obs[:200] + "..."
			}
			sb.WriteString(fmt.Sprintf(" 结果=%s", obs))
		}
		sb.WriteString("\n")
	}
	sb.WriteString("\n请回答（JSON 格式）：\n")
	sb.WriteString(`{"satisfaction": 0.0-1.0, "issues": ["问题1"], "adjustment": "continue|revise|replan"}`)
	sb.WriteString("\n其中 satisfaction 表示当前进展满足目标的程度，adjustment 表示下一步决策。")
	return sb.String()
}

// parseReflection 解析 LLM 反思输出（宽松解析，失败时返回默认继续）。
func parseReflection(content string) ReflectionResult {
	result := ReflectionResult{Satisfaction: 0.8, Adjustment: "continue"}

	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start < 0 || end <= start {
		return result
	}

	jsonStr := content[start : end+1]
	if strings.Contains(jsonStr, "\"replan\"") {
		result.Adjustment = "replan"
		result.Satisfaction = 0.3
	} else if strings.Contains(jsonStr, "\"revise\"") {
		result.Adjustment = "revise"
		result.Satisfaction = 0.5
	}

	if idx := strings.Index(jsonStr, "\"satisfaction\""); idx >= 0 {
		var val float64
		if n, _ := fmt.Sscanf(jsonStr[idx:], `"satisfaction": %f`, &val); n == 1 {
			result.Satisfaction = val
		}
	}

	return result
}

// updateDeviation 根据工具执行结果更新偏差分数。
func (rs *ReplanState) updateDeviation(toolFailed bool, emptyResult bool) {
	if toolFailed {
		rs.DeviationScore += 0.5
		rs.ConsecutiveFailures++
	} else if emptyResult {
		rs.DeviationScore += 0.3
		rs.ConsecutiveFailures = 0
	} else {
		rs.ConsecutiveFailures = 0
	}
}

// shouldReplan 判断是否应触发重规划。
func (rs *ReplanState) shouldReplan(cfg ReflectionConfig, reflection *ReflectionResult, currentStep, maxSteps int) bool {
	if rs.ReplanCount >= cfg.MaxReplans {
		return false
	}
	if rs.ConsecutiveFailures >= 3 {
		return true
	}
	if reflection != nil && reflection.Adjustment == "replan" {
		return true
	}
	if currentStep > int(float64(maxSteps)*0.7) && rs.DeviationScore > 1.0 {
		return true
	}
	if rs.DeviationScore >= cfg.DeviationThreshold {
		return true
	}
	return false
}

// buildReplanPrompt 构建重规划 Prompt 前缀。
func buildReplanPrompt(goal string, successfulObs []string) string {
	var sb strings.Builder
	sb.WriteString("之前的执行路径遇到问题，需要重新规划。\n\n")
	sb.WriteString(fmt.Sprintf("原始目标：%s\n\n", goal))
	if len(successfulObs) > 0 {
		sb.WriteString("已获取的有效信息：\n")
		for _, obs := range successfulObs {
			if len(obs) > 300 {
				obs = obs[:300] + "..."
			}
			sb.WriteString("- " + obs + "\n")
		}
		sb.WriteString("\n")
	}
	sb.WriteString("请基于以上已有信息重新规划执行步骤。")
	return sb.String()
}

// executeReflection 执行一次反思调用（使用 LLM，无工具）。
func (e *Engine) executeReflection(ctx context.Context, goal string, recentSteps []StepResult) *ReflectionResult {
	if e.llmGateway == nil {
		return nil
	}

	prompt := buildReflectionPrompt(goal, recentSteps)
	msgs := []llm.Message{
		{Role: "system", Content: "你是一个执行质量评估器，请客观评估当前进展并以 JSON 格式回答。"},
		{Role: "user", Content: prompt},
	}

	resp, err := e.llmGateway.Chat(ctx, msgs, nil)
	if err != nil {
		return nil // 反思失败不阻断主流程
	}

	result := parseReflection(resp.Content)
	return &result
}
