package engine

import "context"

// ToolGate 在工具执行前进行安全检查（如安全策略评估、人工审批）。
// 返回 nil 表示放行；返回非 nil error 表示拦截，错误信息会作为
// 该工具的"执行结果"反馈给 LLM，使 Agent 知晓调用被拒绝及原因。
type ToolGate interface {
	// CheckToolCall 检查一次工具调用是否被允许。
	// toolName 为工具名，argsSummary 为工具参数的 JSON 摘要。
	CheckToolCall(ctx context.Context, toolName, argsSummary string) error
}

// OutputGate 在最终答案返回前进行安全检查（如敏感信息脱敏、输出审查）。
// 返回处理后的内容（可能被脱敏修改）；返回非 nil error 表示输出被拒绝。
type OutputGate interface {
	// CheckOutput 检查并可能修改输出内容。content 为原始输出，
	// 返回值为实际对外输出的内容（脱敏后与原始内容可能不同）。
	CheckOutput(ctx context.Context, content string) (string, error)
}

// ToolReturnGate 在工具执行成功后、返回内容注入 LLM 上下文前进行安全检查
// （tool_return 检查点：对返回中包含的密钥等敏感内容脱敏、拦截违规内容）。
type ToolReturnGate interface {
	// CheckToolReturn 检查工具返回内容并可能修改（脱敏）。
	// toolName 为工具名，risk 为工具风险等级，output 为原始返回；
	// 返回值为实际注入上下文的内容；返回非 nil error 表示返回内容被拦截。
	CheckToolReturn(ctx context.Context, toolName string, risk int, output string) (string, error)
}
