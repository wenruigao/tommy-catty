package router

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/wenruigao/tommy-catty/internal/engine"
	"github.com/wenruigao/tommy-catty/internal/llm"
)

// fakeLLM 可控的分类 LLM：返回预设内容或错误，并记录调用次数。
type fakeLLM struct {
	content string
	err     error
	delay   time.Duration
	calls   int
	gotMsgs []llm.Message
}

func (f *fakeLLM) Chat(ctx context.Context, messages []llm.Message, _ []llm.ToolDef) (llm.ChatResponse, error) {
	f.calls++
	f.gotMsgs = messages
	// 真实 LLM 客户端会尊重已取消/超时的 context，fake 同样如此
	if err := ctx.Err(); err != nil {
		return llm.ChatResponse{}, err
	}
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return llm.ChatResponse{}, ctx.Err()
		}
	}
	if f.err != nil {
		return llm.ChatResponse{}, f.err
	}
	return llm.ChatResponse{Content: f.content}, nil
}

// testAgents 构造一组标准测试 Agent。
func testAgents() map[string]*AgentSpec {
	return map[string]*AgentSpec{
		"general": {
			Name: "general", Description: "通用助手", SystemPrompt: "你是通用助手",
		},
		"coder": {
			Name: "coder", Description: "编程专家", SystemPrompt: "你是工程师",
			Tools: []string{"code_run"}, Keywords: []string{"代码"}, Priority: 20,
		},
		"researcher": {
			Name: "researcher", Description: "调研专家", SystemPrompt: "你是研究员",
			Tools: []string{"web_search"}, Keywords: []string{"搜索"}, Priority: 10,
		},
	}
}

// newTestRouter 构建测试路由器。llmClient 参数类型为接口，
// 传 nil 时得到真正的 nil 接口（若声明为 *fakeLLM 会得到 typed-nil，
// 使 r.llm != nil 成立却在调用时 panic）。
func newTestRouter(t *testing.T, llmClient engine.LLMClient, cfg Config) *Router {
	t.Helper()
	cfg.FallbackAgent = "general"
	r, err := NewRouter(testAgents(), cfg, llmClient)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return r
}

// ============================================================
// 规则优先
// ============================================================

func TestClassify_RuleHit(t *testing.T) {
	fake := &fakeLLM{content: `{"agent":"researcher","confidence":0.9}`}
	r := newTestRouter(t, fake, DefaultConfig())

	d := r.Classify(context.Background(), "帮我写个代码")

	if d.Agent != "coder" {
		t.Errorf("agent = %q, want coder", d.Agent)
	}
	if d.Method != MethodRule {
		t.Errorf("method = %q, want rule", d.Method)
	}
	if fake.calls != 0 {
		t.Errorf("规则命中时不应调用 LLM，实际调用 %d 次", fake.calls)
	}
	if d.Reason == "" {
		t.Error("reason should not be empty")
	}
}

func TestClassify_RuleHit_PopulatesWhitelistSource(t *testing.T) {
	r := newTestRouter(t, nil, DefaultConfig())
	d := r.Classify(context.Background(), "这段代码有问题")

	spec := r.Agent(d.Agent)
	if spec == nil {
		t.Fatalf("Agent(%q) returned nil", d.Agent)
	}
	if len(spec.Tools) != 1 || spec.Tools[0] != "code_run" {
		t.Errorf("coder tools = %v", spec.Tools)
	}
}

// ============================================================
// LLM 兜底
// ============================================================

func TestClassify_LLMFallback(t *testing.T) {
	fake := &fakeLLM{content: `{"agent":"researcher","confidence":0.85,"reason":"需要检索"}`}
	r := newTestRouter(t, fake, DefaultConfig())

	d := r.Classify(context.Background(), "帮我了解一下这个行业的情况")

	if d.Agent != "researcher" {
		t.Errorf("agent = %q, want researcher", d.Agent)
	}
	if d.Method != MethodLLM {
		t.Errorf("method = %q, want llm", d.Method)
	}
	if d.Confidence != 0.85 {
		t.Errorf("confidence = %v, want 0.85", d.Confidence)
	}
	if d.Reason != "需要检索" {
		t.Errorf("reason = %q", d.Reason)
	}
	if fake.calls != 1 {
		t.Errorf("LLM calls = %d, want 1", fake.calls)
	}
}

