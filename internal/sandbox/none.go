package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
)

// NoneSandbox 直通沙箱：不做 OS 级隔离，仅保留独立进程组与组杀修复。
// 即未启用沙箱时的默认行为——与历史版本兼容，但修复了"超时只杀
// 直接子进程、孙进程逃逸"的缺陷。
type NoneSandbox struct{}

// NewNone 创建直通沙箱。
func NewNone() *NoneSandbox { return &NoneSandbox{} }

// Name 返回实现名。
func (s *NoneSandbox) Name() string { return "none" }

// Available 直通模式在任何主机都可用。
func (s *NoneSandbox) Available() error { return nil }

// Compile 将执行请求编译为普通子进程：独立进程组 + 组杀取消 + WaitDelay。
func (s *NoneSandbox) Compile(ctx context.Context, spec ExecSpec) (*exec.Cmd, func(), error) {
	if len(spec.Argv) == 0 {
		return nil, nil, errors.New("sandbox: 空的命令行")
	}
	dir, err := absWorkDir(spec)
	if err != nil {
		return nil, nil, err
	}
	cmd := baseCmd(ctx, spec.Argv)
	cmd.Dir = dir
	cmd.Env = spec.Env
	return cmd, func() {}, nil
}

// String 便于日志打印。
func (s *NoneSandbox) String() string { return fmt.Sprintf("sandbox(%s)", s.Name()) }
