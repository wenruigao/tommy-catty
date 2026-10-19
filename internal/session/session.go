// Package session 实现多用户会话隔离，每个用户持有独立的
// Engine、Memory、CtxManager、Tracer 实例，保证对话上下文和记忆互不可见。
package session

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/wenruigao/tommy-catty/internal/ctxmgr"
	"github.com/wenruigao/tommy-catty/internal/engine"
	"github.com/wenruigao/tommy-catty/internal/llm"
	"github.com/wenruigao/tommy-catty/internal/memory"
	"github.com/wenruigao/tommy-catty/internal/memstore"
	"github.com/wenruigao/tommy-catty/internal/metrics"
	"github.com/wenruigao/tommy-catty/internal/router"
	"github.com/wenruigao/tommy-catty/internal/tool"
	"github.com/wenruigao/tommy-catty/internal/trace"
)

// ErrRateLimited 当用户请求被限流时返回此错误（HTTP 层频率限流或全局并发限流）。
var ErrRateLimited = errors.New("rate limit exceeded, please try again later")

// ErrConcurrencyLimited 当全局并发执行数达到上限时返回此错误。
var ErrConcurrencyLimited = errors.New("too many concurrent requests, please try again later")

// routerTraceID 路由决策 span 的 traceID。
//
// 路由发生在 engine.Run 之前，而引擎的 task span 使用其内部生成的 TaskID，
// 两者无法共用同一 traceID；用固定 ID 使路由 span 在导出结果中可辨识。
const routerTraceID = "router"

// UserSession 封装单个用户的全部有状态组件。
// 同一用户的请求通过 mu 串行执行；不同用户之间无共享指针，天然并行安全。
type UserSession struct {
	UserID     string
	CreatedAt  time.Time
	LastActive time.Time

	engine         *engine.Engine
	memory         *memory.CombinedMemory
	ctxManager     *ctxmgr.Manager
	tracer         *trace.Tracer
	sem            chan struct{}                       // 全局并发信号量（nil 不限）
	router         *router.Router                      // 可为 nil（未启用意图路由）
	route          *routeState                         // 本次 Run 的路由结果（Run 持锁期间读写，串行安全）
	exporter       *trace.Exporter                     // 可为 nil
	profiler       *UserProfiler                       // 可为 nil（禁用用户画像生成）
	skillHint      func(input string) string           // 可为 nil
	onTaskComplete func(result *engine.ExecutionTrace) // 可为 nil

	mu sync.Mutex // 同用户串行保护
}

