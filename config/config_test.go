package config

import (
	"os"
	"strings"
	"testing"
)

// ============================================================
// resolveEnvVar tests
// ============================================================

func TestResolveEnvVar_PlainString(t *testing.T) {
	result := resolveEnvVar("sk-plain-text-key")
	if result != "sk-plain-text-key" {
		t.Errorf("plain string: got %q, want sk-plain-text-key", result)
	}
}

func TestResolveEnvVar_EmptyString(t *testing.T) {
	result := resolveEnvVar("")
	if result != "" {
		t.Errorf("empty string: got %q, want empty", result)
	}
}

func TestResolveEnvVar_EnvVarSet(t *testing.T) {
	os.Setenv("TEST_ENV_VAR", "my-secret-value")
	defer os.Unsetenv("TEST_ENV_VAR")
	result := resolveEnvVar("${TEST_ENV_VAR}")
	if result != "my-secret-value" {
		t.Errorf("env var: got %q, want my-secret-value", result)
	}
}

func TestResolveEnvVar_EnvVarNotSet(t *testing.T) {
	result := resolveEnvVar("${NONEXISTENT_ENV_VAR_12345}")
	if result != "" {
		t.Errorf("unset env var: got %q, want empty", result)
	}
}

func TestResolveEnvVar_NotEnvPattern(t *testing.T) {
	result := resolveEnvVar("not${prefix}but")
	if result != "not${prefix}but" {
		t.Errorf("not env pattern: got %q", result)
	}
}

func TestResolveEnvVar_PartialBrace(t *testing.T) {
	result := resolveEnvVar("${incomplete")
	if result != "${incomplete" {
		t.Errorf("partial brace: got %q", result)
	}
}

// ============================================================
// applyDefaults tests
// ============================================================

func TestApplyDefaults_ZeroValues(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	if cfg.Engine.MaxIterations != 20 {
		t.Errorf("Engine.MaxIterations = %d, want 20", cfg.Engine.MaxIterations)
	}
	if cfg.PolicyFile != "config/policy.yaml" {
		t.Errorf("PolicyFile = %q, want config/policy.yaml", cfg.PolicyFile)
	}
	if cfg.SkillStorePath != "data/skills.json" {
		t.Errorf("SkillStorePath = %q, want data/skills.json", cfg.SkillStorePath)
	}
	if cfg.WorkDir != "." {
		t.Errorf("WorkDir = %q, want .", cfg.WorkDir)
	}
	if cfg.LLM.Providers == nil {
		t.Error("LLM.Providers should be initialized")
	}
}

func TestApplyDefaults_PreservesExisting(t *testing.T) {
	cfg := &Config{
		Engine:         EngineConfig{MaxIterations: 10},
		PolicyFile:     "custom.yaml",
		SkillStorePath: "custom-skills.json",
		WorkDir:        "/tmp",
	}
	cfg.applyDefaults()
	if cfg.Engine.MaxIterations != 10 {
		t.Errorf("Engine.MaxIterations should be preserved, got %d", cfg.Engine.MaxIterations)
	}
	if cfg.PolicyFile != "custom.yaml" {
		t.Errorf("PolicyFile should be preserved, got %q", cfg.PolicyFile)
	}
	if cfg.SkillStorePath != "custom-skills.json" {
		t.Errorf("SkillStorePath should be preserved, got %q", cfg.SkillStorePath)
	}
	if cfg.WorkDir != "/tmp" {
		t.Errorf("WorkDir should be preserved, got %q", cfg.WorkDir)
	}
}

// ============================================================
// Validate tests
// ============================================================

func TestValidate_EmptyProviders(t *testing.T) {
	cfg := &Config{LLM: LLMConfig{Providers: map[string]ProviderEntry{}}}
	err := cfg.Validate()
	if err == nil {
		t.Error("empty providers should return error")
	}
}

