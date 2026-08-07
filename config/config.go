// Package config 提供应用配置加载和管理
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/tommy-cat/agent/internal/engine"
	"github.com/tommy-cat/agent/internal/llm"
	"github.com/tommy-cat/agent/internal/search"
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

	// AuditLogPath 安全审计日志 JSONL 落盘路径（空则禁用审计）：
	// 记录 L2+ 工具调用与所有命中策略的决策（操作人/输入/审批可追溯）
	AuditLogPath string `yaml:"audit_log_path"`

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

	// DBQueryCache db_query 查询结果缓存配置（nil 时默认启用：容量 200 / TTL 5 分钟）
	DBQueryCache *DBQueryCacheConfig `yaml:"db_query_cache"`

	// KnowledgeBases 本地知识库配置
	KnowledgeBases []KnowledgeBaseEntry `yaml:"knowledge_bases"`

	// Search 搜索功能配置
	Search search.SearchConfig `yaml:"search"`

	// MCP MCP Server 接入配置
	MCP MCPConfig `yaml:"mcp"`

	// Persona Agent 人格体系配置（agent.md / soul.md / 用户画像）
	Persona PersonaConfig `yaml:"persona"`

	// Channels 通用 Channel 接入层配置（key 为渠道名，如 webhook）：
	// 对齐 OpenClaw 的 Channel 机制，声明式接入外部消息平台；未配置时接入层不启动
	Channels map[string]ChannelEntry `yaml:"channels"`
}

// PersonaConfig Agent 人格体系配置
type PersonaConfig struct {
	// AgentMDPath agent.md 路径（Agent 职责与权限边界，默认 config/agent.md）
	AgentMDPath string `yaml:"agent_md_path"`

	// SoulMDPath soul.md 路径（Agent 人格与对话风格，默认 config/soul.md）
	SoulMDPath string `yaml:"soul_md_path"`

	// UserProfilesDir 用户画像目录（默认 data/users）
	UserProfilesDir string `yaml:"user_profiles_dir"`

	// ProfileUpdateIntervalRuns 每完成多少次任务更新一次用户画像（默认 3）
	ProfileUpdateIntervalRuns int `yaml:"profile_update_interval_runs"`
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

	// Retry 重试策略配置（直接使用 llm 包的类型）
	Retry *llm.RetryConfig `yaml:"retry"`

	// CircuitBreaker 熔断器配置（直接使用 llm 包的类型）
	CircuitBreaker *llm.CircuitBreakerYAMLConfig `yaml:"circuit_breaker"`

	// Cache 语义缓存配置（可选，仅 L1 精确哈希层；L2 向量相似层属 P2）
	Cache *llm.CacheYAMLConfig `yaml:"cache"`

	// Meter Token 计量/预算配置（可选；计量始终启用，此处控制日预算）
	Meter *llm.MeterYAMLConfig `yaml:"meter"`
}

// DBQueryCacheConfig db_query 查询结果缓存配置（跨数据源共享一个缓存实例）。
type DBQueryCacheConfig struct {
	Enabled  bool   `yaml:"enabled"`  // 显式置 false 才禁用（缺省启用）
	Capacity int    `yaml:"capacity"` // 缓存条目容量（默认 200）
	TTL      string `yaml:"ttl"`      // 过期时间，如 "5m"（默认 5 分钟）
}

// ProviderEntry 单个模型供应商的配置条目
// 保留此类型是因为 Timeout 字段使用 YAML 友好的 string（如 "120s"），
// 而 llm.ProviderConfig 使用 time.Duration，需要转换。
type ProviderEntry struct {
	// Protocol 协议类型："openai"（默认，OpenAI Chat Completions 兼容协议）
	// 或 "anthropic"（Anthropic Messages API）
	Protocol string `yaml:"protocol"`

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

	// Reflection 自我反思与重规划配置（默认禁用）
	Reflection ReflectionEntry `yaml:"reflection"`

	// TraceExportPath 追踪 span 的 JSONL 导出路径（空则禁用导出）
	TraceExportPath string `yaml:"trace_export_path"`
}

