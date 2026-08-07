// Package bootstrap 负责根据应用配置构建数据源连接池、知识库管理器，
// 并将 db_query 与 kb_* 工具注册到工具注册表。
// CLI 与 HTTP 两种入口共享此逻辑，避免重复。
package bootstrap

import (
	"context"
	"fmt"
	"time"

	"github.com/tommy-cat/agent/config"
	"github.com/tommy-cat/agent/internal/kb"
	"github.com/tommy-cat/agent/internal/mcp"
	"github.com/tommy-cat/agent/internal/tool"
	"github.com/tommy-cat/agent/internal/tool/dbquery"
	"github.com/tommy-cat/agent/internal/tool/kbtools"
)

// Result 汇总 bootstrap 过程构建的资源，便于调用方做清理。
type Result struct {
	Pool      *dbquery.Pool
	KBManager *kb.Manager
	DBCount   int
	KBCount   int
	Warnings  []string
}

// Close 释放底层资源（数据库连接池）。
func (r *Result) Close() {
	if r.Pool != nil {
		r.Pool.Close()
	}
}

// RegisterDataTools 根据配置构建数据源池与知识库，并注册相关工具。
// 即使没有任何数据库/知识库配置，也会安全返回（不注册对应工具）。
func RegisterDataTools(cfg *config.Config, registry *tool.Registry) *Result {
	res := &Result{KBManager: kb.NewManager()}

	// ---- 数据库 ----
	if len(cfg.Databases) > 0 {
		pool := dbquery.NewPool()
		for name, entry := range cfg.Databases {
			dsCfg := toDataSourceConfig(name, entry)
			if err := pool.Register(dsCfg); err != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("数据源 %q 注册失败: %v", name, err))
				continue
			}
			res.DBCount++
		}
		res.Pool = pool
		// db_query 结果缓存：缺省启用（容量 200 / TTL 5 分钟），db_query_cache.enabled=false 关闭
		var qcache *dbquery.QueryCache
		if cfg.DBQueryCache == nil || cfg.DBQueryCache.Enabled {
			capacity, ttl := 0, time.Duration(0)
			if cfg.DBQueryCache != nil {
				capacity = cfg.DBQueryCache.Capacity
				if cfg.DBQueryCache.TTL != "" {
					if d, perr := time.ParseDuration(cfg.DBQueryCache.TTL); perr == nil {
						ttl = d
					} else {
						res.Warnings = append(res.Warnings, fmt.Sprintf("db_query_cache.ttl %q 非法，使用默认 5m", cfg.DBQueryCache.TTL))
					}
				}
			}
			qcache = dbquery.NewQueryCache(capacity, ttl)
		}
		// 注册 db_query 工具（只读，超时取 60s 上限，单查询内部另有 QueryTimeout）
		registry.Register(dbquery.NewDBQueryToolWithCache(pool, qcache), tool.RiskReadOnly, 60*time.Second)
	}

	// ---- 知识库 ----
	if len(cfg.KnowledgeBases) > 0 {
		kbCfgs := make([]kb.KBConfig, 0, len(cfg.KnowledgeBases))
		for _, e := range cfg.KnowledgeBases {
			kbCfgs = append(kbCfgs, toKBConfig(e))
		}
		built, errs := res.KBManager.Build(kbCfgs)
		for _, err := range errs {
			res.Warnings = append(res.Warnings, err.Error())
		}
		res.KBCount = len(built)
		// 注册 kb_* 工具（只读）
		registry.Register(kbtools.NewKBSearchTool(res.KBManager), tool.RiskReadOnly, 30*time.Second)
		registry.Register(kbtools.NewKBReadTool(res.KBManager), tool.RiskReadOnly, 30*time.Second)
		registry.Register(kbtools.NewKBListTool(res.KBManager), tool.RiskReadOnly, 30*time.Second)
	}

	return res
}

