package session

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/wenruigao/tommy-catty/internal/security"
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

// riskDangerousLevel 高危工具风险等级（与 tool.RiskDangerous 同值，
// 避免本包反向依赖 tool 包）。该等级工具在无策略命中时也必须走审批。
const riskDangerousLevel = 3

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
// 每个实例持有独立的限流令牌桶：多用户场景下必须为每个用户会话
// 创建独立实例（NewToolGateAdapterForUser），跨用户共享同一实例
// 会导致限流配额被互相耗尽。
type ToolGateAdapter struct {
	engine   *security.Engine
	approver Approver   // 可为 nil（require_approval 一律拒绝）
	riskOf   RiskLookup // 可为 nil（所有工具风险等级视为 0）
	userID   string     // 审计身份（写入 Checkpoint.UserID，空表示未设置）
	bucket   *tokenBucket
}

// NewToolGateAdapter 创建工具调用门禁适配器（匿名身份，兼容既有调用）。
// secEngine 为安全策略引擎；approver 为审批回调，传 nil 时
// 所有 require_approval 决策一律拒绝（适用于无法交互的场景）。
func NewToolGateAdapter(secEngine *security.Engine, approver Approver) *ToolGateAdapter {
	return NewToolGateAdapterForUser(secEngine, approver, "")
}

// NewToolGateAdapterForUser 创建带用户身份的工具调用门禁适配器。
// userID 会写入安全检查点供审计落盘；每次调用都创建独立限流桶，
// 保证 per-user 限流互不影响。
func NewToolGateAdapterForUser(secEngine *security.Engine, approver Approver, userID string) *ToolGateAdapter {
	return &ToolGateAdapter{engine: secEngine, approver: approver, userID: userID, bucket: newTokenBucket()}
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
		UserID:    g.userID,
		Timestamp: time.Now(),
	})

	// 风险等级兜底审批：L3 高危工具即使没有任何策略命中（例如策略集
	// 未包含基于风险等级的规则），也必须经人工确认后才能执行，
	// 避免高危工具在无策略覆盖时"裸奔"直接执行。
	if len(decisions) == 0 && risk >= riskDangerousLevel {
		decisions = []security.Decision{{
			Effect:   security.EffectRequireApproval,
			PolicyID: "default-l3-approval",
			Message:  "L3 高危工具必须经人工确认后执行（风险等级默认门禁）",
		}}
	}

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