// ReflectionEntry 引擎自我反思机制的 YAML 配置（对应 engine.ReflectionConfig）。
type ReflectionEntry struct {
	// Enabled 是否启用反思（默认 false）
	Enabled bool `yaml:"enabled"`
	// IntervalSteps 每隔多少步执行一次阶段性反思（默认 5）
	IntervalSteps int `yaml:"interval_steps"`
	// SatisfactionThreshold 满意度低于此分数触发调整（默认 0.6）
	SatisfactionThreshold float64 `yaml:"satisfaction_threshold"`
	// MaxReplans 最大重规划次数（默认 2）
	MaxReplans int `yaml:"max_replans"`
	// DeviationThreshold 累积偏差超过此值触发重规划（默认 1.5）
	DeviationThreshold float64 `yaml:"deviation_threshold"`
}

// ToReflectionConfig 转换为 engine.ReflectionConfig；未启用时返回 nil（禁用反思）。
// 未显式设置的数值字段沿用 engine 包默认值。
func (e ReflectionEntry) ToReflectionConfig() *engine.ReflectionConfig {
	if !e.Enabled {
		return nil
	}
	cfg := engine.DefaultReflectionConfig()
	cfg.Enabled = true
	if e.IntervalSteps > 0 {
		cfg.IntervalSteps = e.IntervalSteps
	}
	if e.SatisfactionThreshold > 0 {
		cfg.SatisfactionThreshold = e.SatisfactionThreshold
	}
	if e.MaxReplans > 0 {
		cfg.MaxReplans = e.MaxReplans
	}
	if e.DeviationThreshold > 0 {
		cfg.DeviationThreshold = e.DeviationThreshold
	}
	return &cfg
}

// ServerConfig HTTP 服务配置（多用户模式）
type ServerConfig struct {
	// Mode 运行模式："cli"（默认，单用户 REPL）或 "http"（多用户 HTTP 服务）
	Mode string `yaml:"mode"`

	// Addr HTTP 监听地址（mode 为 http 时生效）
	Addr string `yaml:"addr"`

	// AuthMode 认证方式："header"（信任 X-User-ID）| "jwt" | "api_key"
	AuthMode string `yaml:"auth_mode"`

	// AuthAPIKey 共享密钥（auth_mode 为 api_key 时必填），支持 ${ENV_VAR} 引用
	AuthAPIKey string `yaml:"auth_api_key"`

	// AuthJWTSecret JWT HS256 签名密钥（auth_mode 为 jwt 时必填），支持 ${ENV_VAR} 引用
	AuthJWTSecret string `yaml:"auth_jwt_secret"`

	// AuthUserID auth_mode 为 api_key 时绑定的固定用户身份（建议配置）：
	// 非空时忽略客户端的 X-User-ID，防止同一密钥持有者互相冒充
	AuthUserID string `yaml:"auth_user_id"`
}

