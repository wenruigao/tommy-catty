package llm

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/tommy-cat/agent/internal/metrics"
)

var (
	// ErrProviderNotFound 表示请求的供应商未注册
	ErrProviderNotFound = errors.New("llm: provider not found")
	// ErrAllProvidersFailed 表示所有供应商（含回退）均调用失败
	ErrAllProvidersFailed = errors.New("llm: all providers failed")
	// ErrCircuitOpen 表示供应商熔断器已打开
	ErrCircuitOpen = errors.New("llm: circuit breaker open")
	// ErrBudgetExceeded 表示日 Token 预算已用尽，拒绝新的调用
	ErrBudgetExceeded = errors.New("llm: daily token budget exceeded")
)

// GatewayConfig 网关配置（从 YAML 配置文件映射）
type GatewayConfig struct {
	// Providers 模型供应商配置列表（key 为供应商名称）
	Providers map[string]ProviderConfig `yaml:"providers"`

	// DefaultProvider 默认使用的供应商名称
	DefaultProvider string `yaml:"default_provider"`

	// FallbackProvider 降级供应商名称
	FallbackProvider string `yaml:"fallback_provider"`

	// Retry 重试策略配置（可选，不配置则使用默认值）
	Retry *RetryConfig `yaml:"retry"`

	// CircuitBreaker 熔断器配置（可选）
	CircuitBreaker *CircuitBreakerYAMLConfig `yaml:"circuit_breaker"`

	// Cache 语义缓存配置（可选，仅 L1 精确哈希层；L2 向量相似层属 P2 未实现）
	Cache *CacheYAMLConfig `yaml:"cache"`

	// Meter Token 计量/预算配置（可选；计量始终启用，此处控制日预算）
	Meter *MeterYAMLConfig `yaml:"meter"`
}

// RetryConfig YAML 可序列化的重试配置
type RetryConfig struct {
	MaxRetries        int     `yaml:"max_retries"`
	BaseBackoffMs     int     `yaml:"base_backoff_ms"`
	MaxBackoffMs      int     `yaml:"max_backoff_ms"`
	BackoffMultiplier float64 `yaml:"backoff_multiplier"`
	JitterFactor      float64 `yaml:"jitter_factor"`
	RetryOnUnknown    bool    `yaml:"retry_on_unknown"`
	MaxTotalTimeoutS  int     `yaml:"max_total_timeout_s"`
}

// ToRetryPolicy 转换为内部 RetryPolicy
func (rc *RetryConfig) ToRetryPolicy() RetryPolicy {
	p := DefaultRetryPolicy()
	if rc.MaxRetries > 0 {
		p.MaxRetries = rc.MaxRetries
	}
	if rc.BaseBackoffMs > 0 {
		p.BaseBackoff = time.Duration(rc.BaseBackoffMs) * time.Millisecond
	}
	if rc.MaxBackoffMs > 0 {
		p.MaxBackoff = time.Duration(rc.MaxBackoffMs) * time.Millisecond
	}
	if rc.BackoffMultiplier > 0 {
		p.BackoffMultiplier = rc.BackoffMultiplier
	}
	if rc.JitterFactor > 0 {
		p.JitterFactor = rc.JitterFactor
	}
	p.RetryOnUnknown = rc.RetryOnUnknown
	if rc.MaxTotalTimeoutS > 0 {
		p.MaxTotalTimeout = time.Duration(rc.MaxTotalTimeoutS) * time.Second
	}
	return p
}

// CircuitBreakerYAMLConfig YAML 可序列化的熔断器配置
type CircuitBreakerYAMLConfig struct {
	FailureThreshold int `yaml:"failure_threshold"`
	SuccessThreshold int `yaml:"success_threshold"`
	OpenTimeoutS     int `yaml:"open_timeout_s"`
}

// ToConfig 转换为内部 CircuitBreakerConfig
func (c *CircuitBreakerYAMLConfig) ToConfig() CircuitBreakerConfig {
	cfg := DefaultCircuitBreakerConfig()
	if c.FailureThreshold > 0 {
		cfg.FailureThreshold = c.FailureThreshold
	}
	if c.SuccessThreshold > 0 {
		cfg.SuccessThreshold = c.SuccessThreshold
	}
	if c.OpenTimeoutS > 0 {
		cfg.OpenTimeout = time.Duration(c.OpenTimeoutS) * time.Second
	}
	return cfg
}

