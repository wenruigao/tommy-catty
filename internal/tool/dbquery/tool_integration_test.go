package dbquery

import (
	"context"
	"strings"
	"testing"

	_ "modernc.org/sqlite" // 注册 sqlite 驱动（用于测试）
)

// setupTestPool 创建一个带内存 SQLite 的测试连接池，并预置数据。
func setupTestPool(t *testing.T) *Pool {
	t.Helper()
	pool := NewPool()
	cfg := DataSourceConfig{
		Name:          "test",
		Driver:        "sqlite",
		DSN:           ":memory:",
		MaxRows:       100,
		AllowSubquery: true,
		AllowedTables: []string{"users", "orders"},
		DeniedColumns: []string{"users.password_hash"},
	}
	if err := pool.Register(cfg); err != nil {
		t.Fatalf("register pool: %v", err)
	}

	// 预置数据
	db, _, err := pool.Get("test")
	if err != nil {
		t.Fatalf("get db: %v", err)
	}
	stmts := []string{
		`CREATE TABLE users (id INTEGER, name TEXT, password_hash TEXT)`,
		`CREATE TABLE orders (id INTEGER, user_id INTEGER, amount REAL)`,
		`INSERT INTO users VALUES (1, 'alice', 'secret-hash-1')`,
		`INSERT INTO users VALUES (2, 'bob', 'secret-hash-2')`,
		`INSERT INTO orders VALUES (100, 1, 99.5)`,
		`INSERT INTO orders VALUES (101, 2, 200.0)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("setup stmt %q: %v", s, err)
		}
	}
	return pool
}

// TestEndToEndQuery 验证合法查询的完整流程。
func TestEndToEndQuery(t *testing.T) {
	pool := setupTestPool(t)
	defer pool.Close()

	tool := NewDBQueryTool(pool)
	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"datasource": "test",
		"sql":        "SELECT id, name FROM users ORDER BY id",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if !strings.Contains(res.Output, "alice") || !strings.Contains(res.Output, "bob") {
		t.Errorf("expected alice and bob in output, got:\n%s", res.Output)
	}
	if !strings.Contains(res.Output, "查询成功") {
		t.Errorf("expected success header, got:\n%s", res.Output)
	}
}

// TestEndToEndWriteBlocked 验证写操作被验证器拦截（不到达数据库）。
func TestEndToEndWriteBlocked(t *testing.T) {
	pool := setupTestPool(t)
	defer pool.Close()

	tool := NewDBQueryTool(pool)
	res, _ := tool.Execute(context.Background(), map[string]interface{}{
		"datasource": "test",
		"sql":        "DELETE FROM users",
	})
	if res.Error == "" {
		t.Fatal("expected DELETE to be blocked")
	}
	if !strings.Contains(res.Error, "安全验证失败") {
		t.Errorf("expected validation error, got: %s", res.Error)
	}

	// 确认数据未被删除
	db, _, _ := pool.Get("test")
	var count int
	_ = db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
	if count != 2 {
		t.Errorf("data was modified! users count = %d, want 2", count)
	}
}

// TestEndToEndDeniedColumn 验证敏感列被拦截。
func TestEndToEndDeniedColumn(t *testing.T) {
	pool := setupTestPool(t)
	defer pool.Close()

	tool := NewDBQueryTool(pool)
	res, _ := tool.Execute(context.Background(), map[string]interface{}{
		"datasource": "test",
		"sql":        "SELECT password_hash FROM users",
	})
	if res.Error == "" {
		t.Fatal("expected password_hash query to be blocked")
	}
	if !strings.Contains(res.Error, "禁止的列") {
		t.Errorf("expected denied column error, got: %s", res.Error)
	}
}

// TestEndToEndNonWhitelistedTable 验证非白名单表被拦截。
func TestEndToEndNonWhitelistedTable(t *testing.T) {
	pool := setupTestPool(t)
	defer pool.Close()

	// 创建一个未授权的表
	db, _, _ := pool.Get("test")
	_, _ = db.Exec("CREATE TABLE secrets (id INTEGER, token TEXT)")

	tool := NewDBQueryTool(pool)
	res, _ := tool.Execute(context.Background(), map[string]interface{}{
		"datasource": "test",
		"sql":        "SELECT * FROM secrets",
	})
	if res.Error == "" {
		t.Fatal("expected non-whitelisted table to be blocked")
	}
}

// TestEndToEndUnknownDatasource 验证未知数据源返回错误。
func TestEndToEndUnknownDatasource(t *testing.T) {
	pool := setupTestPool(t)
	defer pool.Close()

	tool := NewDBQueryTool(pool)
	res, _ := tool.Execute(context.Background(), map[string]interface{}{
		"datasource": "nonexistent",
		"sql":        "SELECT 1",
	})
	if res.Error == "" {
		t.Fatal("expected unknown datasource error")
	}
}
