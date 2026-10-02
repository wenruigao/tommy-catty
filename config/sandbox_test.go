package config

import (
	"strings"
	"testing"
)

// TestSandboxDefaults 验证沙箱配置默认值（type=none 与旧行为一致）。
func TestSandboxDefaults(t *testing.T) {
	cfg := Default()
	if cfg.Sandbox.Type != "none" {
		t.Errorf("Sandbox.Type 默认 = %q, want none", cfg.Sandbox.Type)
	}
	if cfg.Sandbox.TimeoutSeconds != 30 {
		t.Errorf("Sandbox.TimeoutSeconds 默认 = %d, want 30", cfg.Sandbox.TimeoutSeconds)
	}
	if cfg.Sandbox.MemoryLimitMB != 512 {
		t.Errorf("Sandbox.MemoryLimitMB 默认 = %d, want 512", cfg.Sandbox.MemoryLimitMB)
	}
	if cfg.Sandbox.CPULimitSeconds != 10 {
		t.Errorf("Sandbox.CPULimitSeconds 默认 = %d, want 10", cfg.Sandbox.CPULimitSeconds)
	}
	if cfg.Sandbox.MaxProcesses != 64 {
		t.Errorf("Sandbox.MaxProcesses 默认 = %d, want 64", cfg.Sandbox.MaxProcesses)
	}
	if cfg.Sandbox.OnUnavailable != "degrade" {
		t.Errorf("Sandbox.OnUnavailable 默认 = %q, want degrade", cfg.Sandbox.OnUnavailable)
	}
}

// TestSandboxValidate 验证沙箱配置校验。
func TestSandboxValidate(t *testing.T) {
	base := func() *Config {
		cfg := Default()
		// Validate 要求至少一个 LLM provider
		cfg.LLM.Providers["test"] = ProviderEntry{BaseURL: "http://localhost", Model: "m"}
		return cfg
	}

	t.Run("type 非法", func(t *testing.T) {
		cfg := base()
		cfg.Sandbox.Type = "docker-raw"
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "sandbox.type") {
			t.Errorf("应报 sandbox.type 非法: %v", err)
		}
	})

	t.Run("on_unavailable 非法", func(t *testing.T) {
		cfg := base()
		cfg.Sandbox.OnUnavailable = "panic"
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "on_unavailable") {
			t.Errorf("应报 on_unavailable 非法: %v", err)
		}
	})

	t.Run("container 缺镜像", func(t *testing.T) {
		cfg := base()
		cfg.Sandbox.Type = "container"
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "container.image") {
			t.Errorf("container 模式应要求显式配置 image: %v", err)
		}
	})

	t.Run("合法配置", func(t *testing.T) {
		cfg := base()
		cfg.Sandbox.Type = "native"
		if err := cfg.Validate(); err != nil {
			t.Errorf("合法配置不应报错: %v", err)
		}
	})
}

// TestSandboxToSandbox 验证 YAML 配置到 sandbox.Config 的字段映射。
func TestSandboxToSandbox(t *testing.T) {
	in := SandboxConfig{
		AllowNet:        true,
		TimeoutSeconds:  60,
		MemoryLimitMB:   1024,
		CPULimitSeconds: 20,
		MaxProcesses:    128,
		Container: ContainerSandboxConfig{
			Runtime:     "podman",
			Image:       "golang:1.26",
			TmpfsSizeMB: 128,
			User:        "1000:1000",
		},
	}
	out := in.ToSandbox()
	if !out.AllowNet || out.TimeoutSeconds != 60 || out.MemoryLimitMB != 1024 ||
		out.CPULimitSeconds != 20 || out.MaxProcesses != 128 {
		t.Errorf("顶层字段映射错误: %+v", out)
	}
	if out.Container.Runtime != "podman" || out.Container.Image != "golang:1.26" ||
		out.Container.TmpfsSizeMB != 128 || out.Container.User != "1000:1000" {
		t.Errorf("Container 字段映射错误: %+v", out.Container)
	}
}
