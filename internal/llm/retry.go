package llm

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ============================================================
// 错误分类
// ============================================================

// ErrorCategory 错误类别
type ErrorCategory int

const (
	// CategoryRetryable 可重试错误（网络超时、5xx、429 等瞬态故障）
	CategoryRetryable ErrorCategory = iota
	// CategoryRateLimited 速率限制（429），需尊重 Retry-After
	CategoryRateLimited
	// CategoryNonRetryable 不可重试错误（400/401/403/404 等客户端错误）
	CategoryNonRetryable
	// CategoryUnknown 未知错误，默认按可重试处理
	CategoryUnknown
)

// APIError 携带 HTTP 状态码的结构化错误
type APIError struct {
	StatusCode int           // HTTP 状态码
	Message    string        // 错误描述
	RetryAfter time.Duration // 服务端建议的等待时间（429 时有效）
	Provider   string        // 供应商名称
	Retryable  bool          // 是否可重试
}

func (e *APIError) Error() string {
	return fmt.Sprintf("[%s] status=%d: %s", e.Provider, e.StatusCode, e.Message)
}

// ClassifyError 根据错误内容判断错误类别
func ClassifyError(err error) ErrorCategory {
	if err == nil {
		return CategoryNonRetryable
	}

	// 结构化 APIError 直接判断
	if apiErr, ok := err.(*APIError); ok {
		if apiErr.Retryable {
			if apiErr.StatusCode == http.StatusTooManyRequests {
				return CategoryRateLimited
			}
			return CategoryRetryable
		}
		return CategoryNonRetryable
	}

	errMsg := err.Error()

	// 网络层错误 — 可重试
	retryablePatterns := []string{
		"connection refused",
		"connection reset",
		"connection timed out",
		"i/o timeout",
		"no such host",
		"TLS handshake timeout",
		"EOF",
		"broken pipe",
		"network is unreachable",
		"temporary failure",
	}
	for _, p := range retryablePatterns {
		if strings.Contains(strings.ToLower(errMsg), p) {
			return CategoryRetryable
		}
	}

	// HTTP 状态码判断
	if strings.Contains(errMsg, "status=429") {
		return CategoryRateLimited
	}
	for _, code := range []string{"status=500", "status=502", "status=503", "status=504"} {
		if strings.Contains(errMsg, code) {
			return CategoryRetryable
		}
	}
	for _, code := range []string{"status=400", "status=401", "status=403", "status=404", "status=422"} {
		if strings.Contains(errMsg, code) {
			return CategoryNonRetryable
		}
	}

	// 上下文取消/超时 — 不可重试
	if strings.Contains(errMsg, "context canceled") || strings.Contains(errMsg, "context deadline exceeded") {
		return CategoryNonRetryable
	}

	return CategoryUnknown
}

// ============================================================
// 重试策略配置
// ============================================================

// RetryPolicy 重试策略配置
type RetryPolicy struct {
	// MaxRetries 最大重试次数（不含首次调用）
	MaxRetries int

	// BaseBackoff 基础退避时间
	BaseBackoff time.Duration

	// MaxBackoff 最大退避时间上限
	MaxBackoff time.Duration

	// BackoffMultiplier 退避倍数（指数增长因子）
	BackoffMultiplier float64

	// JitterFactor 抖动因子 (0.0~1.0)，防止惊群效应
	// 实际等待时间 = backoff * (1 - jitter + rand*2*jitter)
	JitterFactor float64

	// RetryOnUnknown 未知错误是否重试
	RetryOnUnknown bool

	// MaxTotalTimeout 所有重试的总时间上限（0 表示不限制）
	MaxTotalTimeout time.Duration
}

// DefaultRetryPolicy 默认重试策略
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxRetries:        3,
		BaseBackoff:       500 * time.Millisecond,
		MaxBackoff:        30 * time.Second,
		BackoffMultiplier: 2.0,
		JitterFactor:      0.2,
		RetryOnUnknown:    true,
		MaxTotalTimeout:   2 * time.Minute,
	}
}

