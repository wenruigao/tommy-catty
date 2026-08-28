package session

import (
	"context"
	"fmt"
	"time"

	"github.com/wenruigao/tommy-catty/internal/security"
)

// ReturnGateAdapter 将安全策略引擎适配为 engine.ToolReturnGate 接口，
// 在工具返回注入 LLM 上下文前评估 tool_return 检查点：
// deny 拦截（以拦截说明替代原返回），redact 对返回中的敏感内容
// （如密钥、密码）脱敏。修复前 tool_return 检查点从未被评估，
// protect-secrets 的 tool_return 分支是死代码。
type ReturnGateAdapter struct {
	engine *security.Engine
	userID string // 审计身份（写入 Checkpoint.UserID）
}

// NewReturnGateAdapterForUser 创建带用户身份的工具返回门禁适配器。
func NewReturnGateAdapterForUser(secEngine *security.Engine, userID string) *ReturnGateAdapter {
	return &ReturnGateAdapter{engine: secEngine, userID: userID}
}

// CheckToolReturn 评估工具返回内容，返回可能脱敏后的输出；
// 命中 deny 策略时返回错误（返回内容被拦截）。
func (g *ReturnGateAdapter) CheckToolReturn(_ context.Context, toolName string, risk int, output string) (string, error) {
	decisions := g.engine.Evaluate(security.Checkpoint{
		Type:      "tool_return",
		ToolName:  toolName,
		ToolRisk:  risk,
		Content:   output,
		UserID:    g.userID,
		Timestamp: time.Now(),
	})

	needRedact := false
	for _, d := range decisions {
		switch d.Effect {
		case security.EffectDeny:
			return "", fmt.Errorf("安全策略拦截工具 %q 的返回内容 [%s]: %s", toolName, d.PolicyID, d.Message)
		case security.EffectRedact:
			needRedact = true
		}
		// require_approval / throttle 对"已产生的返回"无意义，忽略
	}

	if needRedact {
		output = g.engine.Redact(output)
	}
	return output, nil
}
