package metrics

import (
	"runtime"
	"sync"
)

// ─────────────────────────────────────────────────────────────
// 指标名称常量（Prometheus 命名规范：tommy_<子系统>_<指标>_<单位>）
// ─────────────────────────────────────────────────────────────

// LLM 网关指标
const (
	MetricLLMCalls           = "tommy_llm_calls_total"
	MetricLLMRetries         = "tommy_llm_retries_total"
	MetricLLMCircuitState    = "tommy_llm_circuit_breaker_state"
	MetricLLMCacheHits       = "tommy_llm_cache_hits_total"
	MetricLLMCacheMisses     = "tommy_llm_cache_misses_total"
	MetricLLMCacheSize       = "tommy_llm_cache_size"
	MetricLLMTokens          = "tommy_llm_tokens_total"
	MetricLLMTokensCached    = "tommy_llm_tokens_cached_total"
	MetricLLMRequestDuration = "tommy_llm_request_duration_seconds"
	MetricLLMBudgetUsed      = "tommy_llm_budget_used_tokens"
	MetricLLMBudgetLimit     = "tommy_llm_budget_limit_tokens"
)

// 会话层指标
const (
	MetricSessionActive      = "tommy_session_active"
	MetricSessionCreated     = "tommy_session_created_total"
	MetricSessionRateLimited = "tommy_session_rate_limited_total"
)

// 工具层指标
const (
	MetricToolCalls    = "tommy_tool_calls_total"
	MetricToolDuration = "tommy_tool_duration_seconds"
)

// 安全层指标
const (
	MetricSecurityEvents = "tommy_security_events_total"
)

// Channel 层指标
const (
	MetricChannelMessages = "tommy_channel_messages_total"
	MetricChannelDelivery = "tommy_channel_delivery_total"
)

// 多 Agent 层指标
const (
	MetricAgentDelegations    = "tommy_agent_delegations_total"
	MetricAgentWorkers        = "tommy_agent_workers_total"
	MetricAgentWorkerDuration = "tommy_agent_worker_duration_seconds"
)

// 运行时指标
const (
	MetricRuntimeMemory     = "tommy_runtime_memory_bytes"
	MetricRuntimeGoroutines = "tommy_runtime_goroutines"
)

// 数据库查询缓存指标
const (
	MetricDBQueryCacheHits   = "tommy_dbquery_cache_hits_total"
	MetricDBQueryCacheMisses = "tommy_dbquery_cache_misses_total"
	MetricDBQueryCacheSize   = "tommy_dbquery_cache_size"
)

// ─────────────────────────────────────────────────────────────
// 全局指标注册与访问器
// ─────────────────────────────────────────────────────────────

var (
	once       sync.Once
	registered bool

	// LLM 网关
	llmCalls           *CounterVec
	llmRetries         *CounterVec
	llmCircuitState    *GaugeVec
	llmTokens          *CounterVec
	llmTokensCached    *Counter
	llmRequestDuration *CounterVec
	llmBudgetUsed      *Gauge
	llmBudgetLimit     *Gauge

	// 会话层
	sessionActive      *Gauge
	sessionCreated     *Counter
	sessionRateLimited *Counter

	// 工具层
	toolCalls    *CounterVec
	toolDuration *CounterVec

	// 安全层
	securityEvents *CounterVec

	// Channel 层
	channelMessages *CounterVec
	channelDelivery *CounterVec

	// 多 Agent 层
	agentDelegations    *Counter
	agentWorkers        *CounterVec
	agentWorkerDuration *CounterVec

	// 运行时
	runtimeMemory     *Gauge
	runtimeGoroutines *Gauge

	// 数据库查询缓存
	dbQueryCacheHits   *Counter
	dbQueryCacheMisses *Counter
	dbQueryCacheSize   *Gauge
)

