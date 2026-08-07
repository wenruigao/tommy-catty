package dbquery

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/tommy-cat/agent/internal/tool"
)

// DBQueryTool 数据库只读查询工具。
// 通过 SQL 安全验证器保证只读，通过连接池访问多个数据源。
type DBQueryTool struct {
	pool  *Pool
	cache *QueryCache // 查询结果缓存（nil 表示禁用）
}

// NewDBQueryTool 创建数据库查询工具。
func NewDBQueryTool(pool *Pool) *DBQueryTool {
	return &DBQueryTool{pool: pool}
}

// NewDBQueryToolWithCache 创建带结果缓存的数据库查询工具（cache 传 nil 则禁用缓存）。
func NewDBQueryToolWithCache(pool *Pool, cache *QueryCache) *DBQueryTool {
	return &DBQueryTool{pool: pool, cache: cache}
}

func (t *DBQueryTool) Name() string { return "db_query" }

func (t *DBQueryTool) Description() string {
	return "执行 SQL SELECT 查询并返回结果（Markdown 表格）。仅支持只读查询，禁止任何数据修改操作。查询前会经过安全验证（语句类型、危险模式、表/列权限）。"
}

func (t *DBQueryTool) Parameters() tool.JSONSchema {
	return tool.JSONSchema{
		Type: "object",
		Properties: map[string]tool.Property{
			"datasource": {
				Type:        "string",
				Description: "数据源名称（对应配置中 databases 的条目名）",
			},
			"sql": {
				Type:        "string",
				Description: "SQL SELECT 查询语句",
			},
			"max_rows": {
				Type:        "integer",
				Description: "最大返回行数（默认取数据源配置的 max_rows）",
			},
		},
		Required: []string{"datasource", "sql"},
	}
}

// Execute 执行查询：验证 SQL → 获取连接 → 执行 → 格式化结果。
func (t *DBQueryTool) Execute(ctx context.Context, args map[string]interface{}) (tool.Result, error) {
	datasource, ok := args["datasource"].(string)
	if !ok || datasource == "" {
		return tool.Result{Error: "参数 datasource 必填"}, nil
	}
	rawSQL, ok := args["sql"].(string)
	if !ok || rawSQL == "" {
		return tool.Result{Error: "参数 sql 必填"}, nil
	}

	// 获取数据源连接和配置
	db, cfg, err := t.pool.Get(datasource)
	if err != nil {
		return tool.Result{Error: err.Error()}, nil
	}

	// 可选的 max_rows 覆盖
	valCfg := cfg.toValidateConfig()
	maxRowsOverridden := false
	if mr, ok := args["max_rows"]; ok {
		switch v := mr.(type) {
		case int:
			if v > 0 && v < valCfg.MaxRows {
				valCfg.MaxRows = v
				maxRowsOverridden = true
			}
		case float64:
			if int(v) > 0 && int(v) < valCfg.MaxRows {
				valCfg.MaxRows = int(v)
				maxRowsOverridden = true
			}
		}
	}

	// SQL 安全验证
	validated, err := Validate(rawSQL, valCfg)
	if err != nil {
		return tool.Result{Error: fmt.Sprintf("SQL 安全验证失败: %v", err)}, nil
	}

	// 结果缓存查询：max_rows 覆盖时不启用（行数上限不同，结果不可复用）
	cacheable := t.cache != nil && !maxRowsOverridden
	if cacheable {
		if cached, hit := t.cache.Get(datasource, validated.SQL); hit {
			return tool.Result{
				Output: cached.FormatMarkdown(),
				Metadata: map[string]interface{}{
					"datasource": datasource,
					"row_count":  cached.RowCount,
					"tables":     validated.Tables,
					"cache":      "hit",
				},
			}, nil
		}
	}

	// 带超时的查询上下文
	queryCtx := ctx
	if cfg.QueryTimeout > 0 {
		var cancel context.CancelFunc
		queryCtx, cancel = context.WithTimeout(ctx, cfg.QueryTimeout)
		defer cancel()
	}

	// 执行查询
	start := time.Now()
	result, err := runQuery(queryCtx, db, validated.SQL, valCfg.MaxRows)
	if err != nil {
		return tool.Result{Error: fmt.Sprintf("查询执行失败: %v", err)}, nil
	}
	result.Duration = time.Since(start)

	// 写入结果缓存（Put 内部跳过截断结果）
	if cacheable {
		t.cache.Put(datasource, validated.SQL, result)
	}

	return tool.Result{
		Output: result.FormatMarkdown(),
		Metadata: map[string]interface{}{
			"datasource": datasource,
			"row_count":  result.RowCount,
			"tables":     validated.Tables,
			"duration":   result.Duration.String(),
		},
	}, nil
}

// runQuery 执行查询并扫描结果集，强制行数上限。
func runQuery(ctx context.Context, db *sql.DB, query string, maxRows int) (*QueryResult, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	result := &QueryResult{Columns: cols}

	// 准备扫描缓冲区
	scanBuf := make([]interface{}, len(cols))
	scanPtrs := make([]interface{}, len(cols))
	for i := range scanBuf {
		scanPtrs[i] = &scanBuf[i]
	}

	for rows.Next() {
		if result.RowCount >= maxRows {
			result.Truncated = true
			break
		}
		if err := rows.Scan(scanPtrs...); err != nil {
			return nil, err
		}
		row := make([]string, len(cols))
		for i, v := range scanBuf {
			row[i] = formatValue(v)
		}
		result.Rows = append(result.Rows, row)
		result.RowCount++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return result, nil
}

// formatValue 将数据库值转换为字符串。
func formatValue(v interface{}) string {
	switch val := v.(type) {
	case nil:
		return "NULL"
	case []byte:
		return string(val)
	case time.Time:
		return val.Format("2006-01-02 15:04:05")
	default:
		return fmt.Sprintf("%v", val)
	}
}
