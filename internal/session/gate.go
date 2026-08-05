package session

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/tommy-cat/agent/internal/security"
)

const (
	// throttleBucketCapacity 限流令牌桶容量（允许的最大突发调用次数）。
	throttleBucketCapacity = 30
	// throttleRefillWindow 令牌桶补满整个容量所需的时间窗口，
	// 即限流速率为 throttleBucketCapacity 次 / throttleRefillWindow（30 次/分钟）。
	throttleRefillWindow = time.Minute
)

// Approver 是工具调用的人工审批回调。
// 返回 true 表示批准放行，false 表示拒绝。
type Approver func(ctx context.Context, toolName, argsSummary, reason string) bool

// RiskLookup 查询工具的风险等级（0~3），工具未知时应返回 0。
// 通常由工具注册表的 ToolMeta 提供（见 cmd 入口的接线）。
type RiskLookup func(toolName string) int

// tokenBucket 简单的令牌桶限流器，用于实现 throttle 策略效果。
type tokenBucket struct {
	mu     sync.Mutex
	tokens float64   // 当前可用令牌数
	last   time.Time // 上次结算时间
}

// newTokenBucket 创建一个初始满容量的令牌桶。
func newTokenBucket() *tokenBucket {
	return &tokenBucket{tokens: throttleBucketCapacity, last: time.Now()}
}

// allow 尝试消费一个令牌，不足时返回 false。
func (b *tokenBucket) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	// 按 elapsed 与补满窗口的比例补充令牌
	b.tokens += now.Sub(b.last).Seconds() * (throttleBucketCapacity / throttleRefillWindow.Seconds())
	if b.tokens > throttleBucketCapacity {
		b.tokens = throttleBucketCapacity
	}
	b.last = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// ToolGateAdapter 将安全策略引擎适配为 engine.ToolGate 接口，
// 在每次工具调用前进行策略评估与人工审批。
type ToolGateAdapter struct {
	engine   *security.Engine
	approver Approver   // 可为 nil（require_approval 一律拒绝）
	riskOf   RiskLookup // 可为 nil（所有工具风险等级视为 0）
	bucket   *tokenBucket
}

// NewToolGateAdapter 创建工具调用门禁适配器。
// secEngine 为安全策略引擎；approver 为审批回调，传 nil 时
// 所有 require_approval 决策一律拒绝（适用于无法交互的场景）。
func NewToolGateAdapter(secEngine *security.Engine, approver Approver) *ToolGateAdapter {
	return &ToolGateAdapter{engine: secEngine, approver: approver, bucket: newTokenBucket()}
}

// SetRiskLookup 设置工具风险等级查询函数，
// 使按 tool_risk 匹配的策略（如仅管控 L3 高危工具）能够命中。
func (g *ToolGateAdapter) SetRiskLookup(fn RiskLookup) {
	g.riskOf = fn
}

// CheckToolCall 检查一次工具调用是否被安全策略放行。
// 返回 nil 表示放行；返回非 nil error 表示拦截，错误信息会作为
// 工具"执行结果"反馈给 LLM。
func (g *ToolGateAdapter) CheckToolCall(ctx context.Context, toolName, argsSummary string) error {
	risk := 0
	if g.riskOf != nil {
		risk = g.riskOf(toolName)
	}

	decisions := g.engine.Evaluate(security.Checkpoint{
		Type:      "tool_call",
		ToolName:  toolName,
		ToolRisk:  risk,
		Content:   argsSummary,
		Timestamp: time.Now(),
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
		case security.EffectThrottle:
			if !g.bucket.allow() {
				return fmt.Errorf("工具 %q 的调用触发限流 [%s]: %s（已超过每分钟 %d 次上限，请稍后再试）",
					toolName, d.PolicyID, d.Message, throttleBucketCapacity)
			}
		}
	}
	return nil
}
