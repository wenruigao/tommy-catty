// Package router 实现入口级意图路由：请求进入 ReAct 循环之前先对用户问题
// 做分类，分派给对应的预设专职 Agent（独立系统提示词 + 工具白名单）。
//
// 分类链路为「规则优先 → LLM 兜底 → 静默回落」，任何异常都回落兜底 Agent，
// 因此路由是尽力而为的增强，不会成为请求路径上的新失败点。
//
// 路由只做收窄不做扩张：Agent 的工具白名单必须是全局 Registry 的子集，
// ToolGate / OutputGate 等安全门禁原样继承，不因路由而降级。
package router

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/wenruigao/tommy-catty/internal/engine"
	"github.com/wenruigao/tommy-catty/internal/llm"
)

// Decision 路由决策结果。
type Decision struct {
	// Agent 目标 Agent 名（保证非空且存在于 agents 中）
	Agent string `json:"agent"`
	// Confidence 置信度 0~1
	Confidence float64 `json:"confidence"`
	// Method 决策方式：MethodRule | MethodLLM | MethodFallback
	Method string `json:"method"`
	// Reason 决策依据（进日志与 trace span，便于排查误分类）
	Reason string `json:"reason"`
}

// Router 意图路由器。
type Router struct {
	agents   map[string]*AgentSpec
	order    []*AgentSpec // 按 priority 降序，供规则匹配遍历
	fallback string
	cfg      Config
	llm      engine.LLMClient // 可为 nil（此时跳过 LLM 分类，直接回落）
}

// NewRouter 构建路由器。agents 会被校验（含正则预编译），失败返回错误。
// llmClient 为 nil 时禁用 LLM 兜底，仅规则匹配 + 回落。
func NewRouter(agents map[string]*AgentSpec, cfg Config, llmClient engine.LLMClient) (*Router, error) {
	cfg.Normalize()

	if err := ValidateAgents(agents, cfg.FallbackAgent); err != nil {
		return nil, err
	}

	order := make([]*AgentSpec, 0, len(agents))
	for _, a := range agents {
		order = append(order, a)
	}
	sortByPriority(order)

	return &Router{
		agents:   agents,
		order:    order,
		fallback: cfg.FallbackAgent,
		cfg:      cfg,
		llm:      llmClient,
	}, nil
}

// Classify 对用户输入做意图分类。永不返回 error：任何异常均回落兜底 Agent。
func (r *Router) Classify(ctx context.Context, input string) Decision {
	if r == nil {
		return Decision{}
	}

	// ① 规则匹配（零成本零延迟）
	if m := matchRules(r.order, input); m != nil && m.confidence >= r.cfg.ConfidenceThreshold {
		return Decision{
			Agent:      m.agent.Name,
			Confidence: m.confidence,
			Method:     MethodRule,
			Reason:     m.reason(),
		}
	}

	// ② LLM 分类兜底
	if r.llm != nil && r.cfg.UseLLM {
		if d, ok := r.classifyByLLM(ctx, input); ok {
			return d
		}
	}

	// ③ 静默回落
	return r.fallbackDecision("规则未命中且 LLM 分类不可用或不可信")
}

// classifyByLLM 用 LLM 做分类。返回 (decision, true) 表示分类可信并已采纳。
//
// 带 panic 防护：调用方传入 typed-nil 的 LLMClient（接口非 nil 但底层指针为 nil）
// 等异常装配会在解引用时 panic。路由承诺「绝不成为请求路径上的新失败点」，
// 故此处兜底捕获并转为回落，而非让 panic 冒泡中断整个用户请求。
func (r *Router) classifyByLLM(ctx context.Context, input string) (d Decision, ok bool) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("router: LLM 分类发生 panic，回落兜底 Agent", "recover", rec)
			d, ok = Decision{}, false
		}
	}()

	// 独立超时：分类不应吃满业务请求的超时预算
	callCtx, cancel := context.WithTimeout(ctx, r.cfg.LLMTimeout)
	defer cancel()

	msgs := []llm.Message{
		{Role: "system", Content: classifySystemPrompt},
		{Role: "user", Content: buildClassifyPrompt(r.order, input, r.fallback)},
	}

	start := time.Now()
	resp, err := r.llm.Chat(callCtx, msgs, nil) // 不传工具：分类是纯文本任务
	if err != nil {
		slog.Warn("router: LLM 分类失败，回落兜底 Agent", "error", err, "elapsed", time.Since(start))
		return Decision{}, false
	}

	agent, confidence, reason, err := parseClassifyResponse(resp.Content)
	if err != nil {
		slog.Warn("router: LLM 分类结果解析失败，回落兜底 Agent", "error", err)
		return Decision{}, false
	}

	// 校验 Agent 名真实存在（防 LLM 臆造）
	spec, exists := r.agents[agent]
	if !exists {
		slog.Warn("router: LLM 返回未知 Agent，回落兜底 Agent", "agent", agent)
		return Decision{}, false
	}

	if confidence < r.cfg.ConfidenceThreshold {
		slog.Info("router: LLM 分类置信度不足，回落兜底 Agent",
			"agent", agent, "confidence", confidence, "threshold", r.cfg.ConfidenceThreshold)
		return Decision{}, false
	}

	return Decision{
		Agent:      spec.Name,
		Confidence: confidence,
		Method:     MethodLLM,
		Reason:     reason,
	}, true
}

// fallbackDecision 构建回落决策。
func (r *Router) fallbackDecision(reason string) Decision {
	return Decision{
		Agent:      r.fallback,
		Confidence: 0,
		Method:     MethodFallback,
		Reason:     reason,
	}
}

// parseClassifyResponse 从 LLM 输出中容错解析分类结果。
// LLM 可能在 JSON 前后附加说明文字，故先截取首个 '{' 到末个 '}'。
func parseClassifyResponse(content string) (agent string, confidence float64, reason string, err error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return "", 0, "", fmt.Errorf("LLM 返回空内容")
	}

	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start < 0 || end <= start {
		return "", 0, "", fmt.Errorf("未找到有效的 JSON 块")
	}

	var raw struct {
		Agent      string  `json:"agent"`
		Confidence float64 `json:"confidence"`
		Reason     string  `json:"reason"`
	}
	if uerr := json.Unmarshal([]byte(content[start:end+1]), &raw); uerr != nil {
		return "", 0, "", fmt.Errorf("JSON 解析失败: %w", uerr)
	}

	if strings.TrimSpace(raw.Agent) == "" {
		return "", 0, "", fmt.Errorf("分类结果缺少 agent 字段")
	}
	// 置信度越界时收敛到合法区间（LLM 可能返回 0.85 或 85）
	confidence = raw.Confidence
	if confidence > 1 {
		confidence = confidence / 100
	}
	if confidence < 0 {
		confidence = 0
	}

	return strings.TrimSpace(raw.Agent), confidence, strings.TrimSpace(raw.Reason), nil
}

// Agent 按名称取 Agent 定义（供 session 层读取 SystemPrompt / Tools）。
// 不存在返回 nil。
func (r *Router) Agent(name string) *AgentSpec {
	if r == nil {
		return nil
	}
	return r.agents[name]
}

// FallbackAgent 返回兜底 Agent 名。
func (r *Router) FallbackAgent() string {
	if r == nil {
		return ""
	}
	return r.fallback
}

// AgentNames 返回全部 Agent 名（按 priority 降序），供启动日志与诊断使用。
func (r *Router) AgentNames() []string {
	if r == nil {
		return nil
	}
	names := make([]string, 0, len(r.order))
	for _, a := range r.order {
		names = append(names, a.Name)
	}
	return names
}
