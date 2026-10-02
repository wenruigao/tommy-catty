package sandbox

import (
	"context"
	"errors"
	"os/exec"
)

// Native OS 原生沙箱（平台实现见 native_linux.go / native_darwin.go）：
// Linux 经非特权用户命名空间 + rlimit；macOS 经 sandbox-exec（Seatbelt profile）。
type Native struct {
	cfg      Config
	warnings []string // 构造期收集的降级/能力告警
}

// newNativeSandbox 创建原生沙箱实例（平台文件提供构造钩子）。
func newNativeSandbox(cfg Config) *Native {
	return &Native{
		cfg:      cfg,
		warnings: nativePlatformWarnings(cfg),
	}
}

// Name 返回实现名。
func (n *Native) Name() string { return "native" }

// Warnings 返回构造期告警（如缺少 prlimit 工具、限额过小等）。
func (n *Native) Warnings() []string { return n.warnings }

// Available 探测当前主机是否支持原生沙箱（平台实现）。
func (n *Native) Available() error { return nativeAvailable(n.cfg) }

// Compile 将执行请求编译为平台原生的沙箱化命令。
func (n *Native) Compile(ctx context.Context, spec ExecSpec) (*exec.Cmd, func(), error) {
	if len(spec.Argv) == 0 {
		return nil, nil, errors.New("sandbox: 空的命令行")
	}
	dir, err := absWorkDir(spec)
	if err != nil {
		return nil, nil, err
	}
	return nativeCompile(n, ctx, ExecSpec{Argv: spec.Argv, WorkDir: dir, Env: spec.Env})
}
