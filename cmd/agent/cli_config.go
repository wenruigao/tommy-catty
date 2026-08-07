// cli_config.go 实现 CLI 的 /config 命令族：
// 查看/设置/删除配置键、快捷切换默认供应商。
// 变更持久化到覆盖层文件 config.local.yaml（主配置文件永不改动），
// 内存配置同步更新使 /config 立即反映新值；运行组件统一重启后生效。
package main

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/tommy-cat/agent/config"
	"github.com/tommy-cat/agent/internal/security"
)

// configManager /config 命令的执行器
type configManager struct {
	mainPath string
	cfg      *config.Config
	store    *config.OverlayStore
	audit    *security.AuditLogger
}

// newConfigManager 创建 /config 执行器；覆盖层加载失败时降级为内存存储（仅警告）。
func newConfigManager(mainPath string, cfg *config.Config, audit *security.AuditLogger) *configManager {
	overlayPath := config.OverlayPath(mainPath)
	store, err := config.NewOverlayStore(overlayPath)
	if err != nil {
		fmt.Printf("  ⚠️  覆盖层配置加载失败 (%v)，/config 变更将覆盖该文件\n", err)
		store = config.NewEmptyOverlayStore(overlayPath)
	}
	return &configManager{mainPath: mainPath, cfg: cfg, store: store, audit: audit}
}

// handleConfigCommand 分发 /config 子命令并打印结果
func handleConfigCommand(args []string, mgr *configManager) {
	switch {
	case len(args) == 0:
		fmt.Print(mgr.List())
	case args[0] == "path":
		fmt.Print(mgr.PathInfo())
	case args[0] == "get" && len(args) == 2:
		printConfigResult(mgr.Get(args[1]))
	case args[0] == "set" && len(args) >= 3:
		printConfigResult(mgr.Set(args[1], strings.Join(args[2:], " ")))
	case args[0] == "unset" && len(args) == 2:
		printConfigResult(mgr.Unset(args[1]))
	case args[0] == "use" && len(args) == 2:
		printConfigResult(mgr.Set("llm.default_provider", args[1]))
	default:
		fmt.Println(`  用法:
    /config                 列出全部可配置键
    /config get <key>       查看单个配置
    /config set <key> <值>  设置配置（持久化到 config.local.yaml）
    /config unset <key>     移除覆盖，恢复主配置/默认值
    /config use <provider>  切换默认模型供应商
    /config path            显示配置文件路径`)
	}
}

func printConfigResult(msg string, err error) {
	if err != nil {
		fmt.Printf("  ❌ %v\n", err)
		return
	}
	fmt.Println(msg)
}

// List 返回全部键的清单文本（按键名排序，密钥脱敏，标注来源）。
func (m *configManager) List() string {
	var sb strings.Builder
	sb.WriteString("  ⚙️  配置清单（[local]=覆盖层 config.local.yaml，[config]=主配置/默认值）\n")
	for _, spec := range m.allSpecs() {
		value := m.displayValue(spec)
		if spec.Secret {
			value = config.MaskValue(value)
		}
		source := "[config]"
		if m.store.Has(spec.Key) {
			source = "[local] "
		}
		fmt.Fprintf(&sb, "    %-46s = %-22s %s  %s\n", spec.Key, value, source, spec.Desc)
	}
	sb.WriteString("  修改: /config set <key> <值>    详情: /config get <key>\n")
	return sb.String()
}

// allSpecs 汇总静态键与各供应商的动态键，按键名排序。
func (m *configManager) allSpecs() []*config.KeySpec {
	static := config.AllKeySpecs()
	specs := make([]*config.KeySpec, 0, len(static)+8)
	for i := range static {
		specs = append(specs, &static[i])
	}
	for name := range m.cfg.LLM.Providers {
		for _, field := range config.ProviderFields {
			if spec, ok := config.LookupKey("llm.providers." + name + "." + field); ok {
				specs = append(specs, spec)
			}
		}
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Key < specs[j].Key })
	return specs
}

