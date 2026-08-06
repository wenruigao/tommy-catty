package security

import (
	"strings"
	"testing"
	"time"
)

// ============================================================
// parseTime tests
// ============================================================

func TestParseTime_Valid(t *testing.T) {
	h, m, err := parseTime("09:00")
	if err != nil {
		t.Fatalf("parseTime: %v", err)
	}
	if h != 9 || m != 0 {
		t.Errorf("parseTime = (%d, %d), want (9, 0)", h, m)
	}
}

func TestParseTime_Midnight(t *testing.T) {
	h, m, err := parseTime("00:00")
	if err != nil {
		t.Fatalf("parseTime: %v", err)
	}
	if h != 0 || m != 0 {
		t.Errorf("parseTime = (%d, %d), want (0, 0)", h, m)
	}
}

func TestParseTime_EndOfDay(t *testing.T) {
	h, m, err := parseTime("23:59")
	if err != nil {
		t.Fatalf("parseTime: %v", err)
	}
	if h != 23 || m != 59 {
		t.Errorf("parseTime = (%d, %d), want (23, 59)", h, m)
	}
}

func TestParseTime_Invalid_OutOfRange(t *testing.T) {
	tests := []string{"24:00", "12:60", "-1:00"}
	for _, s := range tests {
		_, _, err := parseTime(s)
		if err == nil {
			t.Errorf("parseTime(%q) should return error", s)
		}
	}
}

func TestParseTime_Invalid_Format(t *testing.T) {
	tests := []string{"invalid", "abc", "12"}
	for _, s := range tests {
		_, _, err := parseTime(s)
		if err == nil {
			t.Errorf("parseTime(%q) should return error", s)
		}
	}
}

// ============================================================
// inTimeRange tests
// ============================================================

func TestInTimeRange_Inside(t *testing.T) {
	tm := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	if !inTimeRange("09:00-18:00", tm) {
		t.Error("12:00 should be inside 09:00-18:00")
	}
}

func TestInTimeRange_Outside(t *testing.T) {
	tm := time.Date(2024, 1, 1, 20, 0, 0, 0, time.UTC)
	if inTimeRange("09:00-18:00", tm) {
		t.Error("20:00 should be outside 09:00-18:00")
	}
}

func TestInTimeRange_BoundaryStart(t *testing.T) {
	tm := time.Date(2024, 1, 1, 9, 0, 0, 0, time.UTC)
	if !inTimeRange("09:00-18:00", tm) {
		t.Error("09:00 should be inside at boundary")
	}
}

func TestInTimeRange_BoundaryEnd(t *testing.T) {
	tm := time.Date(2024, 1, 1, 18, 0, 0, 0, time.UTC)
	if !inTimeRange("09:00-18:00", tm) {
		t.Error("18:00 should be inside at boundary")
	}
}

func TestInTimeRange_Overnight(t *testing.T) {
	night := time.Date(2024, 1, 1, 23, 0, 0, 0, time.UTC)
	if !inTimeRange("22:00-06:00", night) {
		t.Error("23:00 should be inside 22:00-06:00 (overnight)")
	}

	early := time.Date(2024, 1, 1, 3, 0, 0, 0, time.UTC)
	if !inTimeRange("22:00-06:00", early) {
		t.Error("03:00 should be inside 22:00-06:00 (overnight)")
	}

	noon := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	if inTimeRange("22:00-06:00", noon) {
		t.Error("12:00 should be outside 22:00-06:00 (overnight)")
	}
}

func TestInTimeRange_InvalidFormat(t *testing.T) {
	tm := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	if inTimeRange("invalid", tm) {
		t.Error("invalid timeRange should return false")
	}
}

func TestInTimeRange_Empty(t *testing.T) {
	tm := time.Now()
	if inTimeRange("", tm) {
		t.Error("empty timeRange should return false")
	}
}

// ============================================================
// containsStr tests
// ============================================================

func TestContainsStr_Found(t *testing.T) {
	if !containsStr([]string{"abc", "def", "ghi"}, "DEF") {
		t.Error("containsStr should be case-insensitive")
	}
}

func TestContainsStr_NotFound(t *testing.T) {
	if containsStr([]string{"abc", "def"}, "xyz") {
		t.Error("containsStr should return false for missing element")
	}
}

func TestContainsStr_Empty(t *testing.T) {
	if containsStr([]string{}, "abc") {
		t.Error("containsStr on empty slice should return false")
	}
}

// ============================================================
// matchesCondition tests
// ============================================================

