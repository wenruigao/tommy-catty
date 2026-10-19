package bootstrap

import (
	"context"
	"strings"
	"testing"

	"github.com/wenruigao/tommy-catty/config"
	"github.com/wenruigao/tommy-catty/internal/engine"
	"github.com/wenruigao/tommy-catty/internal/llm"
	"github.com/wenruigao/tommy-catty/internal/tool"
)

// noopLLM 装配测试用的空 LLM 客户端（路由器仅在规则未命中时才调用它）。
type noopLLM struct{}

func (noopLLM) Chat(_ context.Context, _ []llm.Message, _ []llm.ToolDef) (llm.ChatResponse, error) {
	return llm.ChatResponse{Content: `{}`}, nil
}

// routerTestRegistry 构造含常用工具的注册表。
func routerTestRegistry(t *testing.T) *tool.Registry {
	t.Helper()
	reg := tool.NewRegistry()
	for _, name := range []string{"code_run", "web_search", "db_query", "file_read"} {
		reg.Register(&stubRouterTool{name: name}, tool.RiskReadOnly, 0)
	}
	return reg
}

type stubRouterTool struct{ name string }

func (s *stubRouterTool) Name() string                { return s.name }
func (s *stubRouterTool) Description() string         { return s.name }
func (s *stubRouterTool) Parameters() tool.JSONSchema { return tool.JSONSchema{Type: "object"} }
func (s *stubRouterTool) Execute(_ context.Context, _ map[string]interface{}) (tool.Result, error) {
	return tool.Result{Output: "ok"}, nil
}

// baseRouterConfig 构造一份合法的路由配置。
func baseRouterConfig() *config.Config {
	cfg := config.Default()
	cfg.Router.Enabled = true
	cfg.Router.FallbackAgent = "general"
	cfg.Router.Agents = map[string]config.RouterAgentEntry{
		"general": {
			Description:  "通用助手",
			SystemPrompt: "你是通用助手",
		},
		"coder": {
			Description:  "编程专家",
			SystemPrompt: "你是工程师",
			Tools:        []string{"code_run", "file_read"},
			Keywords:     []string{"代码"},
			Priority:     20,
		},
	}
	return cfg
}

var _ engine.LLMClient = noopLLM{}

// ============================================================
// 装配成功路径
// ============================================================

func TestRegisterRouter_Success(t *testing.T) {
	cfg := baseRouterConfig()
	reg := routerTestRegistry(t)

	r, warnings, err := RegisterRouter(cfg, reg, noopLLM{})
	if err != nil {
		t.Fatalf("RegisterRouter: %v", err)
	}
	if r == nil {
		t.Fatal("router should not be nil when enabled")
	}
	// 全部白名单工具都已注册 → 不应有告警
	if len(warnings) != 0 {
		t.Errorf("expected no warnings, got %v", warnings)
	}
	if r.FallbackAgent() != "general" {
		t.Errorf("fallback = %q, want general", r.FallbackAgent())
	}
	// priority 降序：coder(20) 在 general(0) 之前
	names := r.AgentNames()
	if names[0] != "coder" {
		t.Errorf("names[0] = %q, want coder (highest priority)", names[0])
	}
}

func TestRegisterRouter_PropagatesConfig(t *testing.T) {
	cfg := baseRouterConfig()
	cfg.Router.LLMTimeout = "3s"
	cfg.Router.ConfidenceThreshold = 0.75
	useLLM := false
	cfg.Router.UseLLM = &useLLM

	r, _, err := RegisterRouter(cfg, routerTestRegistry(t), noopLLM{})
	if err != nil {
		t.Fatalf("RegisterRouter: %v", err)
	}
	if r == nil {
		t.Fatal("router should not be nil")
	}

	// 通过行为验证配置生效：use_llm=false 时规则未命中即回落，不调用 LLM
	d := r.Classify(context.Background(), "完全不匹配任何规则的输入")
	if d.Method != "fallback" {
		t.Errorf("method = %q, want fallback (use_llm=false)", d.Method)
	}
	if d.Agent != "general" {
		t.Errorf("agent = %q, want general", d.Agent)
	}
}

