package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// tavilyAPIURL 是 Tavily Search API 的默认端点。
const tavilyAPIURL = "https://api.tavily.com/search"

// TavilyProvider 通过 Tavily Search API 执行搜索（专为 AI Agent 设计）。
// 需要 API Key，注册地址: https://tavily.com
type TavilyProvider struct {
	apiKey   string
	endpoint string
	client   *http.Client
}

// NewTavilyProvider 创建 Tavily 搜索提供者。
func NewTavilyProvider(apiKey string) *TavilyProvider {
	return &TavilyProvider{
		apiKey:   apiKey,
		endpoint: tavilyAPIURL,
		client:   &http.Client{Timeout: 15 * time.Second},
	}
}

func (t *TavilyProvider) Name() string { return "tavily" }

func (t *TavilyProvider) Search(ctx context.Context, query string, maxResults int) ([]Result, error) {
	if maxResults <= 0 {
		maxResults = 5
	}

	reqBody := map[string]interface{}{
		"api_key":             t.apiKey,
		"query":               query,
		"max_results":         maxResults,
		"include_answer":      false,
		"include_raw_content": false,
		"search_depth":        "basic",
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("tavily: 序列化请求失败: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		t.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("tavily: 创建请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// 同时在 Authorization 头携带 Key，兼容支持 Bearer 认证的部署
	req.Header.Set("Authorization", "Bearer "+t.apiKey)

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tavily: 请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("tavily: HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var apiResp struct {
		Results []struct {
			Title   string  `json:"title"`
			URL     string  `json:"url"`
			Content string  `json:"content"`
			Score   float64 `json:"score"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("tavily: 解析响应失败: %w", err)
	}

	results := make([]Result, 0, len(apiResp.Results))
	for _, r := range apiResp.Results {
		results = append(results, Result{
			Title:   r.Title,
			URL:     r.URL,
			Snippet: r.Content,
		})
	}
	return results, nil
}