// displayValue 取键的展示值：Secret 键优先读 ${ENV} 解析前的原始值
// （避免环境变量引用被解析为空串后无法还原），失败时回退内存配置。
func (m *configManager) displayValue(spec *config.KeySpec) string {
	if spec.Secret {
		if raw, err := config.LoadRawMerged(m.mainPath); err == nil {
			if v, ok := config.LookupRaw(raw, spec.Key); ok {
				if s, isStr := v.(string); isStr {
					return s
				}
			}
		}
	}
	return spec.Get(m.cfg)
}

// Get 查看单个键（值 + 说明 + 来源）
func (m *configManager) Get(key string) (string, error) {
	spec, ok := config.LookupKey(key)
	if !ok {
		return "", unknownKeyError(key)
	}
	value := m.displayValue(spec)
	if spec.Secret {
		value = config.MaskValue(value)
	}
	source := "主配置/默认值"
	if m.store.Has(key) {
		source = "覆盖层 config.local.yaml"
	}
	return fmt.Sprintf("  %s = %s\n      说明: %s\n      来源: %s", key, value, spec.Desc, source), nil
}

// Set 设置键值：类型/语义校验 → 持久化覆盖层 → 同步内存配置 → 审计。
func (m *configManager) Set(key, value string) (string, error) {
	spec, ok := config.LookupKey(key)
	if !ok {
		return "", unknownKeyError(key)
	}
	if err := config.ValidateValue(spec, value); err != nil {
		return "", err
	}
	if err := spec.Set(m.cfg, value); err != nil {
		return "", err
	}
	m.store.Set(key, config.ParseNative(spec, value))
	if err := m.store.Save(); err != nil {
		return "", fmt.Errorf("持久化覆盖层配置失败: %w", err)
	}
	if m.audit != nil {
		m.audit.LogConfigChange("local", "set", key)
	}

	var sb strings.Builder
	if spec.Secret && !config.IsEnvRef(value) {
		sb.WriteString("  ⚠️  正在以明文保存秘密值，建议改用 ${ENV_VAR} 环境变量引用\n")
	}
	display := value
	if spec.Secret {
		display = config.MaskValue(value)
	}
	fmt.Fprintf(&sb, "  ✅ 已设置 %s = %s\n  已保存至 %s，重启后生效", key, display, m.store.Path())
	return sb.String(), nil
}

// Unset 移除覆盖层中的键，恢复主配置/默认值（重载配置同步内存）。
func (m *configManager) Unset(key string) (string, error) {
	if _, ok := config.LookupKey(key); !ok {
		return "", unknownKeyError(key)
	}
	if !m.store.Unset(key) {
		return "", fmt.Errorf("覆盖层不包含 %s（无需 unset，当前值来自主配置/默认）", key)
	}
	if err := m.store.Save(); err != nil {
		return "", fmt.Errorf("持久化覆盖层配置失败: %w", err)
	}
	if m.audit != nil {
		m.audit.LogConfigChange("local", "unset", key)
	}
	// 重载配置使内存值恢复为主配置/默认
	if newCfg, err := config.LoadWithOverlay(m.mainPath); err == nil {
		*m.cfg = *newCfg
	}
	return fmt.Sprintf("  ✅ 已移除 %s 的覆盖值，恢复为主配置/默认值（重启后生效）", key), nil
}

// PathInfo 显示主配置文件与覆盖层文件路径
func (m *configManager) PathInfo() string {
	overlay := m.store.Path()
	marker := "（不存在，首次 /config set 时创建）"
	if _, err := os.Stat(overlay); err == nil {
		marker = ""
	}
	return fmt.Sprintf("  主配置文件: %s\n  覆盖层文件: %s %s\n  覆盖层内容可通过 /config unset <key> 逐项移除", m.mainPath, overlay, marker)
}

// unknownKeyError 未知键错误（附最相近键名提示）
func unknownKeyError(key string) error {
	msg := fmt.Sprintf("未知配置键: %s", key)
	if s := config.SuggestKey(key); s != "" {
		msg += fmt.Sprintf("（是否想输入 %s？）", s)
	}
	msg += "\n      使用 /config 查看全部可配置键"
	return errors.New(msg)
}
