//go:build linux

package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// procAttr 为子进程设置平台相关的进程属性（linux）：
// 独立进程组（配合 baseCmd 的 Cancel 回调实现按进程组整树终止）+
// Pdeathsig（agent 进程死亡时由内核连带终止子进程，防孤儿）。
func procAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Setpgid:   true,
		Pdeathsig: syscall.SIGKILL,
	}
}

// nativePlatformWarnings 收集 linux 原生沙箱的构造期告警。
func nativePlatformWarnings(cfg Config) []string {
	var warns []string
	if _, err := exec.LookPath("prlimit"); err != nil {
		warns = append(warns, "未找到 prlimit（util-linux），native 模式不启用 rlimit 资源限制")
	}
	if cfg.MemoryLimitMB < 1024 {
		warns = append(warns, fmt.Sprintf("native(linux) 经 RLIMIT_AS 限制虚拟地址空间，Go 工具链需要更大限额，建议 memory_limit_mb >= 1024（当前 %d）", cfg.MemoryLimitMB))
	}
	if cfg.AllowNet {
		warns = append(warns, "allow_net=true：linux 原生沙箱将不创建网络命名空间，沙箱内进程可访问宿主网络")
	}
	return warns
}

// nativeAvailable 通过实际探测验证非特权用户命名空间可用
// （部分发行版经内核参数或 AppArmor 禁用 userns，如 Debian / Ubuntu 24.04）。
func nativeAvailable(cfg Config) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "exit 0")
	cmd.SysProcAttr = usernsProcAttr(cfg, !cfg.AllowNet)
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin"}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("非特权用户命名空间不可用（内核可能禁用 userns 或策略受限）: %w", err)
	}
	return nil
}

// linuxTmpfsWrapper 为 tmpfs /tmp 包裹脚本：
// 在新挂载命名空间内挂载独立 tmpfs 后再 exec 目标命令；
// 挂载失败直接退出（fail-closed），避免静默共享宿主 /tmp。
// 通过 `exec "$@"` 透传参数，无需对目标命令做引号转义。
const linuxTmpfsWrapper = `mount -t tmpfs -o size=%dm,mode=1777 tmpfs /tmp 2>/dev/null || { echo "sandbox: /tmp tmpfs 挂载失败" >&2; exit 111; }; exec "$@"`

// nativeCompile 组装 linux 原生沙箱命令：
// [prlimit ...]（可选）→ sh 包裹（挂载 tmpfs /tmp）→ 目标命令；
// 进程属性为非特权用户命名空间（user/pid/net/ipc/uts + 挂载命名空间）。
func nativeCompile(n *Native, ctx context.Context, spec ExecSpec) (*exec.Cmd, func(), error) {
	argv := spec.Argv
	// rlimit 资源限额（SysProcAttr 无 Rlimit 字段，经 prlimit 包裹实现）。
	// 注意：不设 RLIMIT_NPROC——它按真实 UID 统计宿主机全部同名进程，
	// 在多进程开发机上会误伤；进程数限额由 container 模式的 --pids-limit 承担。
	if _, err := exec.LookPath("prlimit"); err == nil {
		argv = append([]string{
			"prlimit",
			fmt.Sprintf("--as=%d", n.cfg.MemoryLimitMB<<20),    // 虚拟地址空间
			fmt.Sprintf("--cpu=%d", n.cfg.CPULimitSeconds),     // CPU 时间
			fmt.Sprintf("--fsize=%d", n.cfg.MemoryLimitMB<<20), // 单文件写入上限
			"--",
		}, argv...)
	}
	// sh 包裹层：在新挂载命名空间内挂载独立 tmpfs /tmp 后 exec 目标命令
	argv = append([]string{"sh", "-c", fmt.Sprintf(linuxTmpfsWrapper, n.cfg.MemoryLimitMB), "sh"}, argv...)

	cmd := baseCmd(ctx, argv)
	cmd.SysProcAttr = usernsProcAttr(n.cfg, !n.cfg.AllowNet)
	cmd.Dir = spec.WorkDir
	cmd.Env = spec.Env
	return cmd, func() {}, nil
}

// usernsProcAttr 构建非特权用户命名空间的进程属性：
//   - clone 时创建 user/pid/ipc/uts（可选 net）命名空间，父进程随后写
//     uid/gid 映射（容器内 root ↔ 宿主当前用户，文件权限不受影响）；
//   - 子进程内 unshare 出挂载命名空间，Go 运行时会自动将 / 重标记为
//     private（MS_REC|MS_PRIVATE），防止沙箱内挂载传播回宿主机；
//   - CLONE_NEWNET 提供禁网（全新空网络命名空间）。
func usernsProcAttr(cfg Config, isolateNet bool) *syscall.SysProcAttr {
	uid, gid := os.Getuid(), os.Getgid()
	attr := &syscall.SysProcAttr{
		Setpgid:      true,
		Cloneflags:   syscall.CLONE_NEWUSER | syscall.CLONE_NEWPID | syscall.CLONE_NEWIPC | syscall.CLONE_NEWUTS,
		Unshareflags: syscall.CLONE_NEWNS,
		UidMappings:  []syscall.SysProcIDMap{{ContainerID: 0, HostID: uid, Size: 1}},
		GidMappings:  []syscall.SysProcIDMap{{ContainerID: 0, HostID: gid, Size: 1}},
		// 子进程在用户命名空间内以 root 运行（映射到宿主当前用户），
		// 保证 tmpfs 挂载等工作正常且不放大宿主权限
		Credential: &syscall.Credential{Uid: 0, Gid: 0, NoSetGroups: true},
	}
	if isolateNet {
		attr.Cloneflags |= syscall.CLONE_NEWNET
	}
	return attr
}
