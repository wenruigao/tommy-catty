package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/wenruigao/tommy-catty/config"
	"github.com/wenruigao/tommy-catty/internal/memstore"
	"gopkg.in/yaml.v3"
)

// DoctorConfig Doctor 检查所需的配置信息
type DoctorConfig struct {
	ConfigPath     string                       // config.yaml 路径
	PolicyPath     string                       // policy.yaml 路径
	SkillStorePath string                       // skills.json 路径
	WorkDir        string                       // 工作目录
	Providers      map[string]ProviderCheckInfo // LLM 供应商信息

	// 记忆存储后端信息（memstore 连通性检查）
	MemoryType string // file / sqlite / remote
	MemoryPath string // file/sqlite 后端路径
	MemoryURL  string // remote 后端地址
}

// ProviderCheckInfo 供应商检查信息
type ProviderCheckInfo struct {
	BaseURL string
	APIKey  string
	Model   string
}

// RegisterAllChecks 注册所有内置检查项
func RegisterAllChecks(d *Doctor, cfg DoctorConfig) {
	d.AddCheck(checkConfigFile(cfg))
	d.AddCheck(checkLLMProviders(cfg))
	d.AddCheck(checkSecurityPolicy(cfg))
	d.AddCheck(checkToolAvailability())
	d.AddCheck(checkSkillStore(cfg))
	d.AddCheck(checkWorkDirectory(cfg))
	d.AddCheck(checkMemoryStorage(cfg))
	d.AddCheck(checkNetwork())
	d.AddCheck(checkResources())
}

// checkMemoryStorage 检查记忆存储后端（remote 验证连通性，本地后端验证目录可写）
func checkMemoryStorage(cfg DoctorConfig) Check {
	return Check{
		Name:       "Memory storage",
		Category:   "memory",
		Severity:   SeverityWarning,
		Suggestion: "检查 memory.storage 配置（type/path/url），remote 后端需先启动 memstore 服务",
		Run: func(ctx context.Context) (CheckStatus, string) {
			switch cfg.MemoryType {
			case "remote":
				if cfg.MemoryURL == "" {
					return StatusError, "remote 后端未配置 memory.storage.url"
				}
				store := memstore.NewRemoteStore(cfg.MemoryURL, "", 3*time.Second)
				if err := store.HealthCheck(ctx); err != nil {
					return StatusError, fmt.Sprintf("远程记忆服务不可达: %v", err)
				}
				return StatusOK, cfg.MemoryURL
			case "sqlite":
				path := cfg.MemoryPath
				if path == "" {
					path = "data/memory.db"
				}
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					return StatusError, fmt.Sprintf("数据库目录不可创建: %v", err)
				}
				return StatusOK, path
			default: // file
				dir := cfg.MemoryPath
				if dir == "" {
					dir = "data/memories"
				}
				if err := os.MkdirAll(dir, 0o755); err != nil {
					return StatusError, fmt.Sprintf("记忆目录不可创建: %v", err)
				}
				return StatusOK, dir
			}
		},
	}
}

// checkConfigFile 检查配置文件完整性
func checkConfigFile(cfg DoctorConfig) Check {
	return Check{
		Name:       "Config file",
		Category:   "config",
		Severity:   SeverityCritical,
		Suggestion: "Run with default config or recreate config.yaml",
		Run: func(ctx context.Context) (CheckStatus, string) {
			data, err := os.ReadFile(cfg.ConfigPath)
			if err != nil {
				return StatusError, fmt.Sprintf("%s not found or unreadable", cfg.ConfigPath)
			}
			var c config.Config
			if err := yaml.Unmarshal(data, &c); err != nil {
				return StatusError, fmt.Sprintf("YAML parse error: %v", err)
			}
			providerCount := len(c.LLM.Providers)
			if providerCount == 0 {
				return StatusWarning, "config loaded but no providers configured"
			}
			return StatusOK, fmt.Sprintf("%s loaded (%d providers)", filepath.Base(cfg.ConfigPath), providerCount)
		},
		Fix: func(ctx context.Context) (bool, string) {
			// 如果配置文件不存在，创建默认配置
			if _, err := os.Stat(cfg.ConfigPath); os.IsNotExist(err) {
				dir := filepath.Dir(cfg.ConfigPath)
				os.MkdirAll(dir, 0755)
				defaultCfg := `llm:
  default_provider: "deepseek"
  fallback_provider: "qwen"
  providers:
    deepseek:
      base_url: "https://api.deepseek.com/chat/completions"
      api_key: "${DEEPSEEK_API_KEY}"
      model: "deepseek-chat"
      max_tokens: 65536
engine:
  max_iterations: 20
policy_file: "config/policy.yaml"
skill_store_path: "data/skills.json"
work_dir: "."
`
				if err := os.WriteFile(cfg.ConfigPath, []byte(defaultCfg), 0644); err != nil {
					return false, fmt.Sprintf("failed to create default config: %v", err)
				}
				return true, "created default config.yaml"
			}
			return false, "config exists but has errors, manual fix needed"
		},
	}
}

