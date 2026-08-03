package search

import "context"

// Result 表示一条搜索结果。
type Result struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// Provider 定义搜索引擎的统一接口。
type Provider interface {
	// Name 返回搜索引擎名称。
	Name() string
	// Search 执行搜索并返回结果列表。
	Search(ctx context.Context, query string, maxResults int) ([]Result, error)
}
