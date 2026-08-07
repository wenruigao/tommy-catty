// overlay.go 实现配置覆盖层（config.local.yaml）：
// CLI /config set 的变更持久化到覆盖层文件，主配置文件（含注释）永不被修改。
// 加载优先级：内置默认 < 主配置 config.yaml < 覆盖层 config.local.yaml
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// OverlayFileName 覆盖层文件固定文件名（与主配置文件同目录）
const OverlayFileName = "config.local.yaml"

// OverlayPath 返回主配置文件对应的覆盖层文件路径（同目录 config.local.yaml）
func OverlayPath(mainPath string) string {
	return filepath.Join(filepath.Dir(mainPath), OverlayFileName)
}

// LoadWithOverlay 加载主配置文件与覆盖层文件（若存在），覆盖层优先合并，
// 随后照常执行环境变量解析与默认值填充。主配置缺失时返回错误（与 Load 一致）。
func LoadWithOverlay(mainPath string) (*Config, error) {
	base, err := LoadRawMerged(mainPath)
	if err != nil {
		return nil, err
	}
	merged, err := yaml.Marshal(base)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(merged, &cfg); err != nil {
		return nil, err
	}
	cfg.resolveEnvVars()
	cfg.applyDefaults()
	return &cfg, nil
}

// LoadRawMerged 读取主配置与覆盖层并返回合并后的原始 map
// （未经 ${ENV} 解析），供展示层还原环境变量引用原貌。
func LoadRawMerged(mainPath string) (map[string]any, error) {
	data, err := os.ReadFile(mainPath)
	if err != nil {
		return nil, err
	}
	var base map[string]any
	if err := yaml.Unmarshal(data, &base); err != nil {
		return nil, fmt.Errorf("解析配置文件 %s 失败: %w", mainPath, err)
	}

	overlayPath := OverlayPath(mainPath)
	if odata, oerr := os.ReadFile(overlayPath); oerr == nil {
		var overlay map[string]any
		if err := yaml.Unmarshal(odata, &overlay); err != nil {
			return nil, fmt.Errorf("解析覆盖层配置 %s 失败: %w", overlayPath, err)
		}
		base = mergeMaps(base, overlay)
	}
	return base, nil
}

// LookupRaw 在嵌套 map 中按点分键路径取原始值。
func LookupRaw(m map[string]any, key string) (any, bool) {
	var cur any = m
	for _, seg := range strings.Split(key, ".") {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = mm[seg]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// mergeMaps 深合并两个 map：overlay 优先。
// 嵌套 map 递归合并（按键保留双方），其他类型（标量/切片）由 overlay 整体替换。
func mergeMaps(base, overlay map[string]any) map[string]any {
	out := make(map[string]any, len(base))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overlay {
		if bv, ok := out[k]; ok {
			bm, bIsMap := bv.(map[string]any)
			om, oIsMap := v.(map[string]any)
			if bIsMap && oIsMap {
				out[k] = mergeMaps(bm, om)
				continue
			}
		}
		out[k] = v
	}
	return out
}

// OverlayStore 覆盖层文件的读写器（点分键路径，如 llm.default_provider）。
// 内存中以通用嵌套 map 表达，Save 时序列化为 YAML。
type OverlayStore struct {
	path string
	data map[string]any
}

// NewOverlayStore 加载覆盖层文件；文件不存在时返回空存储（不视为错误）。
func NewOverlayStore(path string) (*OverlayStore, error) {
	s := NewEmptyOverlayStore(path)
	odata, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	if err := yaml.Unmarshal(odata, &s.data); err != nil {
		return nil, fmt.Errorf("解析覆盖层配置 %s 失败: %w", path, err)
	}
	if s.data == nil {
		s.data = map[string]any{}
	}
	return s, nil
}

// NewEmptyOverlayStore 创建空的覆盖层存储（文件不存在或加载失败降级时使用）。
func NewEmptyOverlayStore(path string) *OverlayStore {
	return &OverlayStore{path: path, data: map[string]any{}}
}

// Path 返回覆盖层文件路径。
func (s *OverlayStore) Path() string { return s.path }

// Has 判断覆盖层是否包含指定键路径。
func (s *OverlayStore) Has(key string) bool {
	_, ok := s.lookup(key)
	return ok
}

// Get 返回键路径的原始值（不存在时 ok=false）。
func (s *OverlayStore) Get(key string) (any, bool) {
	return s.lookup(key)
}

func (s *OverlayStore) lookup(key string) (any, bool) {
	var cur any = s.data
	for _, seg := range strings.Split(key, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[seg]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// Set 在键路径上设置值（自动创建中间层）。
func (s *OverlayStore) Set(key string, value any) {
	segs := strings.Split(key, ".")
	m := s.data
	for _, seg := range segs[:len(segs)-1] {
		next, ok := m[seg].(map[string]any)
		if !ok {
			next = map[string]any{}
			m[seg] = next
		}
		m = next
	}
	m[segs[len(segs)-1]] = value
}

// Unset 移除键路径并顺带清理空的父级 map，返回是否有变更。
func (s *OverlayStore) Unset(key string) bool {
	segs := strings.Split(key, ".")
	m := s.data
	chain := []map[string]any{m} // chain[i] 为深度 i 处的 map（chain[0] 即根）
	for _, seg := range segs[:len(segs)-1] {
		next, ok := m[seg].(map[string]any)
		if !ok {
			return false
		}
		m = next
		chain = append(chain, m)
	}
	last := segs[len(segs)-1]
	if _, ok := m[last]; !ok {
		return false
	}
	delete(m, last)
	// 自最深层向外：空 map 从父级中移除
	for i := len(chain) - 1; i > 0; i-- {
		if len(chain[i]) == 0 {
			delete(chain[i-1], segs[i-1])
		}
	}
	return true
}

// Save 将当前覆盖层内容序列化写入文件；内容为空时删除文件（恢复"无覆盖"状态）。
func (s *OverlayStore) Save() error {
	if len(s.data) == 0 {
		if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	data, err := yaml.Marshal(s.data)
	if err != nil {
		return err
	}
	if dir := filepath.Dir(s.path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(s.path, data, 0o644)
}
