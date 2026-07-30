// Package config 提供应用配置加载和管理
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/tommy-cat/agent/internal/llm"
	"gopkg.in/yaml.v3"
)

// Config 应用全局配置
type Config struct {
	// LLM 配置
	LLM LLMConfig `yaml:"llm"`

	// Agent 引擎配置
	Engine EngineConfig `yaml:"engine"`

	// 安全策略文件路径
	PolicyFile string `yaml:"policy_file"`

	// Skill 存储路径
	SkillStorePath string `yaml:"skill_store_path"`

	// 工作目录（文件操作沙箱范围）
	WorkDir string `yaml:"work_dir"`

	// HTTP 服务配置（多用户模式）
	Server ServerConfig `yaml:"server"`

	// 会话管理配置
	Session SessionConfig `yaml:"session"`

	// Databases 数据库只读查询数据源配置（key 为数据源名称）
	Databases map[string]DatabaseEntry `yaml:"databases"`

	// KnowledgeBases 本地知识库配置
	KnowledgeBases []KnowledgeBaseEntry `yaml:"knowledge_bases"`
}

// DatabaseEntry 单个数据库数据源的 YAML 配置（对应 db_query 工具）。
type DatabaseEntry struct {
	// Driver 驱动类型：mysql | postgres | sqlite
	Driver string `yaml:"driver"`
	// DSN 数据源连接串，支持 ${ENV_VAR} 引用
	DSN string `yaml:"dsn"`

	// MaxOpenConns 连接池最大打开连接数（默认 5）
	MaxOpenConns int `yaml:"max_open_conns"`
	// MaxIdleConns 连接池最大空闲连接数（默认 2）
	MaxIdleConns int `yaml:"max_idle_conns"`
	// ConnMaxLifetime 连接最大存活时间（如 "30m"）
	ConnMaxLifetime string `yaml:"conn_max_lifetime"`
	// QueryTimeout 单条查询超时（如 "30s"）
	QueryTimeout string `yaml:"query_timeout"`

	// MaxRows 结果集行数硬上限（默认 500）
	MaxRows int `yaml:"max_rows"`
	// AllowUnion 是否允许 UNION 查询（默认 false）
	AllowUnion bool `yaml:"allow_union"`
	// AllowSubquery 是否允许子查询（默认 true）
	AllowSubquery *bool `yaml:"allow_subquery"`

	// AllowedTables 表名白名单（支持 * 通配符）
	AllowedTables []string `yaml:"allowed_tables"`
	// DeniedColumns 列黑名单（格式 table.column 或 *.column）
	DeniedColumns []string `yaml:"denied_columns"`
}

// KnowledgeBaseEntry 单个本地知识库的 YAML 配置（对应 kb_* 工具）。
type KnowledgeBaseEntry struct {
	// Name 知识库名称
	Name string `yaml:"name"`
	// Paths 待索引的目录/文件列表
	Paths []string `yaml:"paths"`
	// Exclude 排除的 glob 模式
	Exclude []string `yaml:"exclude"`
	// Extensions 允许的文件扩展名（含点，如 .md）
	Extensions []string `yaml:"extensions"`
	// Strategy 分块策略：auto | heading | paragraph
	Strategy string `yaml:"strategy"`
	// MaxTokens 每块最大 token 数（默认 500）
	MaxTokens int `yaml:"max_tokens"`
	// Overlap 滑动窗口重叠 token 数
	Overlap int `yaml:"overlap"`
	// MaxFileMB 单文件大小上限（MB，默认 5）
	MaxFileMB int `yaml:"max_file_mb"`
	// TopK 默认检索返回数（默认 5）
	TopK int `yaml:"top_k"`
}

// LLMConfig LLM 网关配置
// 模型接入完全声明式：在 providers 中添加任意数量的供应商配置即可
type LLMConfig struct {
	// Providers 模型供应商配置（key 为供应商名称，如 "deepseek", "qwen", "ollama"）
	Providers map[string]ProviderEntry `yaml:"providers"`

	// DefaultProvider 默认使用的供应商名称
	DefaultProvider string `yaml:"default_provider"`

	// FallbackProvider 降级供应商名称
	FallbackProvider string `yaml:"fallback_provider"`

	// Retry 重试策略配置
	Retry *RetryEntry `yaml:"retry"`

	// CircuitBreaker 熔断器配置
	CircuitBreaker *CircuitBreakerEntry `yaml:"circuit_breaker"`
}