func TestClassify_LLM_NoToolsPassed(t *testing.T) {
	fake := &fakeLLM{content: `{"agent":"general","confidence":0.9}`}
	r := newTestRouter(t, fake, DefaultConfig())
	r.Classify(context.Background(), "随便聊聊")

	// 分类是纯文本任务，不应传工具定义
	if len(fake.gotMsgs) != 2 {
		t.Fatalf("expected system+user messages, got %d", len(fake.gotMsgs))
	}
	if fake.gotMsgs[0].Role != "system" {
		t.Errorf("first message role = %q, want system", fake.gotMsgs[0].Role)
	}
}

func TestClassify_LLM_ToleratesSurroundingText(t *testing.T) {
	fake := &fakeLLM{content: "好的，我的判断是：\n```json\n{\"agent\":\"coder\",\"confidence\":0.9}\n```\n以上。"}
	r := newTestRouter(t, fake, DefaultConfig())

	d := r.Classify(context.Background(), "无任何规则触发词的问题")
	if d.Agent != "coder" || d.Method != MethodLLM {
		t.Errorf("expected coder via llm, got %q/%q", d.Agent, d.Method)
	}
}

func TestClassify_LLM_PercentConfidenceNormalized(t *testing.T) {
	fake := &fakeLLM{content: `{"agent":"coder","confidence":85}`}
	r := newTestRouter(t, fake, DefaultConfig())

	d := r.Classify(context.Background(), "无触发词")
	if d.Confidence != 0.85 {
		t.Errorf("confidence = %v, want 0.85 (normalized from 85)", d.Confidence)
	}
	if d.Method != MethodLLM {
		t.Errorf("method = %q, want llm", d.Method)
	}
}

func TestClassify_UseLLMDisabled(t *testing.T) {
	fake := &fakeLLM{content: `{"agent":"coder","confidence":0.99}`}
	cfg := DefaultConfig()
	cfg.UseLLM = false
	r := newTestRouter(t, fake, cfg)

	d := r.Classify(context.Background(), "无触发词")
	if d.Method != MethodFallback {
		t.Errorf("method = %q, want fallback when use_llm=false", d.Method)
	}
	if fake.calls != 0 {
		t.Errorf("LLM should not be called, got %d calls", fake.calls)
	}
}

func TestClassify_NilLLM_FallsBack(t *testing.T) {
	r := newTestRouter(t, nil, DefaultConfig())

	d := r.Classify(context.Background(), "无触发词")
	if d.Agent != "general" || d.Method != MethodFallback {
		t.Errorf("got %q/%q, want general/fallback", d.Agent, d.Method)
	}
}

// TestClassify_TypedNilLLM_DoesNotPanic 锁定 panic 防护：
// 接口非 nil 但底层指针为 nil 时（错误装配），Chat 解引用会 panic，
// 路由必须捕获并回落，而不是让 panic 冒泡中断用户请求。
func TestClassify_TypedNilLLM_DoesNotPanic(t *testing.T) {
	var typedNil *fakeLLM // 接口 != nil，但调用即 panic
	r := newTestRouter(t, typedNil, DefaultConfig())

	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("Classify must not panic on typed-nil LLM client, got: %v", rec)
		}
	}()

	d := r.Classify(context.Background(), "无触发词")
	if d.Agent != "general" || d.Method != MethodFallback {
		t.Errorf("got %q/%q, want general/fallback", d.Agent, d.Method)
	}
}

// ============================================================
// 失败降级：绝不阻断请求
// ============================================================