// CalculateBackoff 计算第 attempt 次重试的退避时间（含抖动）
func (p RetryPolicy) CalculateBackoff(attempt int, retryAfter time.Duration) time.Duration {
	// 如果服务端指定了 Retry-After，优先使用
	if retryAfter > 0 {
		return retryAfter
	}

	// 指数退避: base * multiplier^(attempt-1)
	backoff := float64(p.BaseBackoff) * math.Pow(p.BackoffMultiplier, float64(attempt-1))

	// 上限截断
	if backoff > float64(p.MaxBackoff) {
		backoff = float64(p.MaxBackoff)
	}

	// 添加抖动: backoff * (1 - jitter + rand*2*jitter)
	if p.JitterFactor > 0 {
		jitter := 1.0 - p.JitterFactor + rand.Float64()*2*p.JitterFactor
		backoff *= jitter
	}

	return time.Duration(backoff)
}

// ShouldRetry 判断是否应该重试
func (p RetryPolicy) ShouldRetry(err error, attempt int) bool {
	if attempt >= p.MaxRetries {
		return false
	}

	category := ClassifyError(err)
	switch category {
	case CategoryRetryable, CategoryRateLimited:
		return true
	case CategoryNonRetryable:
		return false
	case CategoryUnknown:
		return p.RetryOnUnknown
	}
	return false
}

// ============================================================
// 熔断器 (Circuit Breaker)
// ============================================================

// CircuitState 熔断器状态
type CircuitState int

const (
	CircuitClosed   CircuitState = iota // 正常状态，请求正常通过
	CircuitOpen                         // 熔断状态，拒绝所有请求
	CircuitHalfOpen                     // 半开状态，允许探测请求
)

func (s CircuitState) String() string {
	switch s {
	case CircuitClosed:
		return "closed"
	case CircuitOpen:
		return "open"
	case CircuitHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// CircuitBreakerConfig 熔断器配置
type CircuitBreakerConfig struct {
	// FailureThreshold 连续失败多少次后触发熔断
	FailureThreshold int

	// SuccessThreshold 半开状态下连续成功多少次后恢复
	SuccessThreshold int

	// OpenTimeout 熔断后多久进入半开状态
	OpenTimeout time.Duration
}

// DefaultCircuitBreakerConfig 默认熔断器配置
func DefaultCircuitBreakerConfig() CircuitBreakerConfig {
	return CircuitBreakerConfig{
		FailureThreshold: 5,
		SuccessThreshold: 2,
		OpenTimeout:      60 * time.Second,
	}
}

// CircuitBreaker 熔断器实现
type CircuitBreaker struct {
	mu              sync.Mutex
	config          CircuitBreakerConfig
	state           CircuitState
	failures        int       // 连续失败计数
	successes       int       // 半开状态连续成功计数
	lastFailure     time.Time // 最后一次失败时间
	lastStateChange time.Time
}

// NewCircuitBreaker 创建熔断器
func NewCircuitBreaker(cfg CircuitBreakerConfig) *CircuitBreaker {
	return &CircuitBreaker{
		config:          cfg,
		state:           CircuitClosed,
		lastStateChange: time.Now(),
	}
}

// Allow 判断当前是否允许请求通过
func (cb *CircuitBreaker) Allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case CircuitClosed:
		return true
	case CircuitOpen:
		// 超过 OpenTimeout 后进入半开状态
		if time.Since(cb.lastFailure) > cb.config.OpenTimeout {
			cb.setState(CircuitHalfOpen)
			cb.successes = 0
			return true
		}
		return false
	case CircuitHalfOpen:
		return true
	}
	return false
}

// RecordSuccess 记录一次成功
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case CircuitClosed:
		cb.failures = 0
	case CircuitHalfOpen:
		cb.successes++
		if cb.successes >= cb.config.SuccessThreshold {
			cb.setState(CircuitClosed)
			cb.failures = 0
			cb.successes = 0
		}
	}
}

