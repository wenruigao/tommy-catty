package dbquery

import (
	"fmt"
	"strings"
	"time"
)

// QueryResult 查询结果的结构化表示。
type QueryResult struct {
	Columns   []string
	Rows      [][]string
	RowCount  int
	Duration  time.Duration
	Truncated bool // 是否因超过上限被截断
}

// FormatMarkdown 将查询结果格式化为 Markdown 表格（便于 LLM 理解）。
func (r *QueryResult) FormatMarkdown() string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("查询成功 (%d 行, 耗时 %s)\n", r.RowCount, r.Duration.Round(time.Millisecond)))
	if r.Truncated {
		sb.WriteString("（结果已按行数上限截断）\n")
	}
	sb.WriteString("\n")

	if len(r.Columns) == 0 {
		sb.WriteString("（无列）\n")
		return sb.String()
	}

	// 表头
	sb.WriteString("| " + strings.Join(r.Columns, " | ") + " |\n")
	// 分隔行
	seps := make([]string, len(r.Columns))
	for i := range seps {
		seps[i] = "---"
	}
	sb.WriteString("| " + strings.Join(seps, " | ") + " |\n")
	// 数据行
	for _, row := range r.Rows {
		cells := make([]string, len(r.Columns))
		for i := range r.Columns {
			if i < len(row) {
				cells[i] = escapeMarkdownCell(row[i])
			} else {
				cells[i] = ""
			}
		}
		sb.WriteString("| " + strings.Join(cells, " | ") + " |\n")
	}

	return sb.String()
}

// escapeMarkdownCell 转义单元格中的管道符和换行，避免破坏表格结构。
func escapeMarkdownCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	// 限制单个单元格长度，避免超长内容
	if len([]rune(s)) > 200 {
		runes := []rune(s)
		s = string(runes[:200]) + "..."
	}
	return s
}
