package bootstrap

import (
	"context"
	"strings"
	"testing"

	"github.com/wenruigao/tommy-catty/config"
	"github.com/wenruigao/tommy-catty/internal/tool"
)

// TestRegisterBuiltinTools_SandboxNone 验证默认（none）注册后工具可正常执行。
func TestRegisterBuiltinTools_SandboxNone(t *testing.T) {
	cfg := config.Default()
	reg := tool.NewRegistry()
	warnings, err := RegisterBuiltinTools(cfg, reg)
	if err != nil {
		t.Fatalf("none 模式不应报错: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("none 模式不应产生告警: %v", warnings)
	}
	meta, ok := reg.Get("shell_exec")
	if !ok {
		t.Fatal("shell_exec 未注册")
	}
	res, err := meta.Execute(context.Background(), map[string]interface{}{"command": "echo boot-ok"})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(res.Output, "boot-ok") {
		t.Errorf("执行结果异常: %q", res.Output)
	}
}

// TestRegisterBuiltinTools_SandboxDegrade 验证沙箱不可用时按 degrade 降级。
func TestRegisterBuiltinTools_SandboxDegrade(t *testing.T) {
	cfg := config.Default()
	cfg.Sandbox.Type = "container"
	cfg.Sandbox.OnUnavailable = "degrade"
	// 指定必然不存在的运行时二进制，保证 Available() 稳定失败
	cfg.Sandbox.Container.Runtime = "definitely-not-a-runtime-xyz"
	cfg.Sandbox.Container.Image = "ubuntu:24.04"

	reg := tool.NewRegistry()
	warnings, err := RegisterBuiltinTools(cfg, reg)
	if err != nil {
		t.Fatalf("degrade 模式不应报错: %v", err)
	}
	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "已降级") || !strings.Contains(joined, "container") {
		t.Errorf("告警应说明降级原因: %v", warnings)
	}
	// 降级后 shell_exec 仍可用（直通模式）
	meta, ok := reg.Get("shell_exec")
	if !ok {
		t.Fatal("降级后 shell_exec 未注册")
	}
	if _, err := meta.Execute(context.Background(), map[string]interface{}{"command": "true"}); err != nil {
		t.Fatalf("降级后执行失败: %v", err)
	}
}

// TestRegisterBuiltinTools_SandboxErrorMode 验证 on_unavailable=error 时拒绝启动。
func TestRegisterBuiltinTools_SandboxErrorMode(t *testing.T) {
	cfg := config.Default()
	cfg.Sandbox.Type = "container"
	cfg.Sandbox.OnUnavailable = "error"
	cfg.Sandbox.Container.Runtime = "definitely-not-a-runtime-xyz"
	cfg.Sandbox.Container.Image = "ubuntu:24.04"

	reg := tool.NewRegistry()
	if _, err := RegisterBuiltinTools(cfg, reg); err == nil {
		t.Fatal("error 模式下沙箱不可用应返回错误")
	}
}

// TestRegisterBuiltinTools_SandboxTimeout 验证沙箱超时配置覆盖注册超时。
func TestRegisterBuiltinTools_SandboxTimeout(t *testing.T) {
	cfg := config.Default()
	cfg.Sandbox.TimeoutSeconds = 77
	reg := tool.NewRegistry()
	if _, err := RegisterBuiltinTools(cfg, reg); err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	for _, name := range []string{"shell_exec", "code_run"} {
		meta, ok := reg.Get(name)
		if !ok {
			t.Fatalf("%s 未注册", name)
		}
		if d := meta.Timeout.Seconds(); d != 77 {
			t.Errorf("%s 超时 = %vs, want 77s", name, d)
		}
	}
}