// RecordFailure 记录一次失败
func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.lastFailure = time.Now()

	switch cb.state {
	case CircuitClosed:
		cb.failures++
		if cb.failures >= cb.config.FailureThreshold {
			cb.setState(CircuitOpen)
		}
	case CircuitHalfOpen:
		// 半开状态下失败，立即重新熔断
		cb.setState(CircuitOpen)
		cb.successes = 0
	}
}

// State 返回当前状态
func (cb *CircuitBreaker) State() CircuitState {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.state
}

func (cb *CircuitBreaker) setState(s CircuitState) {
	cb.state = s
	cb.lastStateChange = time.Now()
}

// ============================================================
// 重试事件钩子
// ============================================================

// RetryEvent 重试事件（用于日志和监控）
type RetryEvent struct {
	Provider     string        // 供应商名称
	Attempt      int           // 第几次尝试（从 1 开始）
	MaxRetries   int           // 最大重试次数
	Error        error         // 本次错误
	Category     ErrorCategory // 错误类别
	Backoff      time.Duration // 本次退避时间
	CircuitState CircuitState  // 熔断器状态
	Timestamp    time.Time
}

// RetryHook 重试事件回调函数
type RetryHook func(event RetryEvent)

// ============================================================
// 重试执行器
// ============================================================

// RetryExecutor 封装完整的重试逻辑（含熔断器）
type RetryExecutor struct {
	policy   RetryPolicy
	breakers map[string]*CircuitBreaker // 每个供应商独立的熔断器
	cbConfig CircuitBreakerConfig
	hooks    []RetryHook
	mu       sync.RWMutex
}

// NewRetryExecutor 创建重试执行器
func NewRetryExecutor(policy RetryPolicy, cbConfig CircuitBreakerConfig) *RetryExecutor {
	return &RetryExecutor{
		policy:   policy,
		breakers: make(map[string]*CircuitBreaker),
		cbConfig: cbConfig,
	}
}

// AddHook 注册重试事件钩子
func (re *RetryExecutor) AddHook(hook RetryHook) {
	re.mu.Lock()
	defer re.mu.Unlock()
	re.hooks = append(re.hooks, hook)
}

// getBreaker 获取或创建供应商对应的熔断器
func (re *RetryExecutor) getBreaker(provider string) *CircuitBreaker {
	re.mu.RLock()
	cb, ok := re.breakers[provider]
	re.mu.RUnlock()
	if ok {
		return cb
	}

	re.mu.Lock()
	defer re.mu.Unlock()
	// 双重检查
	if cb, ok = re.breakers[provider]; ok {
		return cb
	}
	cb = NewCircuitBreaker(re.cbConfig)
	re.breakers[provider] = cb
	return cb
}

// Execute 执行带重试和熔断的操作
func (re *RetryExecutor) Execute(ctx context.Context, provider string, fn func(ctx context.Context) (ChatResponse, error)) (ChatResponse, error) {
	breaker := re.getBreaker(provider)

	// 总超时控制
	if re.policy.MaxTotalTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, re.policy.MaxTotalTimeout)
		defer cancel()
	}

	var lastErr error

	for attempt := 0; attempt <= re.policy.MaxRetries; attempt++ {
		// 检查上下文
		if ctx.Err() != nil {
			return ChatResponse{}, ctx.Err()
		}

		// 熔断器检查
		if !breaker.Allow() {
			lastErr = fmt.Errorf("provider %s circuit breaker is open, rejecting request", provider)
			// 熔断状态下不重试，直接返回
			return ChatResponse{}, lastErr
		}

		// 执行实际调用
		resp, err := fn(ctx)
		if err == nil {
			breaker.RecordSuccess()
			return resp, nil
		}

		lastErr = err
		breaker.RecordFailure()

		// 判断是否应该重试
		if !re.policy.ShouldRetry(err, attempt) {
			return ChatResponse{}, fmt.Errorf("provider %s: non-retryable error: %w", provider, err)
		}

		// 最后一次尝试失败，不再等待
		if attempt >= re.policy.MaxRetries {
			break
		}

		// 计算退避时间
		var retryAfter time.Duration
		if apiErr, ok := err.(*APIError); ok {
			retryAfter = apiErr.RetryAfter
		}
		backoff := re.policy.CalculateBackoff(attempt+1, retryAfter)

		// 触发钩子
		re.emitHook(RetryEvent{
			Provider:     provider,
			Attempt:      attempt + 1,
			MaxRetries:   re.policy.MaxRetries,
			Error:        err,
			Category:     ClassifyError(err),
			Backoff:      backoff,
			CircuitState: breaker.State(),
			Timestamp:    time.Now(),
		})

		// 等待退避
		select {
		case <-ctx.Done():
			return ChatResponse{}, ctx.Err()
		case <-time.After(backoff):
		}
	}

	return ChatResponse{}, fmt.Errorf("provider %s failed after %d attempts: %w", provider, re.policy.MaxRetries+1, lastErr)
}