// toDataSourceConfig 将 YAML 配置转换为 dbquery.DataSourceConfig。
func toDataSourceConfig(name string, e config.DatabaseEntry) dbquery.DataSourceConfig {
	cfg := dbquery.DataSourceConfig{
		Name:          name,
		Driver:        e.Driver,
		DSN:           e.DSN,
		MaxOpenConns:  e.MaxOpenConns,
		MaxIdleConns:  e.MaxIdleConns,
		MaxRows:       e.MaxRows,
		AllowUnion:    e.AllowUnion,
		AllowedTables: e.AllowedTables,
		DeniedColumns: e.DeniedColumns,
	}
	if e.ConnMaxLifetime != "" {
		if d, err := time.ParseDuration(e.ConnMaxLifetime); err == nil {
			cfg.ConnMaxLifetime = d
		}
	}
	if e.QueryTimeout != "" {
		if d, err := time.ParseDuration(e.QueryTimeout); err == nil {
			cfg.QueryTimeout = d
		}
	}
	// AllowSubquery 默认 true（指针为 nil 时）
	if e.AllowSubquery == nil {
		cfg.AllowSubquery = true
	} else {
		cfg.AllowSubquery = *e.AllowSubquery
	}
	return cfg
}

// toKBConfig 将 YAML 配置转换为 kb.KBConfig。
func toKBConfig(e config.KnowledgeBaseEntry) kb.KBConfig {
	return kb.KBConfig{
		Name:       e.Name,
		Paths:      e.Paths,
		Exclude:    e.Exclude,
		Extensions: e.Extensions,
		Strategy:   e.Strategy,
		MaxTokens:  e.MaxTokens,
		Overlap:    e.Overlap,
		MaxFileMB:  e.MaxFileMB,
		TopK:       e.TopK,
	}
}

// MCPResult 汇总 MCP 装配结果，便于调用方做清理。
type MCPResult struct {
	// Manager MCP 连接管理器（未配置时为 nil）
	Manager *mcp.Manager
	// ServerCount 成功连接的 server 数
	ServerCount int
	// ToolCount 注册的远程工具数
	ToolCount int
	// Warnings 连接失败的告警信息（不中断启动）
	Warnings []string
}

// Close 关闭所有 MCP 连接。
func (r *MCPResult) Close() {
	if r.Manager != nil {
		r.Manager.CloseAll()
	}
}

// RegisterMCPTools 根据配置连接 MCP Server 并将其工具注册到工具注册表。
// 配置为空时直接跳过；单个 server 连接失败只记录告警，不中断启动。
func RegisterMCPTools(ctx context.Context, cfg *config.Config, registry *tool.Registry) *MCPResult {
	res := &MCPResult{}
	if len(cfg.MCP.Servers) == 0 {
		return res
	}

	configs := make([]mcp.ClientConfig, 0, len(cfg.MCP.Servers))
	for _, e := range cfg.MCP.Servers {
		configs = append(configs, toMCPClientConfig(e))
	}

	mgr := mcp.NewManager()
	for _, err := range mgr.ConnectAll(ctx, configs) {
		res.Warnings = append(res.Warnings, fmt.Sprintf("MCP server 连接失败: %v", err))
	}
	res.ServerCount = len(mgr.Clients())
	res.ToolCount = mgr.RegisterTools(registry)
	res.Manager = mgr
	return res
}

// toMCPClientConfig 将 YAML 配置转换为 mcp.ClientConfig。
func toMCPClientConfig(e config.MCPServerEntry) mcp.ClientConfig {
	cfg := mcp.ClientConfig{
		Name:             e.Name,
		Transport:        e.Transport,
		Command:          e.Command,
		Args:             e.Args,
		Env:              e.Env,
		WorkDir:          e.WorkDir,
		URL:              e.URL,
		Headers:          e.Headers,
		ToolPrefix:       e.ToolPrefix,
		DefaultRiskLevel: e.RiskLevel,
	}
	if e.TimeoutSeconds > 0 {
		cfg.Timeout = time.Duration(e.TimeoutSeconds) * time.Second
	}
	return cfg
}
