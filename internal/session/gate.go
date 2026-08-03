package session

import (
	"context"
	"fmt"

	"github.com/tommy-cat/agent/internal/security"
)

// Approver 是工具调用的人工审批回调。
// 返回 true 表示批准放行，false 表示拒绝。
type Approver func(ctx context.Context, toolName, argsSummary, reason string) bool

// ToolGateAdapter 将安全策略引擎适配为 engine.ToolGate 接口，
// 在每次工具调用前进行策略评估与人工审批。
type ToolGateAdapter struct {
	engine   *security.Engine
	approver Approver // 可为 nil（require_approval 一律拒绝）
}

// NewToolGateAdapter 创建工具调用门禁适配器。
// secEngine 为安全策略引擎；approver 为审批回调，传 nil 时
// 所有 require_approval 决策一律拒绝（适用于无法交互的场景）。
func NewToolGateAdapter(secEngine *security.Engine, approver Approver) *ToolGateAdapter {
	return &ToolGateAdapter{engine: secEngine, approver: approver}
}

// CheckToolCall 检查一次工具调用是否被安全策略放行。
// 返回 nil 表示放行；返回非 nil error 表示拦截，错误信息会作为
// 工具"执行结果"反馈给 LLM。
func (g *ToolGateAdapter) CheckToolCall(ctx context.Context, toolName, argsSummary string) error {
	decisions := g.engine.Evaluate(security.Checkpoint{
		Type:     "tool_call",
		ToolName: toolName,
		Content:  argsSummary,
	})

	for _, d := range decisions {
		switch d.Effect {
		case security.EffectDeny:
			return fmt.Errorf("安全策略拦截工具 %q 的调用 [%s]: %s", toolName, d.PolicyID, d.Message)
		case security.EffectRequireApproval:
			reason := fmt.Sprintf("安全策略要求审批 [%s]: %s", d.PolicyID, d.Message)
			if g.approver == nil || !g.approver(ctx, toolName, argsSummary, reason) {
				return fmt.Errorf("工具 %q 的调用未获审批，已拒绝（%s）", toolName, reason)
			}
		}
	}
	return nil
}