// ExecuteStream 执行带重试的流式请求（仅重试连接建立阶段）
func (re *RetryExecutor) ExecuteStream(ctx context.Context, provider string, fn func(ctx context.Context) (<-chan StreamChunk, error)) (<-chan StreamChunk, error) {
	breaker := re.getBreaker(provider)

	if re.policy.MaxTotalTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, re.policy.MaxTotalTimeout)
		// 注意: 流式场景不能 defer cancel，否则 channel 会被关闭
		// 由调用方负责取消
		_ = cancel
	}

	var lastErr error

	for attempt := 0; attempt <= re.policy.MaxRetries; attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		if !breaker.Allow() {
			return nil, fmt.Errorf("provider %s circuit breaker is open", provider)
		}

		ch, err := fn(ctx)
		if err == nil {
			breaker.RecordSuccess()
			return ch, nil
		}

		lastErr = err
		breaker.RecordFailure()

		if !re.policy.ShouldRetry(err, attempt) {
			return nil, fmt.Errorf("provider %s: non-retryable stream error: %w", provider, err)
		}

		if attempt >= re.policy.MaxRetries {
			break
		}

		backoff := re.policy.CalculateBackoff(attempt+1, 0)

		re.emitHook(RetryEvent{
			Provider:     provider,
			Attempt:      attempt + 1,
			MaxRetries:   re.policy.MaxRetries,
			Error:        err,
			Category:     ClassifyError(err),
			Backoff:      backoff,
			CircuitState: breaker.State(),
			Timestamp:    time.Now(),
		})

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
	}

	return nil, fmt.Errorf("provider %s stream failed after %d attempts: %w", provider, re.policy.MaxRetries+1, lastErr)
}

func (re *RetryExecutor) emitHook(event RetryEvent) {
	re.mu.RLock()
	hooks := re.hooks
	re.mu.RUnlock()

	for _, h := range hooks {
		h(event)
	}
}

// ============================================================
// 辅助: 从 HTTP 响应构建 APIError
// ============================================================

// NewAPIError 从 HTTP 响应构建结构化错误
func NewAPIError(provider string, statusCode int, body string, retryAfterHeader string) *APIError {
	apiErr := &APIError{
		StatusCode: statusCode,
		Message:    truncate(body, 256),
		Provider:   provider,
	}

	// 判断是否可重试
	switch {
	case statusCode == http.StatusTooManyRequests:
		apiErr.Retryable = true
		apiErr.RetryAfter = parseRetryAfter(retryAfterHeader)
	case statusCode >= 500:
		apiErr.Retryable = true
	default:
		apiErr.Retryable = false
	}

	return apiErr
}

// parseRetryAfter 解析 Retry-After 头（支持秒数和 HTTP 日期格式）
func parseRetryAfter(header string) time.Duration {
	if header == "" {
		return 0
	}

	// 尝试解析为秒数
	var seconds int
	if _, err := fmt.Sscanf(header, "%d", &seconds); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}

	// 尝试解析为 HTTP 日期
	if t, err := http.ParseTime(header); err == nil {
		d := time.Until(t)
		if d > 0 {
			return d
		}
	}

	// 默认 5 秒
	return 5 * time.Second
}

// truncate 截断字符串
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
