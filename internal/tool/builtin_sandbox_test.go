package tool

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/wenruigao/tommy-catty/internal/sandbox"
)

// fakeSandbox 记录 Compile 收到的请求，返回固定输出的命令，
// 用于验证工具层到沙箱层的接线与 Metadata 标注。
type fakeSandbox struct {
	lastSpec sandbox.ExecSpec
}

func (f *fakeSandbox) Name() string       { return "fake" }
func (f *fakeSandbox) Available() error   { return nil }
func (f *fakeSandbox) Warnings() []string { return nil }
func (f *fakeSandbox) Compile(_ context.Context, spec sandbox.ExecSpec) (*exec.Cmd, func(), error) {
	f.lastSpec = spec
	return exec.Command("/bin/echo", "fake-run"), func() {}, nil
}

// TestShellExec_SandboxWiring 验证 shell_exec 经沙箱执行并透传规范化的 ExecSpec。
func TestShellExec_SandboxWiring(t *testing.T) {
	fs := &fakeSandbox{}
	tool := NewShellExecTool()
	tool.Sandbox = fs

	res, err := tool.Execute(context.Background(), map[string]interface{}{"command": "echo hi"})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(res.Output, "fake-run") {
		t.Errorf("输出应来自沙箱返回的命令: %q", res.Output)
	}
	// Argv 应为 sh -c 包装后的完整命令行
	if len(fs.lastSpec.Argv) != 3 || fs.lastSpec.Argv[0] != "sh" || fs.lastSpec.Argv[2] != "echo hi" {
		t.Errorf("Argv 拼装错误: %v", fs.lastSpec.Argv)
	}
	// 环境变量应经过白名单过滤后传入
	if len(fs.lastSpec.Env) == 0 {
		t.Error("Env 不应为空（应传入白名单过滤后的环境）")
	}
	// Metadata 应标注沙箱名
	if got, _ := res.Metadata["sandbox"].(string); got != "fake" {
		t.Errorf("Metadata[sandbox] = %v, want fake", res.Metadata["sandbox"])
	}
}

// TestCodeRun_SandboxWiring 验证 code_run 经沙箱执行。
func TestCodeRun_SandboxWiring(t *testing.T) {
	fs := &fakeSandbox{}
	tool := &CodeRunTool{Sandbox: fs}

	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"language": "python",
		"code":     "print('x')",
	})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(res.Output, "fake-run") {
		t.Errorf("输出应来自沙箱返回的命令: %q", res.Output)
	}
	if len(fs.lastSpec.Argv) == 0 || fs.lastSpec.Argv[0] != "python3" {
		t.Errorf("Argv 应以 python3 开头: %v", fs.lastSpec.Argv)
	}
	if got, _ := res.Metadata["sandbox"].(string); got != "fake" {
		t.Errorf("Metadata[sandbox] = %v, want fake", res.Metadata["sandbox"])
	}
}

// TestShellExec_NilSandboxFallback 验证未注入沙箱时退化为直通模式（旧行为）。
func TestShellExec_NilSandboxFallback(t *testing.T) {
	tool := NewShellExecTool()
	res, err := tool.Execute(context.Background(), map[string]interface{}{"command": "echo legacy-ok"})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(res.Output, "legacy-ok") {
		t.Errorf("直通模式执行结果异常: %q", res.Output)
	}
	if got, _ := res.Metadata["sandbox"].(string); got != "none" {
		t.Errorf("Metadata[sandbox] = %v, want none", res.Metadata["sandbox"])
	}
}

// TestRegisterBuiltinToolsWithSandbox 验证注册函数注入沙箱与超时。
func TestRegisterBuiltinToolsWithSandbox(t *testing.T) {
	reg := NewRegistry()
	fs := &fakeSandbox{}
	RegisterBuiltinToolsWithSandbox(reg, "", fs, 45*1e9) // 45s 用 Duration 字面量表达

	for _, name := range []string{"shell_exec", "code_run"} {
		meta, ok := reg.Get(name)
		if !ok {
			t.Fatalf("%s 未注册", name)
		}
		if meta.Timeout != 45*1e9 {
			t.Errorf("%s 超时 = %v, want 45s", name, meta.Timeout)
		}
	}
}