func TestClassify_LLMError_FallsBack(t *testing.T) {
	fake := &fakeLLM{err: errors.New("provider down")}
	r := newTestRouter(t, fake, DefaultConfig())

	d := r.Classify(context.Background(), "无触发词")
	if d.Agent != "general" || d.Method != MethodFallback {
		t.Errorf("got %q/%q, want general/fallback", d.Agent, d.Method)
	}
}

func TestClassify_LLMTimeout_FallsBack(t *testing.T) {
	fake := &fakeLLM{content: `{"agent":"coder","confidence":0.9}`, delay: 200 * time.Millisecond}
	cfg := DefaultConfig()
	cfg.LLMTimeout = 20 * time.Millisecond
	r := newTestRouter(t, fake, cfg)

	start := time.Now()
	d := r.Classify(context.Background(), "无触发词")
	elapsed := time.Since(start)

	if d.Method != MethodFallback {
		t.Errorf("method = %q, want fallback on timeout", d.Method)
	}
	if elapsed > 150*time.Millisecond {
		t.Errorf("should not wait for slow LLM, took %v", elapsed)
	}
}

func TestClassify_InvalidJSON_FallsBack(t *testing.T) {
	fake := &fakeLLM{content: "这不是 JSON"}
	r := newTestRouter(t, fake, DefaultConfig())

	d := r.Classify(context.Background(), "无触发词")
	if d.Method != MethodFallback {
		t.Errorf("method = %q, want fallback on invalid JSON", d.Method)
	}
}

func TestClassify_EmptyResponse_FallsBack(t *testing.T) {
	fake := &fakeLLM{content: "   "}
	r := newTestRouter(t, fake, DefaultConfig())

	if d := r.Classify(context.Background(), "无触发词"); d.Method != MethodFallback {
		t.Errorf("method = %q, want fallback on empty response", d.Method)
	}
}

func TestClassify_UnknownAgent_FallsBack(t *testing.T) {
	fake := &fakeLLM{content: `{"agent":"hacker_agent","confidence":0.99}`}
	r := newTestRouter(t, fake, DefaultConfig())

	d := r.Classify(context.Background(), "无触发词")
	if d.Agent != "general" {
		t.Errorf("agent = %q, must not accept hallucinated agent", d.Agent)
	}
	if d.Method != MethodFallback {
		t.Errorf("method = %q, want fallback", d.Method)
	}
}

func TestClassify_MissingAgentField_FallsBack(t *testing.T) {
	fake := &fakeLLM{content: `{"confidence":0.9}`}
	r := newTestRouter(t, fake, DefaultConfig())

	if d := r.Classify(context.Background(), "无触发词"); d.Method != MethodFallback {
		t.Errorf("method = %q, want fallback", d.Method)
	}
}

func TestClassify_LowConfidence_FallsBack(t *testing.T) {
	fake := &fakeLLM{content: `{"agent":"coder","confidence":0.3}`}
	cfg := DefaultConfig()
	cfg.ConfidenceThreshold = 0.6
	r := newTestRouter(t, fake, cfg)

	d := r.Classify(context.Background(), "无触发词")
	if d.Agent != "general" || d.Method != MethodFallback {
		t.Errorf("got %q/%q, want general/fallback for low confidence", d.Agent, d.Method)
	}
}

func TestClassify_LowConfidenceRule_FallsToLLM(t *testing.T) {
	// 阈值抬高到 0.99，规则命中（0.75）不再达标 → 转 LLM
	fake := &fakeLLM{content: `{"agent":"researcher","confidence":0.995}`}
	cfg := DefaultConfig()
	cfg.ConfidenceThreshold = 0.99
	r := newTestRouter(t, fake, cfg)

	d := r.Classify(context.Background(), "写个代码")
	if d.Agent != "researcher" || d.Method != MethodLLM {
		t.Errorf("got %q/%q, want researcher/llm", d.Agent, d.Method)
	}
}

