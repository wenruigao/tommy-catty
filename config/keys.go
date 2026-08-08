// keys.go 定义 /config 命令可操作的配置键白名单：
// 每个键含类型、说明、读取/写入函数；未知键一律拒绝。
package config

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/tommy-cat/agent/internal/llm"
)

// KeyKind 配置键的取值类型
type KeyKind int

const (
	KindString KeyKind = iota
	KindInt
	KindBool
	KindDuration
	KindEnum
)

// kindText Kind 的可读文本（schema 输出用）
func kindText(k KeyKind) string {
	switch k {
	case KindString:
		return "string"
	case KindInt:
		return "int"
	case KindBool:
		return "bool"
	case KindDuration:
		return "duration"
	case KindEnum:
		return "enum"
	}
	return "unknown"
}

// KeySpec 描述一个可配置键
type KeySpec struct {
	Key        string
	Kind       KeyKind
	Desc       string
	EnumValues []string
	Secret     bool // 展示时脱敏
	HotApply   bool // 预留：是否支持热生效（当前统一重启生效）
	Validate   func(value string) error
	Get        func(c *Config) string
	Set        func(c *Config, value string) error
}

// ProviderFields llm.providers.<name> 下可通过 /config 设置的字段
var ProviderFields = []string{"model", "base_url", "max_tokens", "timeout", "api_key"}