func TestMatchesCondition_Empty(t *testing.T) {
	cond := PolicyCondition{}
	cp := Checkpoint{}
	if !matchesCondition(cond, cp) {
		t.Error("empty condition should match anything")
	}
}

func TestMatchesCondition_ToolNames_Match(t *testing.T) {
	cond := PolicyCondition{ToolNames: []string{"web_search", "file_read"}}
	cp := Checkpoint{ToolName: "web_search"}
	if !matchesCondition(cond, cp) {
		t.Error("ToolNames should match")
	}
}

func TestMatchesCondition_ToolNames_NoMatch(t *testing.T) {
	cond := PolicyCondition{ToolNames: []string{"file_read"}}
	cp := Checkpoint{ToolName: "web_search"}
	if matchesCondition(cond, cp) {
		t.Error("ToolNames should not match")
	}
}

func TestMatchesCondition_ToolRisk_Match(t *testing.T) {
	cond := PolicyCondition{ToolRisk: []string{"L3"}}
	cp := Checkpoint{ToolRisk: 3}
	if !matchesCondition(cond, cp) {
		t.Error("L3 should match ToolRisk=3")
	}
}

func TestMatchesCondition_ToolRisk_NoMatch(t *testing.T) {
	cond := PolicyCondition{ToolRisk: []string{"L1"}}
	cp := Checkpoint{ToolRisk: 3}
	if matchesCondition(cond, cp) {
		t.Error("L1 should not match ToolRisk=3")
	}
}

func TestMatchesCondition_ActionType(t *testing.T) {
	cond := PolicyCondition{ActionType: []string{"tool_call"}}
	cp := Checkpoint{Type: "tool_call"}
	if !matchesCondition(cond, cp) {
		t.Error("ActionType should match")
	}

	cp2 := Checkpoint{Type: "task_start"}
	if matchesCondition(cond, cp2) {
		t.Error("ActionType should not match different type")
	}
}

func TestMatchesCondition_Pattern_Match(t *testing.T) {
	cond := PolicyCondition{Pattern: `rm\s+-rf`}
	cond.compilePattern()
	cp := Checkpoint{Content: "rm -rf /tmp"}
	if !matchesCondition(cond, cp) {
		t.Error("Pattern should match rm -rf")
	}
}

func TestMatchesCondition_Pattern_NoMatch(t *testing.T) {
	cond := PolicyCondition{Pattern: `rm\s+-rf`}
	cond.compilePattern()
	cp := Checkpoint{Content: "echo hello"}
	if matchesCondition(cond, cp) {
		t.Error("Pattern should not match safe command")
	}
}

func TestMatchesCondition_Pattern_Invalid(t *testing.T) {
	cond := PolicyCondition{Pattern: `[invalid`}
	cond.compilePattern()
	cp := Checkpoint{Content: "anything"}
	if matchesCondition(cond, cp) {
		t.Error("invalid pattern should not match")
	}
}

func TestMatchesCondition_Sensitive(t *testing.T) {
	cond := PolicyCondition{Sensitive: []string{"password", "secret"}}
	cp := Checkpoint{Content: "my password is 12345"}
	if !matchesCondition(cond, cp) {
		t.Error("Sensitive should match password content")
	}

	cp2 := Checkpoint{Content: "normal text"}
	if matchesCondition(cond, cp2) {
		t.Error("Sensitive should not match normal content")
	}
}

func TestMatchesCondition_Sensitive_CaseInsensitive(t *testing.T) {
	cond := PolicyCondition{Sensitive: []string{"PASSWORD"}}
	cp := Checkpoint{Content: "my Password is here"}
	if !matchesCondition(cond, cp) {
		t.Error("Sensitive should be case-insensitive")
	}
}

func TestMatchesCondition_MaxCost_Exceed(t *testing.T) {
	cond := PolicyCondition{MaxCost: 10}
	cp := Checkpoint{Cost: 15}
	if !matchesCondition(cond, cp) {
		t.Error("Cost>MaxCost should match condition")
	}
}

func TestMatchesCondition_MaxCost_Below(t *testing.T) {
	cond := PolicyCondition{MaxCost: 10}
	cp := Checkpoint{Cost: 5}
	if matchesCondition(cond, cp) {
		t.Error("Cost<=MaxCost should not match condition")
	}
}

func TestMatchesCondition_MaxCost_Zero(t *testing.T) {
	cond := PolicyCondition{MaxCost: 0}
	cp := Checkpoint{Cost: 100}
	if !matchesCondition(cond, cp) { // MaxCost=0 means no limit, should always match
		t.Error("MaxCost=0 should match any cost")
	}
}

// ============================================================
// Engine tests
// ============================================================

