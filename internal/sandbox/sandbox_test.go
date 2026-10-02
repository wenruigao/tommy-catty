package sandbox

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestNoneSandbox_BasicExec 验证直通沙箱的基本执行、工作目录与环境传递。
func TestNoneSandbox_BasicExec(t *testing.T) {
	dir := t.TempDir()
	sb := NewNone()
	cmd, cleanup, err := sb.Compile(context.Background(), ExecSpec{
		Argv:    []string{"sh", "-c", "echo hello-$FOO; pwd"},
		WorkDir: dir,
		Env:     []string{"FOO=bar", "PATH=/usr/bin:/bin"},
	})
	if err != nil {
		t.Fatalf("Compile 失败: %v", err)
	}
	defer cleanup()

	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out.String(), "hello-bar") {
		t.Errorf("环境变量未传递到子进程: %q", out.String())
	}
	if !strings.Contains(out.String(), filepath.Base(dir)) {
		t.Errorf("工作目录未生效: %q (期望包含 %s)", out.String(), dir)
	}
}

// TestNoneSandbox_AbsWorkDir 验证空工作目录回退到当前进程工作目录。
func TestNoneSandbox_AbsWorkDir(t *testing.T) {
	sb := NewNone()
	cwd, _ := os.Getwd()
	cmd, cleanup, err := sb.Compile(context.Background(), ExecSpec{
		Argv: []string{"pwd"},
		Env:  []string{"PATH=/usr/bin:/bin"},
	})
	if err != nil {
		t.Fatalf("Compile 失败: %v", err)
	}
	defer cleanup()
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != cwd {
		t.Errorf("空 WorkDir 应回退到进程 CWD: got %q, want %q", got, cwd)
	}
}

