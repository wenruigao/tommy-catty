package session

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/wenruigao/tommy-catty/internal/security"
)

// TestToolGateAdapter_ToolRiskPassthrough 验证 CheckToolCall 会将工具风险等级
// 透传到 Checkpoint，使按 tool_risk 匹配的策略能够命中。
func TestToolGateAdapter_ToolRiskPassthrough(t *testing.T) {
	eng := security.NewEngine()
	eng.AddPolicy(security.Policy{
		ID:       "deny-l3",
		Priority: 1,
		Enabled:  true,
		When:     security.PolicyCondition{ToolRisk: []string{"L3"}},
		Then:     security.PolicyAction{Effect: security.EffectDeny, Message: "高危工具禁止调用"},
	})

	gate := NewToolGateAdapter(eng, nil)
	gate.SetRiskLookup(func(toolName string) int {
		if toolName == "shell_exec" {
			return 3
		}
		return 1
	})

	// L3 工具应被拦截
	if err := gate.CheckToolCall(context.Background(), "shell_exec", `{}`); err == nil {
		t.Fatal("L3 风险工具应被 tool_risk 策略拦截")
	}
	// L1 工具不命中策略，应放行
	if err := gate.CheckToolCall(context.Background(), "web_search", `{}`); err != nil {
		t.Errorf("L1 风险工具应放行，得到错误: %v", err)
	}
}

// TestToolGateAdapter_TimestampSet 验证 Checkpoint 带上了触发时间，
// 使按 time_range 匹配的策略可以基于当前时间命中。
func TestToolGateAdapter_TimestampSet(t *testing.T) {
	now := time.Now()
	// 构造覆盖当前时间的全天时间范围（取当前小时前后各一小时，跨午夜截断）
	start := now.Add(-time.Hour).Format("15:04")
	end := now.Add(time.Hour).Format("15:04")

	eng := security.NewEngine()
	eng.AddPolicy(security.Policy{
		ID:       "deny-office-hours",
		Priority: 1,
		Enabled:  true,
		When:     security.PolicyCondition{TimeRange: start + "-" + end},
		Then:     security.PolicyAction{Effect: security.EffectDeny, Message: "当前时段禁止调用"},
	})

	gate := NewToolGateAdapter(eng, nil)
	if err := gate.CheckToolCall(context.Background(), "any_tool", `{}`); err == nil {
		t.Fatal("处于时间范围内应被 time_range 策略拦截（Checkpoint 需携带 Timestamp）")
	}
}

// TestToolGateAdapter_Throttle 验证 throttle 决策触发令牌桶限流：
// 桶容量内放行，超限后返回中文限流错误。
func TestToolGateAdapter_Throttle(t *testing.T) {
	eng := security.NewEngine()
	eng.AddPolicy(security.Policy{
		ID:       "throttle-all",
		Priority: 1,
		Enabled:  true,
		When:     security.PolicyCondition{ToolNames: []string{"web_search"}},
		Then:     security.PolicyAction{Effect: security.EffectThrottle, Message: "搜索调用过于频繁"},
	})

	gate := NewToolGateAdapter(eng, nil)

	// 桶容量（30 次）内应全部放行
	for i := 0; i < throttleBucketCapacity; i++ {
		if err := gate.CheckToolCall(context.Background(), "web_search", `{}`); err != nil {
			t.Fatalf("第 %d 次调用应在限流额度内放行，得到错误: %v", i+1, err)
		}
	}

	// 超出容量应返回限流错误
	err := gate.CheckToolCall(context.Background(), "web_search", `{}`)
	if err == nil {
		t.Fatal("超出限流额度应被拦截")
	}
	if !strings.Contains(err.Error(), "限流") {
		t.Errorf("错误信息应说明限流，得到: %v", err)
	}

	// 不命中 throttle 策略的工具不受令牌桶影响
	if err := gate.CheckToolCall(context.Background(), "file_read", `{}`); err != nil {
		t.Errorf("未命中 throttle 策略的工具应放行，得到错误: %v", err)
	}
}
