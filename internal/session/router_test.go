package session

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/wenruigao/tommy-catty/internal/ctxmgr"
	"github.com/wenruigao/tommy-catty/internal/engine"
	"github.com/wenruigao/tommy-catty/internal/llm"
	"github.com/wenruigao/tommy-catty/internal/router"
)

// capturingLLM 记录每次收到的 system prompt 与工具定义，用于断言路由效果。
type capturingLLM struct {
	mu          sync.Mutex
	systemSeen  []string
	toolDefSeen [][]string
}

func (c *capturingLLM) Chat(_ context.Context, messages []llm.Message, tools []llm.ToolDef) (llm.ChatResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, m := range messages {
		if m.Role == "system" {
			c.systemSeen = append(c.systemSeen, m.Content)
		}
	}
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name)
	}
	c.toolDefSeen = append(c.toolDefSeen, names)

	return llm.ChatResponse{Content: "done", Usage: llm.Usage{TotalTokens: 5}}, nil
}

func (c *capturingLLM) lastSystem() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.systemSeen) == 0 {
		return ""
	}
	return c.systemSeen[len(c.systemSeen)-1]
}

func (c *capturingLLM) allSystems() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.systemSeen))
	copy(out, c.systemSeen)
	return out
}

func (c *capturingLLM) lastToolDefs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.toolDefSeen) == 0 {
		return nil
	}
	return c.toolDefSeen[len(c.toolDefSeen)-1]
}

// routerTestAgents 构造路由测试用 Agent 集。
func routerTestAgents() map[string]*router.AgentSpec {
	return map[string]*router.AgentSpec{
		"general": {
			Name: "general", Description: "通用助手", SystemPrompt: "GENERAL_PROMPT_MARKER",
		},
		"coder": {
			Name: "coder", Description: "编程专家", SystemPrompt: "CODER_PROMPT_MARKER",
			Tools: []string{"code_run"}, Keywords: []string{"代码"}, Priority: 20,
		},
		"researcher": {
			Name: "researcher", Description: "调研专家", SystemPrompt: "RESEARCHER_PROMPT_MARKER",
			Tools: []string{"web_search"}, Keywords: []string{"搜索"}, Priority: 10,
		},
	}
}

// routerTestDeps 构造启用路由的会话依赖。
func routerTestDeps(t *testing.T, llmClient *capturingLLM, r *router.Router, agentMD string) SessionDeps {
	t.Helper()
	reg := newTestRegistry(t, "code_run", "web_search", "db_query")
	return SessionDeps{
		LLM:           llmClient,
		Tools:         reg,
		MaxIterations: 3,
		SystemPrompt:  "BASE_PROMPT_MARKER",
		MemorySize:    50,
		CtxConfig:     ctxmgr.DefaultConfig(),
		AgentMD:       agentMD,
		Router:        r,
	}
}

func newTestRouter(t *testing.T, useLLM bool) *router.Router {
	t.Helper()
	cfg := router.DefaultConfig()
	cfg.UseLLM = useLLM
	r, err := router.NewRouter(routerTestAgents(), cfg, nil)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return r
}

// ============================================================
// 路由命中：提示词与工具集
// ============================================================

