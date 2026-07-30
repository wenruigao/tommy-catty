// manager.go 管理多个知识库实例，供工具层按名称访问。
package kb

import (
	"fmt"
	"sort"
	"sync"
)

// Manager 持有多个已构建的知识库。
type Manager struct {
	mu  sync.RWMutex
	kbs map[string]*KnowledgeBase
}

// NewManager 创建空管理器。
func NewManager() *Manager {
	return &Manager{kbs: make(map[string]*KnowledgeBase)}
}

// Build 根据配置列表构建并注册所有知识库。
// 返回成功构建的名称列表与遇到的错误（不中断整体流程）。
func (m *Manager) Build(configs []KBConfig) (built []string, errs []error) {
	for _, cfg := range configs {
		if cfg.Name == "" {
			errs = append(errs, fmt.Errorf("knowledge base config missing name"))
			continue
		}
		kb, err := BuildKnowledgeBase(cfg)
		if err != nil {
			errs = append(errs, fmt.Errorf("build kb %q: %w", cfg.Name, err))
			continue
		}
		m.Register(cfg.Name, kb)
		built = append(built, cfg.Name)
	}
	return built, errs
}

// Register 注册一个知识库。
func (m *Manager) Register(name string, kb *KnowledgeBase) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.kbs[name] = kb
}

// Get 按名称获取知识库。
func (m *Manager) Get(name string) (*KnowledgeBase, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	kb, ok := m.kbs[name]
	return kb, ok
}

// Names 返回所有知识库名称（升序）。
func (m *Manager) Names() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := make([]string, 0, len(m.kbs))
	for n := range m.kbs {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