// SessionDeps 创建 UserSession 所需的共享依赖（由 SessionManager 注入）。
type SessionDeps struct {
	LLM           engine.LLMClient
	Tools         engine.ToolCaller
	MaxIterations int
	SystemPrompt  string
	MemorySize    int // 每用户工作记忆容量
	PrewarmCount  int // 会话创建时预热的历史记忆条数（<=0 取默认 10）
	CtxConfig     ctxmgr.Config
	Summarizer    ctxmgr.Summarizer // 可为 nil
	RateLimit     RateLimitConfig
	// ConcurrencySem 全局并发信号量（buffered channel，容量 = 最大并发执行数）。
	// nil 表示不限并发。由 SessionManager 创建并共享给所有 UserSession。
	ConcurrencySem chan struct{}
	// Reflection 反思配置（nil 则禁用反思）
	Reflection *engine.ReflectionConfig
	// ToolGate 工具调用安全门禁（nil 则不检查）。
	// 多用户场景优先使用 NewToolGate：共享同一实例会使限流桶跨用户共用。
	ToolGate engine.ToolGate
	// OutputGate 最终输出安全门禁（nil 则不检查）。
	// 多用户场景优先使用 NewOutputGate（每用户独立审计身份）。
	OutputGate engine.OutputGate
	// ReturnGate 工具返回安全门禁（tool_return 检查点，nil 则不检查）。
	ReturnGate engine.ToolReturnGate
	// NewToolGate 每用户工具门禁工厂（非 nil 时优先于 ToolGate）：
	// 为每个用户会话创建独立门禁实例（独立限流桶 + 审计身份），
	// 避免全体用户共享一个限流桶导致配额被互相耗尽。
	NewToolGate func(userID string) engine.ToolGate
	// NewOutputGate 每用户输出门禁工厂（非 nil 时优先于 OutputGate）。
	NewOutputGate func(userID string) engine.OutputGate
	// NewReturnGate 每用户工具返回门禁工厂（非 nil 时优先于 ReturnGate）。
	NewReturnGate func(userID string) engine.ToolReturnGate
	// ToolRiskLookup 工具风险等级查询（供引擎 tool_return 检查点使用，
	// nil 则所有工具视为 0 级）。
	ToolRiskLookup func(toolName string) int
	// TraceExporter 追踪导出器（nil 则不导出）
	TraceExporter *trace.Exporter

	// AgentMD agent.md 内容（Agent 职责与权限边界，空则不注入系统提示词）
	AgentMD string
	// SoulMD soul.md 内容（Agent 人格与对话风格，空则不注入系统提示词）
	SoulMD string
	// UserProfilesDir 用户画像目录（如 data/users），空则不注入用户画像
	UserProfilesDir string
	// Profiler 用户画像生成器（nil 则禁用画像生成）
	Profiler *UserProfiler
	// MemStore 记忆存储后端（长期记忆 + 用户画像持久化；nil 则仅工作记忆，与旧行为一致）
	MemStore memstore.Store
	// SkillHintProvider Skill 匹配提示（nil 或不命中则不拼接）。
	// 返回非空时，提示文本会拼接到用户目标之前一并交给引擎。
	SkillHintProvider func(input string) string
	// OnTaskComplete 任务成功完成后的回调（如自动生成并持久化 Skill），nil 则不调用。
	OnTaskComplete func(result *engine.ExecutionTrace)

	// Router 意图路由器（nil 则不启用路由，行为与旧版完全一致）。
	// 启用后每次 Run 先分类，按命中的 Agent 切换系统提示词与工具白名单。
	Router *router.Router
}

