// Package dbquery 提供数据库只读查询工具。
// validator.go 实现 SQL 安全验证器，采用多层纵深防御策略，
// 任何一层拒绝即终止执行，确保 Agent 只能执行安全的只读查询。
package dbquery

import (
	"fmt"
	"regexp"
	"strings"
)

// ValidateConfig 验证器的数据源级配置（来自 DataSourceConfig）。
type ValidateConfig struct {
	// AllowedTables 允许访问的表名白名单（支持通配符 orders_*）。
	// 空切片表示允许所有表（不推荐生产使用）。
	AllowedTables []string
	// DeniedColumns 禁止查询的列黑名单，格式 "table.column" 或 "*.column"。
	DeniedColumns []string
	// AllowUnion 是否允许 UNION 查询。
	AllowUnion bool
	// AllowSubquery 是否允许子查询。
	AllowSubquery bool
	// MaxRows 结果集行数硬上限。
	MaxRows int
}

// ValidationResult 验证通过后的结果，包含规范化后的可执行 SQL。
type ValidationResult struct {
	// SQL 经过约束注入后的最终可执行 SQL（已追加/覆盖 LIMIT）。
	SQL string
	// Tables 从 SQL 中提取出的表名列表。
	Tables []string
	// Limit 注入的行数上限。
	Limit int
}