// ChannelEntry 单个 Channel（渠道）的 YAML 配置条目（对应 internal/channel 接入层）。
type ChannelEntry struct {
	// Enabled 是否启用该渠道
	Enabled bool `yaml:"enabled"`

	// AllowUsers 允许使用的平台用户白名单（空或 ["*"] 表示不限制）
	AllowUsers []string `yaml:"allow_users"`

	// GroupMode 群消息模式：always | mention_only | never
	//（mention 判断由渠道 adapter 侧完成，默认 mention_only）
	GroupMode string `yaml:"group_mode"`

	// AckMessage 受理提示（非空时收到消息先回一条，长任务体验关键）
	AckMessage string `yaml:"ack_message"`

	// RequestTimeout 单条消息执行超时（如 "120s"，默认 120s）
	RequestTimeout string `yaml:"request_timeout"`

	// Token 渠道接入令牌（webhook 渠道必填），支持 ${ENV_VAR} 引用
	Token string `yaml:"token"`

	// CallbackURL webhook 默认投递地址，支持 ${ENV_VAR} 引用
	//（单次请求可用 callback_url 字段覆盖）
	CallbackURL string `yaml:"callback_url"`

	// ── 以下字段供各平台 adapter 使用（按需配置，缺省即不启用对应平台）──

	// ClientID 钉钉应用 appKey，支持 ${ENV_VAR} 引用
	ClientID string `yaml:"client_id"`

	// ClientSecret 钉钉应用 appSecret（兼回调加签密钥），支持 ${ENV_VAR} 引用
	ClientSecret string `yaml:"client_secret"`

	// AppID 飞书 app_id / 微信公众号 appid / QQ AppID，支持 ${ENV_VAR} 引用
	AppID string `yaml:"app_id"`

	// AppSecret 飞书 app_secret / 微信公众号 appsecret / QQ AppSecret，支持 ${ENV_VAR} 引用
	AppSecret string `yaml:"app_secret"`

	// VerificationToken 飞书事件订阅验证令牌（可选），支持 ${ENV_VAR} 引用
	VerificationToken string `yaml:"verification_token"`

	// EncryptKey 飞书事件加密密钥（配置后强制校验 X-Lark-Signature），支持 ${ENV_VAR} 引用
	EncryptKey string `yaml:"encrypt_key"`

	// CorpID 企业微信企业 ID
	CorpID string `yaml:"corp_id"`

	// AgentID 企业微信自建应用 agentid
	AgentID string `yaml:"agent_id"`

	// AgentSecret 企业微信自建应用 secret，支持 ${ENV_VAR} 引用
	AgentSecret string `yaml:"agent_secret"`

	// EncodingAESKey 企业微信回调加密密钥（43 位，配置后支持加密回调），支持 ${ENV_VAR} 引用
	EncodingAESKey string `yaml:"encoding_aes_key"`

	// APIToken WhatsApp 出站 Cloud API 令牌（缺省复用 token），支持 ${ENV_VAR} 引用
	APIToken string `yaml:"api_token"`

	// PhoneNumberID WhatsApp Cloud API 的 phone_number_id（出站必填）
	PhoneNumberID string `yaml:"phone_number_id"`

	// APIBase 平台 API 基址（默认各平台官方端点，可指向代理）
	APIBase string `yaml:"api_base"`

	// PathPrefix 入站回调路由前缀（默认各渠道 /channels/<渠道名>）
	PathPrefix string `yaml:"path_prefix"`
}

// MCPConfig MCP Server 接入配置。
type MCPConfig struct {
	// Servers MCP Server 列表（为空则跳过 MCP 装配）
	Servers []MCPServerEntry `yaml:"servers"`
}