func TestClassify_CancelledContext_FallsBack(t *testing.T) {
	fake := &fakeLLM{content: `{"agent":"coder","confidence":0.9}`}
	r := newTestRouter(t, fake, DefaultConfig())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	d := r.Classify(ctx, "无触发词")
	if d.Method != MethodFallback {
		t.Errorf("method = %q, want fallback on cancelled context", d.Method)
	}
}

func TestClassify_NeverReturnsError(t *testing.T) {
	// 无论 LLM 如何异常，Classify 都必须返回可用的 Decision（不阻断请求）
	cases := []*fakeLLM{
		{err: errors.New("boom")},
		{content: ""},
		{content: "{{{"},
		{content: `{"agent":"","confidence":1}`},
		{content: `{"agent":"nope","confidence":-5}`},
	}
	for i, fake := range cases {
		r := newTestRouter(t, fake, DefaultConfig())
		d := r.Classify(context.Background(), "无触发词")
		if d.Agent == "" {
			t.Errorf("case %d: Agent must never be empty", i)
		}
		if r.Agent(d.Agent) == nil {
			t.Errorf("case %d: Agent %q must exist in registry", i, d.Agent)
		}
	}
}

// ============================================================
// NewRouter 校验
// ============================================================

func TestNewRouter_ValidationError(t *testing.T) {
	tests := []struct {
		name   string
		agents map[string]*AgentSpec
		cfg    Config
	}{
		{
			name:   "empty agents",
			agents: map[string]*AgentSpec{},
			cfg:    Config{FallbackAgent: "general"},
		},
		{
			name:   "fallback agent missing",
			agents: map[string]*AgentSpec{"coder": {Name: "coder", Description: "d", SystemPrompt: "p", Keywords: []string{"x"}}},
			cfg:    Config{FallbackAgent: "general"},
		},
		{
			name: "agent missing description",
			agents: map[string]*AgentSpec{
				"general": {Name: "general", Description: "d", SystemPrompt: "p"},
				"bad":     {Name: "bad", SystemPrompt: "p", Keywords: []string{"x"}},
			},
			cfg: Config{FallbackAgent: "general"},
		},
		{
			name: "invalid regex",
			agents: map[string]*AgentSpec{
				"general": {Name: "general", Description: "d", SystemPrompt: "p"},
				"bad":     {Name: "bad", Description: "d", SystemPrompt: "p", Patterns: []string{"[未闭合"}},
			},
			cfg: Config{FallbackAgent: "general"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewRouter(tt.agents, tt.cfg, nil); err == nil {
				t.Error("expected validation error")
			}
		})
	}
}

func TestNewRouter_NormalizesConfig(t *testing.T) {
	agents := map[string]*AgentSpec{
		"general": {Name: "general", Description: "d", SystemPrompt: "p"},
	}
	r, err := NewRouter(agents, Config{}, nil) // 全零配置
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	if r.cfg.LLMTimeout != 5*time.Second {
		t.Errorf("LLMTimeout = %v, want 5s default", r.cfg.LLMTimeout)
	}
	if r.cfg.ConfidenceThreshold != 0.6 {
		t.Errorf("ConfidenceThreshold = %v, want 0.6 default", r.cfg.ConfidenceThreshold)
	}
	if r.fallback != "general" {
		t.Errorf("fallback = %q, want general", r.fallback)
	}
}

func TestNewRouter_SortsByPriority(t *testing.T) {
	agents := map[string]*AgentSpec{
		"general": {Name: "general", Description: "d", SystemPrompt: "p"},
		"low":     {Name: "low", Description: "d", SystemPrompt: "p", Keywords: []string{"x"}, Priority: 1},
		"high":    {Name: "high", Description: "d", SystemPrompt: "p", Keywords: []string{"x"}, Priority: 99},
	}
	r, err := NewRouter(agents, DefaultConfig(), nil)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	if r.order[0].Name != "high" {
		t.Errorf("order[0] = %q, want high", r.order[0].Name)
	}
}

