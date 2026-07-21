package llm

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sync"
	"time"
)

var (
	// ErrProviderNotFound 表示请求的供应商未注册
	ErrProviderNotFound = errors.New("llm: provider not found")
	// ErrAllProvidersFailed 表示所有供应商（含回退）均调用失败
	ErrAllProvidersFailed = errors.New("llm: all providers failed")
)

const (
	maxRetries  = 3                      // 最大重试次数
	baseBackoff = 500 * time.Millisecond // 基础退避时间
)

// GatewayConfig 网关配置（从 YAML 配置文件映射）
type GatewayConfig struct {
	// Providers 模型供应商配置列表（key 为供应商名称）
	Providers map[string]ProviderConfig `yaml:"providers"`

	// DefaultProvider 默认使用的供应商名称
	DefaultProvider string `yaml:"default_provider"`

	// FallbackProvider 降级供应商名称
	FallbackProvider string `yaml:"fallback_provider"`
}

// Gateway 是 LLM 供应商的统一网关，负责路由、重试和故障回退
type Gateway struct {
	mu               sync.RWMutex
	providers        map[string]LLMProvider
	defaultProvider  string
	fallbackProvider string
	httpClient       *http.Client
}

// NewGateway 创建一个空的 LLM 网关实例
func NewGateway() *Gateway {
	return &Gateway{
		providers: make(map[string]LLMProvider),
		httpClient: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

// NewGatewayFromConfig 根据配置创建网关并自动注册所有供应商
// 这是推荐的初始化方式：所有模型接入完全由配置驱动，无需硬编码
func NewGatewayFromConfig(cfg GatewayConfig) *Gateway {
	gw := NewGateway()

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

// Chat 发送聊天请求，自动路由到对应供应商，支持重试和回退
func (g *Gateway) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	provider, err := g.resolveProvider(req.Model)
	if err != nil {
		return ChatResponse{}, err
	}

	// 带重试的主供应商调用
	resp, err := g.chatWithRetry(ctx, provider, req)
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
			resp, ferr = g.chatWithRetry(ctx, fallback, req)
			if ferr == nil {
				return resp, nil
			}
		}
	}

	return ChatResponse{}, fmt.Errorf("%w: primary error: %v", ErrAllProvidersFailed, err)
}

// ChatStream 发送流式聊天请求，支持回退
func (g *Gateway) ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, error) {
	provider, err := g.resolveProvider(req.Model)
	if err != nil {
		return nil, err
	}

	// 尝试主供应商
	ch, err := provider.ChatStream(ctx, req)
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
			ch, ferr = fallback.ChatStream(ctx, req)
			if ferr == nil {
				return ch, nil
			}
		}
	}

	return nil, fmt.Errorf("%w: primary error: %v", ErrAllProvidersFailed, err)
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

// chatWithRetry 带指数退避的重试调用
func (g *Gateway) chatWithRetry(ctx context.Context, provider LLMProvider, req ChatRequest) (ChatResponse, error) {
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			// 指数退避: 500ms, 1s, 2s
			backoff := time.Duration(float64(baseBackoff) * math.Pow(2, float64(attempt-1)))
			select {
			case <-ctx.Done():
				return ChatResponse{}, ctx.Err()
			case <-time.After(backoff):
			}
		}

		resp, err := provider.Chat(ctx, req)
		if err == nil {
			return resp, nil
		}
		lastErr = err

		// 如果是上下文取消，不再重试
		if ctx.Err() != nil {
			return ChatResponse{}, ctx.Err()
		}
	}

	return ChatResponse{}, fmt.Errorf("provider %s failed after %d attempts: %w", provider.Name(), maxRetries, lastErr)
}