// RetryEntry 重试策略 YAML 配置
type RetryEntry struct {
	// MaxRetries 最大重试次数（不含首次调用，默认 3）
	MaxRetries int `yaml:"max_retries"`

	// BaseBackoffMs 基础退避时间（毫秒，默认 500）
	BaseBackoffMs int `yaml:"base_backoff_ms"`

	// MaxBackoffMs 最大退避时间上限（毫秒，默认 30000）
	MaxBackoffMs int `yaml:"max_backoff_ms"`

	// BackoffMultiplier 退避倍数（默认 2.0）
	BackoffMultiplier float64 `yaml:"backoff_multiplier"`

	// JitterFactor 抖动因子 0.0~1.0（默认 0.2）
	JitterFactor float64 `yaml:"jitter_factor"`

	// RetryOnUnknown 未知错误是否重试（默认 true）
	RetryOnUnknown bool `yaml:"retry_on_unknown"`

	// MaxTotalTimeoutS 所有重试的总时间上限（秒，默认 120）
	MaxTotalTimeoutS int `yaml:"max_total_timeout_s"`
}

// CircuitBreakerEntry 熔断器 YAML 配置
type CircuitBreakerEntry struct {
	// FailureThreshold 连续失败多少次后触发熔断（默认 5）
	FailureThreshold int `yaml:"failure_threshold"`

	// SuccessThreshold 半开状态下连续成功多少次后恢复（默认 2）
	SuccessThreshold int `yaml:"success_threshold"`

	// OpenTimeoutS 熔断后多久进入半开状态（秒，默认 60）
	OpenTimeoutS int `yaml:"open_timeout_s"`
}

// ProviderEntry 单个模型供应商的配置条目
type ProviderEntry struct {
	// BaseURL API 端点（OpenAI 兼容的 chat/completions 地址）
	BaseURL string `yaml:"base_url"`

	// APIKey 认证密钥，支持两种写法:
	//   直接写: api_key: "sk-xxx"
	//   环境变量引用: api_key: "${DEEPSEEK_API_KEY}"
	APIKey string `yaml:"api_key"`

	// Model 默认模型名称
	Model string `yaml:"model"`

	// MaxTokens 最大上下文 token 数（默认 32768）
	MaxTokens int `yaml:"max_tokens"`

	// Timeout 请求超时（如 "60s", "2m"，默认 120s）
	Timeout string `yaml:"timeout"`

	// Headers 额外自定义请求头
	Headers map[string]string `yaml:"headers"`
}

// EngineConfig Agent 引擎配置
type EngineConfig struct {
	// 最大 ReAct 循环次数
	MaxIterations int `yaml:"max_iterations"`

	// 系统提示词
	SystemPrompt string `yaml:"system_prompt"`
}

// ServerConfig HTTP 服务配置（多用户模式）
type ServerConfig struct {
	// Mode 运行模式："cli"（默认，单用户 REPL）或 "http"（多用户 HTTP 服务）
	Mode string `yaml:"mode"`

	// Addr HTTP 监听地址（mode 为 http 时生效）
	Addr string `yaml:"addr"`

	// AuthMode 认证方式："header"（信任 X-User-ID）| "jwt" | "oauth2"
	AuthMode string `yaml:"auth_mode"`
}

// SessionConfig 会话管理配置
type SessionConfig struct {
	// MaxSessions 最大活跃会话数（默认 1000）
	MaxSessions int `yaml:"max_sessions"`

	// TTL 空闲超时（如 "30m"，默认 30 分钟）
	TTL string `yaml:"ttl"`

	// MemorySize 每用户工作记忆容量（默认 100）
	MemorySize int `yaml:"memory_size"`

	// CleanupInterval 过期扫描间隔（如 "5m"，默认 5 分钟）
	CleanupInterval string `yaml:"cleanup_interval"`

	// RequestsPerMinute 每用户每分钟最大请求数（0 表示不限流）
	RequestsPerMinute int `yaml:"requests_per_minute"`
}

// Load 从 YAML 文件加载配置
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	cfg.resolveEnvVars()
	cfg.applyDefaults()
	return &cfg, nil
}

// Default 返回默认配置
func Default() *Config {
	cfg := &Config{}
	cfg.applyDefaults()
	return cfg
}

