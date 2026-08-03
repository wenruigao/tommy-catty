package tool

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ============================================================
// WebSearchTool - 网络搜索工具
// ============================================================

// Searcher 定义搜索能力的抽象接口，供 WebSearchTool 使用。
type Searcher interface {
	Search(ctx context.Context, query string, maxResults int) ([]SearchResult, error)
}

// SearchResult 搜索结果条目。
type SearchResult struct {
	Title   string
	URL     string
	Snippet string
}

// WebSearchTool 调用真实搜索引擎返回搜索结果。
type WebSearchTool struct {
	searcher Searcher
}

// NewWebSearchTool 创建搜索工具，searcher 为搜索后端实现。
func NewWebSearchTool(searcher Searcher) *WebSearchTool {
	return &WebSearchTool{searcher: searcher}
}

func (t *WebSearchTool) Name() string { return "web_search" }

func (t *WebSearchTool) Description() string {
	return "搜索互联网获取相关信息，返回搜索结果摘要列表。用于查找最新信息、技术文档、新闻等。"
}

func (t *WebSearchTool) Parameters() JSONSchema {
	return JSONSchema{
		Type: "object",
		Properties: map[string]Property{
			"query": {
				Type:        "string",
				Description: "搜索关键词",
			},
			"max_results": {
				Type:        "integer",
				Description: "最大返回结果数量",
				Default:     5,
			},
		},
		Required: []string{"query"},
	}
}

func (t *WebSearchTool) Execute(ctx context.Context, args map[string]interface{}) (Result, error) {
	query, ok := args["query"].(string)
	if !ok || query == "" {
		return Result{}, fmt.Errorf("parameter 'query' is required and must be a non-empty string")
	}

	maxResults := 5
	if mr, ok := args["max_results"]; ok {
		switch v := mr.(type) {
		case int:
			maxResults = v
		case float64:
			maxResults = int(v)
		}
	}

	results, err := t.searcher.Search(ctx, query, maxResults)
	if err != nil {
		return Result{}, fmt.Errorf("search failed: %w", err)
	}

	if len(results) == 0 {
		return Result{
			Output: fmt.Sprintf("未找到与 %q 相关的搜索结果。", query),
			Metadata: map[string]interface{}{
				"query":        query,
				"result_count": 0,
			},
		}, nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("搜索结果 (关键词: %q, 共 %d 条):\n\n", query, len(results)))
	for i, r := range results {
		sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, r.Title))
		sb.WriteString(fmt.Sprintf("   链接: %s\n", r.URL))
		if r.Snippet != "" {
			snippet := r.Snippet
			if len([]rune(snippet)) > 300 {
				snippet = string([]rune(snippet)[:300]) + "..."
			}
			sb.WriteString(fmt.Sprintf("   摘要: %s\n", snippet))
		}
		sb.WriteString("\n")
	}

	return Result{
		Output: sb.String(),
		Metadata: map[string]interface{}{
			"query":        query,
			"result_count": len(results),
		},
	}, nil
}

// ============================================================
// WebFetchTool - 网页抓取工具
// ============================================================

// WebFetchTool 通过 HTTP GET 请求获取指定 URL 的网页内容
type WebFetchTool struct {
	client *http.Client
}

