package tool

import (
	"syscall"
)

// resourceLimits 为子进程设置进程属性。
// 通过 Setpgid 让子进程拥有独立的进程组，便于父进程按进程组终止整个
// 子进程树，防止孙进程逃逸后继续运行。
// 注意：这不是 rlimit 资源限制，不限制内存或 CPU 时间。
func resourceLimits() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Setpgid: true, // 独立进程组，便于按组终止子进程树
	}
}
