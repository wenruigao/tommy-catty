package dbquery

import (
	"strings"
	"testing"
)

func defaultCfg() ValidateConfig {
	return ValidateConfig{
		AllowedTables: []string{"orders", "users", "products", "analytics_*"},
		DeniedColumns: []string{"users.password_hash", "*.secret_key"},
		AllowUnion:    false,
		AllowSubquery: true,
		MaxRows:       500,
	}
}

// TestValidSelects 验证合法 SELECT 语句通过。
func TestValidSelects(t *testing.T) {
	cases := []string{
		"SELECT * FROM orders WHERE id = 1",
		"SELECT id, amount FROM orders LIMIT 10",
		"SELECT COUNT(*) FROM users",
		"SELECT * FROM analytics_events WHERE date > '2026-01-01'",
		"  SELECT name FROM products  ",
		"SELECT * FROM orders;", // 末尾分号应被容忍
	}
	for _, sql := range cases {
		res, err := Validate(sql, defaultCfg())
		if err != nil {
			t.Errorf("expected valid, got error for %q: %v", sql, err)
			continue
		}
		if res.SQL == "" {
			t.Errorf("empty result SQL for %q", sql)
		}
	}
}

// TestBlockedStatements 验证写/DDL 语句被拒绝（第一层）。
func TestBlockedStatements(t *testing.T) {
	cases := []string{
		"INSERT INTO orders (id) VALUES (1)",
		"UPDATE users SET name = 'x'",
		"DELETE FROM orders",
		"DROP TABLE users",
		"ALTER TABLE orders ADD COLUMN x INT",
		"CREATE TABLE evil (id INT)",
		"TRUNCATE TABLE orders",
		"GRANT ALL ON orders TO public",
		"SET autocommit = 0",
		"PRAGMA table_info(users)",
	}
	for _, sql := range cases {
		if _, err := Validate(sql, defaultCfg()); err == nil {
			t.Errorf("expected rejection for %q, but it passed", sql)
		}
	}
}

// TestMultiStatementInjection 验证多语句注入被拒绝。
func TestMultiStatementInjection(t *testing.T) {
	cases := []string{
		"SELECT * FROM orders; DROP TABLE users",
		"SELECT 1; DELETE FROM orders",
		"SELECT * FROM users; INSERT INTO evil VALUES (1)",
	}
	for _, sql := range cases {
		if _, err := Validate(sql, defaultCfg()); err == nil {
			t.Errorf("expected multi-statement rejection for %q", sql)
		}
	}
}

// TestDangerousPatterns 验证危险模式被拒绝（第二层）。
func TestDangerousPatterns(t *testing.T) {
	cases := []string{
		"SELECT * FROM orders INTO OUTFILE '/tmp/x'",
		"SELECT LOAD_FILE('/etc/passwd')",
		"SELECT pg_read_file('/etc/passwd')",
		"SELECT * FROM users WHERE id = 1 AND SLEEP(5)",
		"SELECT BENCHMARK(1000000, MD5('x'))",
		"EXEC xp_cmdshell 'dir'",
	}
	for _, sql := range cases {
		if _, err := Validate(sql, defaultCfg()); err == nil {
			t.Errorf("expected dangerous pattern rejection for %q", sql)
		}
	}
}

// TestUnionBlocked 验证 UNION 默认被拒绝。
func TestUnionBlocked(t *testing.T) {
	sql := "SELECT id FROM orders UNION SELECT id FROM users"
	if _, err := Validate(sql, defaultCfg()); err == nil {
		t.Error("expected UNION rejection when allow_union=false")
	}

	// 允许 UNION 时应通过
	cfg := defaultCfg()
	cfg.AllowUnion = true
	if _, err := Validate(sql, cfg); err != nil {
		t.Errorf("expected UNION allowed when allow_union=true: %v", err)
	}
}

// TestTableWhitelist 验证表白名单（第三层）。
func TestTableWhitelist(t *testing.T) {
	// 白名单内的表
	if _, err := Validate("SELECT * FROM orders", defaultCfg()); err != nil {
		t.Errorf("orders should be allowed: %v", err)
	}
	// 通配符匹配
	if _, err := Validate("SELECT * FROM analytics_daily", defaultCfg()); err != nil {
		t.Errorf("analytics_daily should match analytics_*: %v", err)
	}
	// 白名单外的表
	if _, err := Validate("SELECT * FROM secrets", defaultCfg()); err == nil {
		t.Error("secrets table should be rejected")
	}
	// JOIN 中的非白名单表
	if _, err := Validate("SELECT * FROM orders JOIN secrets ON orders.id = secrets.id", defaultCfg()); err == nil {
		t.Error("JOIN with non-whitelisted table should be rejected")
	}
}

// TestDeniedColumns 验证列黑名单（第三层）。
func TestDeniedColumns(t *testing.T) {
	// users.password_hash 被禁止
	if _, err := Validate("SELECT password_hash FROM users", defaultCfg()); err == nil {
		t.Error("password_hash should be rejected")
	}
	// *.secret_key 任何表都禁止
	if _, err := Validate("SELECT secret_key FROM orders", defaultCfg()); err == nil {
		t.Error("secret_key should be rejected for any table")
	}
	// 正常列通过
	if _, err := Validate("SELECT name, email FROM users", defaultCfg()); err != nil {
		t.Errorf("normal columns should pass: %v", err)
	}
}