// CacheYAMLConfig YAML 可序列化的语义缓存配置（L1 精确哈希层）。
// L2 向量相似层依赖 embedding 模型，属 P2 阶段，暂未实现。
type CacheYAMLConfig struct {
	Enabled  bool   `yaml:"enabled"`  // 为 true 时才启用语义缓存
	Capacity int    `yaml:"capacity"` // 缓存条目容量（默认 500）
	TTL      string `yaml:"ttl"`      // 过期时间，如 "10m"（默认 10 分钟）
}

// MeterYAMLConfig YAML 可序列化的 Token 计量/预算配置。
type MeterYAMLConfig struct {
	// DailyTokenLimit 每日 Token 预算，<= 0 表示不限（仍会计量汇总，经 /api/v1/usage 暴露）
	DailyTokenLimit int `yaml:"daily_token_limit"`
}

// Gateway 是 LLM 供应商的统一网关，负责路由、重试和故障回退
type Gateway struct {
	mu               sync.RWMutex
	providers        map[string]LLMProvider
	defaultProvider  string
	fallbackProvider string
	httpClient       *http.Client
	retryExecutor    *RetryExecutor
	cache            *SemanticCache // 语义缓存 L1（nil 表示禁用）
	meter            *Meter         // Token 计量器
	budgetWarnDay    time.Time      // 当日已发出 80% 预算预警的日期（去重）
}

// NewGateway 创建一个空的 LLM 网关实例（使用默认重试策略）
func NewGateway() *Gateway {
	return NewGatewayWithRetry(DefaultRetryPolicy(), DefaultCircuitBreakerConfig())
}

// NewGatewayWithRetry 创建带有自定义重试策略的网关
func NewGatewayWithRetry(policy RetryPolicy, cbConfig CircuitBreakerConfig) *Gateway {
	executor := NewRetryExecutor(policy, cbConfig)

	// 注册默认日志钩子 + 指标上报
	executor.AddHook(func(event RetryEvent) {
		log.Printf("[RETRY] provider=%s attempt=%d/%d category=%v backoff=%s circuit=%s err=%v",
			event.Provider, event.Attempt, event.MaxRetries,
			event.Category, event.Backoff.Round(time.Millisecond),
			event.CircuitState, event.Error)
		// ★ 指标上报：重试次数 + 熔断器状态
		metrics.LLMRetries().With(map[string]string{"provider": event.Provider}).Add(1)
		metrics.LLMCircuitState().With(map[string]string{"provider": event.Provider}).Set(float64(event.CircuitState))
	})

	return &Gateway{
		providers:     make(map[string]LLMProvider),
		httpClient:    &http.Client{Timeout: 120 * time.Second},
		retryExecutor: executor,
	}
}

// NewGatewayFromConfig 根据配置创建网关并自动注册所有供应商
// 这是推荐的初始化方式：所有模型接入完全由配置驱动，无需硬编码
func NewGatewayFromConfig(cfg GatewayConfig) *Gateway {
	// 解析重试策略
	policy := DefaultRetryPolicy()
	if cfg.Retry != nil {
		policy = cfg.Retry.ToRetryPolicy()
	}

	// 解析熔断器配置
	cbConfig := DefaultCircuitBreakerConfig()
	if cfg.CircuitBreaker != nil {
		cbConfig = cfg.CircuitBreaker.ToConfig()
	}

	gw := NewGatewayWithRetry(policy, cbConfig)

	for name, pcfg := range cfg.Providers {
		pcfg.Name = name // 确保 name 字段一致
		// 按协议类型选择供应商实现，默认 OpenAI 兼容协议
		if pcfg.Protocol == "anthropic" {
			gw.Register(NewAnthropicProvider(pcfg))
		} else {
			gw.Register(NewGenericProvider(pcfg))
		}
	}

	gw.SetDefault(cfg.DefaultProvider)
	gw.SetFallback(cfg.FallbackProvider)

	// 语义缓存：显式启用才生效（L1 精确哈希层；缓存键含工具列表，见 cacheKey）
	if cfg.Cache != nil && cfg.Cache.Enabled {
		var ttl time.Duration
		if cfg.Cache.TTL != "" {
			if d, terr := time.ParseDuration(cfg.Cache.TTL); terr == nil {
				ttl = d
			}
		}
		gw.cache = NewSemanticCache(cfg.Cache.Capacity, ttl)
	}

	// Token 计量：始终启用（未配置预算时仅做用量汇总，供 /api/v1/usage 暴露）
	dailyLimit := 0
	if cfg.Meter != nil {
		dailyLimit = cfg.Meter.DailyTokenLimit
	}
	gw.meter = NewMeter(dailyLimit)

	return gw
}

