// common.go 提供各平台渠道共享的底层工具：HTTP JSON 调用、
// 平台 access token 内存缓存、恒定时间字符串比较。
package channel

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// maxResponseBody 平台回调/API 响应体读取上限（1MB，防内存耗尽）。
const maxResponseBody = 1 << 20

// httpPostJSON 发送 JSON POST 请求，返回响应体与状态码。
func httpPostJSON(ctx context.Context, client *http.Client, url string, headers map[string]string, payload any) ([]byte, int, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, fmt.Errorf("序列化请求体失败: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return doHTTPRequest(client, req)
}

// httpGetJSON 发送 GET 请求，返回响应体与状态码。
func httpGetJSON(ctx context.Context, client *http.Client, url string, headers map[string]string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return doHTTPRequest(client, req)
}

// doHTTPRequest 执行请求并读取（限长）响应体。
func doHTTPRequest(client *http.Client, req *http.Request) ([]byte, int, error) {
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return data, resp.StatusCode, nil
}

// tokenCache 平台 access token 的内存缓存（提前 2 分钟过期刷新）。
type tokenCache struct {
	mu     sync.Mutex
	token  string
	expiry time.Time
}

// get 返回缓存 token，过期时调用 refresh 刷新。
func (c *tokenCache) get(refresh func() (string, time.Duration, error)) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.expiry.Add(-2*time.Minute)) {
		return c.token, nil
	}
	tok, ttl, err := refresh()
	if err != nil {
		return "", err
	}
	c.token = tok
	c.expiry = time.Now().Add(ttl)
	return tok, nil
}

// ctEqual 恒定时间字符串比较（防时序侧信道）。
func ctEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// apiError 解析平台 API 的通用错误字段并生成错误。
func apiError(platform string, statusCode int, body []byte) error {
	var generic struct {
		ErrCode     int    `json:"errcode"`
		ErrMsg      string `json:"errmsg"`
		Error       string `json:"error"`
		Description string `json:"description"`
		Message     string `json:"message"`
	}
	_ = json.Unmarshal(body, &generic)
	msg := generic.ErrMsg
	if msg == "" {
		msg = generic.Error
	}
	if msg == "" {
		msg = generic.Description
	}
	if msg == "" {
		msg = generic.Message
	}
	if msg == "" {
		msg = string(body)
	}
	return fmt.Errorf("%s API 调用失败 (status %d): %s", platform, statusCode, msg)
}
