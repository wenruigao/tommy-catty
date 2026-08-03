package security

import (
	"os"
	"testing"
)

// newDefaultEngine 构建加载了全部默认模板的策略引擎。
func newDefaultEngine() *Engine {
	e := NewEngine()
	for _, p := range DefaultPolicies() {
		e.AddPolicy(p)
	}
	return e
}

// deniedBy 判断决策列表中是否包含指定策略的 deny 决策。
func deniedBy(decisions []Decision, policyID string) bool {
	for _, d := range decisions {
		if d.Effect == EffectDeny && d.PolicyID == policyID {
			return true
		}
	}
	return false
}

// TestDefaultPolicies_BlockDestructiveRm 验证默认模板的 block-destructive 策略
// 能拦截 rm -rf 的各种破坏性变体（rm 整体封禁已从工具层下沉到策略层）。
func TestDefaultPolicies_BlockDestructiveRm(t *testing.T) {
	e := newDefaultEngine()
	blocked := []string{
		"rm -rf /",
		"rm -rf /*",
		`rm -rf "/"`,
		`rm -rf '/'`,
		"rm -rf ~",
		"rm -rf /etc",
		"rm -rf /home/user/tmp",
		"rm -Rf /tmp/x",
		"rm -fr /",
		"rm -r -f /",
		"rm -f -r /",
		"rm -i -r -f /tmp/x",
		"rm -v -rf /tmp/x",
		"rm --recursive --force /",
		"rm --force --recursive /",
		`sh -c "rm -rf /"`,
	}
	for _, cmd := range blocked {
		cp := Checkpoint{Type: "tool_call", ToolName: "shell_exec", Content: cmd}
		if !deniedBy(e.Evaluate(cp), "block-destructive") {
			t.Errorf("block-destructive 应拦截: %q", cmd)
		}
	}
}

// TestDefaultPolicies_RmBenignForms 验证普通 rm 用法不会被默认模板误拦。
func TestDefaultPolicies_RmBenignForms(t *testing.T) {
	e := newDefaultEngine()
	allowed := []string{
		"rm file.txt",
		"rm -f file.txt",
		"rm -f /tmp/old.log",
		"rm -r ./build",
		"rm -i file.txt",
		"ls -la",
		"git status",
	}
	for _, cmd := range allowed {
		cp := Checkpoint{Type: "tool_call", ToolName: "shell_exec", Content: cmd}
		if deniedBy(e.Evaluate(cp), "block-destructive") {
			t.Errorf("block-destructive 不应拦截合法命令: %q", cmd)
		}
	}
}

// TestPolicyYAML_BlocksRmRf 验证 config/policy.yaml 仅靠策略层即可拦截 rm -rf。
func TestPolicyYAML_BlocksRmRf(t *testing.T) {
	data, err := os.ReadFile("../../config/policy.yaml")
	if err != nil {
		t.Fatalf("读取 policy.yaml 失败: %v", err)
	}
	e := NewEngine()
	if err := e.LoadFromYAML(data); err != nil {
		t.Fatalf("加载 policy.yaml 失败: %v", err)
	}
	blocked := []string{
		"rm -rf /",
		"rm -rf /*",
		`rm -rf "/"`,
		"rm -rf ~",
		"rm -rf /etc",
		"rm -r -f /",
		"rm --recursive --force /",
	}
	for _, cmd := range blocked {
		cp := Checkpoint{Type: "tool_call", ToolName: "shell_exec", Content: cmd}
		decisions := e.Evaluate(cp)
		denied := false
		for _, d := range decisions {
			if d.Effect == EffectDeny {
				denied = true
				break
			}
		}
		if !denied {
			t.Errorf("policy.yaml 应拦截: %q", cmd)
		}
	}

	// 普通 rm 用法不应被 policy.yaml 拦截
	cp := Checkpoint{Type: "tool_call", ToolName: "shell_exec", Content: "rm file.txt"}
	for _, d := range e.Evaluate(cp) {
		if d.Effect == EffectDeny {
			t.Errorf("policy.yaml 不应拦截 rm file.txt，命中策略: %s", d.PolicyID)
		}
	}
}
