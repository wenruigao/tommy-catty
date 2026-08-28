package session

import (
	"context"
	"strings"
	"testing"

	"github.com/wenruigao/tommy-catty/internal/security"
)

// TestToolGate_L3DefaultApproval 验证风险等级兜底审批：
// L3 工具即使没有任何策略命中，也必须走审批，不得直接放行。
func TestToolGate_L3DefaultApproval(t *testing.T) {
	eng := security.NewEngine() // 无任何策略

	// 1) 无 approver（HTTP 模式）：L3 调用必须被拒绝
	gate := NewToolGateAdapterForUser(eng, nil, "alice")
	gate.SetRiskLookup(func(string) int { return 3 })
	err := gate.CheckToolCall(context.Background(), "shell_exec", `{"command":"ls"}`)
	if err == nil {
		t.Fatal("L3 工具无策略命中且无 approver 时应拒绝，而非直接放行")
	}
	if !strings.Contains(err.Error(), "未获审批") {
		t.Errorf("错误应说明未获审批，得到: %v", err)
	}

	// 2) approver 批准：放行
	approved := NewToolGateAdapterForUser(eng, func(_ context.Context, _, _, _ string) bool { return true }, "alice")
	approved.SetRiskLookup(func(string) int { return 3 })
	if err := approved.CheckToolCall(context.Background(), "shell_exec", `{"command":"ls"}`); err != nil {
		t.Errorf("L3 工具经批准应放行，得到: %v", err)
	}

	// 3) approver 拒绝：拦截
	rejected := NewToolGateAdapterForUser(eng, func(_ context.Context, _, _, _ string) bool { return false }, "alice")
	rejected.SetRiskLookup(func(string) int { return 3 })
	if err := rejected.CheckToolCall(context.Background(), "shell_exec", `{}`); err == nil {
		t.Fatal("L3 工具审批被拒时应拦截")
	}

	// 4) 低风险工具不受兜底审批影响
	low := NewToolGateAdapterForUser(eng, nil, "alice")
	low.SetRiskLookup(func(string) int { return 1 })
	if err := low.CheckToolCall(context.Background(), "web_search", `{}`); err != nil {
		t.Errorf("低风险工具应放行，得到: %v", err)
	}
}

// TestToolGate_PerUserBucketIsolation 验证每个用户持有独立限流桶：
// 一个用户耗尽配额不影响另一个用户。
func TestToolGate_PerUserBucketIsolation(t *testing.T) {
	eng := security.NewEngine()
	eng.AddPolicy(security.Policy{
		ID:       "throttle-all",
		Priority: 1,
		Enabled:  true,
		When:     security.PolicyCondition{ToolNames: []string{"web_search"}},
		Then:     security.PolicyAction{Effect: security.EffectThrottle, Message: "限流"},
	})

	alice := NewToolGateAdapterForUser(eng, nil, "alice")
	bob := NewToolGateAdapterForUser(eng, nil, "bob")

	// alice 耗尽自己的配额
	var lastErr error
	for i := 0; i <= throttleBucketCapacity; i++ {
		lastErr = alice.CheckToolCall(context.Background(), "web_search", `{}`)
	}
	if lastErr == nil {
		t.Fatal("alice 超出配额应被限流")
	}

	// bob 不受影响
	if err := bob.CheckToolCall(context.Background(), "web_search", `{}`); err != nil {
		t.Errorf("bob 的独立限流桶不应受 alice 耗尽影响，得到: %v", err)
	}
}

// TestReturnGate_RedactsSecrets 验证 tool_return 检查点被实际评估：
// 工具返回中的密钥在注入上下文前被脱敏。
func TestReturnGate_RedactsSecrets(t *testing.T) {
	eng := security.NewEngine()
	for _, p := range security.DefaultPolicies() {
		eng.AddPolicy(p)
	}

	gate := NewReturnGateAdapterForUser(eng, "alice")
	output := "配置读取成功: api_key=abcdef1234567890abcdef"
	cleaned, err := gate.CheckToolReturn(context.Background(), "file_read", 0, output)
	if err != nil {
		t.Fatalf("tool_return 评估不应拦截普通返回: %v", err)
	}
	if strings.Contains(cleaned, "abcdef1234567890abcdef") {
		t.Errorf("返回中的密钥应被脱敏，实际: %s", cleaned)
	}
	if !strings.Contains(cleaned, "***") {
		t.Errorf("脱敏后应包含 ***，实际: %s", cleaned)
	}
}

// TestReturnGate_DenyBlocks 验证 tool_return 命中 deny 策略时拦截返回内容。
func TestReturnGate_DenyBlocks(t *testing.T) {
	eng := security.NewEngine()
	eng.AddPolicy(security.Policy{
		ID:       "deny-return",
		Priority: 1,
		Enabled:  true,
		When:     security.PolicyCondition{ActionType: []string{"tool_return"}, Pattern: "TOPSECRET"},
		Then:     security.PolicyAction{Effect: security.EffectDeny, Message: "禁止返回机密内容"},
	})

	gate := NewReturnGateAdapterForUser(eng, "alice")
	if _, err := gate.CheckToolReturn(context.Background(), "file_read", 0, "内容是 TOPSECRET"); err == nil {
		t.Fatal("命中 deny 的 tool_return 应被拦截")
	}
}
