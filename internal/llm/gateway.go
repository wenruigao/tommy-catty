package llm

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

var (
	// ErrProviderNotFound 表示请求的供应商未注册
	ErrProviderNotFound = errors.New("llm: provider not found")
	// ErrAllProvidersFailed 表示所有供应商（含回退）均调用失败
	ErrAllProvidersFailed = errors.New("llm: all providers failed")
	// ErrCircuitOpen 表示供应商熔断器已打开
	ErrCircuitOpen = errors.New("llm: circuit breaker open")
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

// Gateway 是 LLM 供应商的统一网关，负责路由、重试和故障回退
type Gateway struct {
	mu               sync.RWMutex
	providers        map[string]LLMProvider
	defaultProvider  string
	fallbackProvider string
	httpClient       *http.Client
	retryExecutor    *RetryExecutor
}

// NewGateway 创建一个空的 LLM 网关实例（使用默认重试策略）
func NewGateway() *Gateway {
	return NewGatewayWithRetry(DefaultRetryPolicy(), DefaultCircuitBreakerConfig())
}

// NewGatewayWithRetry 创建带有自定义重试策略的网关
func NewGatewayWithRetry(policy RetryPolicy, cbConfig CircuitBreakerConfig) *Gateway {
	executor := NewRetryExecutor(policy, cbConfig)

	// 注册默认日志钩子
	executor.AddHook(func(event RetryEvent) {
		log.Printf("[RETRY] provider=%s attempt=%d/%d category=%v backoff=%s circuit=%s err=%v",
			event.Provider, event.Attempt, event.MaxRetries,
			event.Category, event.Backoff.Round(time.Millisecond),
			event.CircuitState, event.Error)
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
		provider := NewGenericProvider(pcfg)
		gw.Register(provider)
	}

	gw.SetDefault(cfg.DefaultProvider)
	gw.SetFallback(cfg.FallbackProvider)
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

// Chat 发送聊天请求，自动路由到对应供应商，支持重试、熔断和回退
func (g *Gateway) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	provider, err := g.resolveProvider(req.Model)
	if err != nil {
		return ChatResponse{}, err
	}

	// 使用 RetryExecutor 执行（含重试 + 熔断）
	resp, err := g.retryExecutor.Execute(ctx, provider.Name(), func(ctx context.Context) (ChatResponse, error) {
		return provider.Chat(ctx, req)
	})
	if err == nil {
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
				return resp, nil
			}
			return ChatResponse{}, fmt.Errorf("%w: primary(%s): %v; fallback(%s): %v",
				ErrAllProvidersFailed, provider.Name(), err, fallback.Name(), ferr)
		}
	}

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

// resolveProvider 根据模型名称或默认配置解析目标供应商
func (g *Gateway) resolveProvider(model string) (LLMProvider, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	// 优先按供应商名称匹配
	if model != "" {
		if p, ok := g.providers[model]; ok {
			return p, nil
		}
	}

	// 使用默认供应商
	if g.defaultProvider != "" {
		if p, ok := g.providers[g.defaultProvider]; ok {
			return p, nil
		}
	}

	// 如果只有一个供应商，直接使用
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