func TestEngine_Evaluate_DisabledPolicy(t *testing.T) {
	e := NewEngine()
	e.AddPolicy(Policy{
		ID:      "test-1",
		Enabled: false,
		When:    PolicyCondition{ToolNames: []string{"web_search"}},
		Then:    PolicyAction{Effect: EffectDeny},
	})
	decisions := e.Evaluate(Checkpoint{ToolName: "web_search"})
	if len(decisions) != 0 {
		t.Error("disabled policy should not produce decisions")
	}
}

func TestEngine_Evaluate_SingleMatch(t *testing.T) {
	e := NewEngine()
	e.AddPolicy(Policy{
		ID:      "block-search",
		Enabled: true,
		When:    PolicyCondition{ToolNames: []string{"web_search"}},
		Then:    PolicyAction{Effect: EffectDeny, Message: "blocked"},
	})
	decisions := e.Evaluate(Checkpoint{ToolName: "web_search"})
	if len(decisions) != 1 {
		t.Fatalf("expected 1 decision, got %d", len(decisions))
	}
	if decisions[0].Effect != EffectDeny {
		t.Errorf("Effect = %q, want deny", decisions[0].Effect)
	}
	if decisions[0].PolicyID != "block-search" {
		t.Errorf("PolicyID = %q", decisions[0].PolicyID)
	}
}

func TestEngine_Evaluate_NoMatch(t *testing.T) {
	e := NewEngine()
	e.AddPolicy(Policy{
		ID:      "block-search",
		Enabled: true,
		When:    PolicyCondition{ToolNames: []string{"web_search"}},
		Then:    PolicyAction{Effect: EffectDeny},
	})
	decisions := e.Evaluate(Checkpoint{ToolName: "file_read"})
	if len(decisions) != 0 {
		t.Error("non-matching policy should not produce decisions")
	}
}

func TestEngine_Evaluate_MultipleMatches_Sorted(t *testing.T) {
	e := NewEngine()
	e.AddPolicy(Policy{
		ID:       "low-priority",
		Priority: 10,
		Enabled:  true,
		When:     PolicyCondition{},
		Then:     PolicyAction{Effect: EffectAllow},
	})
	e.AddPolicy(Policy{
		ID:       "high-priority",
		Priority: 1,
		Enabled:  true,
		When:     PolicyCondition{},
		Then:     PolicyAction{Effect: EffectDeny},
	})
	decisions := e.Evaluate(Checkpoint{})
	// deny 短路：高优先级 deny 后不再评估低优先级策略
	if len(decisions) != 1 {
		t.Fatalf("expected 1 decision (deny short-circuit), got %d", len(decisions))
	}
	if decisions[0].PolicyID != "high-priority" {
		t.Errorf("first decision should be high-priority, got %q", decisions[0].PolicyID)
	}
}

func TestEngine_RemovePolicy(t *testing.T) {
	e := NewEngine()
	e.AddPolicy(Policy{ID: "keep", Enabled: true})
	e.AddPolicy(Policy{ID: "remove", Enabled: true})
	e.RemovePolicy("remove")
	decisions := e.Evaluate(Checkpoint{})
	if len(decisions) != 1 {
		t.Errorf("after removal, should have 1 policy, got %d", len(decisions))
	}
}

func TestEngine_RemovePolicy_NonExistent(t *testing.T) {
	e := NewEngine()
	e.AddPolicy(Policy{ID: "a", Enabled: true})
	e.RemovePolicy("b") // should not panic
	decisions := e.Evaluate(Checkpoint{})
	if len(decisions) != 1 {
		t.Errorf("removing non-existent policy should not affect existing, got %d", len(decisions))
	}
}

func TestEngine_LoadFromYAML_Valid(t *testing.T) {
	e := NewEngine()
	yamlData := []byte(`
policies:
  - id: "test-1"
    name: "Test Policy"
    enabled: true
    priority: 1
    then:
      effect: deny
      message: "blocked"
`)
	err := e.LoadFromYAML(yamlData)
	if err != nil {
		t.Fatalf("LoadFromYAML: %v", err)
	}
	decisions := e.Evaluate(Checkpoint{})
	if len(decisions) != 1 {
		t.Errorf("expected 1 policy from YAML, got %d", len(decisions))
	}
}

func TestEngine_LoadFromYAML_Invalid(t *testing.T) {
	e := NewEngine()
	err := e.LoadFromYAML([]byte(`invalid: [`))
	if err == nil {
		t.Error("LoadFromYAML should return error for invalid YAML")
	}
}

