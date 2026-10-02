// Package sandbox 为 shell_exec / code_run 等高危工具提供 OS 级执行隔离。
//
// 三档实现按配置选择（config.yaml 的 sandbox.type）：
//   - none：直通模式（默认）。不做 OS 级隔离，仅保留独立进程组，
//     并在超时/取消时按进程组整树终止（修复孙进程逃逸）。
//   - native：OS 原生沙箱。Linux 使用非特权用户命名空间
//     （user/pid/net/ipc/uts + rlimit）；macOS 使用 sandbox-exec（Seatbelt）。
//   - container：容器沙箱。经 docker/podman 以只读根文件系统、禁网、
//     资源限额方式运行。
//
// 沙箱是"执行隔离"层，与引擎 ToolGate 的"策略裁决"层正交：
// 门禁决定"是否允许调用"，沙箱决定"调用在哪里执行"。
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// waitDelay 超时取消后等待管道关闭的兜底时长：
// 组杀完成后仍有孙进程持有 stdout/stderr 管道时，由 WaitDelay 强制收口，
// 保证 cmd.Run() 不会因孤儿进程握住管道而永久阻塞。
const waitDelay = 3 * time.Second

// Config 沙箱配置（字段与 config.SandboxConfig 对应；独立定义以避免
// sandbox → config → engine → tool 的包间循环依赖，由装配层做转换）。
type Config struct {
	// AllowNet 是否允许沙箱内进程访问网络（默认 false 禁网）
	AllowNet bool

	// TimeoutSeconds 单次执行墙钟超时（秒，默认 30；container 模式用于换算 --cpus）
	TimeoutSeconds int

	// MemoryLimitMB 内存上限 MB（native: RLIMIT_AS；container: --memory，默认 512）
	MemoryLimitMB int

	// CPULimitSeconds CPU 时间上限秒（native: RLIMIT_CPU；container 换算 --cpus，默认 10）
	CPULimitSeconds int

	// MaxProcesses 进程数上限（container: --pids-limit，默认 64）
	MaxProcesses int

	// Container 容器沙箱配置
	Container ContainerConfig
}

// ContainerConfig 容器沙箱配置。
type ContainerConfig struct {
	// Runtime 容器运行时可执行名（docker | podman；空串自动探测）
	Runtime string
	// Image 容器镜像
	Image string
	// TmpfsSizeMB 容器内 /tmp tmpfs 容量 MB（默认 64）
	TmpfsSizeMB int
	// User 容器内运行用户（如 "1000:1000"；空串使用镜像默认用户）
	User string
}

// normalize 填充零值默认（保证直接构造 Config 也可用）。
func (c Config) normalize() Config {
	if c.TimeoutSeconds <= 0 {
		c.TimeoutSeconds = 30
	}
	if c.MemoryLimitMB <= 0 {
		c.MemoryLimitMB = 512
	}
	if c.CPULimitSeconds <= 0 {
		c.CPULimitSeconds = 10
	}
	if c.MaxProcesses <= 0 {
		c.MaxProcesses = 64
	}
	if c.Container.Image == "" {
		c.Container.Image = "ubuntu:24.04"
	}
	if c.Container.TmpfsSizeMB <= 0 {
		c.Container.TmpfsSizeMB = 64
	}
	return c
}

// ExecSpec 描述一次待沙箱化的进程执行请求。
type ExecSpec struct {
	// Argv 完整命令行（如 ["/bin/sh", "-c", "ls -l"]），不可为空
	Argv []string

	// WorkDir 主机侧工作目录（建议传绝对路径；容器模式会挂载进容器）。
	// 为空时自动取当前进程工作目录。
	WorkDir string

	// Env 子进程环境变量（KEY=VALUE 形式，应已由调用方按白名单过滤；
	// 传空切片意味着子进程环境为空，与 exec.Cmd 语义一致）
	Env []string
}

// Sandbox 执行沙箱抽象：将一次执行请求编译为可在隔离环境中运行的 exec.Cmd。
type Sandbox interface {
	// Name 返回沙箱实现名（如 "none"、"native"、"container:docker"），
	// 用于审计日志与工具执行 Metadata 标注。
	Name() string

	// Available 探测当前主机是否支持该沙箱（如内核 userns 权限、
	// 容器运行时与镜像可用）。启动装配时调用一次。
	Available() error

	// Compile 将一次执行请求编译为 exec.Cmd。返回的 Cmd 已配置好
	// 独立进程组、超时取消（按进程组整树 SIGKILL）与 WaitDelay；
	// 返回的 cleanup 在进程结束后必须调用（容器模式负责回收容器）。
	Compile(ctx context.Context, spec ExecSpec) (*exec.Cmd, func(), error)
}

// Warner 可选接口：沙箱实现可暴露构造/降级告警（如缺少 rlimit 工具），
// 由启动装配层打印。
type Warner interface {
	Warnings() []string
}

// New 根据沙箱类型构造实例：kind 为 "native" / "container"，其他值
// （含空串与 "none"）一律返回直通实现。构造本身不做可用性探测
// （由调用方显式调用 Available 决定降级策略）。
func New(kind string, cfg Config) Sandbox {
	cfg = cfg.normalize()
	switch kind {
	case "native":
		return newNativeSandbox(cfg)
	case "container":
		return newContainerSandbox(cfg)
	default:
		return NewNone()
	}
}

// baseCmd 构建所有沙箱实现共用的 exec.Cmd 基座：
// 独立进程组（Setpgid）+ 取消时按进程组整树 SIGKILL + WaitDelay 收口。
func baseCmd(ctx context.Context, argv []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.SysProcAttr = procAttr()
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// pgid == 子进程 PID（Setpgid 使子进程成为新进程组长），
		// 负号表示向整个进程组发信号，连带终止 sh -c 派生的孙进程
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			// 进程组已不存在（正常退出竞态）时按"进程已结束"处理，
			// 避免吞掉命令的真实退出码
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			return err
		}
		return nil
	}
	cmd.WaitDelay = waitDelay
	return cmd
}

// absWorkDir 规范化执行工作目录：为空时取当前进程工作目录，统一转绝对路径
// （容器模式挂载、native 模式 profile 生成均要求绝对路径）。
func absWorkDir(spec ExecSpec) (string, error) {
	dir := spec.WorkDir
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("sandbox: 无法获取当前工作目录: %w", err)
		}
		dir = wd
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("sandbox: 工作目录 %q 解析失败: %w", dir, err)
	}
	return abs, nil
}
