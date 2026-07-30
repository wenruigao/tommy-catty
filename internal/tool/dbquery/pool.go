package dbquery

import (
	"database/sql"
	"fmt"
	"sync"
)

// Pool 管理多个数据源的连接池。
type Pool struct {
	dbs map[string]*sql.DB
	cfg map[string]*DataSourceConfig
	mu  sync.RWMutex
}

// NewPool 创建连接池管理器。
func NewPool() *Pool {
	return &Pool{
		dbs: make(map[string]*sql.DB),
		cfg: make(map[string]*DataSourceConfig),
	}
}

// driverNames 将配置驱动名映射到 database/sql 注册的驱动名。
var driverNames = map[string]string{
	"mysql":    "mysql",
	"postgres": "postgres",
	"sqlite":   "sqlite",
	"sqlite3":  "sqlite",
}

// Register 注册并初始化一个数据源连接池。
// 连接是惰性建立的（sql.Open 不立即连接），首次查询时才真正连接。
func (p *Pool) Register(cfg DataSourceConfig) error {
	cfg.applyDefaults()

	driverName, ok := driverNames[cfg.Driver]
	if !ok {
		return fmt.Errorf("不支持的数据库驱动: %s（支持 mysql/postgres/sqlite）", cfg.Driver)
	}

	db, err := sql.Open(driverName, cfg.DSN)
	if err != nil {
		return fmt.Errorf("打开数据源 %q 失败: %w", cfg.Name, err)
	}

	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime)

	p.mu.Lock()
	defer p.mu.Unlock()
	// 关闭已存在的同名连接
	if old, exists := p.dbs[cfg.Name]; exists {
		_ = old.Close()
	}
	p.dbs[cfg.Name] = db
	p.cfg[cfg.Name] = &cfg
	return nil
}

// Get 获取指定数据源的连接和配置。
func (p *Pool) Get(name string) (*sql.DB, *DataSourceConfig, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	db, ok := p.dbs[name]
	if !ok {
		return nil, nil, fmt.Errorf("数据源 %q 未配置", name)
	}
	return db, p.cfg[name], nil
}

// Names 返回所有已注册的数据源名称。
func (p *Pool) Names() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	names := make([]string, 0, len(p.dbs))
	for name := range p.dbs {
		names = append(names, name)
	}
	return names
}

// Close 关闭所有连接池。
func (p *Pool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, db := range p.dbs {
		_ = db.Close()
	}
	p.dbs = make(map[string]*sql.DB)
	p.cfg = make(map[string]*DataSourceConfig)
}
