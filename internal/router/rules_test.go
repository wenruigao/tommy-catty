package router

import (
	"strings"
	"testing"
)

// testAgent 构造测试用 AgentSpec（正则已预编译）。
func testAgent(name string, priority int, keywords []string, patterns []string) *AgentSpec {
	a := &AgentSpec{
		Name:         name,
		Description:  name + " 描述",
		SystemPrompt: "你是 " + name,
		Keywords:     keywords,
		Patterns:     patterns,
		Priority:     priority,
	}
	if err := a.compilePatterns(); err != nil {
		panic(err)
	}
	return a
}

func TestMatchRules_KeywordHit(t *testing.T) {
	agents := []*AgentSpec{
		testAgent("coder", 10, []string{"代码", "脚本"}, nil),
		testAgent("general", 0, nil, nil),
	}

	m := matchRules(agents, "帮我写个代码")
	if m == nil {
		t.Fatal("expected a rule match")
	}
	if m.agent.Name != "coder" {
		t.Errorf("matched agent = %q, want coder", m.agent.Name)
	}
	if m.confidence < 0.6 {
		t.Errorf("confidence = %v, want >= 0.6", m.confidence)
	}
}

func TestMatchRules_CaseInsensitive(t *testing.T) {
	agents := []*AgentSpec{testAgent("coder", 10, []string{"DEBUG", "Bug"}, nil)}

	for _, input := range []string{"debug this", "DEBUG THIS", "有个 bug", "有个 BUG"} {
		if m := matchRules(agents, input); m == nil {
			t.Errorf("input %q should match case-insensitively", input)
		}
	}
}

func TestMatchRules_NoHit(t *testing.T) {
	agents := []*AgentSpec{testAgent("coder", 10, []string{"代码"}, nil)}

	if m := matchRules(agents, "今天天气怎么样"); m != nil {
		t.Errorf("expected no match, got %+v", m)
	}
}

func TestMatchRules_EmptyInput(t *testing.T) {
	agents := []*AgentSpec{testAgent("coder", 10, []string{"代码"}, nil)}

	for _, input := range []string{"", "   ", "\t\n"} {
		if m := matchRules(agents, input); m != nil {
			t.Errorf("empty input %q should not match", input)
		}
	}
}

func TestMatchRules_MultipleHitsHigherConfidence(t *testing.T) {
	agents := []*AgentSpec{testAgent("coder", 10, []string{"代码", "脚本", "报错"}, nil)}

	single := matchRules(agents, "写个代码")
	multi := matchRules(agents, "这个代码脚本一直报错")

	if single == nil || multi == nil {
		t.Fatal("both inputs should match")
	}
	if multi.confidence <= single.confidence {
		t.Errorf("more hits should score higher: single=%v multi=%v",
			single.confidence, multi.confidence)
	}
}

func TestMatchRules_PatternMatch(t *testing.T) {
	agents := []*AgentSpec{
		testAgent("coder", 10, []string{"代码"}, nil),
		testAgent("researcher", 5, nil, []string{`^搜索.*`}),
	}

	m := matchRules(agents, "搜索一下相关资料")
	if m == nil || m.agent.Name != "researcher" {
		t.Fatalf("expected researcher via pattern, got %+v", m)
	}
	if m.confidence != confidencePattern {
		t.Errorf("pattern confidence = %v, want %v", m.confidence, confidencePattern)
	}
}

func TestMatchRules_PatternPlusKeywordBoost(t *testing.T) {
	agents := []*AgentSpec{testAgent("coder", 10, []string{"代码"}, []string{`^写.*`})}

	m := matchRules(agents, "写个代码")
	if m == nil {
		t.Fatal("expected match")
	}
	if !m.patternHit || m.hits == 0 {
		t.Errorf("expected both pattern and keyword hit: %+v", m)
	}
	// 0.90 + 0.15 = 1.05 超出上限，应封顶到 confidenceMaxRules
	if m.confidence != confidenceMaxRules {
		t.Errorf("confidence = %v, want capped %v", m.confidence, confidenceMaxRules)
	}
}

func TestMatchRules_ConfidenceCapped(t *testing.T) {
	kws := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	agents := []*AgentSpec{testAgent("x", 10, kws, nil)}

	m := matchRules(agents, "a b c d e f g h")
	if m == nil {
		t.Fatal("expected match")
	}
	if m.confidence > confidenceMaxRules {
		t.Errorf("confidence %v exceeds cap %v", m.confidence, confidenceMaxRules)
	}
}

func TestMatchRules_HigherConfidenceWinsOverPriority(t *testing.T) {
	// priority 高的只命中 1 个关键词；priority 低的命中 3 个 → 后者置信度更高应胜出
	agents := []*AgentSpec{
		testAgent("high", 100, []string{"数据"}, nil),
		testAgent("low", 1, []string{"查询", "统计", "报表"}, nil),
	}
	sortByPriority(agents)

	m := matchRules(agents, "查询统计报表")
	if m == nil {
		t.Fatal("expected match")
	}
	if m.agent.Name != "low" {
		t.Errorf("winner = %q, want low (higher confidence)", m.agent.Name)
	}
}