// NewUserSession 创建一个用户会话实例，分配独立的有状态组件。
func NewUserSession(userID string, deps SessionDeps) *UserSession {
	memSize := deps.MemorySize
	if memSize <= 0 {
		memSize = 100
	}

	working := memory.NewWorkingMemory(memSize)
	// 长期记忆：注入 MemStore 时经适配器持久化（file/sqlite/remote 后端），
	// 冲突消解（conflict.ResolveConflict）已在 sqlite 后端写入链路接线；
	// MemStore 为 nil 时退化为纯工作记忆（与旧行为一致）
	longTerm := memstore.NewMemoryAdapter(deps.MemStore, userID)
	combined := memory.NewCombinedMemory(working, longTerm)

	// 会话预热：注入最近长期记忆到工作记忆（带 prewarm 标签，参与上下文构建，
	// CombinedMemory.Store 不会将其重复落盘），并加载用户画像验证可读性
	if deps.MemStore != nil {
		prewarmCount := deps.PrewarmCount
		if prewarmCount <= 0 {
			prewarmCount = 10
		}
		if entries, err := deps.MemStore.RecentMemories(context.Background(), userID, prewarmCount); err == nil {
			for i := len(entries) - 1; i >= 0; i-- { // 返回为新到旧，按时间顺序写入
				e := entries[i]
				role := "user"
				for _, tag := range e.Tags {
					if tag == "assistant" {
						role = "assistant"
						break
					}
				}
				_ = working.Store(context.Background(), memory.MemoryEntry{
					ID:        e.ID,
					Content:   e.Content,
					Timestamp: e.Timestamp,
					Tags:      append([]string{role, memory.PrewarmTag}, e.Tags...),
				})
			}
		} else {
			slog.Warn("session: 记忆预热失败", "user", userID, "error", err)
		}
		// 加载用户画像（store 优先，失败回退本地文件；均无则视为新用户）
		_ = loadUserProfileVia(deps.MemStore, deps.UserProfilesDir, userID)
	}

	ctxMgr := ctxmgr.NewManager(deps.CtxConfig, deps.Summarizer)

	// 路由状态：由 Run() 写入，promptProvider 与工具过滤器读取。
	// 三者的读写都发生在 Run() 持 s.mu 期间，故无需额外同步。
	// 用独立持有结构而非 UserSession 字段，是因为 engine 构造需要闭包，
	// 而闭包又需要先于 UserSession 存在（循环依赖）。
	route := &routeState{}

	basePrompt := deps.SystemPrompt
	if basePrompt == "" {
		basePrompt = DefaultBasePrompt
	}

	// 人格装配：配置了 agent.md / soul.md / 用户画像目录，或启用了意图路由时，
	// 每次 Run 动态生成系统提示词（用户画像每次读取最新文件；
	// 路由命中时用该 Agent 的 system_prompt 替换 base prompt）。
	//
	// 注意：路由命中时 Agent 的提示词仍经 BuildSystemPrompt 与 agent.md 组装，
	// 因此 config/agent.md 声明的职责与权限边界保持最高优先级，不被路由绕过。
	var promptProvider func() string
	needDynamicPrompt := deps.AgentMD != "" || deps.SoulMD != "" ||
		deps.UserProfilesDir != "" || deps.MemStore != nil || deps.Router != nil
	if needDynamicPrompt {
		promptProvider = func() string {
			base := basePrompt
			if route.agent != "" && deps.Router != nil {
				if spec := deps.Router.Agent(route.agent); spec != nil && spec.SystemPrompt != "" {
					base = spec.SystemPrompt
				}
			}
			userMD := loadUserProfileVia(deps.MemStore, deps.UserProfilesDir, userID)
			return BuildSystemPrompt(deps.AgentMD, base, deps.SoulMD, userMD)
		}
	}

	// 工具装配：启用路由时包一层白名单过滤器，按当前 Agent 收窄可用工具集。
	// 过滤器只做收窄不做扩张，且 ToolGate 等安全门禁在引擎内原样生效。
	tools := deps.Tools
	if deps.Router != nil {
		tools = newRouteToolFilter(deps.Tools, func() map[string]bool { return route.whitelist })
	}

	// 追踪：每个用户持有独立 Tracer，注入引擎后由引擎记录
	// task/llm.chat/tool.* 等 span（/trace 与 JSONL 导出的数据来源）
	tracer := trace.NewTracer()

	// per-user 门禁装配：工厂优先，保证每个用户持有独立限流桶与审计身份
	toolGate := deps.ToolGate
	if deps.NewToolGate != nil {
		toolGate = deps.NewToolGate(userID)
	}
	outputGate := deps.OutputGate
	if deps.NewOutputGate != nil {
		outputGate = deps.NewOutputGate(userID)
	}
	returnGate := deps.ReturnGate
	if deps.NewReturnGate != nil {
		returnGate = deps.NewReturnGate(userID)
	}

	eng := engine.NewEngine(engine.EngineConfig{
		LLM:                  deps.LLM,
		Tools:                tools,
		Memory:               combined,
		MaxIterations:        deps.MaxIterations,
		SystemPrompt:         deps.SystemPrompt,
		CtxManager:           ctxMgr,
		Reflection:           deps.Reflection,
		ToolGate:             toolGate,
		OutputGate:           outputGate,
		ReturnGate:           returnGate,
		UserID:               userID,
		ToolRiskOf:           deps.ToolRiskLookup,
		SystemPromptProvider: promptProvider,
		Tracer:               tracer,
	})

	now := time.Now()
	return &UserSession{
		UserID:         userID,
		CreatedAt:      now,
		LastActive:     now,
		engine:         eng,
		memory:         combined,
		ctxManager:     ctxMgr,
		tracer:         tracer,
		sem:            deps.ConcurrencySem,
		router:         deps.Router,
		route:          route,
		exporter:       deps.TraceExporter,
		profiler:       deps.Profiler,
		skillHint:      deps.SkillHintProvider,
		onTaskComplete: deps.OnTaskComplete,
	}
}

