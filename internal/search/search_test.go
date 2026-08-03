package search

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestTavilySearchSendsAPIKey 验证 Tavily 请求携带 API Key，且正常响应能解析出结果。
func TestTavilySearchSendsAPIKey(t *testing.T) {
	const wantKey = "tvly-test-key-123"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("期望 POST 请求，实际为 %s", r.Method)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("读取请求体失败: %v", err)
		}
		var reqBody map[string]interface{}
		if err := json.Unmarshal(body, &reqBody); err != nil {
			t.Fatalf("请求体不是合法 JSON: %v", err)
		}
		if got := reqBody["api_key"]; got != wantKey {
			t.Errorf("请求体 api_key = %v，期望 %q", got, wantKey)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+wantKey {
			t.Errorf("Authorization 头 = %q，期望 %q", got, "Bearer "+wantKey)
		}
		if got := reqBody["query"]; got != "golang 测试" {
			t.Errorf("请求体 query = %v，期望 %q", got, "golang 测试")
		}

		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"results":[
			{"title":"标题一","url":"https://example.com/1","content":"摘要一","score":0.9},
			{"title":"标题二","url":"https://example.com/2","content":"摘要二","score":0.8}
		]}`)
	}))
	defer server.Close()

	p := NewTavilyProvider(wantKey)
	p.endpoint = server.URL

	results, err := p.Search(context.Background(), "golang 测试", 5)
	if err != nil {
		t.Fatalf("Search 返回错误: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("结果数 = %d，期望 2", len(results))
	}
	if results[0].Title != "标题一" || results[0].URL != "https://example.com/1" || results[0].Snippet != "摘要一" {
		t.Errorf("首条结果解析不正确: %+v", results[0])
	}
}

// TestTavilySearchUnauthorized 验证 Tavily 返回 401 时错误能向上传递。
func TestTavilySearchUnauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":"invalid api key"}`)
	}))
	defer server.Close()

	p := NewTavilyProvider("bad-key")
	p.endpoint = server.URL

	_, err := p.Search(context.Background(), "查询", 5)
	if err == nil {
		t.Fatal("期望返回错误，实际为 nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("错误消息应包含状态码 401，实际: %v", err)
	}
}

// TestParseDDGHTML 验证 DuckDuckGo HTML 解析：标题、uddg 跳转链接解码、摘要提取。
func TestParseDDGHTML(t *testing.T) {
	html := `<html><body>
<div class="result results_links">
  <a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fpage%3Fa%3D1%26b%3D2&amp;rut=abc">示例<b>标题</b></a>
  <a class="result__snippet" href="//duckduckgo.com/l/?uddg=...">这是一段&lt;摘要&gt;文本</a>
</div>
<div class="result results_links">
  <a rel="nofollow" class="result__a" href="https://direct.example.com/no-redirect">直接链接标题</a>
  <a class="result__snippet" href="...">第二段摘要</a>
</div>
</body></html>`

	results := parseDDGHTML(html, 5)
	if len(results) != 2 {
		t.Fatalf("结果数 = %d，期望 2", len(results))
	}

	// 第一条：标题去标签，URL 从 uddg 参数解码出真实地址
	if results[0].Title != "示例标题" {
		t.Errorf("第一条标题 = %q，期望 %q", results[0].Title, "示例标题")
	}
	if results[0].URL != "https://example.com/page?a=1&b=2" {
		t.Errorf("第一条 URL = %q，期望 uddg 解码后的真实地址", results[0].URL)
	}
	if results[0].Snippet != "这是一段<摘要>文本" {
		t.Errorf("第一条摘要 = %q，期望实体解码后的文本", results[0].Snippet)
	}

	// 第二条：无 uddg 参数时保留原始链接
	if results[1].URL != "https://direct.example.com/no-redirect" {
		t.Errorf("第二条 URL = %q，期望保留原始链接", results[1].URL)
	}

	// maxResults 截断
	if got := parseDDGHTML(html, 1); len(got) != 1 {
		t.Errorf("maxResults=1 时结果数 = %d，期望 1", len(got))
	}
}

// fakeProvider 测试用的假搜索引擎。
type fakeProvider struct {
	name    string
	results []Result
	err     error
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) Search(ctx context.Context, query string, maxResults int) ([]Result, error) {
	return f.results, f.err
}

// newTestManager 构造仅含指定提供者的 Manager（绕过真实网络引擎）。
func newTestManager(primary string, providers map[string]Provider) *Manager {
	return &Manager{providers: providers, primary: primary, maxResults: 5}
}

// TestManagerFallbackOnError 验证主引擎失败时 fallback 到备用引擎。
func TestManagerFallbackOnError(t *testing.T) {
	m := newTestManager("primary", map[string]Provider{
		"primary": &fakeProvider{name: "primary", err: errors.New("主引擎故障")},
		"backup": &fakeProvider{name: "backup", results: []Result{
			{Title: "备用结果", URL: "https://backup.example.com", Snippet: "来自备用引擎"},
		}},
	})

	results, err := m.Search(context.Background(), "查询", 5)
	if err != nil {
		t.Fatalf("Search 返回错误: %v", err)
	}
	if len(results) != 1 || results[0].Title != "备用结果" {
		t.Errorf("应返回备用引擎结果，实际: %+v", results)
	}
}

// TestManagerFallbackOnEmpty 验证主引擎返回零结果时 fallback 到备用引擎。
func TestManagerFallbackOnEmpty(t *testing.T) {
	m := newTestManager("primary", map[string]Provider{
		"primary": &fakeProvider{name: "primary", results: []Result{}},
		"backup": &fakeProvider{name: "backup", results: []Result{
			{Title: "备用结果", URL: "https://backup.example.com"},
		}},
	})

	results, err := m.Search(context.Background(), "查询", 5)
	if err != nil {
		t.Fatalf("Search 返回错误: %v", err)
	}
	if len(results) != 1 || results[0].Title != "备用结果" {
		t.Errorf("应返回备用引擎结果，实际: %+v", results)
	}
}

// TestManagerAllProvidersFail 验证所有引擎失败时返回中文错误。
func TestManagerAllProvidersFail(t *testing.T) {
	m := newTestManager("primary", map[string]Provider{
		"primary": &fakeProvider{name: "primary", err: errors.New("主引擎故障")},
		"backup":  &fakeProvider{name: "backup", err: errors.New("备用引擎故障")},
	})

	_, err := m.Search(context.Background(), "查询", 5)
	if err == nil {
		t.Fatal("期望返回错误，实际为 nil")
	}
	if !strings.Contains(err.Error(), "所有搜索引擎均失败") {
		t.Errorf("错误消息应为中文并包含“所有搜索引擎均失败”，实际: %v", err)
	}
}

// TestManagerPrimaryHit 验证主引擎成功时直接使用其结果。
func TestManagerPrimaryHit(t *testing.T) {
	m := newTestManager("primary", map[string]Provider{
		"primary": &fakeProvider{name: "primary", results: []Result{
			{Title: "主引擎结果", URL: "https://primary.example.com"},
		}},
		"backup": &fakeProvider{name: "backup", err: errors.New("不应被调用")},
	})

	results, err := m.Search(context.Background(), "查询", 5)
	if err != nil {
		t.Fatalf("Search 返回错误: %v", err)
	}
	if len(results) != 1 || results[0].Title != "主引擎结果" {
		t.Errorf("应返回主引擎结果，实际: %+v", results)
	}
}
