package search

import (
	"context"
	"fmt"
	"os"
)

// SearchConfig 搜索功能配置。
type SearchConfig struct {
	// DefaultEngine 默认搜索引擎: "duckduckgo" | "tavily"
	DefaultEngine string `yaml:"default_engine"`
	// TavilyAPIKey Tavily API Key，支持 ${ENV_VAR} 引用
	TavilyAPIKey string `yaml:"tavily_api_key"`
	// MaxResults 默认返回结果数
	MaxResults int `yaml:"max_results"`
}

// DefaultSearchConfig 返回默认搜索配置。
func DefaultSearchConfig() SearchConfig {
	return SearchConfig{
		DefaultEngine: "duckduckgo",
		MaxResults:    5,
	}
}

// Manager 管理搜索提供者，支持多引擎和 fallback。
type Manager struct {
	providers  map[string]Provider
	primary    string
	maxResults int
}

// NewManager 根据配置创建搜索管理器。
func NewManager(cfg SearchConfig) *Manager {
	m := &Manager{
		providers:  make(map[string]Provider),
		primary:    cfg.DefaultEngine,
		maxResults: cfg.MaxResults,
	}

	// DuckDuckGo 始终注册（免费，无需配置）
	m.providers["duckduckgo"] = NewDDGProvider()

	// Tavily 需要 API Key
	apiKey := cfg.TavilyAPIKey
	if apiKey == "" {
		apiKey = os.Getenv("TAVILY_API_KEY")
	}
	if apiKey != "" {
		m.providers["tavily"] = NewTavilyProvider(apiKey)
	}

	// 确保 primary 有对应的提供者
	if _, ok := m.providers[m.primary]; !ok {
		m.primary = "duckduckgo"
	}

	return m
}

// Search 执行搜索，自动 fallback 到备用引擎。
func (m *Manager) Search(ctx context.Context, query string, maxResults int) ([]Result, error) {
	if maxResults <= 0 {
		maxResults = m.maxResults
	}

	// 尝试主引擎
	provider, ok := m.providers[m.primary]
	if !ok {
		return nil, fmt.Errorf("search: 未找到搜索引擎 %q", m.primary)
	}

	results, err := provider.Search(ctx, query, maxResults)
	if err == nil && len(results) > 0 {
		return results, nil
	}

	// fallback: 尝试其他引擎
	for name, p := range m.providers {
		if name == m.primary {
			continue
		}
		results, err = p.Search(ctx, query, maxResults)
		if err == nil && len(results) > 0 {
			return results, nil
		}
	}

	if err != nil {
		return nil, fmt.Errorf("search: 所有搜索引擎均失败: %w", err)
	}
	return results, nil
}

// ListProviders 返回所有已注册的搜索引擎名称。
func (m *Manager) ListProviders() []string {
	names := make([]string, 0, len(m.providers))
	for name := range m.providers {
		names = append(names, name)
	}
	return names
}
