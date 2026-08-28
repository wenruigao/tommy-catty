package kbtools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wenruigao/tommy-catty/internal/kb"
)

func setupManager(t *testing.T) *kb.Manager {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "concurrency.md"),
		"# Go 并发\n\nGo 使用 goroutine 和 channel 实现并发。\n\n## 同步\n\nsync 包提供 Mutex 和 WaitGroup。")
	writeFile(t, filepath.Join(dir, "testing.md"),
		"# Go 测试\n\ntesting 包提供单元测试与基准测试支持。")

	m := kb.NewManager()
	_, errs := m.Build([]kb.KBConfig{{Name: "docs", Paths: []string{dir}, Extensions: []string{".md"}}})
	if len(errs) != 0 {
		t.Fatalf("build errors: %v", errs)
	}
	return m
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestKBSearchTool(t *testing.T) {
	m := setupManager(t)
	tool := NewKBSearchTool(m)
	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"kb":    "docs",
		"query": "goroutine channel 并发",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if !strings.Contains(res.Output, "concurrency.md") {
		t.Errorf("expected concurrency.md in output, got: %s", res.Output)
	}
}

func TestKBSearchUnknownKB(t *testing.T) {
	m := setupManager(t)
	tool := NewKBSearchTool(m)
	res, _ := tool.Execute(context.Background(), map[string]interface{}{
		"kb":    "nope",
		"query": "x",
	})
	if res.Error == "" {
		t.Error("expected error for unknown kb")
	}
}

func TestKBListAndRead(t *testing.T) {
	m := setupManager(t)
	ctx := context.Background()

	// list 所有知识库
	listTool := NewKBListTool(m)
	res, _ := listTool.Execute(ctx, map[string]interface{}{})
	if !strings.Contains(res.Output, "docs") {
		t.Errorf("expected 'docs' in list output: %s", res.Output)
	}

	// list 指定知识库的文档
	res, _ = listTool.Execute(ctx, map[string]interface{}{"kb": "docs"})
	if !strings.Contains(res.Output, "doc_id=") {
		t.Errorf("expected doc_id in output: %s", res.Output)
	}

	// read 文档 0
	readTool := NewKBReadTool(m)
	res, _ = readTool.Execute(ctx, map[string]interface{}{"kb": "docs", "doc_id": 0})
	if res.Error != "" {
		t.Fatalf("read error: %s", res.Error)
	}
	if !strings.Contains(res.Output, "片段") {
		t.Errorf("expected chunks in read output: %s", res.Output)
	}
}

func TestKBReadMissingDoc(t *testing.T) {
	m := setupManager(t)
	readTool := NewKBReadTool(m)
	res, _ := readTool.Execute(context.Background(), map[string]interface{}{"kb": "docs", "doc_id": 999})
	if res.Error == "" {
		t.Error("expected error for missing doc")
	}
}
