// Package session 实现多用户会话隔离，每个用户持有独立的
// Engine、Memory、CtxManager、Tracer 实例，保证对话上下文和记忆互不可见。
package session

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/wenruigao/tommy-catty/internal/ctxmgr"
	"github.com/wenruigao/tommy-catty/internal/engine"
	"github.com/wenruigao/tommy-catty/internal/llm"
	"github.com/wenruigao/tommy-catty/internal/memory"
	"github.com/wenruigao/tommy-catty/internal/memstore"
	"github.com/wenruigao/tommy-catty/internal/metrics"
	"github.com/wenruigao/tommy-catty/internal/tool"
	"github.com/wenruigao/tommy-catty/internal/trace"
)

// ErrRateLimited 当用户请求被限流时返回此错误。
var ErrRateLimited = errors.New("rate limit exceeded, please try again later")

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
	limiter        *RateLimiter
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
			log.Printf("  ⚠️  session: 用户 %s 记忆预热失败: %v", userID, err)
		}
		// 加载用户画像（store 优先，失败回退本地文件；均无则视为新用户）
		_ = loadUserProfileVia(deps.MemStore, deps.UserProfilesDir, userID)
	}

	ctxMgr := ctxmgr.NewManager(deps.CtxConfig, deps.Summarizer)

	// 人格装配：配置了 agent.md / soul.md / 用户画像目录时，
	// 每次 Run 动态生成系统提示词（用户画像每次读取最新文件）。
	var promptProvider func() string
	if deps.AgentMD != "" || deps.SoulMD != "" || deps.UserProfilesDir != "" || deps.MemStore != nil {
		basePrompt := deps.SystemPrompt
		if basePrompt == "" {
			basePrompt = DefaultBasePrompt
		}
		promptProvider = func() string {
			userMD := loadUserProfileVia(deps.MemStore, deps.UserProfilesDir, userID)
			return BuildSystemPrompt(deps.AgentMD, basePrompt, deps.SoulMD, userMD)
		}
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
		Tools:                deps.Tools,
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
		limiter:        NewRateLimiter(deps.RateLimit),
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

	// 限流检查（per-user 滑动窗口）
	if !s.limiter.Allow() {
		metrics.SessionRateLimited().Add(1)
		return nil, ErrRateLimited
	}

	// 输入层防御：剥离用户输入中的注入指令短语（不只标记）
	if cleaned, suspicious := tool.SanitizeInput(goal); suspicious {
		goal = cleaned
	}

	// Skill 匹配：命中时将已验证的执行经验拼接到目标之前
	if s.skillHint != nil {
		if hint := s.skillHint(goal); hint != "" {
			goal = hint + "\n\n用户任务：" + goal
		}
	}

	s.tracer.Reset()
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
