package search

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// DDGProvider 通过 DuckDuckGo HTML 接口执行搜索（免费，无需 API Key）。
type DDGProvider struct {
	client *http.Client
}

// NewDDGProvider 创建 DuckDuckGo 搜索提供者。
func NewDDGProvider() *DDGProvider {
	return &DDGProvider{
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

func (d *DDGProvider) Name() string { return "duckduckgo" }

func (d *DDGProvider) Search(ctx context.Context, query string, maxResults int) ([]Result, error) {
	if maxResults <= 0 {
		maxResults = 5
	}

	// DuckDuckGo HTML 搜索端点
	form := url.Values{}
	form.Set("q", query)
	form.Set("b", "")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://html.duckduckgo.com/html/", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("ddg: 创建请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36")

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ddg: 请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ddg: HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 限制 1MB
	if err != nil {
		return nil, fmt.Errorf("ddg: 读取响应失败: %w", err)
	}

	return parseDDGHTML(string(body), maxResults), nil
}

// parseDDGHTML 从 DuckDuckGo HTML 页面提取搜索结果。
// 使用正则提取 result__a 链接和 result__snippet 摘要。
func parseDDGHTML(html string, maxResults int) []Result {
	// 提取结果块：每个结果在 <div class="result ..."> 中
	// 链接: <a rel="nofollow" class="result__a" href="URL">Title</a>
	// 摘要: <a class="result__snippet" href="...">Snippet</a>
	linkRe := regexp.MustCompile(`<a[^>]+class="result__a"[^>]*href="([^"]*)"[^>]*>(.*?)</a>`)
	snippetRe := regexp.MustCompile(`<a[^>]+class="result__snippet"[^>]*>(.*?)</a>`)

	linkMatches := linkRe.FindAllStringSubmatch(html, -1)
	snippetMatches := snippetRe.FindAllStringSubmatch(html, -1)

	var results []Result
	for i, lm := range linkMatches {
		if i >= maxResults {
			break
		}
		if len(lm) < 3 {
			continue
		}

		rawURL := lm[1]
		title := stripHTMLTags(lm[2])

		// DuckDuckGo 的链接可能是跳转链接，提取真实 URL
		if strings.Contains(rawURL, "uddg=") {
			if u, err := url.Parse(rawURL); err == nil {
				if real := u.Query().Get("uddg"); real != "" {
					rawURL = real
				}
			}
		}

		snippet := ""
		if i < len(snippetMatches) && len(snippetMatches[i]) >= 2 {
			snippet = stripHTMLTags(snippetMatches[i][1])
		}

		if title != "" {
			results = append(results, Result{
				Title:   title,
				URL:     rawURL,
				Snippet: snippet,
			})
		}
	}

	return results
}

// stripHTMLTags 移除 HTML 标签并解码常见实体。
func stripHTMLTags(s string) string {
	re := regexp.MustCompile(`<[^>]*>`)
	s = re.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", "\"")
	s = strings.ReplaceAll(s, "&#x27;", "'")
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	s = strings.TrimSpace(s)
	return s
}