// Register 注册一个 LLM 供应商到网关
func (g *Gateway) Register(provider LLMProvider) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.providers[provider.Name()] = provider
}

// SetDefault 设置默认供应商名称
func (g *Gateway) SetDefault(name string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.defaultProvider = name
}

// SetFallback 设置回退供应商名称，当主供应商失败时使用
func (g *Gateway) SetFallback(name string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.fallbackProvider = name
}

// ListProviders 返回所有已注册的供应商名称
func (g *Gateway) ListProviders() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	names := make([]string, 0, len(g.providers))
	for name := range g.providers {
		names = append(names, name)
	}
	return names
}

// RetryExecutor 返回网关的重试执行器（供外部注册钩子等）
func (g *Gateway) RetryExecutor() *RetryExecutor {
	return g.retryExecutor
}

// SetCache 设置语义缓存（nil 表示禁用）
func (g *Gateway) SetCache(c *SemanticCache) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cache = c
}

// Cache 返回网关的语义缓存（可能为 nil）
func (g *Gateway) Cache() *SemanticCache {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.cache
}

// SetMeter 设置 Token 计量器（nil 表示不计量）
func (g *Gateway) SetMeter(m *Meter) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.meter = m
}

// Meter 返回网关的 Token 计量器（可能为 nil）
func (g *Gateway) Meter() *Meter {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.meter
}

// Chat 发送聊天请求，自动路由到对应供应商，支持重试、熔断和回退
func (g *Gateway) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	// 预算门禁：超出日 Token 预算后拒绝新调用（成本控制的执行点）
	if m := g.Meter(); m != nil {
		if used, limit, exceeded := m.CheckBudget(); exceeded {
			return ChatResponse{}, fmt.Errorf("%w: used %d / limit %d", ErrBudgetExceeded, used, limit)
		}
	}

	// 语义缓存 L1：命中直接返回（流式请求不进缓存）
	if c := g.Cache(); c != nil && !req.Stream {
		if resp, hit := c.Get(req); hit {
			metrics.LLMCalls().With(map[string]string{"provider": resp.Model, "status": "success"}).Add(1)
			return resp, nil
		}
	}

	provider, err := g.resolveProvider(req.Model)
	if err != nil {
		return ChatResponse{}, err
	}

	// 使用 RetryExecutor 执行（含重试 + 熔断）
	resp, err := g.retryExecutor.Execute(ctx, provider.Name(), func(ctx context.Context) (ChatResponse, error) {
		return provider.Chat(ctx, req)
	})
	if err == nil {
		g.afterChatSuccess(req, resp)
		return resp, nil
	}

	// 主供应商失败，尝试回退供应商
	g.mu.RLock()
	fallbackName := g.fallbackProvider
	g.mu.RUnlock()

	if fallbackName != "" && fallbackName != provider.Name() {
		fallback, ferr := g.getProvider(fallbackName)
		if ferr == nil {
			resp, ferr = g.retryExecutor.Execute(ctx, fallback.Name(), func(ctx context.Context) (ChatResponse, error) {
				return fallback.Chat(ctx, req)
			})
			if ferr == nil {
				g.afterChatSuccess(req, resp)
				return resp, nil
			}
			metrics.LLMCalls().With(map[string]string{"provider": fallback.Name(), "status": "error"}).Add(1)
			return ChatResponse{}, fmt.Errorf("%w: primary(%s): %v; fallback(%s): %v",
				ErrAllProvidersFailed, provider.Name(), err, fallback.Name(), ferr)
		}
	}

	metrics.LLMCalls().With(map[string]string{"provider": provider.Name(), "status": "error"}).Add(1)
	return ChatResponse{}, fmt.Errorf("%w: %v", ErrAllProvidersFailed, err)
}

