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
	"strings"
	"time"
)

// ============================================================
// WebSearchTool - 网络搜索工具
// ============================================================

// WebSearchTool 调用搜索 API 返回搜索结果。
// 当前为占位实现，后续可接入真实搜索服务。
type WebSearchTool struct{}

func (t *WebSearchTool) Name() string { return "web_search" }

func (t *WebSearchTool) Description() string {
	return "搜索互联网获取相关信息，返回搜索结果摘要列表"
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

	// 占位实现：返回格式化的模拟搜索结果
	// TODO: 接入真实搜索 API（如 SerpAPI、Bing Search 等）
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("搜索结果 (关键词: %q, 最多 %d 条):\n\n", query, maxResults))
	for i := 1; i <= maxResults; i++ {
		sb.WriteString(fmt.Sprintf("%d. [占位结果] 关于 %q 的搜索结果 #%d\n", i, query, i))
		sb.WriteString(fmt.Sprintf("   摘要: 这是关于 %q 的第 %d 条模拟搜索结果。\n\n", query, i))
	}

	return Result{
		Output: sb.String(),
		Metadata: map[string]interface{}{
			"query":       query,
			"result_count": maxResults,
			"source":      "placeholder",
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
			"url":           urlStr,
			"status_code":   resp.StatusCode,
			"content_type":  resp.Header.Get("Content-Type"),
			"truncated":     truncated,
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

// validatePath 验证文件路径的安全性，防止路径穿越攻击
func (t *FileReadTool) validatePath(path string) error {
	// 解析为绝对路径
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("invalid path: %w", err)
	}

	// 检查路径穿越
	cleaned := filepath.Clean(absPath)
	if cleaned != absPath {
		return fmt.Errorf("path traversal detected: %s", path)
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
			"path":       path,
			"bytes_written": n,
			"mode":       mode,
		},
	}, nil
}

// validatePath 验证写入路径的安全性
func (t *FileWriteTool) validatePath(path string) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("invalid path: %w", err)
	}

	cleaned := filepath.Clean(absPath)
	if cleaned != absPath {
		return fmt.Errorf("path traversal detected: %s", path)
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
// CodeRunTool - 代码执行工具
// ============================================================

// CodeRunTool 在子进程中执行 Python 或 Go 代码片段
type CodeRunTool struct{}

func (t *CodeRunTool) Name() string { return "code_run" }

func (t *CodeRunTool) Description() string {
	return "在沙箱子进程中执行 Python 或 Go 代码，返回标准输出和错误输出"
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

	var cmd *exec.Cmd
	var tmpFile string

	switch strings.ToLower(language) {
	case "python":
		// 写入临时文件后执行
		tmpFile = filepath.Join(os.TempDir(), fmt.Sprintf("agent_code_%d.py", time.Now().UnixNano()))
		if err := os.WriteFile(tmpFile, []byte(code), 0644); err != nil {
			return Result{}, fmt.Errorf("failed to write temp file: %w", err)
		}
		defer os.Remove(tmpFile)
		cmd = exec.CommandContext(ctx, "python3", tmpFile)

	case "go":
		tmpFile = filepath.Join(os.TempDir(), fmt.Sprintf("agent_code_%d.go", time.Now().UnixNano()))
		if err := os.WriteFile(tmpFile, []byte(code), 0644); err != nil {
			return Result{}, fmt.Errorf("failed to write temp file: %w", err)
		}
		defer os.Remove(tmpFile)
		cmd = exec.CommandContext(ctx, "go", "run", tmpFile)

	default:
		return Result{}, fmt.Errorf("unsupported language: %s (supported: python, go)", language)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	result := Result{
		Output: stdout.String(),
		Metadata: map[string]interface{}{
			"language": language,
			"exit_ok":  err == nil,
		},
	}

	if err != nil {
		result.Error = stderr.String()
		if result.Error == "" {
			result.Error = err.Error()
		}
	} else if stderr.Len() > 0 {
		// 即使成功也可能有 stderr 警告信息
		result.Metadata["stderr"] = stderr.String()
	}

	return result, nil
}

// ============================================================
// ShellExecTool - Shell 命令执行工具
// ============================================================

// ShellExecTool 在受限环境中执行 shell 命令
type ShellExecTool struct {
	// BlockedCommands 禁止执行的命令前缀列表
	BlockedCommands []string
}

func NewShellExecTool() *ShellExecTool {
	return &ShellExecTool{
		BlockedCommands: []string{
			"rm -rf /",
			"mkfs",
			"dd if=",
			":(){ :|:& };:",  // fork bomb
			"shutdown",
			"reboot",
			"halt",
		},
	}
}

func (t *ShellExecTool) Name() string { return "shell_exec" }

func (t *ShellExecTool) Description() string {
	return "在受限 shell 环境中执行命令，返回标准输出和错误输出"
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

	// 检查是否包含被禁止的命令
	if err := t.checkBlocked(command); err != nil {
		return Result{}, err
	}

	cmd := exec.CommandContext(ctx, "sh", "-c", command)

	// 设置工作目录
	if wd, ok := args["working_dir"].(string); ok && wd != "" {
		if _, err := os.Stat(wd); err != nil {
			return Result{}, fmt.Errorf("working directory does not exist: %s", wd)
		}
		cmd.Dir = wd
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	result := Result{
		Output: stdout.String(),
		Metadata: map[string]interface{}{
			"command": command,
			"exit_ok": err == nil,
		},
	}

	if err != nil {
		result.Error = stderr.String()
		if result.Error == "" {
			result.Error = err.Error()
		}
	} else if stderr.Len() > 0 {
		result.Metadata["stderr"] = stderr.String()
	}

	return result, nil
}

// checkBlocked 检查命令是否在黑名单中
func (t *ShellExecTool) checkBlocked(command string) error {
	lower := strings.ToLower(strings.TrimSpace(command))
	for _, blocked := range t.BlockedCommands {
		if strings.Contains(lower, strings.ToLower(blocked)) {
			return fmt.Errorf("command is blocked for safety: contains %q", blocked)
		}
	}
	return nil
}

// ============================================================
// RegisterBuiltinTools - 注册所有内置工具
// ============================================================

// RegisterBuiltinTools 将所有内置工具注册到给定的注册中心
func RegisterBuiltinTools(reg *Registry) {
	// 网络搜索 - 只读，30 秒超时
	reg.Register(&WebSearchTool{}, RiskReadOnly, 30*time.Second)

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