func TestValidate_MissingBaseURL(t *testing.T) {
	cfg := &Config{
		LLM: LLMConfig{
			Providers: map[string]ProviderEntry{
				"deepseek": {Model: "deepseek-chat"},
			},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Error("provider without base URL should return error")
	}
}

func TestValidate_DefaultProviderNotExist(t *testing.T) {
	cfg := &Config{
		LLM: LLMConfig{
			DefaultProvider: "openai",
			Providers: map[string]ProviderEntry{
				"deepseek": {BaseURL: "https://api.deepseek.com", Model: "deepseek-chat"},
			},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Error("default provider not in providers should return error")
	}
}

func TestValidate_Valid(t *testing.T) {
	cfg := &Config{
		LLM: LLMConfig{
			DefaultProvider: "deepseek",
			Providers: map[string]ProviderEntry{
				"deepseek": {BaseURL: "https://api.deepseek.com", Model: "deepseek-chat"},
			},
		},
	}
	cfg.applyDefaults()
	err := cfg.Validate()
	if err != nil {
		t.Errorf("valid config should pass: %v", err)
	}
}

// ============================================================
// DefaultProvider selection test
// ============================================================

func TestApplyDefaults_DefaultProviderSelection(t *testing.T) {
	cfg := &Config{
		LLM: LLMConfig{
			Providers: map[string]ProviderEntry{
				"deepseek": {BaseURL: "https://api.deepseek.com"},
			},
		},
	}
	cfg.applyDefaults()
	if cfg.LLM.DefaultProvider != "deepseek" {
		t.Errorf("DefaultProvider should be set to first provider, got %q", cfg.LLM.DefaultProvider)
	}
}

// ============================================================
// ToGatewayConfig tests
// ============================================================

func TestToGatewayConfig_Basic(t *testing.T) {
	cfg := &Config{
		LLM: LLMConfig{
			DefaultProvider: "deepseek",
			Providers: map[string]ProviderEntry{
				"deepseek": {
					BaseURL:   "https://api.deepseek.com",
					Model:     "deepseek-chat",
					MaxTokens: 4096,
					Timeout:   "30s",
					APIKey:    "sk-test",
				},
			},
		},
	}
	cfg.applyDefaults()
	gwCfg := cfg.ToGatewayConfig()
	if gwCfg.DefaultProvider != "deepseek" {
		t.Errorf("DefaultProvider = %q, want deepseek", gwCfg.DefaultProvider)
	}
	if len(gwCfg.Providers) != 1 {
		t.Errorf("Providers len = %d, want 1", len(gwCfg.Providers))
	}
}

func TestToGatewayConfig_EmptyTimeout(t *testing.T) {
	cfg := &Config{
		LLM: LLMConfig{
			DefaultProvider: "deepseek",
			Providers: map[string]ProviderEntry{
				"deepseek": {BaseURL: "https://api.deepseek.com", Model: "deepseek-chat"},
			},
		},
	}
	cfg.applyDefaults()
	gwCfg := cfg.ToGatewayConfig()
	if len(gwCfg.Providers) != 1 {
		t.Errorf("should have 1 provider, got %d", len(gwCfg.Providers))
	}
}

func TestToGatewayConfig_NoRetryConfig(t *testing.T) {
	cfg := &Config{
		LLM: LLMConfig{
			DefaultProvider: "deepseek",
			Providers: map[string]ProviderEntry{
				"deepseek": {BaseURL: "https://api.deepseek.com", Model: "deepseek-chat"},
			},
		},
	}
	cfg.applyDefaults()
	gwCfg := cfg.ToGatewayConfig()
	if gwCfg.Retry != nil {
		t.Error("Retry should be nil when not configured")
	}
}

// ============================================================
// protocol 字段测试（OpenAI / Anthropic 协议选择）
// ============================================================

func TestToGatewayConfig_Protocol(t *testing.T) {
	cfg := &Config{
		LLM: LLMConfig{
			DefaultProvider: "claude",
			Providers: map[string]ProviderEntry{
				"claude": {
					Protocol: "anthropic",
					APIKey:   "sk-ant-test",
					Model:    "claude-sonnet-4-5",
				},
				"deepseek": {BaseURL: "https://api.deepseek.com", Model: "deepseek-chat"},
			},
		},
	}
	cfg.applyDefaults()
	gwCfg := cfg.ToGatewayConfig()

	// protocol 应透传到 llm.ProviderConfig
	if got := gwCfg.Providers["claude"].Protocol; got != "anthropic" {
		t.Errorf("claude Protocol = %q, want anthropic", got)
	}
	// 未配置 protocol 时应为空（网关按默认 openai 处理）
	if got := gwCfg.Providers["deepseek"].Protocol; got != "" {
		t.Errorf("deepseek Protocol = %q, want 空（默认 openai）", got)
	}
}

func TestValidate_InvalidProtocol(t *testing.T) {
	cfg := &Config{
		LLM: LLMConfig{
			Providers: map[string]ProviderEntry{
				"bad": {Protocol: "gemini", BaseURL: "https://example.com", Model: "m"},
			},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("非法 protocol 应返回错误")
	}
	if !strings.Contains(err.Error(), "protocol") {
		t.Errorf("错误信息 = %q, want 提及 protocol", err.Error())
	}
}

func TestValidate_AnthropicWithoutBaseURL(t *testing.T) {
	cfg := &Config{
		LLM: LLMConfig{
			DefaultProvider: "claude",
			Providers: map[string]ProviderEntry{
				// anthropic 协议允许省略 base_url（缺省使用官方端点）
				"claude": {Protocol: "anthropic", APIKey: "k", Model: "claude-sonnet-4-5"},
			},
		},
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		t.Errorf("anthropic 协议省略 base_url 应通过校验: %v", err)
	}
}

// ============================================================
// resolveEnvVars：Search.TavilyAPIKey / Server.AuthAPIKey 展开
// ============================================================

func TestResolveEnvVars_TavilyAPIKey(t *testing.T) {
	os.Setenv("TEST_TAVILY_KEY", "tvly-secret")
	defer os.Unsetenv("TEST_TAVILY_KEY")

	cfg := &Config{}
	cfg.Search.TavilyAPIKey = "${TEST_TAVILY_KEY}"
	cfg.resolveEnvVars()
	if cfg.Search.TavilyAPIKey != "tvly-secret" {
		t.Errorf("TavilyAPIKey = %q, want tvly-secret", cfg.Search.TavilyAPIKey)
	}
}

func TestResolveEnvVars_ServerAuthAPIKey(t *testing.T) {
	os.Setenv("TEST_AGENT_API_KEY", "agent-secret")
	defer os.Unsetenv("TEST_AGENT_API_KEY")

	cfg := &Config{}
	cfg.Server.AuthAPIKey = "${TEST_AGENT_API_KEY}"
	cfg.resolveEnvVars()
	if cfg.Server.AuthAPIKey != "agent-secret" {
		t.Errorf("AuthAPIKey = %q, want agent-secret", cfg.Server.AuthAPIKey)
	}

	// 未设置环境变量时展开为空串
	cfg2 := &Config{}
	cfg2.Server.AuthAPIKey = "${NONEXISTENT_AGENT_KEY_12345}"
	cfg2.resolveEnvVars()
	if cfg2.Server.AuthAPIKey != "" {
		t.Errorf("AuthAPIKey = %q, want empty", cfg2.Server.AuthAPIKey)
	}
}

// ============================================================
// ReflectionEntry.ToReflectionConfig 测试
// ============================================================

func TestReflectionEntry_Disabled(t *testing.T) {
	entry := ReflectionEntry{Enabled: false}
	if got := entry.ToReflectionConfig(); got != nil {
		t.Errorf("disabled entry should return nil, got %+v", got)
	}
}

func TestReflectionEntry_EnabledDefaults(t *testing.T) {
	entry := ReflectionEntry{Enabled: true}
	got := entry.ToReflectionConfig()
	if got == nil {
		t.Fatal("enabled entry should return config")
	}
	if !got.Enabled {
		t.Error("Enabled should be true")
	}
	// 未显式设置的字段沿用 engine 包默认值
	if got.IntervalSteps != 5 {
		t.Errorf("IntervalSteps = %d, want 5", got.IntervalSteps)
	}
	if got.SatisfactionThreshold != 0.6 {
		t.Errorf("SatisfactionThreshold = %v, want 0.6", got.SatisfactionThreshold)
	}
	if got.MaxReplans != 2 {
		t.Errorf("MaxReplans = %d, want 2", got.MaxReplans)
	}
	if got.DeviationThreshold != 1.5 {
		t.Errorf("DeviationThreshold = %v, want 1.5", got.DeviationThreshold)
	}
}

func TestReflectionEntry_EnabledOverrides(t *testing.T) {
	entry := ReflectionEntry{
		Enabled:               true,
		IntervalSteps:         3,
		SatisfactionThreshold: 0.4,
		MaxReplans:            1,
		DeviationThreshold:    2.0,
	}
	got := entry.ToReflectionConfig()
	if got == nil {
		t.Fatal("enabled entry should return config")
	}
	if got.IntervalSteps != 3 {
		t.Errorf("IntervalSteps = %d, want 3", got.IntervalSteps)
	}
	if got.SatisfactionThreshold != 0.4 {
		t.Errorf("SatisfactionThreshold = %v, want 0.4", got.SatisfactionThreshold)
	}
	if got.MaxReplans != 1 {
		t.Errorf("MaxReplans = %d, want 1", got.MaxReplans)
	}
	if got.DeviationThreshold != 2.0 {
		t.Errorf("DeviationThreshold = %v, want 2.0", got.DeviationThreshold)
	}
}

// ============================================================
// resolveEnvVars：Server.AuthJWTSecret 展开
// ============================================================

func TestResolveEnvVars_ServerAuthJWTSecret(t *testing.T) {
	os.Setenv("TEST_JWT_SECRET", "jwt-secret-value")
	defer os.Unsetenv("TEST_JWT_SECRET")

	cfg := &Config{}
	cfg.Server.AuthJWTSecret = "${TEST_JWT_SECRET}"
	cfg.resolveEnvVars()
	if cfg.Server.AuthJWTSecret != "jwt-secret-value" {
		t.Errorf("AuthJWTSecret = %q, want jwt-secret-value", cfg.Server.AuthJWTSecret)
	}

	// 未设置环境变量时展开为空串
	cfg2 := &Config{}
	cfg2.Server.AuthJWTSecret = "${NONEXISTENT_JWT_SECRET_12345}"
	cfg2.resolveEnvVars()
	if cfg2.Server.AuthJWTSecret != "" {
		t.Errorf("AuthJWTSecret = %q, want empty", cfg2.Server.AuthJWTSecret)
	}
}

func TestApplyDefaults_TraceExportPath(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	if cfg.Engine.TraceExportPath != "" {
		t.Errorf("TraceExportPath 默认应为空（禁用导出），得到 %q", cfg.Engine.TraceExportPath)
	}
}

// ============================================================
// applyDefaults：Persona 人格体系默认值
// ============================================================

func TestApplyDefaults_Persona(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()

	if cfg.Persona.AgentMDPath != "config/agent.md" {
		t.Errorf("AgentMDPath = %q, want config/agent.md", cfg.Persona.AgentMDPath)
	}
	if cfg.Persona.SoulMDPath != "config/soul.md" {
		t.Errorf("SoulMDPath = %q, want config/soul.md", cfg.Persona.SoulMDPath)
	}
	if cfg.Persona.UserProfilesDir != "data/users" {
		t.Errorf("UserProfilesDir = %q, want data/users", cfg.Persona.UserProfilesDir)
	}
	if cfg.Persona.ProfileUpdateIntervalRuns != 3 {
		t.Errorf("ProfileUpdateIntervalRuns = %d, want 3", cfg.Persona.ProfileUpdateIntervalRuns)
	}
}

func TestApplyDefaults_PersonaKeepCustom(t *testing.T) {
	cfg := &Config{}
	cfg.Persona.AgentMDPath = "custom/agent.md"
	cfg.Persona.ProfileUpdateIntervalRuns = 10
	cfg.applyDefaults()

	if cfg.Persona.AgentMDPath != "custom/agent.md" {
		t.Errorf("自定义 AgentMDPath 不应被覆盖，得到 %q", cfg.Persona.AgentMDPath)
	}
	if cfg.Persona.ProfileUpdateIntervalRuns != 10 {
		t.Errorf("自定义 ProfileUpdateIntervalRuns 不应被覆盖，得到 %d", cfg.Persona.ProfileUpdateIntervalRuns)
	}
}
