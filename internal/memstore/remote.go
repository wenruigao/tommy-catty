package memstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/tommy-cat/agent/internal/memory"
)

// RemoteStore 远程后端客户端：经 REST 协议访问 cmd/memstore 服务，
// 供多实例共享记忆与画像。所有请求携带 Bearer 令牌，超时默认 3s；
// 调用方应对错误做降级处理（记忆链路失败仅警告，不阻塞主任务）。
type RemoteStore struct {
	baseURL string
	token   string
	client  *http.Client
}

// NewRemoteStore 创建远程后端客户端；timeout <= 0 时取默认 3s。
func NewRemoteStore(baseURL, token string, timeout time.Duration) *RemoteStore {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	return &RemoteStore{
		baseURL: baseURL,
		token:   token,
		client:  &http.Client{Timeout: timeout},
	}
}

func (s *RemoteStore) SaveMemory(ctx context.Context, entry memory.MemoryEntry) error {
	body, err := json.Marshal(toDTO(entry))
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/memstore/v1/users/%s/memories/%s",
		url.PathEscape(entry.UserID), url.PathEscape(entry.ID))
	return s.do(ctx, http.MethodPut, path, body, nil)
}

func (s *RemoteStore) SearchMemories(ctx context.Context, userID, query string, topK int) ([]memory.MemoryEntry, error) {
	if query == "" || topK <= 0 {
		return nil, nil
	}
	body, err := json.Marshal(map[string]any{"query": query, "top_k": topK})
	if err != nil {
		return nil, err
	}
	var resp struct {
		Results []MemoryDTO `json:"results"`
	}
	path := fmt.Sprintf("/memstore/v1/users/%s/memories/search", url.PathEscape(userID))
	if err := s.do(ctx, http.MethodPost, path, body, &resp); err != nil {
		return nil, err
	}
	return mapDTOs(resp.Results), nil
}

func (s *RemoteStore) RecentMemories(ctx context.Context, userID string, limit int) ([]memory.MemoryEntry, error) {
	if limit <= 0 {
		return nil, nil
	}
	var resp struct {
		Results []MemoryDTO `json:"results"`
	}
	path := fmt.Sprintf("/memstore/v1/users/%s/memories?limit=%d",
		url.PathEscape(userID), limit)
	if err := s.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return mapDTOs(resp.Results), nil
}

func (s *RemoteStore) DeleteMemories(ctx context.Context, userID string) error {
	path := fmt.Sprintf("/memstore/v1/users/%s/memories", url.PathEscape(userID))
	return s.do(ctx, http.MethodDelete, path, nil, nil)
}

func (s *RemoteStore) SaveProfile(ctx context.Context, userID, content string) error {
	body, err := json.Marshal(map[string]string{"content": content})
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/memstore/v1/users/%s/profile", url.PathEscape(userID))
	return s.do(ctx, http.MethodPut, path, body, nil)
}

func (s *RemoteStore) LoadProfile(ctx context.Context, userID string) (string, error) {
	var resp struct {
		Content string `json:"content"`
	}
	path := fmt.Sprintf("/memstore/v1/users/%s/profile", url.PathEscape(userID))
	if err := s.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return "", err
	}
	return resp.Content, nil
}

// Close HTTP 客户端无持久资源，直接返回 nil。
func (s *RemoteStore) Close() error { return nil }

// HealthCheck 探测服务端健康状态（GET /memstore/v1/healthz），供 doctor 接入。
func (s *RemoteStore) HealthCheck(ctx context.Context) error {
	return s.do(ctx, http.MethodGet, "/memstore/v1/healthz", nil, nil)
}

// do 执行一次 JSON 请求；out 非 nil 时解析响应体。非 2xx 返回携带状态码的错误。
func (s *RemoteStore) do(ctx context.Context, method, path string, body []byte, out any) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.baseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("memstore: 远程请求失败: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("memstore: 远程返回 %d: %s", resp.StatusCode, truncate(string(data), 200))
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
