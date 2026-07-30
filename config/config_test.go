package config

import (
	"os"
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
