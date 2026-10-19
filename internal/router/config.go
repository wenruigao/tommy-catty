package router

import "time"

// Config 意图路由器配置。
type Config struct {
	// Enabled 是否启用路由（false 时不构建 Router，行为与旧版一致）
	Enabled bool
	// LLMTimeout LLM 分类调用超时（默认 5s）。超时即回落兜底 Agent。
	LLMTimeout time.Duration
	// ConfidenceThreshold 置信度阈值（默认 0.6）。低于此值的分类结果视为不可信，回落兜底。
	ConfidenceThreshold float64
	// FallbackAgent 兜底 Agent 名（默认 "general"），必须存在于 agents 中。
	FallbackAgent string
	// UseLLM 规则未命中时是否调用 LLM 分类（默认 true）。
	// 置 false 可完全关闭 LLM 兜底，只用规则匹配（零成本零延迟）。
	UseLLM bool
}

// DefaultConfig 返回默认路由配置。
func DefaultConfig() Config {
	return Config{
		LLMTimeout:          5 * time.Second,
		ConfidenceThreshold: 0.6,
		FallbackAgent:       "general",
		UseLLM:              true,
	}
}

// Normalize 填充零值为默认值。
func (c *Config) Normalize() {
	if c.LLMTimeout <= 0 {
		c.LLMTimeout = 5 * time.Second
	}
	if c.ConfidenceThreshold <= 0 {
		c.ConfidenceThreshold = 0.6
	}
	if c.FallbackAgent == "" {
		c.FallbackAgent = "general"
	}
}

// 分类方式常量（进 Decision.Method 与指标 label）。
const (
	// MethodRule 规则匹配命中。
	MethodRule = "rule"
	// MethodLLM LLM 分类命中。
	MethodLLM = "llm"
	// MethodFallback 回落兜底 Agent（规则未命中且 LLM 分类失败/不可信）。
	MethodFallback = "fallback"
)