// checkLLMProviders 检查 LLM 供应商连通性
func checkLLMProviders(cfg DoctorConfig) Check {
	return Check{
		Name:       "LLM providers",
		Category:   "llm",
		Severity:   SeverityCritical,
		Suggestion: "Set API key environment variables or check base_url configuration",
		Run: func(ctx context.Context) (CheckStatus, string) {
			if len(cfg.Providers) == 0 {
				return StatusError, "no providers configured"
			}

			var results []string
			hasError := false
			hasWarning := false

			for name, info := range cfg.Providers {
				// 检查 API Key
				if info.APIKey == "" {
					results = append(results, fmt.Sprintf("%s: API key missing", name))
					hasWarning = true
					continue
				}

				// 检查连通性（发送最小请求）
				status := testProviderConnectivity(ctx, info)
				if status == StatusOK {
					results = append(results, fmt.Sprintf("%s: reachable, model=%s", name, info.Model))
				} else {
					results = append(results, fmt.Sprintf("%s: unreachable", name))
					hasError = true
				}
			}

			summary := strings.Join(results, "; ")
			if hasError {
				return StatusError, summary
			}
			if hasWarning {
				return StatusWarning, summary
			}
			return StatusOK, summary
		},
	}
}

// testProviderConnectivity 测试供应商 API 连通性
func testProviderConnectivity(ctx context.Context, info ProviderCheckInfo) CheckStatus {
	client := &http.Client{Timeout: 5 * time.Second}

	// 发送一个最小的请求来验证连通性和 API Key
	reqBody := fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"hi"}],"max_tokens":1}`, info.Model)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, info.BaseURL, strings.NewReader(reqBody))
	if err != nil {
		return StatusError
	}
	req.Header.Set("Content-Type", "application/json")
	if info.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+info.APIKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		return StatusError
	}
	defer resp.Body.Close()

	// 200 = 完全正常; 401/403 = Key 无效但端点可达; 其他 = 端点异常
	switch resp.StatusCode {
	case http.StatusOK:
		return StatusOK
	case http.StatusUnauthorized, http.StatusForbidden:
		return StatusWarning // 端点可达但 Key 无效
	default:
		return StatusError
	}
}

// checkSecurityPolicy 检查安全策略文件
func checkSecurityPolicy(cfg DoctorConfig) Check {
	return Check{
		Name:       "Security policies",
		Category:   "security",
		Severity:   SeverityWarning,
		Suggestion: "Recreate default policy.yaml or fix YAML syntax",
		Run: func(ctx context.Context) (CheckStatus, string) {
			data, err := os.ReadFile(cfg.PolicyPath)
			if err != nil {
				return StatusWarning, fmt.Sprintf("%s not found (using built-in defaults)", cfg.PolicyPath)
			}
			var policies map[string]interface{}
			if err := yaml.Unmarshal(data, &policies); err != nil {
				return StatusError, fmt.Sprintf("YAML parse error: %v", err)
			}
			// 尝试统计策略数量
			count := 0
			if ps, ok := policies["policies"]; ok {
				if list, ok := ps.([]interface{}); ok {
					count = len(list)
				}
			}
			return StatusOK, fmt.Sprintf("%d policies loaded from %s", count, filepath.Base(cfg.PolicyPath))
		},
		Fix: func(ctx context.Context) (bool, string) {
			if _, err := os.Stat(cfg.PolicyPath); os.IsNotExist(err) {
				dir := filepath.Dir(cfg.PolicyPath)
				os.MkdirAll(dir, 0755)
				defaultPolicy := `policies:
  - id: block-destructive
    name: "Block destructive operations"
    priority: 1
    enabled: true
    when:
      tool_names: [shell_exec, code_run]
      pattern: "(?i)(rm\\s+-rf|drop\\s+table)"
    then:
      effect: deny
      message: "Destructive operation blocked"