// keyRegistry 静态键注册表（动态键 llm.providers.<name>.* 见 LookupProviderKey）
var keyRegistry = []KeySpec{
	{
		Key: "llm.default_provider", Kind: KindString, Desc: "默认模型供应商",
		Get: func(c *Config) string { return displayOr(c.LLM.DefaultProvider, "(自动取首个)") },
		Set: func(c *Config, v string) error {
			if _, ok := c.LLM.Providers[v]; !ok {
				return fmt.Errorf("供应商 %q 不存在（可用: %s）", v, strings.Join(providerNames(c), ", "))
			}
			c.LLM.DefaultProvider = v
			return nil
		},
	},
	{
		Key: "llm.fallback_provider", Kind: KindString, Desc: "降级供应商（主供应商失败时切换）",
		Get: func(c *Config) string { return displayOr(c.LLM.FallbackProvider, "(未设置)") },
		Set: func(c *Config, v string) error {
			if _, ok := c.LLM.Providers[v]; !ok {
				return fmt.Errorf("供应商 %q 不存在（可用: %s）", v, strings.Join(providerNames(c), ", "))
			}
			c.LLM.FallbackProvider = v
			return nil
		},
	},
	{
		Key: "llm.cache.enabled", Kind: KindBool, Desc: "语义缓存开关（L1 精确哈希层）",
		Get: func(c *Config) string {
			if c.LLM.Cache == nil {
				return "false (未启用)"
			}
			return strconv.FormatBool(c.LLM.Cache.Enabled)
		},
		Set: func(c *Config, v string) error {
			if c.LLM.Cache == nil {
				c.LLM.Cache = &llm.CacheYAMLConfig{}
			}
			c.LLM.Cache.Enabled = v == "true"
			return nil
		},
	},
	{
		Key: "llm.cache.capacity", Kind: KindInt, Desc: "语义缓存条目容量",
		Validate: positiveInt("缓存容量"),
		Get: func(c *Config) string {
			if c.LLM.Cache == nil {
				return "(未启用)"
			}
			return strconv.Itoa(c.LLM.Cache.Capacity)
		},
		Set: func(c *Config, v string) error {
			n, _ := strconv.Atoi(v)
			if c.LLM.Cache == nil {
				c.LLM.Cache = &llm.CacheYAMLConfig{}
			}
			c.LLM.Cache.Capacity = n
			return nil
		},
	},
	{
		Key: "llm.cache.ttl", Kind: KindDuration, Desc: "语义缓存过期时间（如 10m）",
		Get: func(c *Config) string {
			if c.LLM.Cache == nil {
				return "(未启用)"
			}
			return displayOr(c.LLM.Cache.TTL, "(默认)")
		},
		Set: func(c *Config, v string) error {
			if c.LLM.Cache == nil {
				c.LLM.Cache = &llm.CacheYAMLConfig{}
			}
			c.LLM.Cache.TTL = v
			return nil
		},
	},
	{
		Key: "llm.meter.daily_token_limit", Kind: KindInt, Desc: "每日 Token 预算（<=0 不限）",
		Validate: nonNegativeInt("日预算"),
		Get: func(c *Config) string {
			if c.LLM.Meter == nil {
				return "(未设置: 不限)"
			}
			return strconv.Itoa(c.LLM.Meter.DailyTokenLimit)
		},
		Set: func(c *Config, v string) error {
			n, _ := strconv.Atoi(v)
			if c.LLM.Meter == nil {
				c.LLM.Meter = &llm.MeterYAMLConfig{}
			}
			c.LLM.Meter.DailyTokenLimit = n
			return nil
		},
	},
	{
		Key: "engine.max_iterations", Kind: KindInt, Desc: "ReAct 最大迭代次数",
		Validate: positiveInt("最大迭代次数"),
		Get:      func(c *Config) string { return strconv.Itoa(c.Engine.MaxIterations) },
		Set: func(c *Config, v string) error {
			n, _ := strconv.Atoi(v)
			c.Engine.MaxIterations = n
			return nil
		},
	},
	{
		Key: "engine.reflection.enabled", Kind: KindBool, Desc: "自我反思与重规划开关",
		Get: func(c *Config) string { return strconv.FormatBool(c.Engine.Reflection.Enabled) },
		Set: func(c *Config, v string) error {
			c.Engine.Reflection.Enabled = v == "true"
			return nil
		},
	},
	{
		Key: "engine.reflection.interval_steps", Kind: KindInt, Desc: "反思间隔步数",
		Validate: positiveInt("反思间隔"),
		Get:      func(c *Config) string { return strconv.Itoa(c.Engine.Reflection.IntervalSteps) },
		Set: func(c *Config, v string) error {
			n, _ := strconv.Atoi(v)
			c.Engine.Reflection.IntervalSteps = n
			return nil
		},
	},
	{
		Key: "engine.reflection.max_replans", Kind: KindInt, Desc: "最大重规划次数",
		Validate: nonNegativeInt("重规划次数"),
		Get:      func(c *Config) string { return strconv.Itoa(c.Engine.Reflection.MaxReplans) },
		Set: func(c *Config, v string) error {
			n, _ := strconv.Atoi(v)
			c.Engine.Reflection.MaxReplans = n
			return nil
		},
	},
	{
		Key: "search.default_engine", Kind: KindEnum, EnumValues: []string{"duckduckgo", "tavily"},
		Desc: "默认搜索引擎",
		Get:  func(c *Config) string { return c.Search.DefaultEngine },
		Set: func(c *Config, v string) error {
			c.Search.DefaultEngine = v
			return nil
		},
	},
	{
		Key: "search.max_results", Kind: KindInt, Desc: "搜索结果默认返回数",
		Validate: positiveInt("结果数"),
		Get:      func(c *Config) string { return strconv.Itoa(c.Search.MaxResults) },
		Set: func(c *Config, v string) error {
			n, _ := strconv.Atoi(v)
			c.Search.MaxResults = n
			return nil
		},
	},
	{
		Key: "search.tavily_api_key", Kind: KindString, Desc: "Tavily API Key", Secret: true,
		Get: func(c *Config) string { return displayOr(c.Search.TavilyAPIKey, "(未设置)") },
		Set: func(c *Config, v string) error {
			c.Search.TavilyAPIKey = v
			return nil
		},
	},
	{
		Key: "session.requests_per_minute", Kind: KindInt, Desc: "每用户每分钟最大请求数（0 不限流）",
		Validate: nonNegativeInt("限流值"),
		Get:      func(c *Config) string { return strconv.Itoa(c.Session.RequestsPerMinute) },
		Set: func(c *Config, v string) error {
			n, _ := strconv.Atoi(v)
			c.Session.RequestsPerMinute = n
			return nil
		},
	},
	{
		Key: "session.ttl", Kind: KindDuration, Desc: "会话空闲超时（如 30m）",
		Get: func(c *Config) string { return c.Session.TTL },
		Set: func(c *Config, v string) error {
			c.Session.TTL = v
			return nil
		},
	},
	{
		Key: "session.memory_size", Kind: KindInt, Desc: "每用户工作记忆容量",
		Validate: positiveInt("记忆容量"),
		Get:      func(c *Config) string { return strconv.Itoa(c.Session.MemorySize) },
		Set: func(c *Config, v string) error {
			n, _ := strconv.Atoi(v)
			c.Session.MemorySize = n
			return nil
		},
	},
	{
		Key: "session.max_sessions", Kind: KindInt, Desc: "最大活跃会话数",
		Validate: positiveInt("会话数上限"),
		Get:      func(c *Config) string { return strconv.Itoa(c.Session.MaxSessions) },
		Set: func(c *Config, v string) error {
			n, _ := strconv.Atoi(v)
			c.Session.MaxSessions = n
			return nil
		},
	},
	{
		Key: "persona.profile_update_interval_runs", Kind: KindInt, Desc: "用户画像更新间隔（任务数）",
		Validate: positiveInt("更新间隔"),
		Get:      func(c *Config) string { return strconv.Itoa(c.Persona.ProfileUpdateIntervalRuns) },
		Set: func(c *Config, v string) error {
			n, _ := strconv.Atoi(v)
			c.Persona.ProfileUpdateIntervalRuns = n
			return nil
		},
	},
	{
		Key: "work_dir", Kind: KindString, Desc: "工作目录（文件操作沙箱范围）",
		Get: func(c *Config) string { return c.WorkDir },
		Set: func(c *Config, v string) error {
			c.WorkDir = v
			return nil
		},
	},
	{
		Key: "audit_log_path", Kind: KindString, Desc: "审计日志路径（空则禁用）",
		Get: func(c *Config) string { return displayOr(c.AuditLogPath, "(未设置: 禁用)") },
		Set: func(c *Config, v string) error {
			c.AuditLogPath = v
			return nil
		},
	},
}