// ChatStream 发送流式聊天请求，支持重试和回退
func (g *Gateway) ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, error) {
	provider, err := g.resolveProvider(req.Model)
	if err != nil {
		return nil, err
	}

	// 使用 RetryExecutor 执行流式请求（仅重试连接建立阶段）
	ch, err := g.retryExecutor.ExecuteStream(ctx, provider.Name(), func(ctx context.Context) (<-chan StreamChunk, error) {
		return provider.ChatStream(ctx, req)
	})
	if err == nil {
		return ch, nil
	}

	// 主供应商失败，尝试回退供应商
	g.mu.RLock()
	fallbackName := g.fallbackProvider
	g.mu.RUnlock()

	if fallbackName != "" && fallbackName != provider.Name() {
		fallback, ferr := g.getProvider(fallbackName)
		if ferr == nil {
			ch, ferr = g.retryExecutor.ExecuteStream(ctx, fallback.Name(), func(ctx context.Context) (<-chan StreamChunk, error) {
				return fallback.ChatStream(ctx, req)
			})
			if ferr == nil {
				return ch, nil
			}
		}
	}

	return nil, fmt.Errorf("%w: %v", ErrAllProvidersFailed, err)
}

// resolveProvider 根据模型名称或默认配置解析目标供应商。
// 匹配顺序：1) 供应商名称 2) 供应商配置的 model 名 3) 默认供应商 4) 唯一供应商
func (g *Gateway) resolveProvider(model string) (LLMProvider, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	if model != "" {
		// 1. 按供应商名称匹配（如 "mimo" 匹配 provider key "mimo"）
		if p, ok := g.providers[model]; ok {
			return p, nil
		}

		// 2. 按供应商配置的 model 名匹配（如 "mimo-v2.5-pro" 匹配 provider.Model()）
		// 按供应商名排序遍历，避免 map 迭代顺序不定导致同名 model 路由结果随机
		names := make([]string, 0, len(g.providers))
		for name := range g.providers {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if g.providers[name].Model() == model {
				return g.providers[name], nil
			}
		}
	}

	// 3. 使用默认供应商
	if g.defaultProvider != "" {
		if p, ok := g.providers[g.defaultProvider]; ok {
			return p, nil
		}
	}

	// 4. 如果只有一个供应商，直接使用
	if len(g.providers) == 1 {
		for _, p := range g.providers {
			return p, nil
		}
	}

	return nil, fmt.Errorf("%w: model=%q", ErrProviderNotFound, model)
}

// getProvider 根据名称获取供应商
func (g *Gateway) getProvider(name string) (LLMProvider, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if p, ok := g.providers[name]; ok {
		return p, nil
	}
	return nil, fmt.Errorf("%w: %s", ErrProviderNotFound, name)
}

// afterChatSuccess 成功调用后的统一收尾：记录 Token 计量（含 80% 预算预警）并写语义缓存。
func (g *Gateway) afterChatSuccess(req ChatRequest, resp ChatResponse) {
	// ★ 指标上报：LLM 调用成功
	metrics.LLMCalls().With(map[string]string{"provider": resp.Model, "status": "success"}).Add(1)

	if m := g.Meter(); m != nil {
		m.RecordUsage(UsageExecution, resp.Model, resp.Usage)
		// ★ 指标上报：Token 消耗
		metrics.LLMTokens().With(map[string]string{"category": "execution", "model": resp.Model}).Add(float64(resp.Usage.TotalTokens))
		metrics.LLMTokensCached().Add(float64(resp.Usage.PromptDetails.CachedTokens))
		used, limit, _ := m.CheckBudget()
		metrics.LLMBudgetUsed().Set(float64(used))
		metrics.LLMBudgetLimit().Set(float64(limit))
		if limit > 0 && float64(used) >= float64(limit)*0.8 {
			today := time.Now().Truncate(24 * time.Hour)
			g.mu.Lock()
			first := !g.budgetWarnDay.Equal(today)
			if first {
				g.budgetWarnDay = today
			}
			g.mu.Unlock()
			if first {
				log.Printf("[METER] 预警: 日 Token 用量已达预算 80%%（%d/%d）", used, limit)
			}
		}
	}
	// 写缓存：仅缓存非流式的纯文本响应（工具调用响应不缓存，避免副作用重放）
	if c := g.Cache(); c != nil && !req.Stream && len(resp.ToolCalls) == 0 && resp.Content != "" {
		c.Put(req, resp)
	}
}