func TestRegisterRouter_AgentSpecFieldsMapped(t *testing.T) {
	cfg := baseRouterConfig()
	cfg.Router.Agents["coder"] = config.RouterAgentEntry{
		Description:   "编程专家",
		SystemPrompt:  "你是工程师",
		Tools:         []string{"code_run"},
		Keywords:      []string{"代码", "脚本"},
		Patterns:      []string{`^写.*`},
		Priority:      42,
		MaxIterations: 30,
	}

	r, _, err := RegisterRouter(cfg, routerTestRegistry(t), noopLLM{})
	if err != nil {
		t.Fatalf("RegisterRouter: %v", err)
	}

	spec := r.Agent("coder")
	if spec == nil {
		t.Fatal("coder agent not found")
	}
	if spec.Description != "编程专家" || spec.SystemPrompt != "你是工程师" {
		t.Errorf("description/system_prompt not mapped: %+v", spec)
	}
	if len(spec.Tools) != 1 || spec.Tools[0] != "code_run" {
		t.Errorf("tools = %v", spec.Tools)
	}
	if len(spec.Keywords) != 2 {
		t.Errorf("keywords = %v", spec.Keywords)
	}
	if len(spec.Patterns) != 1 {
		t.Errorf("patterns = %v", spec.Patterns)
	}
	if spec.Priority != 42 {
		t.Errorf("priority = %d, want 42", spec.Priority)
	}
	if spec.MaxIterations != 30 {
		t.Errorf("max_iterations = %d, want 30", spec.MaxIterations)
	}
}

// ============================================================
// 禁用与空配置：零影响
// ============================================================

func TestRegisterRouter_Disabled(t *testing.T) {
	cfg := baseRouterConfig()
	cfg.Router.Enabled = false

	r, names, err := RegisterRouter(cfg, routerTestRegistry(t), noopLLM{})
	if err != nil {
		t.Fatalf("disabled should not error: %v", err)
	}
	if r != nil {
		t.Error("router should be nil when disabled")
	}
	if names != nil {
		t.Errorf("names should be nil when disabled, got %v", names)
	}
}

func TestRegisterRouter_DefaultConfigDisabled(t *testing.T) {
	cfg := config.Default() // router.enabled 默认 false

	r, _, err := RegisterRouter(cfg, routerTestRegistry(t), noopLLM{})
	if err != nil || r != nil {
		t.Errorf("default config should yield nil router, got r=%v err=%v", r, err)
	}
}

func TestRegisterRouter_EnabledButNoAgents(t *testing.T) {
	cfg := config.Default()
	cfg.Router.Enabled = true
	cfg.Router.Agents = nil

	r, warnings, err := RegisterRouter(cfg, routerTestRegistry(t), noopLLM{})
	if err != nil {
		t.Fatalf("should warn, not error: %v", err)
	}
	if r != nil {
		t.Error("router should be nil when no agents defined")
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "未定义 Agent") {
		t.Errorf("expected a warning about missing agents, got %v", warnings)
	}
}

// ============================================================
// 校验失败：启动期报错而非静默降级
// ============================================================

// TestRegisterRouter_UnknownToolWarns 白名单引用未注册工具时**告警而非报错**：
// web_search / db_query / kb_* 等工具有条件注册（依赖 search / databases /
// knowledge_bases 配置段），未配相应能力时它们本就不存在。白名单无法凭空授予
// 能力，故不构成安全问题；且路由不应因此拒绝启动。
func TestRegisterRouter_UnknownToolWarns(t *testing.T) {
	cfg := baseRouterConfig()
	entry := cfg.Router.Agents["coder"]
	entry.Tools = []string{"code_run", "tool_that_does_not_exist"}
	cfg.Router.Agents["coder"] = entry

	r, warnings, err := RegisterRouter(cfg, routerTestRegistry(t), noopLLM{})
	if err != nil {
		t.Fatalf("unknown tool should warn, not fail: %v", err)
	}
	if r == nil {
		t.Fatal("router should still be constructed")
	}
	if len(warnings) != 1 {
		t.Fatalf("expected 1 warning, got %v", warnings)
	}
	if !strings.Contains(warnings[0], "tool_that_does_not_exist") {
		t.Errorf("warning should name the offending tool: %q", warnings[0])
	}
	if !strings.Contains(warnings[0], "coder") {
		t.Errorf("warning should name the agent: %q", warnings[0])
	}
}

// TestRegisterRouter_UnknownToolStillRoutes 白名单含未注册工具时，
// 已注册的工具仍正常生效（不因个别缺失而整体失效）。
func TestRegisterRouter_UnknownToolStillRoutes(t *testing.T) {
	cfg := baseRouterConfig()
	entry := cfg.Router.Agents["coder"]
	entry.Tools = []string{"code_run", "web_search"} // web_search 未注册
	cfg.Router.Agents["coder"] = entry

	r, _, err := RegisterRouter(cfg, routerTestRegistry(t), noopLLM{})
	if err != nil {
		t.Fatalf("RegisterRouter: %v", err)
	}
	spec := r.Agent("coder")
	if spec == nil {
		t.Fatal("coder agent missing")
	}
	// 白名单原样保留；过滤发生在 ToToolDefs（只返回注册表中真实存在的工具）
	if len(spec.Tools) != 2 {
		t.Errorf("tools = %v, want both entries preserved", spec.Tools)
	}

	d := r.Classify(context.Background(), "写个代码")
	if d.Agent != "coder" {
		t.Errorf("agent = %q, want coder", d.Agent)
	}
}