func TestEngine_LoadFromYAML_Appends(t *testing.T) {
	e := NewEngine()
	e.AddPolicy(Policy{ID: "existing", Enabled: true})
	e.LoadFromYAML([]byte(`
policies:
  - id: "from-yaml"
    enabled: true
    then:
      effect: allow
`))
	decisions := e.Evaluate(Checkpoint{})
	if len(decisions) != 2 {
		t.Errorf("LoadFromYAML should append, got %d policies", len(decisions))
	}
}

// ============================================================
// DefaultPolicies tests
// ============================================================

func TestDefaultPolicies_Count(t *testing.T) {
	// 10 条：原 9 条 + l3-approval（L3 风险等级默认审批兜底策略）
	policies := DefaultPolicies()
	if len(policies) != 10 {
		t.Errorf("DefaultPolicies should return 10 policies, got %d", len(policies))
	}
}

func TestDefaultPolicies_AllHaveID(t *testing.T) {
	for _, p := range DefaultPolicies() {
		if p.ID == "" {
			t.Error("all default policies should have an ID")
		}
	}
}

func TestDefaultPolicies_AllEnabled(t *testing.T) {
	for _, p := range DefaultPolicies() {
		if !p.Enabled {
			t.Errorf("default policy %s should be enabled", p.ID)
		}
	}
}

// ============================================================
// Redact tests
// ============================================================

// TestRedact_ProtectSecretsSkKey 验证 protect-secrets 模板能对 sk- 开头的裸密钥脱敏。
func TestRedact_ProtectSecretsSkKey(t *testing.T) {
	e := newDefaultEngine()
	content := "调用失败，使用的密钥是 sk-abcdefghijklmnopqrstuvwxyz123456 请检查"
	redacted := e.Redact(content)
	if strings.Contains(redacted, "sk-abcdefghijklmnopqrstuvwxyz123456") {
		t.Errorf("sk- 密钥应被脱敏，结果: %q", redacted)
	}
	if !strings.Contains(redacted, "***") {
		t.Errorf("脱敏结果应包含 *** 占位符，结果: %q", redacted)
	}
	if !strings.Contains(redacted, "调用失败") {
		t.Errorf("非敏感内容应保持不变，结果: %q", redacted)
	}
}

// TestRedact_KeyValueForm 验证 key: value 形式的敏感字段被脱敏。
func TestRedact_KeyValueForm(t *testing.T) {
	e := newDefaultEngine()
	content := "配置为 api_key: abc123xyz 其余内容不变"
	redacted := e.Redact(content)
	if strings.Contains(redacted, "abc123xyz") {
		t.Errorf("api_key 值应被脱敏，结果: %q", redacted)
	}
	if !strings.Contains(redacted, "其余内容不变") {
		t.Errorf("非敏感内容应保持不变，结果: %q", redacted)
	}
}

// TestRedact_NoSensitiveContent 验证无敏感内容时原样返回。
func TestRedact_NoSensitiveContent(t *testing.T) {
	e := newDefaultEngine()
	content := "这是一段普通的输出内容，没有任何敏感信息。"
	if got := e.Redact(content); got != content {
		t.Errorf("普通内容不应被修改，got %q", got)
	}
}

// TestRedact_SkipsNonRedactAndDisabledPolicies 验证 Redact 只处理 enabled 且
// effect=redact 的策略：disabled 的 redact 策略与无 pattern 的 redact 策略均被跳过。
func TestRedact_SkipsNonRedactAndDisabledPolicies(t *testing.T) {
	e := NewEngine()
	e.AddPolicy(Policy{
		ID:      "disabled-redact",
		Enabled: false,
		When:    PolicyCondition{Pattern: "secret-word"},
		Then:    PolicyAction{Effect: EffectRedact},
	})
	e.AddPolicy(Policy{
		ID:      "no-pattern-redact",
		Enabled: true,
		// 无 pattern，应跳过
		Then: PolicyAction{Effect: EffectRedact},
	})
	e.AddPolicy(Policy{
		ID:      "deny-policy",
		Enabled: true,
		When:    PolicyCondition{Pattern: "secret-word"},
		Then:    PolicyAction{Effect: EffectDeny},
	})
	content := "this contains secret-word here"
	if got := e.Redact(content); got != content {
		t.Errorf("disabled/无 pattern/非 redact 策略不应参与脱敏，got %q", got)
	}

	e.AddPolicy(Policy{
		ID:      "enabled-redact",
		Enabled: true,
		When:    PolicyCondition{Pattern: "secret-word"},
		Then:    PolicyAction{Effect: EffectRedact},
	})
	if got := e.Redact(content); got != "this contains *** here" {
		t.Errorf("enabled redact 策略应生效，got %q", got)
	}
}