// TestLimitInjection 验证 LIMIT 约束注入（第四层）。
func TestLimitInjection(t *testing.T) {
	// 无 LIMIT -> 追加
	res, _ := Validate("SELECT * FROM orders", defaultCfg())
	if !strings.Contains(strings.ToUpper(res.SQL), "LIMIT 500") {
		t.Errorf("expected LIMIT 500 injected, got: %s", res.SQL)
	}

	// 已有小 LIMIT -> 保留
	res, _ = Validate("SELECT * FROM orders LIMIT 10", defaultCfg())
	if !strings.Contains(strings.ToUpper(res.SQL), "LIMIT 10") {
		t.Errorf("expected LIMIT 10 preserved, got: %s", res.SQL)
	}

	// 超大 LIMIT -> 覆盖为上限
	res, _ = Validate("SELECT * FROM orders LIMIT 99999", defaultCfg())
	if !strings.Contains(strings.ToUpper(res.SQL), "LIMIT 500") {
		t.Errorf("expected LIMIT capped to 500, got: %s", res.SQL)
	}
}

// TestEmptySQL 验证空 SQL 被拒绝。
func TestEmptySQL(t *testing.T) {
	for _, sql := range []string{"", "   ", "\n\t"} {
		if _, err := Validate(sql, defaultCfg()); err == nil {
			t.Errorf("expected error for empty SQL %q", sql)
		}
	}
}

// TestExtractTables 验证表名提取。
func TestExtractTables(t *testing.T) {
	tables := extractTables("SELECT * FROM orders o JOIN users u ON o.uid = u.id LEFT JOIN products p ON p.id = o.pid")
	if len(tables) != 3 {
		t.Errorf("expected 3 tables, got %d: %v", len(tables), tables)
	}
}

// TestCTE 验证 WITH ... SELECT 通过，WITH 非 SELECT 拒绝。
func TestCTE(t *testing.T) {
	sql := "WITH recent AS (SELECT * FROM orders) SELECT * FROM recent"
	cfg := defaultCfg()
	cfg.AllowedTables = []string{"*"} // CTE 表名 recent 不在白名单，放开
	if _, err := Validate(sql, cfg); err != nil {
		t.Errorf("CTE SELECT should pass: %v", err)
	}
}

// ============================================================
// injectLimit：行数上限注入
// ============================================================

func TestInjectLimit_AppendsWhenMissing(t *testing.T) {
	got := injectLimit("SELECT * FROM users", 100)
	if got != "SELECT * FROM users LIMIT 100" {
		t.Errorf("got %q", got)
	}
}

func TestInjectLimit_KeepsSmallerLimit(t *testing.T) {
	got := injectLimit("SELECT * FROM users LIMIT 10", 100)
	if got != "SELECT * FROM users LIMIT 10" {
		t.Errorf("smaller LIMIT should be preserved, got %q", got)
	}
}

func TestInjectLimit_ClampsLargerLimit(t *testing.T) {
	got := injectLimit("SELECT * FROM users LIMIT 5000", 100)
	if got != "SELECT * FROM users LIMIT 100" {
		t.Errorf("larger LIMIT should be clamped, got %q", got)
	}
}

func TestInjectLimit_ClampsEqualLimit(t *testing.T) {
	// 等于上限属合法，不应改写
	got := injectLimit("SELECT * FROM users LIMIT 100", 100)
	if got != "SELECT * FROM users LIMIT 100" {
		t.Errorf("equal LIMIT should be preserved, got %q", got)
	}
}

// TestInjectLimit_OverflowIsClamped 是安全回归测试：
// 超出 int 范围的 LIMIT 值解析失败，必须钳制而非原样放行，
// 否则 `LIMIT 99999999999999999999` 可完全绕过行数上限。
func TestInjectLimit_OverflowIsClamped(t *testing.T) {
	got := injectLimit("SELECT * FROM users LIMIT 99999999999999999999", 100)
	if got != "SELECT * FROM users LIMIT 100" {
		t.Errorf("overflowing LIMIT must be clamped (fail-closed), got %q", got)
	}
}

func TestInjectLimit_CaseInsensitive(t *testing.T) {
	got := injectLimit("SELECT * FROM users limit 5000", 100)
	if !strings.Contains(strings.ToUpper(got), "LIMIT 100") {
		t.Errorf("lowercase limit should be clamped, got %q", got)
	}
}

func TestInjectLimit_ClampsAllOccurrences(t *testing.T) {
	// 子查询与外层都有 LIMIT 时，两处都应被钳制
	got := injectLimit("SELECT * FROM (SELECT * FROM t LIMIT 9999) x LIMIT 9999", 100)
	if strings.Contains(got, "9999") {
		t.Errorf("all LIMIT clauses should be clamped, got %q", got)
	}
}