// 危险模式正则列表（第二层防御）。
var dangerousPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bINTO\s+(OUTFILE|DUMPFILE)\b`), // MySQL 写文件
	regexp.MustCompile(`(?i)\bLOAD_FILE\s*\(`),              // MySQL 读服务器文件
	regexp.MustCompile(`(?i)\bpg_read_file\s*\(`),           // PostgreSQL 读文件
	regexp.MustCompile(`(?i)\bxp_cmdshell\b`),               // SQL Server 命令执行
	regexp.MustCompile(`(?i)\b(SLEEP|BENCHMARK|pg_sleep)\s*\(`), // 时间盲注/资源耗尽
	regexp.MustCompile(`(?i)\b(GRANT|REVOKE)\b`),            // 权限变更
}

// 非 SELECT 的写/DDL 语句首关键词（第一层防御）。
var blockedLeadingKeywords = map[string]bool{
	"INSERT": true, "UPDATE": true, "DELETE": true, "DROP": true,
	"ALTER": true, "CREATE": true, "TRUNCATE": true, "REPLACE": true,
	"MERGE": true, "GRANT": true, "REVOKE": true, "CALL": true,
	"EXEC": true, "EXECUTE": true, "SET": true, "LOCK": true,
	"UNLOCK": true, "RENAME": true, "USE": true, "BEGIN": true,
	"COMMIT": true, "ROLLBACK": true, "SAVEPOINT": true, "PREPARE": true,
	"DEALLOCATE": true, "ANALYZE": true, "VACUUM": true, "ATTACH": true,
	"DETACH": true, "PRAGMA": true,
}

// 允许的首关键词（只读）。
var allowedLeadingKeywords = map[string]bool{
	"SELECT": true, "SHOW": true, "DESCRIBE": true, "DESC": true,
	"EXPLAIN": true, "WITH": true, // WITH ... SELECT (CTE)
}

// Validate 对 SQL 执行全部安全校验，返回规范化后的可执行 SQL。
// 任何一层校验失败都返回 error。
func Validate(rawSQL string, cfg ValidateConfig) (*ValidationResult, error) {
	sql := strings.TrimSpace(rawSQL)
	if sql == "" {
		return nil, fmt.Errorf("SQL 不能为空")
	}

	// 去除末尾分号（便于后续多语句检测）
	sql = strings.TrimRight(sql, "; \t\n")

	// 第一层：多语句检测（分号后还有内容 = 多语句注入）
	if idx := strings.Index(sql, ";"); idx != -1 {
		rest := strings.TrimSpace(sql[idx+1:])
		if rest != "" {
			return nil, fmt.Errorf("禁止多语句执行（检测到分号后的额外语句）")
		}
		sql = strings.TrimSpace(sql[:idx])
	}

	// 第一层：语句类型白名单
	upper := strings.ToUpper(sql)
	firstWord := firstKeyword(upper)
	if !allowedLeadingKeywords[firstWord] {
		if blockedLeadingKeywords[firstWord] {
			return nil, fmt.Errorf("禁止的语句类型: %s（仅允许 SELECT/SHOW/DESCRIBE/EXPLAIN）", firstWord)
		}
		return nil, fmt.Errorf("不支持的语句类型: %s（仅允许只读查询）", firstWord)
	}

	// WITH (CTE) 必须最终是 SELECT
	if firstWord == "WITH" && !containsSelectAfterWith(upper) {
		return nil, fmt.Errorf("WITH 语句必须最终为 SELECT 查询")
	}

	// 第二层：危险模式检测
	for _, re := range dangerousPatterns {
		if re.MatchString(sql) {
			return nil, fmt.Errorf("检测到危险 SQL 模式: %s", re.String())
		}
	}

	// 第二层：UNION 检测（可配置）
	if !cfg.AllowUnion && regexp.MustCompile(`(?i)\bUNION\b`).MatchString(sql) {
		return nil, fmt.Errorf("未授权 UNION 查询（配置 allow_union 以启用）")
	}

	// 第二层：子查询检测（可配置）
	if !cfg.AllowSubquery && hasSubquery(sql) {
		return nil, fmt.Errorf("未授权子查询（配置 allow_subquery 以启用）")
	}

	// 第三层：表名提取 + 白名单校验
	tables := extractTables(sql)
	if len(cfg.AllowedTables) > 0 {
		for _, t := range tables {
			if !matchesAnyPattern(t, cfg.AllowedTables) {
				return nil, fmt.Errorf("表 %q 不在白名单中", t)
			}
		}
	}

	// 第三层：列黑名单校验
	if len(cfg.DeniedColumns) > 0 {
		if violated := checkDeniedColumns(sql, tables, cfg.DeniedColumns); violated != "" {
			return nil, fmt.Errorf("查询涉及禁止的列: %s", violated)
		}
	}

	// 第四层：LIMIT 约束注入
	limit := cfg.MaxRows
	if limit <= 0 {
		limit = 500
	}
	finalSQL := injectLimit(sql, limit)

	return &ValidationResult{
		SQL:    finalSQL,
		Tables: tables,
		Limit:  limit,
	}, nil
}

// firstKeyword 返回 SQL 的第一个关键词（大写）。
func firstKeyword(upper string) string {
	fields := strings.FieldsFunc(upper, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '(' || r == '\r'
	})
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// containsSelectAfterWith 检查 WITH 语句中是否包含 SELECT。
func containsSelectAfterWith(upper string) bool {
	return strings.Contains(upper, "SELECT")
}

// hasSubquery 检测是否存在子查询（括号内紧跟 SELECT）。
func hasSubquery(sql string) bool {
	re := regexp.MustCompile(`(?i)\(\s*SELECT\b`)
	return re.MatchString(sql)
}

// tableRe 用于提取 FROM / JOIN 后的表名。
// RE2 不支持反向引用，故用分组分别处理反引号、双引号、方括号、无引号四种形式。
var tableRe = regexp.MustCompile(
	"(?i)\\b(?:FROM|JOIN)\\s+(?:`([a-zA-Z_][a-zA-Z0-9_.]*)`|\"([a-zA-Z_][a-zA-Z0-9_.]*)\"|\\[([a-zA-Z_][a-zA-Z0-9_.]*)\\]|([a-zA-Z_][a-zA-Z0-9_.]*))")

// extractTables 从 SQL 中提取所有表名（FROM/JOIN 后）。
func extractTables(sql string) []string {
	matches := tableRe.FindAllStringSubmatch(sql, -1)
	seen := make(map[string]bool)
	var tables []string
	for _, m := range matches {
		// 取四个捕获组中非空的那个作为表名
		name := ""
		for _, g := range m[1:] {
			if g != "" {
				name = g
				break
			}
		}
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if !seen[key] {
			seen[key] = true
			tables = append(tables, name)
		}
	}
	return tables
}

// matchesAnyPattern 检查表名是否匹配白名单中的任一模式（支持 * 通配符）。
func matchesAnyPattern(table string, patterns []string) bool {
	// 取表名最后一段（去除 schema 前缀）
	short := table
	if idx := strings.LastIndex(table, "."); idx != -1 {
		short = table[idx+1:]
	}
	short = strings.ToLower(short)

	for _, p := range patterns {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "*" || p == short {
			return true
		}
		// 通配符匹配：orders_* -> 前缀匹配
		if strings.HasSuffix(p, "*") {
			prefix := strings.TrimSuffix(p, "*")
			if strings.HasPrefix(short, prefix) {
				return true
			}
		}
	}
	return false
}

// checkDeniedColumns 检查 SQL 是否引用了禁止的列。
// 返回被违反的列描述，空字符串表示通过。
func checkDeniedColumns(sql string, tables []string, denied []string) string {
	upper := strings.ToUpper(sql)
	for _, d := range denied {
		d = strings.TrimSpace(d)
		parts := strings.SplitN(d, ".", 2)
		if len(parts) != 2 {
			continue
		}
		tbl := strings.ToLower(strings.TrimSpace(parts[0]))
		col := strings.ToLower(strings.TrimSpace(parts[1]))

		// 构造列引用的正则：col 作为独立标识符出现
		colRe := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(col) + `\b`)
		if !colRe.MatchString(upper) {
			continue
		}

		// *.column 形式：任何表都禁止
		if tbl == "*" {
			return d
		}

		// table.column 形式：仅当该表在查询中时才禁止
		for _, t := range tables {
			tShort := strings.ToLower(t)
			if idx := strings.LastIndex(tShort, "."); idx != -1 {
				tShort = tShort[idx+1:]
			}
			if tShort == tbl {
				return d
			}
		}
	}
	return ""
}

// limitRe 匹配已有的 LIMIT 子句。
var limitRe = regexp.MustCompile(`(?i)\bLIMIT\s+(\d+)`)

// injectLimit 确保 SQL 有不超过 max 的 LIMIT 约束。
func injectLimit(sql string, max int) string {
	m := limitRe.FindStringSubmatch(sql)
	if m == nil {
		// 无 LIMIT，追加
		return fmt.Sprintf("%s LIMIT %d", sql, max)
	}
	// 已有 LIMIT，若超过上限则覆盖
	var existing int
	fmt.Sscanf(m[1], "%d", &existing)
	if existing > max {
		return limitRe.ReplaceAllString(sql, fmt.Sprintf("LIMIT %d", max))
	}
	return sql
}