// AllKeySpecs 返回静态键注册表的副本（供列表展示）。
func AllKeySpecs() []KeySpec {
	out := make([]KeySpec, len(keyRegistry))
	copy(out, keyRegistry)
	return out
}

// LookupKey 查找静态键与动态键（llm.providers.<name>.<field>）。
func LookupKey(key string) (*KeySpec, bool) {
	for i := range keyRegistry {
		if keyRegistry[i].Key == key {
			return &keyRegistry[i], true
		}
	}
	if spec := LookupProviderKey(key); spec != nil {
		return spec, true
	}
	return nil, false
}

// LookupProviderKey 解析 llm.providers.<name>.<field> 形式的动态键；
// 形式不匹配或字段不在白名单时返回 nil。
func LookupProviderKey(key string) *KeySpec {
	segs := strings.Split(key, ".")
	if len(segs) != 4 || segs[0] != "llm" || segs[1] != "providers" || segs[2] == "" {
		return nil
	}
	name, field := segs[2], segs[3]

	spec := &KeySpec{Key: key}
	switch field {
	case "model":
		spec.Kind, spec.Desc = KindString, fmt.Sprintf("供应商 %s 的模型名", name)
	case "base_url":
		spec.Kind, spec.Desc = KindString, fmt.Sprintf("供应商 %s 的 API 端点", name)
	case "api_key":
		spec.Kind, spec.Desc = KindString, fmt.Sprintf("供应商 %s 的 API Key", name)
		spec.Secret = true
	case "max_tokens":
		spec.Kind, spec.Desc = KindInt, fmt.Sprintf("供应商 %s 的最大上下文 token", name)
		spec.Validate = positiveInt("max_tokens")
	case "timeout":
		spec.Kind, spec.Desc = KindDuration, fmt.Sprintf("供应商 %s 的请求超时（如 120s）", name)
	default:
		return nil
	}

	spec.Get = func(c *Config) string {
		entry, ok := c.LLM.Providers[name]
		if !ok {
			return "(供应商不存在)"
		}
		switch field {
		case "model":
			return displayOr(entry.Model, "(未设置)")
		case "base_url":
			return displayOr(entry.BaseURL, "(未设置)")
		case "api_key":
			return displayOr(entry.APIKey, "(未设置)")
		case "max_tokens":
			if entry.MaxTokens == 0 {
				return "(默认 32768)"
			}
			return strconv.Itoa(entry.MaxTokens)
		case "timeout":
			return displayOr(entry.Timeout, "(默认 120s)")
		}
		return ""
	}
	spec.Set = func(c *Config, v string) error {
		entry, ok := c.LLM.Providers[name]
		if !ok {
			return fmt.Errorf("供应商 %q 不存在（可用: %s）", name, strings.Join(providerNames(c), ", "))
		}
		switch field {
		case "model":
			entry.Model = v
		case "base_url":
			entry.BaseURL = v
		case "api_key":
			entry.APIKey = v
		case "max_tokens":
			n, _ := strconv.Atoi(v)
			entry.MaxTokens = n
		case "timeout":
			entry.Timeout = v
		}
		c.LLM.Providers[name] = entry
		return nil
	}
	return spec
}

