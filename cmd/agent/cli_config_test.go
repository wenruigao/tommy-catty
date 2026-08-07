// cli_config_test.go 验证 /config 命令执行器的端到端行为
// （临时主配置 + 覆盖层持久化 + 内存同步 + 脱敏）。
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tommy-cat/agent/config"
)

// newTestConfigManager 在临时目录创建主配置并构建执行器。
func newTestConfigManager(t *testing.T) (*configManager, string) {
	t.Helper()
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "config.yaml")
	content := `
llm:
  default_provider: "mimo"
  providers:
    mimo:
      base_url: "http://localhost:1/v1/chat/completions"
      api_key: "sk-main-key-123456"
      model: "m1"
    qwen:
      base_url: "http://localhost:2/v1/chat/completions"
      api_key: "${DASHSCOPE_API_KEY}"
      model: "qwen-max"
engine:
  max_iterations: 10
`
	if err := os.WriteFile(mainPath, []byte(content), 0o644); err != nil {
		t.Fatalf("写入主配置失败: %v", err)
	}
	cfg, err := config.LoadWithOverlay(mainPath)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	return newConfigManager(mainPath, cfg, nil), dir
}

// TestConfigManager_SetPersistsAndUpdatesMemory 验证 set 持久化并同步内存。
func TestConfigManager_SetPersistsAndUpdatesMemory(t *testing.T) {
	mgr, dir := newTestConfigManager(t)

	msg, err := mgr.Set("engine.max_iterations", "5")
	if err != nil {
		t.Fatalf("set 失败: %v", err)
	}
	if !strings.Contains(msg, "重启后生效") {
		t.Errorf("应提示重启后生效，得到: %s", msg)
	}
	if mgr.cfg.Engine.MaxIterations != 5 {
		t.Errorf("内存配置应更新为 5，得到 %d", mgr.cfg.Engine.MaxIterations)
	}

	data, err := os.ReadFile(filepath.Join(dir, config.OverlayFileName))
	if err != nil {
		t.Fatalf("覆盖层文件应已创建: %v", err)
	}
	if !strings.Contains(string(data), "max_iterations: 5") {
		t.Errorf("覆盖层应包含 max_iterations: 5，得到:\n%s", data)
	}

	// 重新加载后覆盖层仍生效
	reloaded, err := config.LoadWithOverlay(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatalf("重载失败: %v", err)
	}
	if reloaded.Engine.MaxIterations != 5 {
		t.Errorf("重载后 max_iterations 应为 5，得到 %d", reloaded.Engine.MaxIterations)
	}
}

// TestConfigManager_SetValidation 验证类型与语义校验。
func TestConfigManager_SetValidation(t *testing.T) {
	mgr, _ := newTestConfigManager(t)

	if _, err := mgr.Set("engine.max_iterations", "abc"); err == nil {
		t.Error("非整数值应报错")
	}
	if _, err := mgr.Set("llm.default_provider", "not-exist"); err == nil {
		t.Error("不存在的供应商应报错")
	}
	if _, err := mgr.Set("search.default_engine", "google"); err == nil {
		t.Error("超出枚举应报错")
	}
	if _, err := mgr.Set("no.such.key", "v"); err == nil {
		t.Error("未知键应报错")
	} else if !strings.Contains(err.Error(), "未知配置键") {
		t.Errorf("错误应提示未知配置键: %v", err)
	}
}

// TestConfigManager_UnsetRestores 验证 unset 移除覆盖并恢复主配置值。
func TestConfigManager_UnsetRestores(t *testing.T) {
	mgr, dir := newTestConfigManager(t)

	if _, err := mgr.Set("engine.max_iterations", "5"); err != nil {
		t.Fatalf("set 失败: %v", err)
	}
	if _, err := mgr.Unset("engine.max_iterations"); err != nil {
		t.Fatalf("unset 失败: %v", err)
	}
	if mgr.cfg.Engine.MaxIterations != 10 {
		t.Errorf("unset 后应恢复主配置值 10，得到 %d", mgr.cfg.Engine.MaxIterations)
	}
	if _, err := os.Stat(filepath.Join(dir, config.OverlayFileName)); !os.IsNotExist(err) {
		t.Errorf("覆盖层清空后文件应被删除: %v", err)
	}

	// 对无覆盖的键 unset 报错
	if _, err := mgr.Unset("engine.max_iterations"); err == nil {
		t.Error("无覆盖键 unset 应报错")
	}
}

// TestConfigManager_Use 验证快捷切换默认供应商。
func TestConfigManager_Use(t *testing.T) {
	mgr, _ := newTestConfigManager(t)

	if _, err := mgr.Set("llm.default_provider", "qwen"); err != nil {
		t.Fatalf("切换失败: %v", err)
	}
	if mgr.cfg.LLM.DefaultProvider != "qwen" {
		t.Errorf("默认供应商应为 qwen，得到 %q", mgr.cfg.LLM.DefaultProvider)
	}
}

// TestConfigManager_SecretMasked 验证密钥在 List/Get 中脱敏。
func TestConfigManager_SecretMasked(t *testing.T) {
	mgr, _ := newTestConfigManager(t)

	list := mgr.List()
	if strings.Contains(list, "sk-main-key-123456") {
		t.Error("List 不应展示明文密钥")
	}
	if !strings.Contains(list, "sk-***56") {
		t.Errorf("List 应展示脱敏值 sk-***56:\n%s", list)
	}
	// ${ENV} 引用原样展示（本身非秘密）
	if !strings.Contains(list, "${DASHSCOPE_API_KEY}") {
		t.Errorf("List 应原样展示 ${ENV} 引用:\n%s", list)
	}

	got, err := mgr.Get("llm.providers.mimo.api_key")
	if err != nil {
		t.Fatalf("get 失败: %v", err)
	}
	if strings.Contains(got, "sk-main-key-123456") {
		t.Error("Get 不应展示明文密钥")
	}
}

// TestConfigManager_SetPlaintextSecretWarns 明文写入秘密键时给出警告。
func TestConfigManager_SetPlaintextSecretWarns(t *testing.T) {
	mgr, _ := newTestConfigManager(t)

	msg, err := mgr.Set("llm.providers.mimo.api_key", "sk-new-plaintext-987654")
	if err != nil {
		t.Fatalf("set 失败: %v", err)
	}
	if !strings.Contains(msg, "明文") {
		t.Errorf("应提示明文保存警告: %s", msg)
	}
	if strings.Contains(msg, "sk-new-plaintext-987654") {
		t.Error("回执不应回显明文秘密值")
	}

	// ${ENV} 引用不警告
	msg, err = mgr.Set("search.tavily_api_key", "${TAVILY_API_KEY}")
	if err != nil {
		t.Fatalf("set 失败: %v", err)
	}
	if strings.Contains(msg, "明文") {
		t.Errorf("${ENV} 引用不应触发明文警告: %s", msg)
	}
}

// TestConfigManager_ListMarksSource 验证来源标记。
func TestConfigManager_ListMarksSource(t *testing.T) {
	mgr, _ := newTestConfigManager(t)

	if _, err := mgr.Set("engine.max_iterations", "7"); err != nil {
		t.Fatalf("set 失败: %v", err)
	}
	list := mgr.List()
	for _, line := range strings.Split(list, "\n") {
		if strings.Contains(line, "engine.max_iterations") {
			if !strings.Contains(line, "[local]") {
				t.Errorf("已覆盖键应标记 [local]: %s", line)
			}
			return
		}
	}
	t.Error("List 应包含 engine.max_iterations 行")
}
