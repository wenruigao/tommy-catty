package router

import (
	"fmt"
	"strings"
)

// classifyPromptTemplate LLM 分类 Prompt 模板（仿 multiagent/prompts.go 的占位符风格）。
const classifyPromptTemplate = `你是一个意图分类器。请判断用户问题最适合由下列哪个专职 Agent 处理。

可用 Agent：
{{AGENTS}}

用户问题：{{INPUT}}

请输出 JSON：
{
  "agent": "Agent 名称（必须是上面列出的之一）",
  "confidence": 0.0 到 1.0 之间的数值,
  "reason": "一句话说明判断依据"
}

规则：
- 只能选择上面列出的 Agent 名称，不要臆造
- 如果问题涉及多种能力、意图不明确，或没有明显合适的专职 Agent，选择 "{{FALLBACK}}"
- confidence 反映你的确定程度：意图清晰给 0.8 以上，模糊时给 0.5 以下
- 只输出 JSON，不要输出其他内容`

// classifySystemPrompt 分类调用的系统提示词。
const classifySystemPrompt = "你是一个意图分类器，只输出 JSON，不要输出其他内容。"

// buildClassifyPrompt 构建分类 Prompt。
// agents 为参与分类的 Agent 列表（不含无触发规则的兜底 Agent 亦可，
// 但兜底 Agent 必须出现在 {{FALLBACK}} 中作为「都不合适」的出口）。
func buildClassifyPrompt(agents []*AgentSpec, input, fallback string) string {
	var sb strings.Builder
	for _, a := range agents {
		sb.WriteString(fmt.Sprintf("- %s: %s\n", a.Name, a.Description))
		if len(a.Tools) > 0 {
			sb.WriteString(fmt.Sprintf("  可用工具: %s\n", strings.Join(a.Tools, ", ")))
		}
	}

	// 输入截断：分类只需意图信号，过长输入徒增 token 成本
	trimmed := input
	const maxInputRunes = 800
	if r := []rune(trimmed); len(r) > maxInputRunes {
		trimmed = string(r[:maxInputRunes]) + "…（已截断）"
	}

	prompt := strings.ReplaceAll(classifyPromptTemplate, "{{AGENTS}}", sb.String())
	prompt = strings.ReplaceAll(prompt, "{{INPUT}}", trimmed)
	prompt = strings.ReplaceAll(prompt, "{{FALLBACK}}", fallback)
	return prompt
}
