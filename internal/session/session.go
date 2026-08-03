// Package session 实现多用户会话隔离，每个用户持有独立的
// Engine、Memory、CtxManager、Tracer 实例，保证对话上下文和记忆互不可见。
package session

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/tommy-cat/agent/internal/ctxmgr"
	"github.com/tommy-cat/agent/internal/engine"
	"github.com/tommy-cat/agent/internal/llm"
	"github.com/tommy-cat/agent/internal/memory"
	"github.com/tommy-cat/agent/internal/trace"
)

// ErrRateLimited 当用户请求被限流时返回此错误。
var ErrRateLimited = errors.New("rate limit exceeded, please try again later")

// UserSession 封装单个用户的全部有状态组件。
// 同一用户的请求通过 mu 串行执行；不同用户之间无共享指针，天然并行安全。
type UserSession struct {
	UserID     string
	CreatedAt  time.Time
	LastActive time.Time

	engine     *engine.Engine
	memory     *memory.CombinedMemory
	ctxManager *ctxmgr.Manager
	tracer     *trace.Tracer
	limiter    *RateLimiter
	exporter   *trace.Exporter // 可为 nil

	mu sync.Mutex // 同用户串行保护
}

// SessionDeps 创建 UserSession 所需的共享依赖（由 SessionManager 注入）。
type SessionDeps struct {
	LLM           engine.LLMClient
	Tools         engine.ToolCaller
	MaxIterations int
	SystemPrompt  string
	MemorySize    int // 每用户工作记忆容量
	CtxConfig     ctxmgr.Config
	Summarizer    ctxmgr.Summarizer // 可为 nil
	RateLimit     RateLimitConfig
	// Reflection 反思配置（nil 则禁用反思）
	Reflection *engine.ReflectionConfig
	// ToolGate 工具调用安全门禁（nil 则不检查）
	ToolGate engine.ToolGate
	// TraceExporter 追踪导出器（nil 则不导出）
	TraceExporter *trace.Exporter
}

// NewUserSession 创建一个用户会话实例，分配独立的有状态组件。
func NewUserSession(userID string, deps SessionDeps) *UserSession {
	memSize := deps.MemorySize
	if memSize <= 0 {
		memSize = 100
	}

	working := memory.NewWorkingMemory(memSize)
	combined := memory.NewCombinedMemory(working, nil)
	ctxMgr := ctxmgr.NewManager(deps.CtxConfig, deps.Summarizer)

	eng := engine.NewEngine(engine.EngineConfig{
		LLM:           deps.LLM,
		Tools:         deps.Tools,
		Memory:        combined,
		MaxIterations: deps.MaxIterations,
		SystemPrompt:  deps.SystemPrompt,
		CtxManager:    ctxMgr,
		Reflection:    deps.Reflection,
		ToolGate:      deps.ToolGate,
	})

	now := time.Now()
	return &UserSession{
		UserID:     userID,
		CreatedAt:  now,
		LastActive: now,
		engine:     eng,
		memory:     combined,
		ctxManager: ctxMgr,
		tracer:     trace.NewTracer(),
		limiter:    NewRateLimiter(deps.RateLimit),
		exporter:   deps.TraceExporter,
	}
}

// Run 执行用户任务（同用户串行）。
func (s *UserSession) Run(ctx context.Context, goal string) (*engine.ExecutionTrace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.LastActive = time.Now()

	// 限流检查
	if !s.limiter.Allow() {
		return nil, ErrRateLimited
	}

	s.tracer.Reset()
	result, err := s.engine.Run(ctx, goal)

	// 导出本次执行的追踪 span（导出失败不影响任务结果）
	if s.exporter != nil {
		_ = s.exporter.Export(s.tracer.GetSpans())
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
