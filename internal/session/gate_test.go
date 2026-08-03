package session

import (
	"context"
	"strings"
	"testing"

	"github.com/tommy-cat/agent/internal/security"
)

// newGateSecurityEngine 构建一个带测试策略的安全引擎：
// - shell_exec 命中 deny
// - file_write 命中 require_approval
// - 其他工具不命中任何策略
func newGateSecurityEngine() *security.Engine {
	eng := security.NewEngine()
	eng.AddPolicy(security.Policy{
		ID:       "deny-shell",
		Priority: 1,
		Enabled:  true,
		When:     security.PolicyCondition{ToolNames: []string{"shell_exec"}},
		Then:     security.PolicyAction{Effect: security.EffectDeny, Message: "禁止执行 Shell 命令"},
	})
	eng.AddPolicy(security.Policy{
		ID:       "approve-write",
		Priority: 2,
		Enabled:  true,
		When:     security.PolicyCondition{ToolNames: []string{"file_write"}},
		Then:     security.PolicyAction{Effect: security.EffectRequireApproval, Message: "写入文件需要人工审批"},
	})
	return eng
}

func TestToolGateAdapter_Allow(t *testing.T) {
	gate := NewToolGateAdapter(newGateSecurityEngine(), nil)
	if err := gate.CheckToolCall(context.Background(), "web_search", `{"q":"go"}`); err != nil {
		t.Errorf("无命中策略应放行，得到错误: %v", err)
	}
}

func TestToolGateAdapter_Deny(t *testing.T) {
	gate := NewToolGateAdapter(newGateSecurityEngine(), nil)
	err := gate.CheckToolCall(context.Background(), "shell_exec", `{"cmd":"rm -rf /"}`)
	if err == nil {
		t.Fatal("deny 决策应拦截")
	}
	if !strings.Contains(err.Error(), "禁止执行 Shell 命令") {
		t.Errorf("错误信息应包含策略描述，得到: %v", err)
	}
}

func TestToolGateAdapter_ApprovalGranted(t *testing.T) {
	var gotTool, gotArgs, gotReason string
	approver := func(_ context.Context, toolName, argsSummary, reason string) bool {
		gotTool, gotArgs, gotReason = toolName, argsSummary, reason
		return true
	}
	gate := NewToolGateAdapter(newGateSecurityEngine(), approver)
	if err := gate.CheckToolCall(context.Background(), "file_write", `{"path":"a.txt"}`); err != nil {
		t.Fatalf("审批通过应放行，得到错误: %v", err)
	}
	if gotTool != "file_write" || gotArgs != `{"path":"a.txt"}` || gotReason == "" {
		t.Errorf("审批回调参数不正确: tool=%q args=%q reason=%q", gotTool, gotArgs, gotReason)
	}
}

func TestToolGateAdapter_ApprovalRejected(t *testing.T) {
	gate := NewToolGateAdapter(newGateSecurityEngine(),
		func(_ context.Context, _, _, _ string) bool { return false })
	err := gate.CheckToolCall(context.Background(), "file_write", `{}`)
	if err == nil {
		t.Fatal("审批拒绝应拦截")
	}
	if !strings.Contains(err.Error(), "未获审批") {
		t.Errorf("错误信息应说明未获审批，得到: %v", err)
	}
}

func TestToolGateAdapter_NoApprover(t *testing.T) {
	// approver 为 nil 时 require_approval 一律拒绝
	gate := NewToolGateAdapter(newGateSecurityEngine(), nil)
	if err := gate.CheckToolCall(context.Background(), "file_write", `{}`); err == nil {
		t.Fatal("无审批回调时 require_approval 应拒绝")
	}
}