// ============================================================
// 访问器
// ============================================================

func TestRouter_Agent(t *testing.T) {
	r := newTestRouter(t, nil, DefaultConfig())

	if r.Agent("coder") == nil {
		t.Error("Agent(coder) should exist")
	}
	if r.Agent("nonexistent") != nil {
		t.Error("Agent(nonexistent) should be nil")
	}
}

func TestRouter_FallbackAgent(t *testing.T) {
	r := newTestRouter(t, nil, DefaultConfig())
	if r.FallbackAgent() != "general" {
		t.Errorf("FallbackAgent = %q, want general", r.FallbackAgent())
	}
}

func TestRouter_AgentNames(t *testing.T) {
	r := newTestRouter(t, nil, DefaultConfig())
	names := r.AgentNames()
	if len(names) != 3 {
		t.Fatalf("AgentNames len = %d, want 3", len(names))
	}
	joined := strings.Join(names, ",")
	for _, want := range []string{"general", "coder", "researcher"} {
		if !strings.Contains(joined, want) {
			t.Errorf("AgentNames missing %q: %v", want, names)
		}
	}
	// priority 降序：coder(20) > researcher(10) > general(0)
	if names[0] != "coder" {
		t.Errorf("names[0] = %q, want coder (highest priority)", names[0])
	}
}

func TestRouter_NilReceiverSafe(t *testing.T) {
	var r *Router

	if d := r.Classify(context.Background(), "x"); d.Agent != "" {
		t.Errorf("nil router Classify should return zero Decision, got %+v", d)
	}
	if r.Agent("x") != nil {
		t.Error("nil router Agent should return nil")
	}
	if r.FallbackAgent() != "" {
		t.Error("nil router FallbackAgent should return empty")
	}
	if r.AgentNames() != nil {
		t.Error("nil router AgentNames should return nil")
	}
}

// ============================================================
// parseClassifyResponse
// ============================================================

func TestParseClassifyResponse(t *testing.T) {
	tests := []struct {
		name       string
		content    string
		wantAgent  string
		wantConf   float64
		wantReason string
		wantErr    bool
	}{
		{"clean", `{"agent":"coder","confidence":0.8,"reason":"r"}`, "coder", 0.8, "r", false},
		{"with prose", `判断如下 {"agent":"writer","confidence":0.7} 完毕`, "writer", 0.7, "", false},
		{"fenced", "```json\n{\"agent\":\"analyst\",\"confidence\":1.0}\n```", "analyst", 1.0, "", false},
		{"percent", `{"agent":"coder","confidence":75}`, "coder", 0.75, "", false},
		{"negative clamped", `{"agent":"coder","confidence":-3}`, "coder", 0, "", false},
		{"empty", "   ", "", 0, "", true},
		{"no json", "抱歉我无法分类", "", 0, "", true},
		{"broken json", `{"agent":`, "", 0, "", true},
		{"missing agent", `{"confidence":0.9}`, "", 0, "", true},
		{"blank agent", `{"agent":"  ","confidence":0.9}`, "", 0, "", true},
		{"trims agent", `{"agent":" coder ","confidence":0.9}`, "coder", 0.9, "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent, conf, reason, err := parseClassifyResponse(tt.content)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got agent=%q conf=%v", agent, conf)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if agent != tt.wantAgent {
				t.Errorf("agent = %q, want %q", agent, tt.wantAgent)
			}
			if conf != tt.wantConf {
				t.Errorf("confidence = %v, want %v", conf, tt.wantConf)
			}
			if reason != tt.wantReason {
				t.Errorf("reason = %q, want %q", reason, tt.wantReason)
			}
		})
	}
}

// ============================================================
// AgentSpec 校验
// ============================================================

