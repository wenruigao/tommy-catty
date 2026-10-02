//go:build darwin

package sandbox

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
)

// procAttr 为子进程设置平台相关的进程属性（darwin）：
// 独立进程组，配合 baseCmd 的 Cancel 回调实现按进程组整树终止。
func procAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// nativePlatformWarnings 收集 darwin 原生沙箱的构造期告警。
func nativePlatformWarnings(cfg Config) []string {
	var warns []string
	if cfg.AllowNet {
		warns = append(warns, "darwin 原生沙箱按 allow_net=true 放行网络（Seatbelt profile 含 allow network*），请注意数据外泄风险")
	}
	return warns
}

// nativeAvailable 探测 sandbox-exec（Seatbelt）是否可用。
func nativeAvailable(cfg Config) error {
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		return fmt.Errorf("未找到 sandbox-exec（macOS 原生沙箱依赖 /usr/bin/sandbox-exec）: %w", err)
	}
	return nil
}

// nativeCompile 用 sandbox-exec 包裹目标命令：
// sandbox-exec -p <profile> <argv...>
func nativeCompile(n *Native, ctx context.Context, spec ExecSpec) (*exec.Cmd, func(), error) {
	profile := seatbeltProfile(n.cfg, spec.WorkDir)
	argv := append([]string{"sandbox-exec", "-p", profile}, spec.Argv...)
	cmd := baseCmd(ctx, argv)
	cmd.Dir = spec.WorkDir
	cmd.Env = spec.Env
	return cmd, func() {}, nil
}

// seatbeltProfile 生成 macOS Seatbelt（sandbox-exec）profile。
// 策略：默认全拒 + 按需放行（进程/文件读/受限文件写/禁网）。
func seatbeltProfile(cfg Config, workDir string) string {
	var b strings.Builder
	b.WriteString("(version 1)\n")
	b.WriteString("(deny default)\n")
	// 进程：允许执行与 fork（LLM 工具链需要运行解释器、编译器）
	b.WriteString("(allow process-exec*)\n")
	b.WriteString("(allow process-fork)\n")
	// 文件读：全局放行（系统框架/工具链运行需要读取大量系统文件）
	b.WriteString("(allow file-read*)\n")
	// 文件写：仅限工作目录与临时目录（TMPDIR 位于 /private/var/folders）
	b.WriteString("(allow file-write*\n")
	b.WriteString("    (subpath \"" + escapeSeatbelt(workDir) + "\")\n")
	for _, p := range []string{"/private/tmp", "/private/var/tmp", "/private/var/folders", "/dev/null"} {
		b.WriteString("    (subpath \"" + p + "\")\n")
	}
	b.WriteString(")\n")
	// 系统接口：运行时（Go/Python）需要的只读系统调用与 Mach 服务
	b.WriteString("(allow sysctl-read)\n")
	b.WriteString("(allow mach-lookup)\n")
	b.WriteString("(allow ipc-posix-shm)\n")
	b.WriteString("(allow ipc-posix-sem)\n")
	// 网络：默认禁网；allow_net=true 时放行
	if cfg.AllowNet {
		b.WriteString("(allow network*)\n")
	} else {
		b.WriteString("(deny network*)\n")
	}
	return b.String()
}

// escapeSeatbelt 转义 Seatbelt profile 字符串中的特殊字符。
func escapeSeatbelt(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `"`, `\"`)
}