// ensureRegistered 确保所有指标已注册到全局注册表（幂等）。
func ensureRegistered() {
	if registered {
		return
	}
	once.Do(func() {
		r := globalRegistry

		// LLM 网关
		llmCalls = r.RegisterCounterVec(MetricLLMCalls, "LLM 调用总数")
		llmRetries = r.RegisterCounterVec(MetricLLMRetries, "LLM 重试总数")
		llmCircuitState = r.RegisterGaugeVec(MetricLLMCircuitState, "熔断器状态（0=closed 1=open 2=half-open）")
		llmTokens = r.RegisterCounterVec(MetricLLMTokens, "Token 消耗总数")
		llmTokensCached = r.RegisterCounter(MetricLLMTokensCached, "提示缓存命中 Token 总数")
		llmRequestDuration = r.RegisterCounterVec(MetricLLMRequestDuration, "LLM 请求累计耗时（秒）")
		llmBudgetUsed = r.RegisterGauge(MetricLLMBudgetUsed, "当日已用 Token")
		llmBudgetLimit = r.RegisterGauge(MetricLLMBudgetLimit, "日 Token 预算上限")

		// 会话层
		sessionActive = r.RegisterGauge(MetricSessionActive, "当前活跃会话数")
		sessionCreated = r.RegisterCounter(MetricSessionCreated, "累计创建会话数")
		sessionRateLimited = r.RegisterCounter(MetricSessionRateLimited, "限流拒绝总数")

		// 工具层
		toolCalls = r.RegisterCounterVec(MetricToolCalls, "工具调用总数")
		toolDuration = r.RegisterCounterVec(MetricToolDuration, "工具执行累计耗时（秒）")

		// 安全层
		securityEvents = r.RegisterCounterVec(MetricSecurityEvents, "安全事件总数")

		// Channel 层
		channelMessages = r.RegisterCounterVec(MetricChannelMessages, "Channel 消息总数")
		channelDelivery = r.RegisterCounterVec(MetricChannelDelivery, "Channel 出站投递总数")

		// 多 Agent 层
		agentDelegations = r.RegisterCounter(MetricAgentDelegations, "delegate_task 调用总数")
		agentWorkers = r.RegisterCounterVec(MetricAgentWorkers, "Worker 执行总数")
		agentWorkerDuration = r.RegisterCounterVec(MetricAgentWorkerDuration, "Worker 累计执行耗时（秒）")

		// 运行时
		runtimeMemory = r.RegisterGauge(MetricRuntimeMemory, "当前进程内存占用（字节）")
		runtimeGoroutines = r.RegisterGauge(MetricRuntimeGoroutines, "当前 goroutine 数")

		// 数据库查询缓存
		dbQueryCacheHits = r.RegisterCounter(MetricDBQueryCacheHits, "db_query 缓存命中总数")
		dbQueryCacheMisses = r.RegisterCounter(MetricDBQueryCacheMisses, "db_query 缓存未命中总数")
		dbQueryCacheSize = r.RegisterGauge(MetricDBQueryCacheSize, "db_query 当前缓存条目数")

		registered = true
	})
}

// ─────────────────────────────────────────────────────────────
// 公开访问器（供其他包在运行时调用）
// ─────────────────────────────────────────────────────────────

// LLMCalls 返回 LLM 调用计数器（labels: provider, status）。
func LLMCalls() *CounterVec { ensureRegistered(); return llmCalls }

// LLMRetries 返回 LLM 重试计数器（labels: provider）。
func LLMRetries() *CounterVec { ensureRegistered(); return llmRetries }

// LLMCircuitState 返回熔断器状态 Gauge（labels: provider）。
func LLMCircuitState() *GaugeVec { ensureRegistered(); return llmCircuitState }

// LLMTokens 返回 Token 消耗计数器（labels: category, model）。
func LLMTokens() *CounterVec { ensureRegistered(); return llmTokens }

// LLMTokensCached 返回提示缓存命中 Token 计数器。
func LLMTokensCached() *Counter { ensureRegistered(); return llmTokensCached }