func TestAgentSpec_Validate(t *testing.T) {
	valid := &AgentSpec{Name: "coder", Description: "d", SystemPrompt: "p", Keywords: []string{"x"}}
	if err := valid.Validate(false); err != nil {
		t.Errorf("valid spec should pass: %v", err)
	}

	// 兜底 Agent 允许没有触发规则
	fallback := &AgentSpec{Name: "general", Description: "d", SystemPrompt: "p"}
	if err := fallback.Validate(true); err != nil {
		t.Errorf("fallback agent without rules should pass: %v", err)
	}
	// 非兜底 Agent 必须有触发规则
	if err := fallback.Validate(false); err == nil {
		t.Error("non-fallback agent without rules should fail")
	}

	tests := []struct {
		name string
		spec *AgentSpec
	}{
		{"empty name", &AgentSpec{Description: "d", SystemPrompt: "p", Keywords: []string{"x"}}},
		{"empty description", &AgentSpec{Name: "a", SystemPrompt: "p", Keywords: []string{"x"}}},
		{"empty system prompt", &AgentSpec{Name: "a", Description: "d", Keywords: []string{"x"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.spec.Validate(false); err == nil {
				t.Error("expected validation error")
			}
		})
	}
}

func TestValidateAgents_KeyNameMismatch(t *testing.T) {
	agents := map[string]*AgentSpec{
		"general": {Name: "general", Description: "d", SystemPrompt: "p"},
		"coder":   {Name: "wrong_name", Description: "d", SystemPrompt: "p", Keywords: []string{"x"}},
	}
	if err := ValidateAgents(agents, "general"); err == nil {
		t.Error("key/name mismatch should fail")
	}
}

// ============================================================
// Prompt 构建
// ============================================================

func TestBuildClassifyPrompt(t *testing.T) {
	agents := []*AgentSpec{
		{Name: "coder", Description: "编程专家", Tools: []string{"code_run", "shell_exec"}},
		{Name: "general", Description: "通用助手"},
	}

	p := buildClassifyPrompt(agents, "写个快排", "general")

	for _, want := range []string{"coder", "编程专家", "code_run", "general", "写个快排"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "{{") {
		t.Error("unreplaced placeholder in prompt")
	}
}

func TestBuildClassifyPrompt_TruncatesLongInput(t *testing.T) {
	agents := []*AgentSpec{{Name: "general", Description: "d"}}
	long := strings.Repeat("字", 2000)

	p := buildClassifyPrompt(agents, long, "general")

	if strings.Contains(p, long) {
		t.Error("long input should be truncated")
	}
	if !strings.Contains(p, "已截断") {
		t.Error("truncation should be marked")
	}
	if runes := len([]rune(p)); runes > 1500 {
		t.Errorf("prompt too long after truncation: %d runes", runes)
	}
}

// ============================================================
// Config
// ============================================================

func TestConfig_Normalize(t *testing.T) {
	c := Config{}
	c.Normalize()

	if c.LLMTimeout != 5*time.Second {
		t.Errorf("LLMTimeout = %v", c.LLMTimeout)
	}
	if c.ConfidenceThreshold != 0.6 {
		t.Errorf("ConfidenceThreshold = %v", c.ConfidenceThreshold)
	}
	if c.FallbackAgent != "general" {
		t.Errorf("FallbackAgent = %q", c.FallbackAgent)
	}
}

func TestConfig_Normalize_PreservesExplicitValues(t *testing.T) {
	c := Config{LLMTimeout: 2 * time.Second, ConfidenceThreshold: 0.9, FallbackAgent: "default"}
	c.Normalize()

	if c.LLMTimeout != 2*time.Second || c.ConfidenceThreshold != 0.9 || c.FallbackAgent != "default" {
		t.Errorf("Normalize overwrote explicit values: %+v", c)
	}
}

func TestDefaultConfig(t *testing.T) {
	c := DefaultConfig()
	if !c.UseLLM {
		t.Error("UseLLM should default to true")
	}
	if c.Enabled {
		t.Error("Enabled defaults to false (zero value)")
	}
}