// ValidateValue 按键的 Kind 做类型校验，再执行自定义 Validate；错误信息为中文。
func ValidateValue(spec *KeySpec, value string) error {
	switch spec.Kind {
	case KindInt:
		if _, err := strconv.Atoi(value); err != nil {
			return fmt.Errorf("值 %q 不是合法整数", value)
		}
	case KindBool:
		if value != "true" && value != "false" {
			return fmt.Errorf("值 %q 不是合法布尔值（应为 true / false）", value)
		}
	case KindDuration:
		if _, err := time.ParseDuration(value); err != nil {
			return fmt.Errorf("值 %q 不是合法时长（格式如 30s、5m、2h）", value)
		}
	case KindEnum:
		for _, ev := range spec.EnumValues {
			if value == ev {
				return spec.validateExtra(value)
			}
		}
		return fmt.Errorf("值 %q 超出取值范围，可选: %s", value, strings.Join(spec.EnumValues, " / "))
	}
	return spec.validateExtra(value)
}

func (s *KeySpec) validateExtra(value string) error {
	if s.Validate != nil {
		return s.Validate(value)
	}
	return nil
}

// ParseNative 按 Kind 把校验通过的字符串值转换为原生 YAML 类型。
func ParseNative(spec *KeySpec, value string) any {
	switch spec.Kind {
	case KindInt:
		if n, err := strconv.Atoi(value); err == nil {
			return n
		}
	case KindBool:
		return value == "true"
	}
	return value
}

// SchemaText 输出键注册表说明（静态键 + cfg 中各供应商的动态键），
// 含类型、说明、枚举可选值与密钥标记，供 /config schema 展示。
func SchemaText(cfg *Config) string {
	static := AllKeySpecs()
	specs := make([]*KeySpec, 0, len(static)+8)
	for i := range static {
		specs = append(specs, &static[i])
	}
	if cfg != nil {
		for name := range cfg.LLM.Providers {
			for _, field := range ProviderFields {
				if spec := LookupProviderKey("llm.providers." + name + "." + field); spec != nil {
					specs = append(specs, spec)
				}
			}
		}
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Key < specs[j].Key })

	var sb strings.Builder
	sb.WriteString("  ⚙️  配置键 Schema（类型 | 密钥 | 说明）\n")
	for _, spec := range specs {
		kind := kindText(spec.Kind)
		if spec.Kind == KindEnum {
			kind += "(" + strings.Join(spec.EnumValues, "/") + ")"
		}
		secret := " "
		if spec.Secret {
			secret = "🔒"
		}
		fmt.Fprintf(&sb, "    %-46s %-22s %s  %s\n", spec.Key, kind, secret, spec.Desc)
	}
	sb.WriteString("  🔒 = 秘密键（展示脱敏，建议以 ${ENV} 或 env:NAME 引用）\n")
	return sb.String()
}

// ValidateFile 校验主配置 + 覆盖层的合法性，返回问题清单（空 = 全部通过）：
// YAML 语法与结构解析错误、${ENV} 引用缺失（警告）、覆盖层键的类型/语义错误。
func ValidateFile(mainPath string) []string {
	var issues []string
	raw, err := LoadRawMerged(mainPath)
	if err != nil {
		return []string{fmt.Sprintf("错误: 配置加载失败: %v", err)}
	}
	merged, err := yaml.Marshal(raw)
	if err != nil {
		return []string{fmt.Sprintf("错误: 配置序列化失败: %v", err)}
	}
	var cfg Config
	if err := yaml.Unmarshal(merged, &cfg); err != nil {
		// 主配置单独可解析时，结构冲突源自覆盖层：记录后继续覆盖层键级校验
		var mainCfg Config
		mdata, rerr := os.ReadFile(mainPath)
		if rerr != nil || yaml.Unmarshal(mdata, &mainCfg) != nil {
			return []string{fmt.Sprintf("错误: 配置结构解析失败: %v", err)}
		}
		issues = append(issues, fmt.Sprintf("错误: 覆盖层与配置结构冲突（可能类型不符）: %v", err))
		cfg = mainCfg
	}
	cfg.applyDefaults()
	scanEnvRefs(raw, "", &issues)

	// 覆盖层键语义校验（类型/枚举/provider 存在性）
	store, serr := NewOverlayStore(OverlayPath(mainPath))
	if serr != nil {
		issues = append(issues, fmt.Sprintf("错误: 覆盖层解析失败: %v", serr))
	} else {
		for _, kv := range FlattenMap(store.data, "") {
			spec, ok := LookupKey(kv.Key)
			if !ok {
				issues = append(issues, fmt.Sprintf("错误: 覆盖层包含未知配置键: %s", kv.Key))
				continue
			}
			v := fmt.Sprint(kv.Value)
			if verr := ValidateValue(spec, v); verr != nil {
				issues = append(issues, fmt.Sprintf("错误: 覆盖层键 %s: %v", kv.Key, verr))
				continue
			}
			if serr := spec.Set(&cfg, v); serr != nil {
				issues = append(issues, fmt.Sprintf("错误: 覆盖层键 %s: %v", kv.Key, serr))
			}
		}
	}
	return issues
}