// MCPServerEntry 单个 MCP Server 的 YAML 配置（对应 mcp.ClientConfig）。
type MCPServerEntry struct {
	// Name 服务器标识名称（用于日志和默认工具名前缀）
	Name string `yaml:"name"`
	// Transport 传输方式：stdio | sse
	Transport string `yaml:"transport"`

	// stdio 传输配置
	Command string   `yaml:"command"`
	Args    []string `yaml:"args"`
	Env     []string `yaml:"env"`
	WorkDir string   `yaml:"work_dir"`

	// sse 传输配置
	URL     string            `yaml:"url"`
	Headers map[string]string `yaml:"headers"`

	// ToolPrefix 工具名前缀（为空时使用 "<name>_"）
	ToolPrefix string `yaml:"tool_prefix"`
	// RiskLevel 工具风险等级（0~3，默认 1）
	RiskLevel int `yaml:"risk_level"`
	// TimeoutSeconds 请求超时（秒，默认 30）
	TimeoutSeconds int `yaml:"timeout_seconds"`
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
			Protocol:         entry.Protocol,
			BaseURL:          entry.BaseURL,
			APIKey:           entry.APIKey,
			Model:            entry.Model,
			MaxContextTokens: entry.MaxTokens,
			Timeout:          timeout,
			Headers:          entry.Headers,
		}
	}

	return llm.GatewayConfig{
		Providers:        providers,
		DefaultProvider:  c.LLM.DefaultProvider,
		FallbackProvider: c.LLM.FallbackProvider,
		Retry:            c.LLM.Retry,
		CircuitBreaker:   c.LLM.CircuitBreaker,
		Cache:            c.LLM.Cache,
		Meter:            c.LLM.Meter,
	}
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
	if c.Session.RequestsPerMinute == 0 {
		// 默认启用 per-user 限流 30 次/分钟（与安全设计的"每会话限流"口径一致，
		// 0 表示不限流会让 per-user 限流实际关闭）
		c.Session.RequestsPerMinute = 30
	}
	if c.Search.DefaultEngine == "" {
		c.Search.DefaultEngine = "duckduckgo"
	}
	if c.Search.MaxResults == 0 {
		c.Search.MaxResults = 5
	}
	if c.Persona.AgentMDPath == "" {
		c.Persona.AgentMDPath = "config/agent.md"
	}
	if c.Persona.SoulMDPath == "" {
		c.Persona.SoulMDPath = "config/soul.md"
	}
	if c.Persona.UserProfilesDir == "" {
		c.Persona.UserProfilesDir = "data/users"
	}
	if c.Persona.ProfileUpdateIntervalRuns == 0 {
		c.Persona.ProfileUpdateIntervalRuns = 3
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
	c.Search.TavilyAPIKey = resolveEnvVar(c.Search.TavilyAPIKey)
	c.Server.AuthAPIKey = resolveEnvVar(c.Server.AuthAPIKey)
	c.Server.AuthJWTSecret = resolveEnvVar(c.Server.AuthJWTSecret)
	for name, ch := range c.Channels {
		ch.Token = resolveEnvVar(ch.Token)
		ch.CallbackURL = resolveEnvVar(ch.CallbackURL)
		ch.ClientSecret = resolveEnvVar(ch.ClientSecret)
		ch.AppSecret = resolveEnvVar(ch.AppSecret)
		ch.VerificationToken = resolveEnvVar(ch.VerificationToken)
		ch.EncryptKey = resolveEnvVar(ch.EncryptKey)
		ch.AgentSecret = resolveEnvVar(ch.AgentSecret)
		ch.EncodingAESKey = resolveEnvVar(ch.EncodingAESKey)
		ch.APIToken = resolveEnvVar(ch.APIToken)
		c.Channels[name] = ch
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
		// protocol 仅允许空（默认 openai）、openai 或 anthropic
		if entry.Protocol != "" && entry.Protocol != "openai" && entry.Protocol != "anthropic" {
			return fmt.Errorf("config: 供应商 %q 的 protocol 非法: %q（仅支持 openai / anthropic）", name, entry.Protocol)
		}
		// anthropic 协议允许省略 base_url（缺省使用官方端点）
		if entry.BaseURL == "" && entry.Protocol != "anthropic" {
			return fmt.Errorf("config: provider %q missing base_url", name)
		}
	}
	if c.LLM.DefaultProvider != "" {
		if _, ok := c.LLM.Providers[c.LLM.DefaultProvider]; !ok {
			return fmt.Errorf("config: default_provider %q not found in providers", c.LLM.DefaultProvider)
		}
	}
	for name, ch := range c.Channels {
		if !ch.Enabled {
			continue
		}
		// 各渠道启用时必须配齐凭证（与认证层"缺密钥即拒绝启动"的口径一致）
		var missing []string
		switch name {
		case "webhook", "telegram", "whatsapp":
			if ch.Token == "" {
				missing = append(missing, "token")
			}
		case "dingtalk":
			if ch.ClientID == "" {
				missing = append(missing, "client_id")
			}
			if ch.ClientSecret == "" {
				missing = append(missing, "client_secret")
			}
		case "feishu", "qq":
			if ch.AppID == "" {
				missing = append(missing, "app_id")
			}
			if ch.AppSecret == "" {
				missing = append(missing, "app_secret")
			}
		case "wechat", "微信":
			if ch.AppID == "" {
				missing = append(missing, "app_id")
			}
			if ch.AppSecret == "" {
				missing = append(missing, "app_secret")
			}
			if ch.Token == "" {
				missing = append(missing, "token")
			}
		case "wecom":
			if ch.CorpID == "" {
				missing = append(missing, "corp_id")
			}
			if ch.AgentID == "" {
				missing = append(missing, "agent_id")
			}
			if ch.AgentSecret == "" {
				missing = append(missing, "agent_secret")
			}
			if ch.Token == "" {
				missing = append(missing, "token")
			}
		}
		if len(missing) > 0 {
			return fmt.Errorf("config: 渠道 %q 已启用但缺少必填凭证: %s（不允许无认证端点）", name, strings.Join(missing, ", "))
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