func TestSessionRouter_RuleHit_SwapsPrompt(t *testing.T) {
	cap := &capturingLLM{}
	r := newTestRouter(t, false)
	s := NewUserSession("u1", routerTestDeps(t, cap, r, ""))

	if _, err := s.Run(context.Background(), "帮我写个代码"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := s.CurrentAgent(); got != "coder" {
		t.Errorf("CurrentAgent = %q, want coder", got)
	}
	sys := cap.lastSystem()
	if !strings.Contains(sys, "CODER_PROMPT_MARKER") {
		t.Errorf("system prompt should contain coder prompt, got: %s", sys)
	}
	if strings.Contains(sys, "BASE_PROMPT_MARKER") {
		t.Error("routed agent prompt should replace the base prompt")
	}
}

func TestSessionRouter_ToolsNarrowedToWhitelist(t *testing.T) {
	cap := &capturingLLM{}
	r := newTestRouter(t, false)
	s := NewUserSession("u1", routerTestDeps(t, cap, r, ""))

	if _, err := s.Run(context.Background(), "帮我写个代码"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	defs := cap.lastToolDefs()
	if len(defs) != 1 || defs[0] != "code_run" {
		t.Errorf("tool defs = %v, want only [code_run]", defs)
	}
}

func TestSessionRouter_DifferentInputsRouteDifferently(t *testing.T) {
	cap := &capturingLLM{}
	r := newTestRouter(t, false)
	s := NewUserSession("u1", routerTestDeps(t, cap, r, ""))
	ctx := context.Background()

	if _, err := s.Run(ctx, "帮我写个代码"); err != nil {
		t.Fatalf("Run(coder): %v", err)
	}
	if got := s.CurrentAgent(); got != "coder" {
		t.Errorf("first route = %q, want coder", got)
	}

	if _, err := s.Run(ctx, "搜索一下相关资料"); err != nil {
		t.Fatalf("Run(researcher): %v", err)
	}
	if got := s.CurrentAgent(); got != "researcher" {
		t.Errorf("second route = %q, want researcher", got)
	}
	if !strings.Contains(cap.lastSystem(), "RESEARCHER_PROMPT_MARKER") {
		t.Error("prompt should follow the new route")
	}
	if defs := cap.lastToolDefs(); len(defs) != 1 || defs[0] != "web_search" {
		t.Errorf("tool defs = %v, want only [web_search]", defs)
	}
}

// ============================================================
// 回落：general 不限制工具
// ============================================================

func TestSessionRouter_Fallback_UnrestrictedTools(t *testing.T) {
	cap := &capturingLLM{}
	r := newTestRouter(t, false) // 关闭 LLM，规则未命中即回落
	s := NewUserSession("u1", routerTestDeps(t, cap, r, ""))

	if _, err := s.Run(context.Background(), "今天天气怎么样"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := s.CurrentAgent(); got != "general" {
		t.Errorf("CurrentAgent = %q, want general", got)
	}
	// general 的 Tools 为空 → 不限制
	if defs := cap.lastToolDefs(); len(defs) != 3 {
		t.Errorf("fallback should not restrict tools, got %v", defs)
	}
}

// ============================================================
// Persona 不被路由绕过
// ============================================================

func TestSessionRouter_AgentMDRetainsTopPriority(t *testing.T) {
	cap := &capturingLLM{}
	r := newTestRouter(t, false)
	const agentMD = "AGENT_MD_BOUNDARY_MARKER: 禁止执行破坏性操作"
	s := NewUserSession("u1", routerTestDeps(t, cap, r, agentMD))

	if _, err := s.Run(context.Background(), "帮我写个代码"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	sys := cap.lastSystem()
	if !strings.Contains(sys, "AGENT_MD_BOUNDARY_MARKER") {
		t.Errorf("agent.md must survive routing, got: %s", sys)
	}
	if !strings.Contains(sys, "CODER_PROMPT_MARKER") {
		t.Error("routed agent prompt should also be present")
	}
}

// ============================================================
// 记忆跨路由共享
// ============================================================

func TestSessionRouter_MemorySharedAcrossRoutes(t *testing.T) {
	cap := &capturingLLM{}
	r := newTestRouter(t, false)
	s := NewUserSession("u1", routerTestDeps(t, cap, r, ""))
	ctx := context.Background()

	if _, err := s.Run(ctx, "帮我写个代码"); err != nil {
		t.Fatalf("Run 1: %v", err)
	}
	if _, err := s.Run(ctx, "搜索一下相关资料"); err != nil {
		t.Fatalf("Run 2: %v", err)
	}

	// 第二轮（researcher）应能看到第一轮（coder）的对话历史
	history := s.GetHistory(50)
	var sawFirstTurn bool
	for _, m := range history {
		if strings.Contains(m.Content, "帮我写个代码") {
			sawFirstTurn = true
			break
		}
	}
	if !sawFirstTurn {
		t.Error("memory should be shared across routed agents; first turn missing from history")
	}
}

// ============================================================
// 未启用路由：行为与旧版一致
// ============================================================

func TestSessionRouter_Disabled_NoRouting(t *testing.T) {
	cap := &capturingLLM{}
	// Router 为 nil
	s := NewUserSession("u1", routerTestDeps(t, cap, nil, ""))

	if _, err := s.Run(context.Background(), "帮我写个代码"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := s.CurrentAgent(); got != "" {
		t.Errorf("CurrentAgent = %q, want empty when router disabled", got)
	}
	// 提示词应保持 base，不被替换
	if sys := cap.lastSystem(); !strings.Contains(sys, "BASE_PROMPT_MARKER") {
		t.Errorf("base prompt should be used when router disabled, got: %s", sys)
	}
	// 工具不受限制
	if defs := cap.lastToolDefs(); len(defs) != 3 {
		t.Errorf("tools should be unrestricted when router disabled, got %v", defs)
	}
}

func TestSessionRouter_Disabled_PersonaStillApplied(t *testing.T) {
	cap := &capturingLLM{}
	s := NewUserSession("u1", routerTestDeps(t, cap, nil, "AGENT_MD_MARKER"))

	if _, err := s.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	sys := cap.lastSystem()
	if !strings.Contains(sys, "AGENT_MD_MARKER") || !strings.Contains(sys, "BASE_PROMPT_MARKER") {
		t.Errorf("persona assembly broken without router, got: %s", sys)
	}
}

// ============================================================
// 路由状态不跨请求泄漏
// ============================================================

func TestSessionRouter_StateResetBetweenRuns(t *testing.T) {
	cap := &capturingLLM{}
	r := newTestRouter(t, false)
	s := NewUserSession("u1", routerTestDeps(t, cap, r, ""))
	ctx := context.Background()

	// 第一轮命中 coder（白名单仅 code_run）
	if _, err := s.Run(ctx, "帮我写个代码"); err != nil {
		t.Fatalf("Run 1: %v", err)
	}
	// 第二轮回落 general（白名单应为空 = 不限制）
	if _, err := s.Run(ctx, "今天天气怎么样"); err != nil {
		t.Fatalf("Run 2: %v", err)
	}

	if defs := cap.lastToolDefs(); len(defs) != 3 {
		t.Errorf("second run should not inherit coder whitelist, got %v", defs)
	}
}

// ============================================================
// applyRoute 单元行为
// ============================================================

func TestApplyRoute_SetsWhitelist(t *testing.T) {
	cap := &capturingLLM{}
	r := newTestRouter(t, false)
	s := NewUserSession("u1", routerTestDeps(t, cap, r, ""))

	s.mu.Lock()
	s.applyRoute(context.Background(), "帮我写个代码")
	agent, wl := s.route.agent, s.route.whitelist
	s.mu.Unlock()

	if agent != "coder" {
		t.Errorf("agent = %q, want coder", agent)
	}
	if len(wl) != 1 || !wl["code_run"] {
		t.Errorf("whitelist = %v, want {code_run:true}", wl)
	}
}

func TestApplyRoute_FallbackClearsWhitelist(t *testing.T) {
	cap := &capturingLLM{}
	r := newTestRouter(t, false)
	s := NewUserSession("u1", routerTestDeps(t, cap, r, ""))

	// 先设置一个白名单，再让路由回落到 general（Tools 为空）
	s.mu.Lock()
	s.route.whitelist = map[string]bool{"stale": true}
	s.applyRoute(context.Background(), "今天天气怎么样")
	wl := s.route.whitelist
	s.mu.Unlock()

	if wl != nil {
		t.Errorf("fallback to general should clear whitelist, got %v", wl)
	}
}

func TestApplyRoute_RecordsTraceSpan(t *testing.T) {
	cap := &capturingLLM{}
	r := newTestRouter(t, false)
	s := NewUserSession("u1", routerTestDeps(t, cap, r, ""))

	s.mu.Lock()
	s.applyRoute(context.Background(), "帮我写个代码")
	spans := s.tracer.GetSpans()
	s.mu.Unlock()

	var found bool
	for _, sp := range spans {
		if sp.Name == "router.classify" {
			found = true
			if sp.Attrs["agent"] != "coder" {
				t.Errorf("span agent attr = %q, want coder", sp.Attrs["agent"])
			}
			if sp.Attrs["method"] != router.MethodRule {
				t.Errorf("span method attr = %q, want rule", sp.Attrs["method"])
			}
		}
	}
	if !found {
		t.Error("router.classify span not recorded")
	}
}

// ============================================================
// 编译期接口断言
// ============================================================

// 确保过滤器满足引擎要求的 ToolCaller 接口。
var _ engine.ToolCaller = (*routeToolFilter)(nil)