// scanEnvRefs 递归扫描 ${ENV} 引用，环境变量未定义时追加警告。
func scanEnvRefs(v any, path string, issues *[]string) {
	switch val := v.(type) {
	case map[string]any:
		for k, child := range val {
			p := k
			if path != "" {
				p = path + "." + k
			}
			scanEnvRefs(child, p, issues)
		}
	case []any:
		for i, child := range val {
			scanEnvRefs(child, fmt.Sprintf("%s[%d]", path, i), issues)
		}
	case string:
		if IsEnvRef(val) {
			name := val[2 : len(val)-1]
			if _, ok := os.LookupEnv(name); !ok {
				*issues = append(*issues, fmt.Sprintf("警告: %s 引用的环境变量 %s 未定义", path, name))
			}
		}
	}
}

// IsSecretKey 按键名片段判定是否为秘密字段。
// nonSecretKeys 为含敏感片段但实际非秘密的键（如数值限额）。
func IsSecretKey(key string) bool {
	lower := strings.ToLower(key)
	for _, ns := range []string{"daily_token_limit"} {
		if strings.Contains(lower, ns) {
			return false
		}
	}
	for _, frag := range []string{"api_key", "token", "secret", "dsn", "password"} {
		if strings.Contains(lower, frag) {
			return true
		}
	}
	return false
}

// IsEnvRef 判断值是否为 ${ENV} 环境变量引用。
func IsEnvRef(v string) bool {
	return strings.HasPrefix(v, "${") && strings.HasSuffix(v, "}")
}

// MaskValue 脱敏展示：${ENV} 引用原样返回；其余显示前 3 位 + 后 2 位。
func MaskValue(v string) string {
	if v == "" {
		return "(未设置)"
	}
	if IsEnvRef(v) {
		return v
	}
	r := []rune(v)
	if len(r) <= 5 {
		return "***"
	}
	return string(r[:3]) + "***" + string(r[len(r)-2:])
}

// SuggestKey 为未知键给出最相近的候选（按公共前缀长度），无合适候选返回空串。
func SuggestKey(key string) string {
	candidates := make([]string, 0, len(keyRegistry)+len(ProviderFields))
	for _, spec := range keyRegistry {
		candidates = append(candidates, spec.Key)
	}
	for _, f := range ProviderFields {
		candidates = append(candidates, "llm.providers.<name>."+f)
	}
	best, bestLen := "", 0
	for _, k := range candidates {
		n := commonPrefixLen(key, k)
		if n > bestLen {
			best, bestLen = k, n
		}
	}
	if bestLen >= 4 {
		return best
	}
	return ""
}

func commonPrefixLen(a, b string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

func displayOr(v, placeholder string) string {
	if v == "" {
		return placeholder
	}
	return v
}

func providerNames(c *Config) []string {
	names := make([]string, 0, len(c.LLM.Providers))
	for name := range c.LLM.Providers {
		names = append(names, name)
	}
	return names
}

func positiveInt(label string) func(string) error {
	return func(v string) error {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return fmt.Errorf("%s必须为正整数，收到: %q", label, v)
		}
		return nil
	}
}

func nonNegativeInt(label string) func(string) error {
	return func(v string) error {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return fmt.Errorf("%s必须为非负整数，收到: %q", label, v)
		}
		return nil
	}
}
