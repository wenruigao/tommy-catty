package dbquery

import "time"

// DataSourceConfig 单个数据源的配置（对应 config.yaml 的 databases 条目）。
type DataSourceConfig struct {
	// Name 数据源名称（map key）。
	Name string
	// Driver 驱动类型：mysql | postgres | sqlite
	Driver string
	// DSN 数据源连接串。
	DSN string

	// MaxOpenConns 连接池最大打开连接数。
	MaxOpenConns int
	// MaxIdleConns 连接池最大空闲连接数。
	MaxIdleConns int
	// ConnMaxLifetime 连接最大存活时间。
	ConnMaxLifetime time.Duration
	// QueryTimeout 单条查询超时。
	QueryTimeout time.Duration

	// MaxRows 结果集行数硬上限。
	MaxRows int
	// AllowUnion 是否允许 UNION 查询。
	AllowUnion bool
	// AllowSubquery 是否允许子查询。
	AllowSubquery bool

	// AllowedTables 表名白名单（支持通配符）。
	AllowedTables []string
	// DeniedColumns 列黑名单（格式 table.column 或 *.column）。
	DeniedColumns []string
}

// applyDefaults 填充默认值。
func (c *DataSourceConfig) applyDefaults() {
	if c.MaxOpenConns <= 0 {
		c.MaxOpenConns = 5
	}
	if c.MaxIdleConns <= 0 {
		c.MaxIdleConns = 2
	}
	if c.ConnMaxLifetime <= 0 {
		c.ConnMaxLifetime = 30 * time.Minute
	}
	if c.QueryTimeout <= 0 {
		c.QueryTimeout = 30 * time.Second
	}
	if c.MaxRows <= 0 {
		c.MaxRows = 500
	}
}

// toValidateConfig 转换为验证器配置。
func (c *DataSourceConfig) toValidateConfig() ValidateConfig {
	return ValidateConfig{
		AllowedTables: c.AllowedTables,
		DeniedColumns: c.DeniedColumns,
		AllowUnion:    c.AllowUnion,
		AllowSubquery: c.AllowSubquery,
		MaxRows:       c.MaxRows,
	}
}