// ToGatewayConfig 将 LLM 配置转换为 llm.GatewayConfig，供网关工厂使用
func (c *Config) ToGatewayConfig() llm.GatewayConfig {
	providers := make(map[string]llm.ProviderConfig)
	for name, entry := range c.LLM.Providers {
		timeout := 120 * time.Second
		if entry.Timeout != "" {
			if d, err := time.ParseDuration(entry.Timeout); err == nil {
				timeout = d
			}
		}
		providers[name] = llm.ProviderConfig{
			Name:             name,
			BaseURL:          entry.BaseURL,
			APIKey:           entry.APIKey,
			Model:            entry.Model,
			MaxContextTokens: entry.MaxTokens,
			Timeout:          timeout,
			Headers:          entry.Headers,
		}
	}

	gwCfg := llm.GatewayConfig{
		Providers:        providers,
		DefaultProvider:  c.LLM.DefaultProvider,
		FallbackProvider: c.LLM.FallbackProvider,
	}

	// 映射重试配置
	if c.LLM.Retry != nil {
		gwCfg.Retry = &llm.RetryConfig{
			MaxRetries:        c.LLM.Retry.MaxRetries,
			BaseBackoffMs:     c.LLM.Retry.BaseBackoffMs,
			MaxBackoffMs:      c.LLM.Retry.MaxBackoffMs,
			BackoffMultiplier: c.LLM.Retry.BackoffMultiplier,
			JitterFactor:      c.LLM.Retry.JitterFactor,
			RetryOnUnknown:    c.LLM.Retry.RetryOnUnknown,
			MaxTotalTimeoutS:  c.LLM.Retry.MaxTotalTimeoutS,
		}
	}

	// 映射熔断器配置
	if c.LLM.CircuitBreaker != nil {
		gwCfg.CircuitBreaker = &llm.CircuitBreakerYAMLConfig{
			FailureThreshold: c.LLM.CircuitBreaker.FailureThreshold,
			SuccessThreshold: c.LLM.CircuitBreaker.SuccessThreshold,
			OpenTimeoutS:     c.LLM.CircuitBreaker.OpenTimeoutS,
		}
	}

	return gwCfg
}

// applyDefaults 填充默认值
func (c *Config) applyDefaults() {
	if c.LLM.Providers == nil {
		c.LLM.Providers = make(map[string]ProviderEntry)
	}
	if c.LLM.DefaultProvider == "" {
		// 取第一个 provider 作为默认
		for name := range c.LLM.Providers {
			c.LLM.DefaultProvider = name
			break
		}
	}
	if c.Engine.MaxIterations == 0 {
		c.Engine.MaxIterations = 20
	}
	if c.PolicyFile == "" {
		c.PolicyFile = "config/policy.yaml"
	}
	if c.SkillStorePath == "" {
		c.SkillStorePath = "data/skills.json"
	}
	if c.WorkDir == "" {
		c.WorkDir = "."
	}
	if c.Server.Mode == "" {
		c.Server.Mode = "cli"
	}
	if c.Server.Addr == "" {
		c.Server.Addr = ":8080"
	}
	if c.Server.AuthMode == "" {
		c.Server.AuthMode = "header"
	}
	if c.Session.MaxSessions == 0 {
		c.Session.MaxSessions = 1000
	}
	if c.Session.TTL == "" {
		c.Session.TTL = "30m"
	}
	if c.Session.MemorySize == 0 {
		c.Session.MemorySize = 100
	}
	if c.Session.CleanupInterval == "" {
		c.Session.CleanupInterval = "5m"
	}
}

// resolveEnvVars 解析配置中的 ${ENV_VAR} 引用
func (c *Config) resolveEnvVars() {
	for name, entry := range c.LLM.Providers {
		entry.APIKey = resolveEnvVar(entry.APIKey)
		entry.BaseURL = resolveEnvVar(entry.BaseURL)
		c.LLM.Providers[name] = entry
	}
	for name, db := range c.Databases {
		db.DSN = resolveEnvVar(db.DSN)
		c.Databases[name] = db
	}
}

// resolveEnvVar 将 "${VAR_NAME}" 格式替换为对应环境变量的值
func resolveEnvVar(s string) string {
	if strings.HasPrefix(s, "${") && strings.HasSuffix(s, "}") {
		varName := s[2 : len(s)-1]
		if v := os.Getenv(varName); v != "" {
			return v
		}
		return ""
	}
	return s
}

// Validate 校验配置完整性
func (c *Config) Validate() error {
	if len(c.LLM.Providers) == 0 {
		return fmt.Errorf("config: no LLM providers configured")
	}
	for name, entry := range c.LLM.Providers {
		if entry.BaseURL == "" {
			return fmt.Errorf("config: provider %q missing base_url", name)
		}
	}
	if c.LLM.DefaultProvider != "" {
		if _, ok := c.LLM.Providers[c.LLM.DefaultProvider]; !ok {
			return fmt.Errorf("config: default_provider %q not found in providers", c.LLM.DefaultProvider)
		}
	}
	return nil
}

// SessionTTLDuration 解析会话空闲超时配置，解析失败时返回 30 分钟。
func (c *Config) SessionTTLDuration() time.Duration {
	if d, err := time.ParseDuration(c.Session.TTL); err == nil {
		return d
	}
	return 30 * time.Minute
}

// SessionCleanupDuration 解析过期扫描间隔配置，解析失败时返回 5 分钟。
func (c *Config) SessionCleanupDuration() time.Duration {
	if d, err := time.ParseDuration(c.Session.CleanupInterval); err == nil {
		return d
	}
	return 5 * time.Minute
}
