package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wenruigao/tommy-catty/internal/memstore"
)

// TestServeAPI_SaveAndRead 验证 REST 端点的写入和读取。
func TestServeAPI_SaveAndRead(t *testing.T) {
	dir := t.TempDir()
	store := memstore.NewFileStore(filepath.Join(dir, "memories"), filepath.Join(dir, "users"), 100)
	defer store.Close()

	srv := memstore.NewServer(store, "test-token")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	client := ts.Client()

	// 健康检查（无需鉴权）
	resp, err := client.Get(ts.URL + "/memstore/v1/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("healthz status = %d", resp.StatusCode)
	}

	// 写入记忆 PUT /users/{uid}/memories/{id}
	entry := map[string]interface{}{
		"id": "m1", "content": "hello memstore",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	}
	body, _ := json.Marshal(entry)
	req, _ := http.NewRequest("PUT", ts.URL+"/memstore/v1/users/u1/memories/m1", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	resp2, err := client.Do(req)
	if err != nil {
		t.Fatalf("PUT memories: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Errorf("PUT memories status = %d", resp2.StatusCode)
	}

	// 读取记忆 GET /users/{uid}/memories
	req3, _ := http.NewRequest("GET", ts.URL+"/memstore/v1/users/u1/memories?limit=10", nil)
	req3.Header.Set("Authorization", "Bearer test-token")
	resp3, err := client.Do(req3)
	if err != nil {
		t.Fatalf("GET memories: %v", err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != 200 {
		t.Errorf("GET memories status = %d", resp3.StatusCode)
	}

	var result struct {
		Results []struct {
			ID      string `json:"id"`
			Content string `json:"content"`
		} `json:"results"`
	}
	json.NewDecoder(resp3.Body).Decode(&result)
	if len(result.Results) != 1 || result.Results[0].Content != "hello memstore" {
		t.Errorf("unexpected results: %+v", result.Results)
	}
}

// TestServeAPI_Unauthorized 验证令牌校验。
func TestServeAPI_Unauthorized(t *testing.T) {
	dir := t.TempDir()
	store := memstore.NewFileStore(filepath.Join(dir, "memories"), filepath.Join(dir, "users"), 100)
	defer store.Close()

	srv := memstore.NewServer(store, "secret")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/memstore/v1/users/u1/memories", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

// TestServeAPI_Profile 验证画像读写。
func TestServeAPI_Profile(t *testing.T) {
	dir := t.TempDir()
	store := memstore.NewFileStore(filepath.Join(dir, "memories"), filepath.Join(dir, "users"), 100)
	defer store.Close()

	srv := memstore.NewServer(store, "")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// 写入画像
	body, _ := json.Marshal(map[string]string{"content": "# User\nlikes Go"})
	req, _ := http.NewRequest("PUT", ts.URL+"/memstore/v1/users/alice/profile", bytes.NewReader(body))
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("PUT profile: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("PUT profile status = %d", resp.StatusCode)
	}

	// 读取画像
	resp2, err := ts.Client().Get(ts.URL + "/memstore/v1/users/alice/profile")
	if err != nil {
		t.Fatalf("GET profile: %v", err)
	}
	defer resp2.Body.Close()
	var profile struct {
		Content string `json:"content"`
	}
	json.NewDecoder(resp2.Body).Decode(&profile)
	if profile.Content != "# User\nlikes Go" {
		t.Errorf("profile = %q", profile.Content)
	}
}

// TestMigrate 验证 migrate 子命令的画像导入逻辑。
func TestMigrate(t *testing.T) {
	dir := t.TempDir()

	// 构造存量画像目录
	usersDir := filepath.Join(dir, "users")
	os.MkdirAll(filepath.Join(usersDir, "alice"), 0755)
	os.WriteFile(filepath.Join(usersDir, "alice", "user.md"), []byte("# Alice\nlikes Go"), 0644)
	os.MkdirAll(filepath.Join(usersDir, "bob"), 0755)
	os.WriteFile(filepath.Join(usersDir, "bob", "user.md"), []byte("# Bob\nlikes Rust"), 0644)
	os.MkdirAll(filepath.Join(usersDir, "empty"), 0755) // 无画像文件，应跳过

	// 目标：sqlite
	dbPath := filepath.Join(dir, "test.db")
	store, err := memstore.NewSQLiteStore(dbPath, 100)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// 模拟 migrate 逻辑
	entries, _ := os.ReadDir(usersDir)
	count := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		userID := e.Name()
		data, err := os.ReadFile(filepath.Join(usersDir, userID, "user.md"))
		if err != nil {
			continue
		}
		if err := store.SaveProfile(ctx, userID, string(data)); err != nil {
			t.Errorf("SaveProfile(%s): %v", userID, err)
			continue
		}
		count++
	}

	if count != 2 {
		t.Errorf("migrated %d profiles, want 2", count)
	}

	profile, err := store.LoadProfile(ctx, "alice")
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	if profile != "# Alice\nlikes Go" {
		t.Errorf("profile = %q", profile)
	}
}
