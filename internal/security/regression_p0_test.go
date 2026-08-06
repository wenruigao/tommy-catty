package security

import (
	"testing"
	"time"
)

// TestDefaultPolicies_OfficeHours_NotInverted 回归 P0 缺陷：
// office-hours 模板条件曾误写为工作时间 09:00-18:00，引擎"条件命中即执行效果"
// 导致语义反转（白天封禁 L3 工具、夜间反而放行）。
// 正确行为：工作时间放行，非工作时间（18:00-09:00）deny。
func TestDefaultPolicies_OfficeHours_NotInverted(t *testing.T) {
	eng := NewEngine()
	for _, p := range DefaultPolicies() {
		eng.AddPolicy(p)
	}

	denyByOfficeHours := func(ts time.Time) bool {
		for _, d := range eng.Evaluate(Checkpoint{
			Type:      "tool_call",
			ToolName:  "shell_exec",
			ToolRisk:  3,
			Timestamp: ts,
		}) {
			if d.PolicyID == "office-hours" && d.Effect == EffectDeny {
				return true
			}
		}
		return false
	}

	workTime := time.Date(2026, 8, 6, 10, 30, 0, 0, time.Local)
	if denyByOfficeHours(workTime) {
		t.Error("office-hours 不应在工作时间 10:30 拦截 L3 工具（语义反转缺陷复现）")
	}
	offTime := time.Date(2026, 8, 6, 22, 30, 0, 0, time.Local)
	if !denyByOfficeHours(offTime) {
		t.Error("office-hours 应在非工作时间 22:30 拦截 L3 工具")
	}
}

// TestLoadPolicies_YAMLPreferredAndFallback 回归 P0 缺陷：
// 模板与 policy.yaml 曾被同时加载，同名策略重复且语义互相干扰。
// 正确行为："YAML 优先、内置模板兜底"——YAML 存在时仅加载 YAML，
// YAML 缺失时回退到全部内置模板。
func TestLoadPolicies_YAMLPreferredAndFallback(t *testing.T) {
	yamlData := []byte("policies:\n" +
		"  - id: custom-only\n" +
		"    name: \"custom\"\n" +
		"    priority: 1\n" +
		"    enabled: true\n" +
		"    when:\n" +
		"      action_type: [task_start]\n" +
		"    then:\n" +
		"      effect: deny\n" +
		"      message: \"denied\"\n")

	eng := NewEngine()
	if err := eng.LoadPolicies(yamlData); err != nil {
		t.Fatalf("LoadPolicies(yaml): %v", err)
	}
	if got := eng.PolicyCount(); got != 1 {
		t.Errorf("YAML 存在时应仅加载 YAML，得到 %d 条策略（疑似模板被重复加载）", got)
	}

	fallback := NewEngine()
	if err := fallback.LoadPolicies(nil); err != nil {
		t.Fatalf("LoadPolicies(nil): %v", err)
	}
	if got := fallback.PolicyCount(); got != len(DefaultPolicies()) {
		t.Errorf("YAML 缺失时应回退内置模板（%d 条），得到 %d 条", len(DefaultPolicies()), got)
	}
}
