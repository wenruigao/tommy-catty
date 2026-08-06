package security

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestAuditLogger_LogAudit_DecisionsRecorded 验证命中策略决策的检查点被审计落盘，
// 且记录携带操作人（user_id）。
func TestAuditLogger_LogAudit_DecisionsRecorded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	logger, err := NewAuditLogger(path)
	if err != nil || logger == nil {
		t.Fatalf("NewAuditLogger 失败: %v", err)
	}
	defer logger.Close()

	cp := Checkpoint{
		Type:      "tool_call",
		ToolName:  "shell_exec",
		ToolRisk:  3,
		Content:   `{"command":"ls"}`,
		UserID:    "alice",
		Timestamp: time.Now(),
	}
	decisions := []Decision{{Effect: EffectRequireApproval, PolicyID: "l3-approval", Message: "需审批"}}
	logger.LogAudit(cp, decisions)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取审计文件失败: %v", err)
	}
	line := string(data)
	for _, want := range []string{`"user_id":"alice"`, `"checkpoint":"tool_call"`, `"effect":"require_approval"`, `l3-approval:require_approval`} {
		if !strings.Contains(line, want) {
			t.Errorf("审计记录应包含 %s，实际: %s", want, line)
		}
	}
}

// TestAuditLogger_LogAudit_L2WithoutDecision 验证 L2+ 工具调用即使无策略命中也落盘，
// 而 L1 以下无决策的调用不落盘。
func TestAuditLogger_LogAudit_L2WithoutDecision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	logger, err := NewAuditLogger(path)
	if err != nil {
		t.Fatalf("NewAuditLogger 失败: %v", err)
	}
	defer logger.Close()

	// L2 无决策 → 记录
	logger.LogAudit(Checkpoint{Type: "tool_call", ToolName: "file_write", ToolRisk: 2, UserID: "bob"}, nil)
	// L0 无决策 → 不记录
	logger.LogAudit(Checkpoint{Type: "tool_call", ToolName: "web_search", ToolRisk: 0, UserID: "bob"}, nil)

	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `"tool_name":"file_write"`) {
		t.Error("L2 工具调用无决策也应审计落盘")
	}
	if strings.Contains(string(data), `"tool_name":"web_search"`) {
		t.Error("L0 无决策的工具调用不应落盘")
	}
}

// TestEngine_Evaluate_AuditHook 验证引擎评估时自动触发审计（含 Checkpoint.UserID）。
func TestEngine_Evaluate_AuditHook(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	logger, err := NewAuditLogger(path)
	if err != nil {
		t.Fatalf("NewAuditLogger 失败: %v", err)
	}
	defer logger.Close()

	eng := NewEngine()
	eng.SetAuditLogger(logger)
	eng.AddPolicy(Policy{
		ID:       "deny-shell",
		Priority: 1,
		Enabled:  true,
		When:     PolicyCondition{ToolNames: []string{"shell_exec"}},
		Then:     PolicyAction{Effect: EffectDeny, Message: "禁止"},
	})

	eng.Evaluate(Checkpoint{
		Type:      "tool_call",
		ToolName:  "shell_exec",
		ToolRisk:  3,
		Content:   "rm -rf /",
		UserID:    "carol",
		Timestamp: time.Now(),
	})

	data, _ := os.ReadFile(path)
	line := string(data)
	if !strings.Contains(line, `"user_id":"carol"`) || !strings.Contains(line, `"effect":"deny"`) {
		t.Errorf("审计应记录操作人与 deny 决策，实际: %s", line)
	}
}

// TestDefaultPolicies_L3Approval 验证内置模板包含基于风险等级的 L3 默认审批策略。
func TestDefaultPolicies_L3Approval(t *testing.T) {
	found := false
	for _, p := range DefaultPolicies() {
		if p.ID == "l3-approval" {
			found = true
			if p.Then.Effect != EffectRequireApproval {
				t.Errorf("l3-approval 效果应为 require_approval，得到 %s", p.Then.Effect)
			}
			if len(p.When.ToolRisk) != 1 || p.When.ToolRisk[0] != "L3" {
				t.Errorf("l3-approval 条件应为 tool_risk L3，得到 %v", p.When.ToolRisk)
			}
		}
	}
	if !found {
		t.Fatal("DefaultPolicies 应包含 l3-approval 策略（L3 风险等级默认审批）")
	}
}
