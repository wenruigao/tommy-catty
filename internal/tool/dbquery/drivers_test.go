package dbquery

import (
	"database/sql"
	"testing"
)

// TestDriversRegistered 回归 P0 缺陷：生产二进制曾未导入任何数据库驱动，
// 导致配置任何数据源都会在首次查询时报 unknown driver。
// 驱动通过 drivers.go 的空导入在 init 中注册。
func TestDriversRegistered(t *testing.T) {
	registered := make(map[string]bool)
	for _, name := range sql.Drivers() {
		registered[name] = true
	}
	for _, want := range []string{"mysql", "postgres", "sqlite"} {
		if !registered[want] {
			t.Errorf("驱动 %q 未注册（db_query 在生产产物中将不可用）", want)
		}
	}
}