func NewWebFetchTool() *WebFetchTool {
	return &WebFetchTool{
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (t *WebFetchTool) Name() string { return "web_fetch" }

func (t *WebFetchTool) Description() string {
	return "获取指定 URL 的网页内容，返回页面文本"
}

func (t *WebFetchTool) Parameters() JSONSchema {
	return JSONSchema{
		Type: "object",
		Properties: map[string]Property{
			"url": {
				Type:        "string",
				Description: "要抓取的网页 URL（必须以 http:// 或 https:// 开头）",
			},
			"max_length": {
				Type:        "integer",
				Description: "返回内容的最大字符数",
				Default:     10000,
			},
		},
		Required: []string{"url"},
	}
}

func (t *WebFetchTool) Execute(ctx context.Context, args map[string]interface{}) (Result, error) {
	urlStr, ok := args["url"].(string)
	if !ok || urlStr == "" {
		return Result{}, fmt.Errorf("parameter 'url' is required and must be a non-empty string")
	}

	// 验证 URL 协议
	if !strings.HasPrefix(urlStr, "http://") && !strings.HasPrefix(urlStr, "https://") {
		return Result{}, fmt.Errorf("url must start with http:// or https://")
	}

	maxLength := 10000
	if ml, ok := args["max_length"]; ok {
		switch v := ml.(type) {
		case int:
			maxLength = v
		case float64:
			maxLength = int(v)
		}
	}

	// 创建带 context 的请求
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return Result{}, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", "TommyCat-Agent/1.0")

	resp, err := t.client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("failed to fetch url: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Result{
			Error: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, resp.Status),
		}, nil
	}

	// 限制读取大小，防止内存溢出
	limitedReader := io.LimitReader(resp.Body, int64(maxLength)+1)
	body, err := io.ReadAll(limitedReader)
	if err != nil {
		return Result{}, fmt.Errorf("failed to read response body: %w", err)
	}

	content := string(body)
	truncated := false
	if len(content) > maxLength {
		content = content[:maxLength]
		truncated = true
	}

	return Result{
		Output: content,
		Metadata: map[string]interface{}{
			"url":            urlStr,
			"status_code":    resp.StatusCode,
			"content_type":   resp.Header.Get("Content-Type"),
			"truncated":      truncated,
			"content_length": len(content),
		},
	}, nil
}

// ============================================================
// FileReadTool - 文件读取工具
// ============================================================

// FileReadTool 读取指定路径的文件内容，带有路径安全验证
type FileReadTool struct {
	// AllowedDirs 限制可读取的目录白名单，为空则不限制
	AllowedDirs []string
}

func (t *FileReadTool) Name() string { return "file_read" }

func (t *FileReadTool) Description() string {
	return "读取指定路径的文件内容，支持文本文件"
}

func (t *FileReadTool) Parameters() JSONSchema {
	return JSONSchema{
		Type: "object",
		Properties: map[string]Property{
			"path": {
				Type:        "string",
				Description: "要读取的文件路径",
			},
			"offset": {
				Type:        "integer",
				Description: "起始行号（从 0 开始），默认读取全部",
				Default:     0,
			},
			"limit": {
				Type:        "integer",
				Description: "最大读取行数，默认不限制",
				Default:     0,
			},
		},
		Required: []string{"path"},
	}
}

func (t *FileReadTool) Execute(ctx context.Context, args map[string]interface{}) (Result, error) {
	path, ok := args["path"].(string)
	if !ok || path == "" {
		return Result{}, fmt.Errorf("parameter 'path' is required and must be a non-empty string")
	}

	// 路径安全验证
	if err := t.validatePath(path); err != nil {
		return Result{}, err
	}

	// 检查文件是否存在
	info, err := os.Stat(path)
	if err != nil {
		return Result{}, fmt.Errorf("cannot access file: %w", err)
	}
	if info.IsDir() {
		return Result{}, fmt.Errorf("path is a directory, not a file: %s", path)
	}

	// 检查 context 是否已取消
	select {
	case <-ctx.Done():
		return Result{}, ctx.Err()
	default:
	}

	// 读取文件内容
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{}, fmt.Errorf("failed to read file: %w", err)
	}

	content := string(data)

	// 按行截取（如果指定了 offset/limit）
	offset := 0
	limit := 0
	if v, ok := args["offset"]; ok {
		switch n := v.(type) {
		case int:
			offset = n
		case float64:
			offset = int(n)
		}
	}
	if v, ok := args["limit"]; ok {
		switch n := v.(type) {
		case int:
			limit = n
		case float64:
			limit = int(n)
		}
	}

	if offset > 0 || limit > 0 {
		lines := strings.Split(content, "\n")
		if offset >= len(lines) {
			content = ""
		} else {
			end := len(lines)
			if limit > 0 && offset+limit < end {
				end = offset + limit
			}
			content = strings.Join(lines[offset:end], "\n")
		}
	}

	return Result{
		Output: content,
		Metadata: map[string]interface{}{
			"path":      path,
			"file_size": info.Size(),
		},
	}, nil
}