// LLMRequestDuration 返回 LLM 请求累计耗时计数器（labels: provider）。
func LLMRequestDuration() *CounterVec { ensureRegistered(); return llmRequestDuration }

// LLMBudgetUsed 返回当日已用 Token Gauge。
func LLMBudgetUsed() *Gauge { ensureRegistered(); return llmBudgetUsed }

// LLMBudgetLimit 返回日 Token 预算上限 Gauge。
func LLMBudgetLimit() *Gauge { ensureRegistered(); return llmBudgetLimit }

// SessionActive 返回活跃会话数 Gauge。
func SessionActive() *Gauge { ensureRegistered(); return sessionActive }

// SessionCreated 返回累计创建会话计数器。
func SessionCreated() *Counter { ensureRegistered(); return sessionCreated }

// SessionRateLimited 返回限流拒绝计数器。
func SessionRateLimited() *Counter { ensureRegistered(); return sessionRateLimited }

// ToolCalls 返回工具调用计数器（labels: tool, status）。
func ToolCalls() *CounterVec { ensureRegistered(); return toolCalls }

// ToolDuration 返回工具执行累计耗时计数器（labels: tool）。
func ToolDuration() *CounterVec { ensureRegistered(); return toolDuration }

// SecurityEvents 返回安全事件计数器（labels: checkpoint, effect）。
func SecurityEvents() *CounterVec { ensureRegistered(); return securityEvents }

// ChannelMessages 返回 Channel 消息计数器（labels: channel, status）。
func ChannelMessages() *CounterVec { ensureRegistered(); return channelMessages }

// ChannelDelivery 返回 Channel 出站投递计数器（labels: channel, status）。
func ChannelDelivery() *CounterVec { ensureRegistered(); return channelDelivery }

// AgentDelegations 返回 delegate_task 调用计数器。
func AgentDelegations() *Counter { ensureRegistered(); return agentDelegations }

// AgentWorkers 返回 Worker 执行计数器（labels: role, status）。
func AgentWorkers() *CounterVec { ensureRegistered(); return agentWorkers }

// AgentWorkerDuration 返回 Worker 累计执行耗时计数器（labels: role）。
func AgentWorkerDuration() *CounterVec { ensureRegistered(); return agentWorkerDuration }

// DBQueryCacheHits 返回 db_query 缓存命中计数器。
func DBQueryCacheHits() *Counter { ensureRegistered(); return dbQueryCacheHits }

// DBQueryCacheMisses 返回 db_query 缓存未命中计数器。
func DBQueryCacheMisses() *Counter { ensureRegistered(); return dbQueryCacheMisses }

// DBQueryCacheSize 返回 db_query 缓存条目数 Gauge。
func DBQueryCacheSize() *Gauge { ensureRegistered(); return dbQueryCacheSize }

// ─────────────────────────────────────────────────────────────
// CollectAll — 从各数据源拉取 Gauge 值（每次 /metrics 抓取前调用）
// ─────────────────────────────────────────────────────────────

// GaugeCollector 定义一个 Gauge 拉取函数。
type GaugeCollector func()

var (
	collectorMu sync.Mutex
	collectors  []GaugeCollector
)

// RegisterCollector 注册一个 Gauge 拉取函数，CollectAll 时依次调用。
func RegisterCollector(fn GaugeCollector) {
	collectorMu.Lock()
	defer collectorMu.Unlock()
	collectors = append(collectors, fn)
}

// CollectAll 执行所有注册的 Gauge 拉取函数，并刷新运行时指标。
// 应在每次 /metrics 端点被请求时调用。
func CollectAll() {
	ensureRegistered()

	// 运行时指标（始终刷新）
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	runtimeMemory.Set(float64(m.Alloc))
	runtimeGoroutines.Set(float64(runtime.NumGoroutine()))

	// 执行注册的拉取函数
	collectorMu.Lock()
	fns := make([]GaugeCollector, len(collectors))
	copy(fns, collectors)
	collectorMu.Unlock()

	for _, fn := range fns {
		fn()
	}
}
