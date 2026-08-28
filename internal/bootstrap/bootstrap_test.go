package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wenruigao/tommy-catty/config"
	"github.com/wenruigao/tommy-catty/internal/tool"

	_ "modernc.org/sqlite"
)

func TestRegisterDataToolsEmpty(t *testing.T) {
	cfg := config.Default()
	reg := tool.NewRegistry()
	res := RegisterDataTools(cfg, reg)
	defer res.Close()
	if res.DBCount != 0 || res.KBCount != 0 {
		t.Errorf("expected zero data tools, got db=%d kb=%d", res.DBCount, res.KBCount)
	}
}

func TestRegisterDataToolsKB(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "guide.md"),
		[]byte("# 部署指南\n\n使用 docker 部署服务。"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.KnowledgeBases = []config.KnowledgeBaseEntry{
		{Name: "docs", Paths: []string{dir}, Extensions: []string{".md"}},
	}

	reg := tool.NewRegistry()
	res := RegisterDataTools(cfg, reg)
	defer res.Close()

	if res.KBCount != 1 {
		t.Fatalf("expected 1 kb, got %d (warnings: %v)", res.KBCount, res.Warnings)
	}
	// 三个 kb 工具应已注册
	for _, name := range []string{"kb_search", "kb_read", "kb_list"} {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("tool %q not registered", name)
		}
	}

	// 通过注册表实际调用 kb_search
	out, err := reg.Call(context.Background(), "kb_search", map[string]interface{}{
		"kb":    "docs",
		"query": "docker 部署",
	})
	if err != nil {
		t.Fatalf("kb_search call failed: %v", err)
	}
	if !strings.Contains(out.Output, "guide.md") {
		t.Errorf("expected guide.md in search output: %s", out.Output)
	}
}

func TestRegisterDataToolsDB(t *testing.T) {
	cfg := config.Default()
	cfg.Databases = map[string]config.DatabaseEntry{
		"local": {
			Driver:        "sqlite",
			DSN:           ":memory:",
			AllowedTables: []string{"users"},
			QueryTimeout:  "5s",
		},
	}

	reg := tool.NewRegistry()
	res := RegisterDataTools(cfg, reg)
	defer res.Close()

	if res.DBCount != 1 {
		t.Fatalf("expected 1 db, got %d (warnings: %v)", res.DBCount, res.Warnings)
	}
	if _, ok := reg.Get("db_query"); !ok {
		t.Error("db_query tool not registered")
	}

	// 写操作应被安全验证器拦截（即使 :memory: 无表也不会到达 DB）
	out, err := reg.Call(context.Background(), "db_query", map[string]interface{}{
		"datasource": "local",
		"sql":        "DELETE FROM users",
	})
	if err != nil {
		t.Fatalf("db_query call returned hard error: %v", err)
	}
	if out.Error == "" {
		t.Error("expected DELETE to be blocked by validator")
	}
}

func TestRegisterDataToolsBadDriver(t *testing.T) {
	cfg := config.Default()
	cfg.Databases = map[string]config.DatabaseEntry{
		"bad": {Driver: "oracle", DSN: "x"},
	}
	reg := tool.NewRegistry()
	res := RegisterDataTools(cfg, reg)
	defer res.Close()
	if res.DBCount != 0 {
		t.Errorf("expected bad driver to be skipped, got db=%d", res.DBCount)
	}
	if len(res.Warnings) == 0 {
		t.Error("expected a warning for unsupported driver")
	}
	_ = time.Second
}
