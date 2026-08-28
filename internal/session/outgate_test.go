package session

import (
	"context"
	"strings"
	"testing"

	"github.com/wenruigao/tommy-catty/internal/security"
)

// newOutputSecurityEngine 构建带输出策略的测试安全引擎：
// - llm_output 中出现 sk-xxx 形式的内容命中 redact
// - llm_output 中出现 "绝密" 命中 deny
func newOutputSecurityEngine() *security.Engine {
	eng := security.NewEngine()
	eng.AddPolicy(security.Policy{
		ID:       "redact-api-key",
		Priority: 1,
		Enabled:  true,
		When: security.PolicyCondition{
			ActionType: []string{"llm_output"},
			Pattern:    `sk-[A-Za-z0-9]+`,
		},
		Then: security.PolicyAction{Effect: security.EffectRedact, Message: "输出包含 API Key，已脱敏"},
	})
	eng.AddPolicy(security.Policy{
		ID:       "deny-top-secret",
		Priority: 2,
		Enabled:  true,
		When: security.PolicyCondition{
			ActionType: []string{"llm_output"},
			Pattern:    `绝密`,
		},
		Then: security.PolicyAction{Effect: security.EffectDeny, Message: "输出包含禁止外发的内容"},
	})
	return eng
}

// TestOutputGateAdapter_Redact 验证命中 redact 策略时 sk-xxx 被脱敏为 ***。
func TestOutputGateAdapter_Redact(t *testing.T) {
	gate := NewOutputGateAdapter(newOutputSecurityEngine())
	out, err := gate.CheckOutput(context.Background(), "你的密钥是 sk-abc123def，请妥善保管")
	if err != nil {
		t.Fatalf("redact 不应拒绝输出，得到错误: %v", err)
	}
	if strings.Contains(out, "sk-abc123def") {
		t.Errorf("API Key 应被脱敏，得到: %q", out)
	}
	if !strings.Contains(out, "***") {
		t.Errorf("脱敏后应包含 ***，得到: %q", out)
	}
}

// TestOutputGateAdapter_Deny 验证命中 deny 策略时输出被拒绝。
func TestOutputGateAdapter_Deny(t *testing.T) {
	gate := NewOutputGateAdapter(newOutputSecurityEngine())
	_, err := gate.CheckOutput(context.Background(), "这是一段绝密内容")
	if err == nil {
		t.Fatal("deny 策略应拒绝输出")
	}
	if !strings.Contains(err.Error(), "禁止外发") {
		t.Errorf("错误信息应包含策略描述，得到: %v", err)
	}
}

// TestOutputGateAdapter_Allow 验证无命中策略时内容原样返回。
func TestOutputGateAdapter_Allow(t *testing.T) {
	gate := NewOutputGateAdapter(newOutputSecurityEngine())
	const content = "这是正常的最终答案。"
	out, err := gate.CheckOutput(context.Background(), content)
	if err != nil {
		t.Fatalf("无命中策略应放行，得到错误: %v", err)
	}
	if out != content {
		t.Errorf("无命中策略时内容应原样返回，得到: %q", out)
	}
}
