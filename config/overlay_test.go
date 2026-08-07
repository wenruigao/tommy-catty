// overlay_test.go 验证覆盖层合并优先级与 OverlayStore 读写。
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTempConfig(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("写入临时配置失败: %v", err)
	}
	return p
}

const mainYAML = `
llm:
  default_provider: "mimo"
  providers:
    mimo:
      base_url: "http://localhost:1/v1/chat/completions"
      api_key: "k-main"
      model: "m1"
engine:
  max_iterations: 10
search:
  default_engine: "duckduckgo"
`

// TestLoadWithOverlay_Precedence 验证合并优先级：overlay > 主配置 > 默认值；
// 且嵌套 map 按键合并（主配置独有键保留）。
func TestLoadWithOverlay_Precedence(t *testing.T) {
	dir := t.TempDir()
	mainPath := writeTempConfig(t, dir, "config.yaml", mainYAML)
	writeTempConfig(t, dir, OverlayFileName, `
llm:
  default_provider: "mimo"
  providers:
    mimo:
      model: "m2-override"
engine:
  max_iterations: 30
`)

	cfg, err := LoadWithOverlay(mainPath)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if got := cfg.LLM.Providers["mimo"].Model; got != "m2-override" {
		t.Errorf("model 应被覆盖层替换，得到 %q", got)
	}
	if cfg.Engine.MaxIterations != 30 {
		t.Errorf("max_iterations 应为 30（覆盖层），得到 %d", cfg.Engine.MaxIterations)
	}
	// 主配置独有键保留
	if got := cfg.LLM.Providers["mimo"].BaseURL; got != "http://localhost:1/v1/chat/completions" {
		t.Errorf("base_url 应保留主配置值，得到 %q", got)
	}
	if got := cfg.LLM.Providers["mimo"].APIKey; got != "k-main" {
		t.Errorf("api_key 应保留主配置值，得到 %q", got)
	}
	if got := cfg.Search.DefaultEngine; got != "duckduckgo" {
		t.Errorf("search.default_engine 应保留，得到 %q", got)
	}
}

// TestLoadWithOverlay_NoOverlayFile 无覆盖层时行为与 Load 一致。
func TestLoadWithOverlay_NoOverlayFile(t *testing.T) {
	dir := t.TempDir()
	mainPath := writeTempConfig(t, dir, "config.yaml", mainYAML)

	cfg, err := LoadWithOverlay(mainPath)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if got := cfg.LLM.Providers["mimo"].Model; got != "m1" {
		t.Errorf("model 应为主配置值 m1，得到 %q", got)
	}
	if cfg.Engine.MaxIterations != 10 {
		t.Errorf("max_iterations 应为 10，得到 %d", cfg.Engine.MaxIterations)
	}
}

// TestLoadWithOverlay_BrokenOverlay 覆盖层解析失败时报错（不静默忽略）。
func TestLoadWithOverlay_BrokenOverlay(t *testing.T) {
	dir := t.TempDir()
	mainPath := writeTempConfig(t, dir, "config.yaml", mainYAML)
	writeTempConfig(t, dir, OverlayFileName, "llm: [broken")

	if _, err := LoadWithOverlay(mainPath); err == nil {
		t.Fatal("覆盖层解析失败应返回错误")
	}
}

// TestLoadWithOverlay_EnvResolution 合并后仍执行环境变量解析。
func TestLoadWithOverlay_EnvResolution(t *testing.T) {
	dir := t.TempDir()
	main := strings.Replace(mainYAML, `api_key: "k-main"`, `api_key: "${TEST_OVERLAY_ENV_KEY}"`, 1)
	mainPath := writeTempConfig(t, dir, "config.yaml", main)
	writeTempConfig(t, dir, OverlayFileName, "engine:\n  max_iterations: 12\n")

	t.Setenv("TEST_OVERLAY_ENV_KEY", "env-secret-123")
	cfg, err := LoadWithOverlay(mainPath)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if got := cfg.LLM.Providers["mimo"].APIKey; got != "env-secret-123" {
		t.Errorf("api_key 应解析环境变量，得到 %q", got)
	}
}

// TestOverlayStore_RoundTrip 验证 Set/Save/重载/Get/Has。
func TestOverlayStore_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), OverlayFileName)
	store, err := NewOverlayStore(path)
	if err != nil {
		t.Fatalf("创建存储失败: %v", err)
	}
	store.Set("llm.default_provider", "qwen")
	store.Set("llm.providers.qwen.model", "qwen-max")
	store.Set("engine.max_iterations", 42)
	if err := store.Save(); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	reloaded, err := NewOverlayStore(path)
	if err != nil {
		t.Fatalf("重载失败: %v", err)
	}
	if !reloaded.Has("llm.default_provider") {
		t.Error("应包含 llm.default_provider")
	}
	if v, ok := reloaded.Get("llm.providers.qwen.model"); !ok || v != "qwen-max" {
		t.Errorf("动态键值应为 qwen-max，得到 %v (ok=%v)", v, ok)
	}
	if v, ok := reloaded.Get("engine.max_iterations"); !ok || v != 42 {
		t.Errorf("整数值应为 42，得到 %v (ok=%v)", v, ok)
	}
}

// TestOverlayStore_UnsetPrunesAndRemovesFile 验证 unset 清理空父级，
// 且覆盖层清空后 Save 删除文件。
func TestOverlayStore_UnsetPrunesAndRemovesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), OverlayFileName)
	store, _ := NewOverlayStore(path)
	store.Set("llm.providers.qwen.model", "x")
	if err := store.Save(); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	if !store.Unset("llm.providers.qwen.model") {
		t.Fatal("Unset 应返回 true")
	}
	if store.Unset("llm.providers.qwen.model") {
		t.Error("重复 Unset 应返回 false")
	}
	if err := store.Save(); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("覆盖层清空后文件应被删除: %v", err)
	}
}

// TestOverlayStore_SetOverwritesNonMap 中间层原为非 map 值时 Set 重建为 map。
func TestOverlayStore_SetOverwritesNonMap(t *testing.T) {
	store := NewEmptyOverlayStore(filepath.Join(t.TempDir(), OverlayFileName))
	store.Set("llm", "scalar")
	store.Set("llm.default_provider", "qwen")
	if v, ok := store.Get("llm.default_provider"); !ok || v != "qwen" {
		t.Errorf("应重建中间层，得到 %v (ok=%v)", v, ok)
	}
}
