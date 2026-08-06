// Package dbquery — 数据库驱动注册。
//
// database/sql 需要驱动在 init 中注册后才能通过 sql.Open 使用。
// 生产二进制必须显式空导入驱动包，否则任何数据源在首次查询时
// 都会报 "unknown driver"。sqlite 使用纯 Go 实现（modernc.org/sqlite），
// 无 cgo 依赖；mysql/postgres 分别使用社区主流驱动。
package dbquery

import (
	_ "github.com/go-sql-driver/mysql" // 注册 "mysql" 驱动
	_ "github.com/lib/pq"              // 注册 "postgres" 驱动
	_ "modernc.org/sqlite"             // 注册 "sqlite" 驱动（纯 Go）
)