// Run 执行用户任务（同用户串行）。
func (s *UserSession) Run(ctx context.Context, goal string) (*engine.ExecutionTrace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.LastActive = time.Now()

	// 全局并发控制：获取信号量（频率限流已由 HTTP 层 RateLimitMiddleware 承担）
	if s.sem != nil {
		select {
		case s.sem <- struct{}{}:
			defer func() { <-s.sem }()
		case <-ctx.Done():
			metrics.SessionRateLimited().Add(1)
			return nil, ErrConcurrencyLimited
		default:
			// 非阻塞尝试：信号量满则立即拒绝（避免排队堆积）
			metrics.SessionRateLimited().Add(1)
			return nil, ErrConcurrencyLimited
		}
	}

	// 输入层防御：剥离用户输入中的注入指令短语（不只标记）
	if cleaned, suspicious := tool.SanitizeInput(goal); suspicious {
		goal = cleaned
	}

	// 追踪重置提前到路由之前，使路由决策 span 与本次执行的 span 一并导出
	s.tracer.Reset()

	// 意图路由：在 ReAct 循环之前分类，决定由哪个预设 Agent 处理。
	// 分类基于净化后的用户原始意图（skillHint 拼接之前），避免注入的提示文本
	// 污染分类结果。Classify 永不返回 error，异常一律回落兜底 Agent。
	s.route.reset()
	if s.router != nil {
		s.applyRoute(ctx, goal)
	}

	// Skill 匹配：命中时将已验证的执行经验拼接到目标之前
	if s.skillHint != nil {
		if hint := s.skillHint(goal); hint != "" {
			goal = hint + "\n\n用户任务：" + goal
		}
	}

	result, err := s.engine.Run(ctx, goal)

	// 导出本次执行的追踪 span（导出失败不影响任务结果）
	if s.exporter != nil {
		_ = s.exporter.Export(s.tracer.GetSpans())
	}

	// 任务成功后按需更新用户画像（失败静默，不影响任务结果）
	if err == nil && s.profiler != nil {
		s.profiler.OnRunComplete(ctx, s.UserID, s.memory.GetContext(profilerHistoryLimit))
	}

	// 任务完成回调（如自动生成并持久化 Skill，不影响任务结果）
	if err == nil && s.onTaskComplete != nil {
		s.onTaskComplete(result)
	}
	return result, err
}

// applyRoute 执行意图分类并把结果写入路由状态，供 promptProvider 与工具过滤器读取。
// 调用方须持有 s.mu。路由失败不阻断请求：Classify 内部已保证回落兜底 Agent。
func (s *UserSession) applyRoute(ctx context.Context, goal string) {
	decision := s.router.Classify(ctx, goal)

	s.route.agent = decision.Agent
	if spec := s.router.Agent(decision.Agent); spec != nil {
		s.route.whitelist = buildToolWhitelist(spec.Tools)
	}

	span := s.tracer.StartSpan(routerTraceID, "router.classify", map[string]string{
		"agent":      decision.Agent,
		"method":     decision.Method,
		"confidence": strconv.FormatFloat(decision.Confidence, 'f', 2, 64),
		"reason":     decision.Reason,
	})
	s.tracer.EndSpan(span, nil)

	metrics.RouterDecisions().With(map[string]string{
		"agent":  decision.Agent,
		"method": decision.Method,
	}).Add(1)

	logAttrs := []interface{}{
		"user", s.UserID,
		"agent", decision.Agent,
		"method", decision.Method,
		"confidence", decision.Confidence,
		"reason", decision.Reason,
	}
	if decision.Method == router.MethodFallback {
		slog.Info("路由决策：回落兜底 Agent", logAttrs...)
	} else {
		slog.Info("路由决策", logAttrs...)
	}

	s.tracer.EndSpan(span, nil)
}

// CurrentAgent 返回最近一次 Run 命中的路由 Agent 名（未启用路由时为空）。
// 供 CLI 展示与诊断使用。
func (s *UserSession) CurrentAgent() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.route.agent
}

// ClearMemory 清空用户工作记忆。
func (s *UserSession) ClearMemory() {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.memory.Clear()
	s.ctxManager.Reset()
}

// GetHistory 获取用户最近的对话上下文消息。
func (s *UserSession) GetHistory(limit int) []llm.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.memory.GetContext(limit)
}

// Tracer 返回用户的追踪器。
func (s *UserSession) Tracer() *trace.Tracer {
	return s.tracer
}

// Touch 更新活跃时间。
func (s *UserSession) Touch() {
	s.LastActive = time.Now()
}
