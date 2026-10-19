// router_test.go 验证 router 配置段的 YAML 解析与默认值。
package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// loadRouterConfig 从 YAML 文本加载配置（测试辅助）。
func loadRouterConfig(t *testing.T, content string) *Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return cfg
}

// TestRouterConfig_DisabledByDefault 未配置 router 段时默认禁用（行为与旧版一致）。
func TestRouterConfig_DisabledByDefault(t *testing.T) {
	cfg := Default()

	if cfg.Router.Enabled {
		t.Error("router 默认应为禁用")
	}
	if cfg.Router.Agents != nil {
		t.Errorf("router.agents 默认应为空，got %v", cfg.Router.Agents)
	}
	// 兜底 Agent 名有默认值，便于启用时不必显式配置
	if cfg.Router.FallbackAgent != "general" {
		t.Errorf("FallbackAgent 默认 = %q, want general", cfg.Router.FallbackAgent)
	}
}

// TestRouterConfig_Parse 验证 router 段完整解析。
func TestRouterConfig_Parse(t *testing.T) {
	cfg := loadRouterConfig(t, `
router:
  enabled: true
  llm_timeout: "3s"
  confidence_threshold: 0.75
  fallback_agent: "default"
  use_llm: false
  agents:
    default:
      description: "通用助手"
      system_prompt: "你是通用助手"
    coder:
      description: "编程专家"
      system_prompt: |
        你是工程师
      tools: ["code_run", "file_read"]
      keywords: ["代码", "脚本"]
      patterns: ['^写.*']
      priority: 20
      max_iterations: 30
`)

	if !cfg.Router.Enabled {
		t.Error("enabled 应解析为 true")
	}
	if cfg.Router.LLMTimeout != "3s" {
		t.Errorf("LLMTimeout = %q, want 3s", cfg.Router.LLMTimeout)
	}
	if cfg.Router.ConfidenceThreshold != 0.75 {
		t.Errorf("ConfidenceThreshold = %v, want 0.75", cfg.Router.ConfidenceThreshold)
	}
	if cfg.Router.FallbackAgent != "default" {
		t.Errorf("FallbackAgent = %q, want default", cfg.Router.FallbackAgent)
	}
	if cfg.RouterUseLLM() {
		t.Error("use_llm: false 应被保留")
	}
	if len(cfg.Router.Agents) != 2 {
		t.Fatalf("agents 数量 = %d, want 2", len(cfg.Router.Agents))
	}

	coder := cfg.Router.Agents["coder"]
	if coder.Description != "编程专家" {
		t.Errorf("coder.description = %q", coder.Description)
	}
	if coder.SystemPrompt != "你是工程师\n" {
		t.Errorf("coder.system_prompt = %q", coder.SystemPrompt)
	}
	if len(coder.Tools) != 2 || coder.Tools[0] != "code_run" || coder.Tools[1] != "file_read" {
		t.Errorf("coder.tools = %v", coder.Tools)
	}
	if len(coder.Keywords) != 2 || coder.Keywords[0] != "代码" {
		t.Errorf("coder.keywords = %v", coder.Keywords)
	}
	if len(coder.Patterns) != 1 || coder.Patterns[0] != "^写.*" {
		t.Errorf("coder.patterns = %v", coder.Patterns)
	}
	if coder.Priority != 20 {
		t.Errorf("coder.priority = %d, want 20", coder.Priority)
	}
	if coder.MaxIterations != 30 {
		t.Errorf("coder.max_iterations = %d, want 30", coder.MaxIterations)
	}
}

// TestRouterConfig_UseLLMDefaultsTrue 未配置 use_llm 时默认为 true。
func TestRouterConfig_UseLLMDefaultsTrue(t *testing.T) {
	cfg := loadRouterConfig(t, "router:\n  enabled: true\n")

	if cfg.Router.UseLLM != nil {
		t.Errorf("未配置时 UseLLM 指针应为 nil，got %v", *cfg.Router.UseLLM)
	}
	if !cfg.RouterUseLLM() {
		t.Error("RouterUseLLM() 未配置时应返回 true")
	}
}

// TestRouterConfig_UseLLMExplicitTrue 显式配置 use_llm: true。
func TestRouterConfig_UseLLMExplicitTrue(t *testing.T) {
	cfg := loadRouterConfig(t, "router:\n  enabled: true\n  use_llm: true\n")

	if !cfg.RouterUseLLM() {
		t.Error("RouterUseLLM() 应返回 true")
	}
}

// TestRouterLLMTimeout 验证超时解析与兜底。
func TestRouterLLMTimeout(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want time.Duration
	}{
		{"unset falls back to 5s", "router:\n  enabled: true\n", 5 * time.Second},
		{"explicit 3s", "router:\n  llm_timeout: \"3s\"\n", 3 * time.Second},
		{"explicit 500ms", "router:\n  llm_timeout: \"500ms\"\n", 500 * time.Millisecond},
		{"explicit 1m", "router:\n  llm_timeout: \"1m\"\n", time.Minute},
		{"invalid falls back to 5s", "router:\n  llm_timeout: \"not-a-duration\"\n", 5 * time.Second},
		{"empty string falls back", "router:\n  llm_timeout: \"\"\n", 5 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := loadRouterConfig(t, tt.yaml)
			if got := cfg.RouterLLMTimeout(); got != tt.want {
				t.Errorf("RouterLLMTimeout() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestRouterConfig_FallbackAgentDefaultApplied 未配置 fallback_agent 时填充默认值。
func TestRouterConfig_FallbackAgentDefaultApplied(t *testing.T) {
	cfg := loadRouterConfig(t, "router:\n  enabled: true\n")

	if cfg.Router.FallbackAgent != "general" {
		t.Errorf("FallbackAgent = %q, want general (applyDefaults)", cfg.Router.FallbackAgent)
	}
}

// TestRouterConfig_FallbackAgentExplicitPreserved 显式配置不被默认值覆盖。
func TestRouterConfig_FallbackAgentExplicitPreserved(t *testing.T) {
	cfg := loadRouterConfig(t, "router:\n  fallback_agent: \"helper\"\n")

	if cfg.Router.FallbackAgent != "helper" {
		t.Errorf("FallbackAgent = %q, want helper", cfg.Router.FallbackAgent)
	}
}

// TestRouterConfig_EmptyAgentsMap 空的 agents 映射不应导致解析失败。
func TestRouterConfig_EmptyAgentsMap(t *testing.T) {
	cfg := loadRouterConfig(t, "router:\n  enabled: true\n  agents: {}\n")

	if !cfg.Router.Enabled {
		t.Error("enabled 应解析为 true")
	}
	if len(cfg.Router.Agents) != 0 {
		t.Errorf("agents 应为空，got %v", cfg.Router.Agents)
	}
}

// TestRouterConfig_CoexistsWithMultiAgent router 与 multi_agent 可同时配置（职责不同）。
func TestRouterConfig_CoexistsWithMultiAgent(t *testing.T) {
	cfg := loadRouterConfig(t, `
router:
  enabled: true
  agents:
    general:
      description: "通用助手"
      system_prompt: "你是通用助手"
multi_agent:
  enabled: true
  roles:
    researcher:
      description: "调研角色"
      system_prompt: "你是研究员"
      tools: ["web_search"]
`)

	if !cfg.Router.Enabled || len(cfg.Router.Agents) != 1 {
		t.Errorf("router 解析异常: %+v", cfg.Router)
	}
	if !cfg.MultiAgent.Enabled || len(cfg.MultiAgent.Roles) != 1 {
		t.Errorf("multi_agent 解析异常: %+v", cfg.MultiAgent)
	}
}