func TestRegisterRouter_MissingFallbackAgentFails(t *testing.T) {
	cfg := baseRouterConfig()
	cfg.Router.FallbackAgent = "nonexistent_agent"

	_, _, err := RegisterRouter(cfg, routerTestRegistry(t), noopLLM{})
	if err == nil {
		t.Fatal("missing fallback agent must fail")
	}
	if !strings.Contains(err.Error(), "nonexistent_agent") {
		t.Errorf("error should name the missing fallback: %v", err)
	}
}

func TestRegisterRouter_InvalidRegexFails(t *testing.T) {
	cfg := baseRouterConfig()
	entry := cfg.Router.Agents["coder"]
	entry.Patterns = []string{"[未闭合"}
	cfg.Router.Agents["coder"] = entry

	_, _, err := RegisterRouter(cfg, routerTestRegistry(t), noopLLM{})
	if err == nil {
		t.Fatal("invalid regex must fail at startup")
	}
}

func TestRegisterRouter_MissingDescriptionFails(t *testing.T) {
	cfg := baseRouterConfig()
	entry := cfg.Router.Agents["coder"]
	entry.Description = ""
	cfg.Router.Agents["coder"] = entry

	if _, _, err := RegisterRouter(cfg, routerTestRegistry(t), noopLLM{}); err == nil {
		t.Fatal("agent without description must fail")
	}
}

func TestRegisterRouter_MissingSystemPromptFails(t *testing.T) {
	cfg := baseRouterConfig()
	entry := cfg.Router.Agents["coder"]
	entry.SystemPrompt = ""
	cfg.Router.Agents["coder"] = entry

	if _, _, err := RegisterRouter(cfg, routerTestRegistry(t), noopLLM{}); err == nil {
		t.Fatal("agent without system_prompt must fail")
	}
}

func TestRegisterRouter_NonFallbackAgentWithoutRulesFails(t *testing.T) {
	cfg := baseRouterConfig()
	// coder 非兜底 Agent，却无任何触发规则
	cfg.Router.Agents["coder"] = config.RouterAgentEntry{
		Description:  "编程专家",
		SystemPrompt: "你是工程师",
		Tools:        []string{"code_run"},
	}

	if _, _, err := RegisterRouter(cfg, routerTestRegistry(t), noopLLM{}); err == nil {
		t.Fatal("non-fallback agent without keywords/patterns must fail")
	}
}

// ============================================================
// 白名单只收窄不扩张
// ============================================================

// TestRegisterRouter_WhitelistCannotExceedRegistry 白名单引用注册表之外的工具时告警。
// 安全性由 ToToolDefs 保证：它只返回注册表中真实存在的工具，
// 因此白名单永远无法凭空授予能力。
func TestRegisterRouter_WhitelistCannotExceedRegistry(t *testing.T) {
	reg := tool.NewRegistry() // 空注册表
	reg.Register(&stubRouterTool{name: "code_run"}, tool.RiskReadOnly, 0)

	cfg := baseRouterConfig()
	entry := cfg.Router.Agents["coder"]
	entry.Tools = []string{"code_run", "file_read"} // file_read 未注册到 reg
	cfg.Router.Agents["coder"] = entry

	r, warnings, err := RegisterRouter(cfg, reg, noopLLM{})
	if err != nil {
		t.Fatalf("should warn, not fail: %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "file_read") {
		t.Errorf("expected a warning naming file_read, got %v", warnings)
	}
	if r == nil {
		t.Fatal("router should still be constructed")
	}
}

func TestRegisterRouter_NilLLMClientAllowed(t *testing.T) {
	// LLM 客户端为 nil 时应能装配（纯规则模式），Classify 回落而非 panic
	cfg := baseRouterConfig()

	r, _, err := RegisterRouter(cfg, routerTestRegistry(t), nil)
	if err != nil {
		t.Fatalf("nil LLM client should be allowed: %v", err)
	}
	d := r.Classify(context.Background(), "无触发词输入")
	if d.Agent != "general" || d.Method != "fallback" {
		t.Errorf("got %q/%q, want general/fallback", d.Agent, d.Method)
	}
}