// containsDotDotSegment 判断路径中是否包含独立的 ".." 路径段。
// 仅当 ".." 作为完整路径段出现时返回 true（如 "../x"、"x/.."、"x/../y"、".."），
// 文件名中包含 ".." 的合法路径（如 "foo..bar.txt"、"a..b/c.txt"）不受影响。
func containsDotDotSegment(path string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(path), "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

// validatePath 验证文件路径的安全性，防止路径穿越攻击
func (t *FileReadTool) validatePath(path string) error {
	// 检查原始路径中的穿越组件（在 Abs 解析之前）：
	// 仅拒绝 ".." 作为独立路径段的形式，避免误伤 foo..bar.txt 之类的合法文件名
	if containsDotDotSegment(path) {
		return fmt.Errorf("path traversal detected: %s", path)
	}

	// 解析为绝对路径
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("invalid path: %w", err)
	}

	// 如果设置了白名单目录，验证路径是否在白名单内
	if len(t.AllowedDirs) > 0 {
		allowed := false
		for _, dir := range t.AllowedDirs {
			absDir, _ := filepath.Abs(dir)
			if strings.HasPrefix(absPath, absDir+string(os.PathSeparator)) || absPath == absDir {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("path not in allowed directories: %s", path)
		}
	}

	return nil
}

// ============================================================
// FileWriteTool - 文件写入工具
// ============================================================

// FileWriteTool 将内容写入指定文件，带有路径安全验证
type FileWriteTool struct {
	// AllowedDirs 限制可写入的目录白名单，为空则不限制
	AllowedDirs []string
}

func (t *FileWriteTool) Name() string { return "file_write" }

func (t *FileWriteTool) Description() string {
	return "将内容写入指定文件路径，如果文件不存在则创建"
}

func (t *FileWriteTool) Parameters() JSONSchema {
	return JSONSchema{
		Type: "object",
		Properties: map[string]Property{
			"path": {
				Type:        "string",
				Description: "要写入的文件路径",
			},
			"content": {
				Type:        "string",
				Description: "要写入的文件内容",
			},
			"append": {
				Type:        "boolean",
				Description: "是否追加模式写入（默认覆盖）",
				Default:     false,
			},
		},
		Required: []string{"path", "content"},
	}
}

func (t *FileWriteTool) Execute(ctx context.Context, args map[string]interface{}) (Result, error) {
	path, ok := args["path"].(string)
	if !ok || path == "" {
		return Result{}, fmt.Errorf("parameter 'path' is required and must be a non-empty string")
	}
	content, ok := args["content"].(string)
	if !ok {
		return Result{}, fmt.Errorf("parameter 'content' is required and must be a string")
	}

	appendMode := false
	if v, ok := args["append"]; ok {
		if b, ok := v.(bool); ok {
			appendMode = b
		}
	}

	// 路径安全验证
	if err := t.validatePath(path); err != nil {
		return Result{}, err
	}

	// 检查 context 是否已取消
	select {
	case <-ctx.Done():
		return Result{}, ctx.Err()
	default:
	}

	// 确保父目录存在
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return Result{}, fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	// 写入文件
	flag := os.O_WRONLY | os.O_CREATE
	if appendMode {
		flag |= os.O_APPEND
	} else {
		flag |= os.O_TRUNC
	}

	f, err := os.OpenFile(path, flag, 0644)
	if err != nil {
		return Result{}, fmt.Errorf("failed to open file: %w", err)
	}
	defer f.Close()

	n, err := f.WriteString(content)
	if err != nil {
		return Result{}, fmt.Errorf("failed to write file: %w", err)
	}

	mode := "overwrite"
	if appendMode {
		mode = "append"
	}

	return Result{
		Output: fmt.Sprintf("successfully wrote %d bytes to %s (%s mode)", n, path, mode),
		Metadata: map[string]interface{}{
			"path":          path,
			"bytes_written": n,
			"mode":          mode,
		},
	}, nil
}

// validatePath 验证写入路径的安全性
func (t *FileWriteTool) validatePath(path string) error {
	// 检查原始路径中的穿越组件：仅拒绝 ".." 作为独立路径段的形式
	if containsDotDotSegment(path) {
		return fmt.Errorf("path traversal detected: %s", path)
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("invalid path: %w", err)
	}

	// 禁止写入敏感系统路径
	dangerous := []string{"/etc", "/usr", "/bin", "/sbin", "/boot", "/sys", "/proc"}
	for _, d := range dangerous {
		if strings.HasPrefix(absPath, d+string(os.PathSeparator)) || absPath == d {
			return fmt.Errorf("writing to system path is not allowed: %s", path)
		}
	}

	if len(t.AllowedDirs) > 0 {
		allowed := false
		for _, dir := range t.AllowedDirs {
			absDir, _ := filepath.Abs(dir)
			if strings.HasPrefix(absPath, absDir+string(os.PathSeparator)) || absPath == absDir {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("path not in allowed directories: %s", path)
		}
	}

	return nil
}

// ============================================================
// CodeRunTool - 代码执行工具（进程组隔离 + 输出截断）
// ============================================================

// CodeRunTool 在受限子进程中执行 Python 或 Go 代码片段。
// 安全措施：独立临时目录（执行后清理）+ 独立进程组 + 输出截断 + 环境隔离；
// 执行超时（墙钟）由注册表统一控制。
type CodeRunTool struct {
	// MaxOutputBytes 最大输出字节数（默认 1MB），超出部分会被截断
	MaxOutputBytes int
}

func (t *CodeRunTool) Name() string { return "code_run" }

func (t *CodeRunTool) Description() string {
	return "在隔离的子进程中执行 Python 或 Go 代码，返回标准输出和错误输出。" +
		"隔离措施：独立临时目录（执行后清理）、独立进程组、输出超过上限自动截断（默认 1MB）；" +
		"执行超时由工具注册表统一控制（默认 30 秒墙钟时间）。"
}

func (t *CodeRunTool) Parameters() JSONSchema {
	return JSONSchema{
		Type: "object",
		Properties: map[string]Property{
			"language": {
				Type:        "string",
				Description: "编程语言",
				Enum:        []string{"python", "go"},
			},
			"code": {
				Type:        "string",
				Description: "要执行的代码内容",
			},
		},
		Required: []string{"language", "code"},
	}
}

func (t *CodeRunTool) Execute(ctx context.Context, args map[string]interface{}) (Result, error) {
	language, ok := args["language"].(string)
	if !ok || language == "" {
		return Result{}, fmt.Errorf("parameter 'language' is required")
	}
	code, ok := args["code"].(string)
	if !ok || code == "" {
		return Result{}, fmt.Errorf("parameter 'code' is required")
	}

	maxOutput := t.MaxOutputBytes
	if maxOutput <= 0 {
		maxOutput = 1 << 20 // 1MB
	}

	// 创建隔离的临时工作目录
	workDir, err := os.MkdirTemp("", "agent_code_*")
	if err != nil {
		return Result{}, fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(workDir)

	var cmd *exec.Cmd
	var tmpFile string

	switch strings.ToLower(language) {
	case "python":
		tmpFile = filepath.Join(workDir, "main.py")
		if err := os.WriteFile(tmpFile, []byte(code), 0600); err != nil {
			return Result{}, fmt.Errorf("failed to write temp file: %w", err)
		}
		cmd = exec.CommandContext(ctx, "python3", "-u", tmpFile)

	case "go":
		tmpFile = filepath.Join(workDir, "main.go")
		if err := os.WriteFile(tmpFile, []byte(code), 0600); err != nil {
			return Result{}, fmt.Errorf("failed to write temp file: %w", err)
		}
		cmd = exec.CommandContext(ctx, "go", "run", tmpFile)

	default:
		return Result{}, fmt.Errorf("unsupported language: %s (supported: python, go)", language)
	}

	// 进程隔离：独立工作目录 + 受限环境变量 + 独立进程组
	cmd.Dir = workDir
	cmd.Env = codeRunEnv()
	cmd.SysProcAttr = resourceLimits()

	// 输出限制
	stdout := &limitedWriter{limit: maxOutput}
	stderr := &limitedWriter{limit: maxOutput}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err = cmd.Run()

	result := Result{
		Output: stdout.String(),
		Metadata: map[string]interface{}{
			"language":           language,
			"exit_ok":            err == nil,
			"output_limit_bytes": maxOutput,
		},
	}

	if err != nil {
		result.Error = stderr.String()
		if result.Error == "" {
			result.Error = err.Error()
		}
	} else if s := stderr.String(); s != "" {
		result.Metadata["stderr"] = s
	}

	return result, nil
}

// codeRunEnv 为代码执行构建受限环境变量。
func codeRunEnv() []string {
	return []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"HOME=" + os.TempDir(),
		"TMPDIR=" + os.TempDir(),
		"LANG=en_US.UTF-8",
		"PYTHONDONTWRITEBYTECODE=1", // Python 不生成 .pyc
		"PYTHONUNBUFFERED=1",        // Python 无缓冲输出
	}
}

// ============================================================
// ShellExecTool - Shell 命令执行工具（多层安全防护）
// ============================================================

// ShellExecTool 在受限环境中执行 shell 命令。
// 安全防护：命令分段解析 + 危险二进制检测 + 管道/链式命令检查 + 环境隔离。
type ShellExecTool struct {
	// BlockedBinaries 禁止执行的命令/二进制名称（匹配命令第一个 token）
	BlockedBinaries map[string]bool
	// BlockedPatterns 危险命令模式（正则）
	BlockedPatterns []*regexp.Regexp
}

func NewShellExecTool() *ShellExecTool {
	// 注意：rm 不在此处整体封禁——rm 属于"可争议"命令（rm file.txt 是合法操作），
	// 其破坏性形式（rm -rf / 等）交由安全策略层（internal/security + config/policy.yaml）裁决；
	// 工具层只兜底绝对危险的命令。
	blocked := map[string]bool{
		"mkfs":      true,
		"dd":        true,
		"shutdown":  true,
		"reboot":    true,
		"halt":      true,
		"poweroff":  true,
		"init":      true,
		"killall":   true,
		"pkill":     true,
		"iptables":  true,
		"ip6tables": true,
		"nft":       true,
		"fdisk":     true,
		"parted":    true,
		"sfdisk":    true,
		"wipefs":    true,
		"shred":     true,
		"swapoff":   true,
		"umount":    true,
	}

	// 只保留绝对危险的模式；rm -rf 等"可争议"命令由安全策略层拦截。
	patterns := []*regexp.Regexp{
		// Fork bomb
		regexp.MustCompile(`:\(\)\s*\{.*\|.*&\s*\}\s*;`),
		// 下载并执行（curl/wget piped to shell）
		regexp.MustCompile(`(?i)(curl|wget)\s+.*\|\s*(sh|bash|zsh|dash)`),
		// chmod 777 系统目录
		regexp.MustCompile(`(?i)chmod\s+(-R\s+)?(777|a\+rwx)\s+/`),
		// 格式化磁盘
		regexp.MustCompile(`(?i)mkfs\.`),
		// dd 写入设备
		regexp.MustCompile(`(?i)dd\s+.*of=/dev/`),
		// 覆盖 MBR
		regexp.MustCompile(`(?i)dd\s+.*of=/dev/[shv]d`),
		// 重定向覆盖 /etc/passwd 等
		regexp.MustCompile(`(?i)(>|>>)\s*/(etc|usr|bin|sbin)/`),
		// eval 执行 base64 编码的恶意代码
		regexp.MustCompile(`(?i)eval\s+.*base64`),
		// python/perl/ruby 反弹 shell
		regexp.MustCompile(`(?i)(python|perl|ruby|node)\s+.*socket\..*(connect|listen)`),
		// nc (netcat) 监听/连接（反弹 shell）
		regexp.MustCompile(`(?i)\bnc\s+.*-[elp]`),
		regexp.MustCompile(`(?i)\bncat\s+.*-[elp]`),
		regexp.MustCompile(`(?i)\bsocat\s+.*exec`),
	}

	return &ShellExecTool{
		BlockedBinaries: blocked,
		BlockedPatterns: patterns,
	}
}

func (t *ShellExecTool) Name() string { return "shell_exec" }

func (t *ShellExecTool) Description() string {
	return "在受限 shell 环境中执行命令，返回标准输出和错误输出。危险操作会被安全策略拦截。"
}

func (t *ShellExecTool) Parameters() JSONSchema {
	return JSONSchema{
		Type: "object",
		Properties: map[string]Property{
			"command": {
				Type:        "string",
				Description: "要执行的 shell 命令",
			},
			"working_dir": {
				Type:        "string",
				Description: "命令执行的工作目录（可选）",
			},
		},
		Required: []string{"command"},
	}
}

func (t *ShellExecTool) Execute(ctx context.Context, args map[string]interface{}) (Result, error) {
	command, ok := args["command"].(string)
	if !ok || command == "" {
		return Result{}, fmt.Errorf("parameter 'command' is required and must be a non-empty string")
	}

	// 多层安全检查
	if err := t.validateCommand(command); err != nil {
		return Result{}, err
	}

	cmd := exec.CommandContext(ctx, "sh", "-c", command)

	// 环境隔离：白名单过滤父进程环境变量，剔除敏感变量
	cmd.Env = sanitizeEnv(os.Environ())

	// 独立进程组：便于按进程组终止整个子进程树（与 code_run 一致）
	cmd.SysProcAttr = resourceLimits()

	// 设置工作目录
	if wd, ok := args["working_dir"].(string); ok && wd != "" {
		if _, err := os.Stat(wd); err != nil {
			return Result{}, fmt.Errorf("working directory does not exist: %s", wd)
		}
		cmd.Dir = wd
	}

	// 输出限制：防止内存耗尽
	const maxOutput = 1 << 20 // 1MB
	cmd.Stdout = &limitedWriter{limit: maxOutput}
	cmd.Stderr = &limitedWriter{limit: maxOutput}

	err := cmd.Run()

	stdout := cmd.Stdout.(*limitedWriter).String()
	stderr := cmd.Stderr.(*limitedWriter).String()

	result := Result{
		Output: stdout,
		Metadata: map[string]interface{}{
			"command": command,
			"exit_ok": err == nil,
		},
	}

	if err != nil {
		result.Error = stderr
		if result.Error == "" {
			result.Error = err.Error()
		}
	} else if stderr != "" {
		result.Metadata["stderr"] = stderr
	}

	return result, nil
}

// validateCommand 对命令进行多层安全验证。
func (t *ShellExecTool) validateCommand(command string) error {
	// 第 1 层：正则模式匹配（最高优先级）
	for _, re := range t.BlockedPatterns {
		if re.MatchString(command) {
			return fmt.Errorf("command blocked by security policy: matches dangerous pattern %q", re.String())
		}
	}

	// 第 2 层：分段解析命令，检查每个段的首个二进制
	segments := splitShellCommand(command)
	for _, seg := range segments {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		binary := extractBinary(seg)
		if binary == "" {
			continue
		}
		// 去除路径前缀，只检查二进制名（大小写不敏感）
		baseName := strings.ToLower(filepath.Base(binary))
		if t.BlockedBinaries[baseName] {
			return fmt.Errorf("command blocked: binary %q is not allowed", baseName)
		}
	}

	return nil
}

// splitShellCommand 按 shell 操作符分割命令。
// 支持: |, &&, ||, ;, `...`, $(...)
func splitShellCommand(cmd string) []string {
	var segments []string
	var current strings.Builder
	runes := []rune(cmd)
	i := 0
	for i < len(runes) {
		ch := runes[i]

		// 跳过引号内的内容
		if ch == '"' || ch == '\'' {
			quote := ch
			current.WriteRune(ch)
			i++
			for i < len(runes) && runes[i] != quote {
				current.WriteRune(runes[i])
				i++
			}
			if i < len(runes) {
				current.WriteRune(runes[i])
			}
			i++
			continue
		}

		// $(...) 子命令
		if ch == '$' && i+1 < len(runes) && runes[i+1] == '(' {
			depth := 1
			current.WriteRune(ch)
			current.WriteRune(runes[i+1])
			i += 2
			for i < len(runes) && depth > 0 {
				if runes[i] == '(' {
					depth++
				} else if runes[i] == ')' {
					depth--
				}
				current.WriteRune(runes[i])
				i++
			}
			continue
		}

		// `...` 子命令
		if ch == '`' {
			current.WriteRune(ch)
			i++
			for i < len(runes) && runes[i] != '`' {
				current.WriteRune(runes[i])
				i++
			}
			if i < len(runes) {
				current.WriteRune(runes[i])
			}
			i++
			continue
		}

		// 检查操作符
		if ch == '|' {
			segments = append(segments, current.String())
			current.Reset()
			i++
			if i < len(runes) && runes[i] == '|' {
				i++ // skip second |
			}
			continue
		}
		if ch == '&' && i+1 < len(runes) && runes[i+1] == '&' {
			segments = append(segments, current.String())
			current.Reset()
			i += 2
			continue
		}
		if ch == ';' {
			segments = append(segments, current.String())
			current.Reset()
			i++
			continue
		}

		current.WriteRune(ch)
		i++
	}
	if current.Len() > 0 {
		segments = append(segments, current.String())
	}
	return segments
}

// extractBinary 从命令段中提取第一个 token（二进制名称）。
func extractBinary(segment string) string {
	segment = strings.TrimSpace(segment)
	if segment == "" {
		return ""
	}

	// 跳过 env / nice / nohup / time 等前缀
	skipPrefixes := []string{"env", "nice", "nohup", "time", "sudo", "stdbuf", "unbuffer"}
	for {
		fields := strings.Fields(segment)
		if len(fields) == 0 {
			return ""
		}
		first := strings.ToLower(fields[0])
		skip := false
		for _, p := range skipPrefixes {
			if first == p {
				segment = strings.Join(fields[1:], " ")
				skip = true
				break
			}
		}
		if !skip {
			// 跳过 KEY=VALUE 形式的环境变量赋值（env 命令后常见）
			if strings.Contains(fields[0], "=") {
				segment = strings.Join(fields[1:], " ")
				continue
			}
			return fields[0]
		}
	}
}

// sanitizeEnv 按白名单过滤环境变量：仅保留 PATH/HOME 等必要变量，
// 剔除密钥等敏感变量，防止泄露给子进程。
func sanitizeEnv(env []string) []string {
	// 保留的安全环境变量
	keepVars := map[string]bool{
		"PATH":            true,
		"HOME":            true,
		"USER":            true,
		"LANG":            true,
		"LC_ALL":          true,
		"LC_CTYPE":        true,
		"TERM":            true,
		"TMPDIR":          true,
		"XDG_RUNTIME_DIR": true,
		"XDG_CONFIG_HOME": true,
		"XDG_DATA_HOME":   true,
		"XDG_CACHE_HOME":  true,
	}

	var result []string
	for _, e := range env {
		key := e
		if idx := strings.IndexByte(e, '='); idx >= 0 {
			key = e[:idx]
		}
		if keepVars[key] {
			result = append(result, e)
		}
	}

	// PATH 继承自父进程环境（经上面的白名单保留）；
	// 若父进程未设置 PATH，则补充安全的默认路径
	hasPATH := false
	for _, e := range result {
		if strings.HasPrefix(e, "PATH=") {
			hasPATH = true
			break
		}
	}
	if !hasPATH {
		result = append(result, "PATH=/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin")
	}

	return result
}

// limitedWriter 限制写入量的 Writer，防止内存耗尽。
type limitedWriter struct {
	buf       bytes.Buffer
	limit     int
	truncated bool // 是否已因超限截断（截断标记只追加一次）
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if w.truncated {
		// 已截断：丢弃后续内容，不再重复追加截断标记
		return len(p), nil
	}
	if w.buf.Len()+len(p) > w.limit {
		remaining := w.limit - w.buf.Len()
		if remaining > 0 {
			w.buf.Write(p[:remaining])
		}
		w.buf.WriteString("\n... [output truncated]")
		w.truncated = true
		return len(p), nil
	}
	return w.buf.Write(p)
}

func (w *limitedWriter) String() string {
	return w.buf.String()
}

// ============================================================
// RegisterBuiltinTools - 注册所有内置工具
// ============================================================

// RegisterBuiltinTools 将所有内置工具注册到给定的注册中心
func RegisterBuiltinTools(reg *Registry) {
	// 网页抓取 - 只读，30 秒超时
	reg.Register(NewWebFetchTool(), RiskReadOnly, 30*time.Second)

	// 文件读取 - 只读，15 秒超时
	reg.Register(&FileReadTool{}, RiskReadOnly, 15*time.Second)

	// 文件写入 - 高风险写操作，15 秒超时
	reg.Register(&FileWriteTool{}, RiskHighWrite, 15*time.Second)

	// 代码执行 - 危险操作，30 秒超时
	reg.Register(&CodeRunTool{}, RiskDangerous, 30*time.Second)

	// Shell 命令 - 危险操作，30 秒超时
	reg.Register(NewShellExecTool(), RiskDangerous, 30*time.Second)
}

// RegisterSearchTool 将搜索工具注册到注册中心（需要搜索后端依赖）。
func RegisterSearchTool(reg *Registry, searcher Searcher) {
	reg.Register(NewWebSearchTool(searcher), RiskReadOnly, 30*time.Second)
}