`
				if err := os.WriteFile(cfg.PolicyPath, []byte(defaultPolicy), 0644); err != nil {
					return false, fmt.Sprintf("failed to create: %v", err)
				}
				return true, "created default policy.yaml"
			}
			return false, "file exists but has syntax errors"
		},
	}
}

// checkToolAvailability 检查工具可用性
func checkToolAvailability() Check {
	return Check{
		Name:       "Tool availability",
		Category:   "tools",
		Severity:   SeverityWarning,
		Suggestion: "Install missing external dependencies (python3, etc.)",
		Run: func(ctx context.Context) (CheckStatus, string) {
			var available []string
			var missing []string

			// 检查外部工具依赖
			externalTools := map[string]string{
				"python3": "code_run tool (Python execution)",
				"sh":      "shell_exec tool",
				"curl":    "web_fetch tool (fallback)",
			}

			for tool, desc := range externalTools {
				if _, err := exec.LookPath(tool); err == nil {
					available = append(available, tool)
				} else {
					missing = append(missing, fmt.Sprintf("%s (%s)", tool, desc))
				}
			}

			msg := fmt.Sprintf("%d built-in tools, external: %s available",
				6, strings.Join(available, ", "))
			if len(missing) > 0 {
				msg += fmt.Sprintf("; missing: %s", strings.Join(missing, ", "))
				return StatusWarning, msg
			}
			return StatusOK, msg
		},
	}
}

// checkSkillStore 检查 Skill 存储完整性
func checkSkillStore(cfg DoctorConfig) Check {
	return Check{
		Name:       "Skill store",
		Category:   "skill",
		Severity:   SeverityInfo,
		Suggestion: "Delete corrupted skills.json to reset",
		Run: func(ctx context.Context) (CheckStatus, string) {
			data, err := os.ReadFile(cfg.SkillStorePath)
			if err != nil {
				if os.IsNotExist(err) {
					return StatusWarning, fmt.Sprintf("%s not found (will be created on first use)", cfg.SkillStorePath)
				}
				return StatusError, fmt.Sprintf("unreadable: %v", err)
			}
			// 验证 JSON 格式
			var skills []interface{}
			if err := json.Unmarshal(data, &skills); err != nil {
				// 也尝试解析为 map
				var skillMap map[string]interface{}
				if err2 := json.Unmarshal(data, &skillMap); err2 != nil {
					return StatusError, fmt.Sprintf("corrupted JSON: %v", err)
				}
				return StatusOK, fmt.Sprintf("%s (%d skills)", filepath.Base(cfg.SkillStorePath), len(skillMap))
			}
			return StatusOK, fmt.Sprintf("%s (%d skills)", filepath.Base(cfg.SkillStorePath), len(skills))
		},
		Fix: func(ctx context.Context) (bool, string) {
			// 确保目录存在
			dir := filepath.Dir(cfg.SkillStorePath)
			if err := os.MkdirAll(dir, 0755); err != nil {
				return false, fmt.Sprintf("cannot create directory: %v", err)
			}
			// 如果文件不存在或损坏，重建
			if _, err := os.Stat(cfg.SkillStorePath); os.IsNotExist(err) {
				if err := os.WriteFile(cfg.SkillStorePath, []byte("[]"), 0644); err != nil {
					return false, fmt.Sprintf("cannot create file: %v", err)
				}
				return true, "created empty skill store"
			}
			// 文件存在但损坏，备份后重建
			data, err := os.ReadFile(cfg.SkillStorePath)
			if err == nil {
				var check []interface{}
				if json.Unmarshal(data, &check) != nil {
					backup := cfg.SkillStorePath + ".bak"
					os.WriteFile(backup, data, 0644)
					os.WriteFile(cfg.SkillStorePath, []byte("[]"), 0644)
					return true, fmt.Sprintf("reset corrupted file (backup: %s)", filepath.Base(backup))
				}
			}
			return false, "file exists and is valid"
		},
	}
}

// checkWorkDirectory 检查工作目录
func checkWorkDirectory(cfg DoctorConfig) Check {
	return Check{
		Name:       "Work directory",
		Category:   "filesystem",
		Severity:   SeverityWarning,
		Suggestion: "Create the work directory or update work_dir in config",
		Run: func(ctx context.Context) (CheckStatus, string) {
			info, err := os.Stat(cfg.WorkDir)
			if err != nil {
				return StatusError, fmt.Sprintf("%s does not exist", cfg.WorkDir)
			}
			if !info.IsDir() {
				return StatusError, fmt.Sprintf("%s is not a directory", cfg.WorkDir)
			}

			// 检查可写性
			testFile := filepath.Join(cfg.WorkDir, ".doctor_write_test")
			if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
				return StatusError, fmt.Sprintf("%s is not writable", cfg.WorkDir)
			}
			os.Remove(testFile)

			// 检查磁盘空间
			var stat syscall.Statfs_t
			if err := syscall.Statfs(cfg.WorkDir, &stat); err == nil {
				freeGB := float64(stat.Bavail*uint64(stat.Bsize)) / (1024 * 1024 * 1024)
				if freeGB < 1.0 {
					return StatusWarning, fmt.Sprintf("%s writable, but only %.1fGB free", cfg.WorkDir, freeGB)
				}
				return StatusOK, fmt.Sprintf("%s writable, %.0fGB free", cfg.WorkDir, freeGB)
			}

			return StatusOK, fmt.Sprintf("%s writable", cfg.WorkDir)
		},
		Fix: func(ctx context.Context) (bool, string) {
			if _, err := os.Stat(cfg.WorkDir); os.IsNotExist(err) {
				if err := os.MkdirAll(cfg.WorkDir, 0755); err != nil {
					return false, fmt.Sprintf("cannot create: %v", err)
				}
				return true, fmt.Sprintf("created directory %s", cfg.WorkDir)
			}
			return false, "directory exists but has permission issues"
		},
	}
}

// checkNetwork 检查网络连通性
func checkNetwork() Check {
	return Check{
		Name:       "Network",
		Category:   "network",
		Severity:   SeverityCritical,
		Suggestion: "Check network connection, DNS settings, or proxy configuration",
		Run: func(ctx context.Context) (CheckStatus, string) {
			start := time.Now()

			// DNS 解析测试
			_, err := net.LookupHost("api.deepseek.com")
			if err != nil {
				return StatusError, fmt.Sprintf("DNS resolution failed: %v", err)
			}

			// HTTPS 连接测试
			client := &http.Client{Timeout: 5 * time.Second}
			req, _ := http.NewRequestWithContext(ctx, http.MethodHead, "https://api.deepseek.com", nil)
			resp, err := client.Do(req)
			if err != nil {
				return StatusError, fmt.Sprintf("HTTPS connection failed: %v", err)
			}
			resp.Body.Close()

			latency := time.Since(start)
			return StatusOK, fmt.Sprintf("DNS + HTTPS OK (%s)", latency.Round(time.Millisecond))
		},
	}
}

// checkResources 检查系统资源
func checkResources() Check {
	return Check{
		Name:       "Resources",
		Category:   "system",
		Severity:   SeverityInfo,
		Suggestion: "Close other applications to free memory",
		Run: func(ctx context.Context) (CheckStatus, string) {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			memMB := m.Alloc / 1024 / 1024
			goroutines := runtime.NumGoroutine()

			msg := fmt.Sprintf("mem=%dMB, goroutines=%d, os=%s/%s",
				memMB, goroutines, runtime.GOOS, runtime.GOARCH)

			if memMB > 512 {
				return StatusWarning, msg + " (high memory usage)"
			}
			return StatusOK, msg
		},
	}
}
