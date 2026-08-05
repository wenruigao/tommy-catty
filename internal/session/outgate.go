package session

import (
	"context"
	"fmt"
	"time"

	"github.com/tommy-cat/agent/internal/security"
)

// OutputGateAdapter 将安全策略引擎适配为 engine.OutputGate 接口，
// 在最终答案返回给用户前进行输出审查：deny 策略直接拒绝输出，
// redact 策略对敏感内容（如 API Key、密码）进行脱敏。
type OutputGateAdapter struct {
	engine *security.Engine
}

// NewOutputGateAdapter 创建输出门禁适配器。
func NewOutputGateAdapter(secEngine *security.Engine) *OutputGateAdapter {
	return &OutputGateAdapter{engine: secEngine}
}

// CheckOutput 检查并可能修改输出内容。
// 命中 deny 策略时返回错误（输出被拒绝）；命中 redact 策略时
// 调用安全引擎的 Redact 方法返回脱敏后的内容；否则原样返回。
func (g *OutputGateAdapter) CheckOutput(_ context.Context, content string) (string, error) {
	decisions := g.engine.Evaluate(security.Checkpoint{
		Type:      "llm_output",
		Content:   content,
		Timestamp: time.Now(),
	})

	needRedact := false
	for _, d := range decisions {
		switch d.Effect {
		case security.EffectDeny:
			return "", fmt.Errorf("安全策略拦截了本次输出 [%s]: %s", d.PolicyID, d.Message)
		case security.EffectRedact:
			needRedact = true
		}
	}

	if needRedact {
		content = g.engine.Redact(content)
	}
	return content, nil
}