// TestNoneSandbox_KillsProcessGroupOnCancel 组杀回归测试：
// sh 派生的后台孙进程持有 stdout 管道，超时取消后必须整组终止，
// 且 cmd.Run() 不得因孤儿进程握住管道而长时间阻塞。
// （修复前：默认 Cancel 只 SIGKILL 直接子进程 sh，孙进程逃逸存活。）
func TestNoneSandbox_KillsProcessGroupOnCancel(t *testing.T) {
	dir := t.TempDir()
	pgidFile := filepath.Join(dir, "pgid")
	// 两个后台 sleep 持有管道，wait 让 sh 存活到超时；
	// $$ 为 sh 自身 PID（Setpgid 下即进程组长 ID）
	script := "sleep 30 & sleep 30 & echo $$ > " + pgidFile + "; wait"

	sb := NewNone()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	cmd, cleanup, err := sb.Compile(ctx, ExecSpec{
		Argv:    []string{"sh", "-c", script},
		WorkDir: dir,
		Env:     []string{"PATH=/usr/bin:/bin"},
	})
	if err != nil {
		t.Fatalf("Compile 失败: %v", err)
	}
	defer cleanup()

	start := time.Now()
	// stdout 直连管道（非 os.File），孙进程握住写端可复现阻塞场景
	cmd.Stdout = &bytes.Buffer{}
	runErr := cmd.Run()
	elapsed := time.Since(start)

	if runErr == nil {
		t.Fatal("超时取消后 cmd.Run 应返回错误（context 超时或被杀）")
	}
	// 组杀 + WaitDelay 收口：远小于 sleep 30s；若超过 10s 说明组杀/收口失效
	if elapsed > 10*time.Second {
		t.Fatalf("超时后 %v 才返回，进程组未被及时终止", elapsed)
	}

	// 读取 sh 写入的 pgid，验证整组（含孙进程）已消亡
	data, err := os.ReadFile(pgidFile)
	if err != nil {
		t.Fatalf("读取 pgid 文件失败: %v", err)
	}
	pgid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pgid <= 0 {
		t.Fatalf("pgid 文件内容非法: %q", data)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if err := syscall.Kill(-pgid, 0); errors.Is(err, syscall.ESRCH) {
			break // 整组已消亡
		}
		if time.Now().After(deadline) {
			t.Fatalf("超时取消后进程组 %d 仍有存活进程（孙进程逃逸）", pgid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestNew_KindDispatch 验证工厂按类型分发。
func TestNew_KindDispatch(t *testing.T) {
	cfg := Config{}
	cases := []struct {
		kind string
		want string
	}{
		{"none", "none"},
		{"", "none"},
		{"unknown-kind", "none"},
		{"native", "native"},
		{"container", "container"},
	}
	for _, c := range cases {
		sb := New(c.kind, cfg)
		if sb.Name() != c.want {
			t.Errorf("New(%q).Name() = %q, want %q", c.kind, sb.Name(), c.want)
		}
	}
}

// TestConfigNormalize 验证零值配置的默认值填充。
func TestConfigNormalize(t *testing.T) {
	got := Config{}.normalize()
	if got.TimeoutSeconds != 30 {
		t.Errorf("TimeoutSeconds 默认值 = %d, want 30", got.TimeoutSeconds)
	}
	if got.MemoryLimitMB != 512 {
		t.Errorf("MemoryLimitMB 默认值 = %d, want 512", got.MemoryLimitMB)
	}
	if got.CPULimitSeconds != 10 {
		t.Errorf("CPULimitSeconds 默认值 = %d, want 10", got.CPULimitSeconds)
	}
	if got.MaxProcesses != 64 {
		t.Errorf("MaxProcesses 默认值 = %d, want 64", got.MaxProcesses)
	}
	if got.Container.Image != "ubuntu:24.04" {
		t.Errorf("Container.Image 默认值 = %q, want ubuntu:24.04", got.Container.Image)
	}
	if got.Container.TmpfsSizeMB != 64 {
		t.Errorf("Container.TmpfsSizeMB 默认值 = %d, want 64", got.Container.TmpfsSizeMB)
	}
}

// TestContainerBuildRunArgs 验证容器 run 参数拼装（确定性断言，不依赖 docker）。
func TestContainerBuildRunArgs(t *testing.T) {
	c := &Container{cfg: Config{
		TimeoutSeconds:  30,
		MemoryLimitMB:   512,
		CPULimitSeconds: 10,
		MaxProcesses:    64,
		Container: ContainerConfig{
			Image:       "ubuntu:24.04",
			TmpfsSizeMB: 64,
		},
	}}
	spec := ExecSpec{
		Argv:    []string{"sh", "-c", "echo hi"},
		WorkDir: "/tmp/work",
		Env:     []string{"PATH=/usr/bin:/bin", "FOO=bar"},
	}
	args := c.buildRunArgs(spec, "tommy-sb-test")

	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--network none", "--pull never", "--read-only",
		"--memory 512m", "--memory-swap 512m", "--pids-limit 64",
		"--cap-drop ALL", "--security-opt no-new-privileges",
		"-v /tmp/work:/workspace", "-w /workspace",
		"ubuntu:24.04 sh -c echo hi",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("run 参数缺少 %q:\n%s", want, joined)
		}
	}
	// 禁网断言（默认）
	if strings.Contains(joined, "--network default") {
		t.Errorf("默认应禁网（--network none）:\n%s", joined)
	}
	// 镜像之后的参数必须是目标 argv
	idx := -1
	for i, a := range args {
		if a == "ubuntu:24.04" {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatalf("run 参数中未找到镜像名:\n%s", joined)
	}
	rest := args[idx+1:]
	if strings.Join(rest, " ") != "sh -c echo hi" {
		t.Errorf("镜像后应为目标 argv，got: %v", rest)
	}
}

// TestContainerBuildRunArgs_AllowNet 验证 allow_net=true 时放开网络。
func TestContainerBuildRunArgs_AllowNet(t *testing.T) {
	c := &Container{cfg: Config{AllowNet: true, Container: ContainerConfig{Image: "img"}}}
	args := c.buildRunArgs(ExecSpec{Argv: []string{"sh"}}, "x")
	if !strings.Contains(strings.Join(args, " "), "--network default") {
		t.Errorf("allow_net=true 应使用 --network default: %v", args)
	}
}

// TestContainerBuildRunArgs_User 验证容器用户注入。
func TestContainerBuildRunArgs_User(t *testing.T) {
	c := &Container{cfg: Config{Container: ContainerConfig{Image: "img", User: "1000:1000"}}}
	args := c.buildRunArgs(ExecSpec{Argv: []string{"sh"}}, "x")
	if !strings.Contains(strings.Join(args, " "), "-u 1000:1000") {
		t.Errorf("应包含 -u 1000:1000: %v", args)
	}
}

// TestContainerEnv 验证宿主特有变量过滤与容器内默认值注入。
func TestContainerEnv(t *testing.T) {
	out := containerEnv([]string{
		"PATH=/opt/homebrew/bin", // 应被过滤（宿主路径容器内无效）
		"HOME=/Users/x",          // 应被过滤
		"TMPDIR=/var/folders/x",  // 应被过滤
		"USER=ryan",              // 应被过滤
		"FOO=bar",                // 应保留
	})
	joined := strings.Join(out, "\n")
	if strings.Contains(joined, "PATH=") && !strings.Contains(joined, "GOCACHE") {
		t.Errorf("宿主 PATH 不应透传: %s", joined)
	}
	if strings.Contains(joined, "/Users") || strings.Contains(joined, "/var/folders") {
		t.Errorf("宿主路径泄漏: %s", joined)
	}
	if !strings.Contains(joined, "FOO=bar") {
		t.Errorf("普通变量应保留: %s", joined)
	}
	for _, want := range []string{"HOME=/tmp", "TMPDIR=/tmp", "GOCACHE=/tmp/gocache", "GOPATH=/tmp/gopath", "GOTOOLCHAIN=local"} {
		if !strings.Contains(joined, want) {
			t.Errorf("缺少容器内默认值 %q: %s", want, joined)
		}
	}
}

// TestCPUCores 验证 CPU 核数换算与夹取。
func TestCPUCores(t *testing.T) {
	cases := []struct {
		cpu, wall int
		want      float64
	}{
		{10, 30, 0.33}, // 10s/30s
		{100, 30, 2.0}, // 上限夹取
		{1, 30, 0.1},   // 下限夹取
		{30, 0, 1.0},   // wall 非法时回退 30（30/30=1）
	}
	for _, c := range cases {
		got := cpuCores(Config{CPULimitSeconds: c.cpu, TimeoutSeconds: c.wall})
		if got < c.want-0.01 || got > c.want+0.01 {
			t.Errorf("cpuCores(cpu=%d, wall=%d) = %v, want ≈%v", c.cpu, c.wall, got, c.want)
		}
	}
}

// TestCompile_EmptyArgv 验证空命令行统一报错。
func TestCompile_EmptyArgv(t *testing.T) {
	sbs := []Sandbox{NewNone()}
	for _, sb := range sbs {
		if _, _, err := sb.Compile(context.Background(), ExecSpec{}); err == nil {
			t.Errorf("%s 空命令行应报错", sb.Name())
		}
	}
}

// TestCompile_RequiresContext 验证 ctx 取消时（编译后启动前）不会 panic。
func TestCompile_RequiresContext(t *testing.T) {
	sb := NewNone()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd, cleanup, err := sb.Compile(ctx, ExecSpec{Argv: []string{"true"}, Env: []string{"PATH=/usr/bin:/bin"}})
	if err != nil {
		t.Fatalf("Compile 失败: %v", err)
	}
	defer cleanup()
	_ = cmd.Run() // 已取消的 ctx：返回错误即可，不允许 hang
}
