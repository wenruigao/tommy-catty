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
	return llm.GatewayConfig{
		Providers:        providers,
		DefaultProvider:  c.LLM.DefaultProvider,
		FallbackProvider: c.LLM.FallbackProvider,
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
}

// resolveEnvVars 解析配置中的 ${ENV_VAR} 引用
func (c *Config) resolveEnvVars() {
	for name, entry := range c.LLM.Providers {
		entry.APIKey = resolveEnvVar(entry.APIKey)
		entry.BaseURL = resolveEnvVar(entry.BaseURL)
		c.LLM.Providers[name] = entry
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

// unused import guard