func TestMatchRules_SameConfidenceHigherPriorityWins(t *testing.T) {
	agents := []*AgentSpec{
		testAgent("lowprio", 1, []string{"共同词"}, nil),
		testAgent("highprio", 50, []string{"共同词"}, nil),
	}
	sortByPriority(agents)

	m := matchRules(agents, "共同词")
	if m == nil {
		t.Fatal("expected match")
	}
	if m.agent.Name != "highprio" {
		t.Errorf("winner = %q, want highprio", m.agent.Name)
	}
}

func TestSortByPriority_Descending(t *testing.T) {
	agents := []*AgentSpec{
		testAgent("a", 5, []string{"x"}, nil),
		testAgent("b", 20, []string{"x"}, nil),
		testAgent("c", 10, []string{"x"}, nil),
	}
	sortByPriority(agents)

	want := []string{"b", "c", "a"}
	for i, w := range want {
		if agents[i].Name != w {
			t.Errorf("order[%d] = %q, want %q", i, agents[i].Name, w)
		}
	}
}

func TestSortByPriority_StableForEqualPriority(t *testing.T) {
	agents := []*AgentSpec{
		testAgent("first", 5, []string{"x"}, nil),
		testAgent("second", 5, []string{"x"}, nil),
	}
	sortByPriority(agents)

	if agents[0].Name != "first" || agents[1].Name != "second" {
		t.Error("equal priority should preserve insertion order")
	}
}

func TestRuleMatch_Reason(t *testing.T) {
	agents := []*AgentSpec{testAgent("coder", 10, []string{"代码", "脚本"}, nil)}

	m := matchRules(agents, "代码和脚本")
	if m == nil {
		t.Fatal("expected match")
	}
	reason := m.reason()
	if !strings.Contains(reason, "代码") || !strings.Contains(reason, "脚本") {
		t.Errorf("reason should list matched terms: %q", reason)
	}
}

func TestRuleMatch_Reason_Nil(t *testing.T) {
	var m *ruleMatch
	if m.reason() != "" {
		t.Error("nil match reason should be empty")
	}
}

func TestRuleMatch_Reason_TruncatesManyMatches(t *testing.T) {
	kws := []string{"k1", "k2", "k3", "k4", "k5", "k6", "k7"}
	agents := []*AgentSpec{testAgent("x", 10, kws, nil)}

	m := matchRules(agents, "k1 k2 k3 k4 k5 k6 k7")
	if m == nil {
		t.Fatal("expected match")
	}
	reason := m.reason()
	if strings.Contains(reason, "k7") {
		t.Errorf("reason should truncate beyond 5 terms: %q", reason)
	}
	if !strings.Contains(reason, "7 项") {
		t.Errorf("reason should report total hit count: %q", reason)
	}
}

func TestMatchOne_EmptyKeywordSkipped(t *testing.T) {
	a := testAgent("x", 10, []string{"", "   "}, nil)
	// 空关键词不应造成误命中
	if m := matchOne(a, "任意输入"); m != nil {
		t.Errorf("empty keyword should not match, got %+v", m)
	}
}

func TestCompilePatterns_InvalidRegex(t *testing.T) {
	a := &AgentSpec{
		Name:         "bad",
		Description:  "d",
		SystemPrompt: "p",
		Patterns:     []string{"[未闭合"},
	}
	if err := a.compilePatterns(); err == nil {
		t.Error("invalid regex should fail to compile")
	}
}

func TestCompilePatterns_Idempotent(t *testing.T) {
	a := testAgent("x", 10, nil, []string{`^a`})
	before := len(a.compiled)
	if err := a.compilePatterns(); err != nil {
		t.Fatalf("second compile: %v", err)
	}
	if len(a.compiled) != before {
		t.Errorf("repeated compilePatterns should not duplicate: %d -> %d", before, len(a.compiled))
	}
}

func TestCompilePatterns_Valid(t *testing.T) {
	a := &AgentSpec{Patterns: []string{`^写.*代码$`, `修复\s+bug`}}
	if err := a.compilePatterns(); err != nil {
		t.Fatalf("valid patterns should compile: %v", err)
	}
	if len(a.compiled) != 2 {
		t.Errorf("compiled = %d, want 2", len(a.compiled))
	}
	if !a.compiled[0].MatchString("写一段代码") {
		t.Error("first pattern should match")
	}
}

func TestMinFloat(t *testing.T) {
	if got := minFloat(1.5, 2.5); got != 1.5 {
		t.Errorf("minFloat(1.5, 2.5) = %v, want 1.5", got)
	}
	if got := minFloat(3.0, 2.0); got != 2.0 {
		t.Errorf("minFloat(3.0, 2.0) = %v, want 2.0", got)
	}
}
