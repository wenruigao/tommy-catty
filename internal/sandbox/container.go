package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Container 容器沙箱：经 docker/podman 运行，提供最强隔离——
// 只读根文件系统 + 独立网络命名空间（禁网）+ cgroup 资源限额 + 全部能力裁剪。
type Container struct {
	cfg     Config
	runtime string // docker 或 podman 的可执行路径；空串表示未找到
}

// newContainerSandbox 创建容器沙箱：按配置或自动探测确定运行时。
func newContainerSandbox(cfg Config) *Container {
	c := &Container{cfg: cfg}
	if rt := cfg.Container.Runtime; rt != "" {
		c.runtime = rt
	} else if p, err := exec.LookPath("docker"); err == nil {
		c.runtime = p
	} else if p, err := exec.LookPath("podman"); err == nil {
		c.runtime = p
	}
	return c
}

// Name 返回实现名（含运行时名，如 "container:docker"）。
func (c *Container) Name() string {
	if c.runtime == "" {
		return "container"
	}
	return "container:" + filepath.Base(c.runtime)
}

// Available 探测容器运行时与镜像是否可用：
// 运行时存在且守护进程可达、镜像已拉取（--pull=never 下缺镜像会直接失败）。
func (c *Container) Available() error {
	if c.runtime == "" {
		return errors.New("未找到容器运行时（docker/podman），请安装或在 sandbox.container.runtime 指定")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, c.runtime, "version").CombinedOutput(); err != nil {
		return fmt.Errorf("容器运行时 %s 不可用（守护进程未启动？）: %v: %s",
			filepath.Base(c.runtime), err, strings.TrimSpace(string(out)))
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	if out, err := exec.CommandContext(ctx2, c.runtime, "image", "inspect", c.cfg.Container.Image).CombinedOutput(); err != nil {
		return fmt.Errorf("镜像 %s 不存在，请先执行 %s pull %s（%v: %s）",
			c.cfg.Container.Image, filepath.Base(c.runtime), c.cfg.Container.Image, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Compile 构建容器化执行命令，并返回容器回收 cleanup：
// 进程取消（超时）时 docker CLI 被组杀不会停掉容器，由 cleanup 的 rm -f 兜底回收。
func (c *Container) Compile(ctx context.Context, spec ExecSpec) (*exec.Cmd, func(), error) {
	if len(spec.Argv) == 0 {
		return nil, nil, errors.New("sandbox: 空的命令行")
	}
	dir, err := absWorkDir(spec)
	if err != nil {
		return nil, nil, err
	}
	name := "tommy-sb-" + uuid.NewString()
	argv := c.buildRunArgs(ExecSpec{Argv: spec.Argv, WorkDir: dir, Env: spec.Env}, name)
	cmd := baseCmd(ctx, argv)
	cmd.Dir = dir
	cmd.Env = spec.Env
	cleanup := func() {
		kctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		// 正常退出时 --rm 已删容器，rm -f 报"不存在"属预期，忽略错误
		_ = exec.CommandContext(kctx, c.runtime, "rm", "-f", name).Run()
	}
	return cmd, cleanup, nil
}

// buildRunArgs 拼装 `<runtime> run` 参数（纯函数，便于单测）。
func (c *Container) buildRunArgs(spec ExecSpec, name string) []string {
	cfg := c.cfg
	network := "none" // 默认禁网
	if cfg.AllowNet {
		network = "default"
	}
	tmpfsMB := cfg.Container.TmpfsSizeMB
	if tmpfsMB <= 0 {
		tmpfsMB = 64
	}
	args := []string{
		"run", "--rm",
		"--name", name,
		"--init",          // 收割僵尸进程并转发信号
		"--pull", "never", // 禁止隐式联网拉取镜像
		"--network", network, // 禁网（allow_net=true 时放开）
		"--memory", fmt.Sprintf("%dm", cfg.MemoryLimitMB),
		"--memory-swap", fmt.Sprintf("%dm", cfg.MemoryLimitMB), // 与内存一致，禁 swap
		"--cpus", fmt.Sprintf("%.2f", cpuCores(cfg)),
		"--pids-limit", strconv.Itoa(cfg.MaxProcesses),
		"--read-only", // 根文件系统只读，临时写入统一落 /tmp
		"--tmpfs", fmt.Sprintf("/tmp:rw,nosuid,size=%dm,mode=1777", tmpfsMB),
		"--cap-drop", "ALL", // 裁剪全部 Linux capabilities
		"--security-opt", "no-new-privileges", // 禁止提权
		"-v", spec.WorkDir + ":/workspace",
		"-w", "/workspace",
	}
	if u := cfg.Container.User; u != "" {
		args = append(args, "-u", u)
	}
	for _, e := range containerEnv(spec.Env) {
		args = append(args, "-e", e)
	}
	args = append(args, cfg.Container.Image)
	args = append(args, spec.Argv...)
	return args
}

// cpuCores 将 CPU 时间预算与墙钟超时的比值换算为 --cpus 核数，夹在 [0.1, 2]。
// 例如 30s 墙钟 + 10s CPU 预算 → 0.33 核。
func cpuCores(cfg Config) float64 {
	wall := cfg.TimeoutSeconds
	if wall <= 0 {
		wall = 30
	}
	ratio := float64(cfg.CPULimitSeconds) / float64(wall)
	if ratio < 0.1 {
		ratio = 0.1
	}
	if ratio > 2 {
		ratio = 2
	}
	return ratio
}

// hostSpecificEnv 宿主机特有的环境变量（容器内语义不同，须剔除）。
var hostSpecificEnv = map[string]bool{
	"PATH":   true, // 宿主路径在容器内无效，交由镜像默认 PATH
	"HOME":   true,
	"TMPDIR": true,
	"USER":   true,
}

// containerEnv 过滤宿主机特有变量并注入容器内默认值：
// 根文件系统只读，HOME/TMPDIR 重定向到可写的 /tmp；
// GOCACHE/GOPATH/GOTOOLCHAIN 保证 go 工具链离线可用。
// 注入项在前、用户变量在后，用户可覆盖默认值。
func containerEnv(env []string) []string {
	out := []string{
		"HOME=/tmp",
		"TMPDIR=/tmp",
		"GOCACHE=/tmp/gocache",
		"GOPATH=/tmp/gopath",
		"GOTOOLCHAIN=local",
		"PYTHONDONTWRITEBYTECODE=1",
		"PYTHONUNBUFFERED=1",
		"LANG=C.UTF-8",
	}
	for _, e := range env {
		key := e
		if i := strings.IndexByte(e, '='); i >= 0 {
			key = e[:i]
		}
		if hostSpecificEnv[key] {
			continue
		}
		out = append(out, e)
	}
	return out
}
