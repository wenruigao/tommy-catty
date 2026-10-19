// Package bootstrap 负责根据应用配置构建数据源连接池、知识库管理器，
// 并将 db_query 与 kb_* 工具注册到工具注册表。
// CLI 与 HTTP 两种入口共享此逻辑，避免重复。
package bootstrap

import (
	"context"
	"fmt"
	"time"

	"github.com/wenruigao/tommy-catty/config"
	"github.com/wenruigao/tommy-catty/internal/engine"
	"github.com/wenruigao/tommy-catty/internal/kb"
	"github.com/wenruigao/tommy-catty/internal/mcp"
	"github.com/wenruigao/tommy-catty/internal/multiagent"
	"github.com/wenruigao/tommy-catty/internal/router"
	"github.com/wenruigao/tommy-catty/internal/sandbox"
	"github.com/wenruigao/tommy-catty/internal/tool"
	"github.com/wenruigao/tommy-catty/internal/tool/dbquery"
	"github.com/wenruigao/tommy-catty/internal/tool/kbtools"
	"github.com/wenruigao/tommy-catty/internal/trace"
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

// RegisterBuiltinTools 构建内置工具沙箱并注册全部内置工具（CLI 与 HTTP 入口共享）。
// 沙箱类型取 cfg.Sandbox.Type（none 直通 / native 原生 / container 容器）；
// 沙箱不可用时按 cfg.Sandbox.OnUnavailable 处理：
//   - degrade（默认）：降级为直通沙箱并返回告警
//   - error：返回错误（调用方应终止启动，保证不出现无隔离执行）
//
// 返回值为降级/能力告警列表（沙箱未启用时为空）。
func RegisterBuiltinTools(cfg *config.Config, registry *tool.Registry) ([]string, error) {
	kind := cfg.Sandbox.Type
	if kind == "" {
		kind = "none"
	}

	var warnings []string
	sb := sandbox.New(kind, cfg.Sandbox.ToSandbox())
	if kind == "native" || kind == "container" {
		if err := sb.Available(); err != nil {
			if cfg.Sandbox.OnUnavailable == "error" {
				return nil, fmt.Errorf("沙箱 %s 不可用: %w", kind, err)
			}
			// degrade：降级为直通沙箱
			sb = sandbox.NewNone()
			warnings = append(warnings, fmt.Sprintf("沙箱 %s 不可用（%v），已降级为 none 直通执行", kind, err))
		}
	}
	// 沙箱实现自身的能力告警（如缺少 prlimit、限额过小）
	if w, ok := sb.(sandbox.Warner); ok {
		warnings = append(warnings, w.Warnings()...)
	}
	if kind != "none" && len(warnings) == 0 {
		warnings = append(warnings, fmt.Sprintf("沙箱已启用: %s（禁网: %v）", sb.Name(), !cfg.Sandbox.AllowNet))
	}

	timeout := time.Duration(cfg.Sandbox.TimeoutSeconds) * time.Second
	tool.RegisterBuiltinToolsWithSandbox(registry, cfg.WorkDir, sb, timeout)
	return warnings, nil
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

// RegisterMultiAgentTools 根据配置构建多 Agent 协作编排器，并注册 delegate_task 工具。
// 未启用（multi_agent.enabled=false）或角色为空时直接跳过。
// llmClient 为编排器使用的 LLM 客户端（通常与主 Engine 共享同一网关适配器）；
// toolGate 为安全门禁（Worker 继承，保证安全策略不降级）；
// tracer 为追踪器（可为 nil）。
func RegisterMultiAgentTools(
	cfg *config.Config,
	registry *tool.Registry,
	llmClient engine.LLMClient,
	toolGate engine.ToolGate,
	tracer *trace.Tracer,
) ([]string, error) {
	if !cfg.MultiAgent.Enabled {
		return nil, nil
	}
	if len(cfg.MultiAgent.Roles) == 0 {
		return []string{"多 Agent 已启用但未定义角色，跳过"}, nil
	}

	// 构建角色注册表
	roles := make(map[string]*multiagent.RoleDef, len(cfg.MultiAgent.Roles))
	for name, entry := range cfg.MultiAgent.Roles {
		roles[name] = &multiagent.RoleDef{
			Name:          name,
			Description:   entry.Description,
			SystemPrompt:  entry.SystemPrompt,
			Tools:         entry.Tools,
			Model:         entry.Model,
			MaxIterations: entry.MaxIterations,
			MaxConcurrent: entry.MaxConcurrent,
		}
	}

	// 校验角色定义
	if err := multiagent.ValidateRoles(roles); err != nil {
		return nil, fmt.Errorf("多 Agent 角色配置校验失败: %w", err)
	}

	// 构建编排器配置
	orchCfg := multiagent.OrchestratorConfig{
		MaxWorkers:       cfg.MultiAgent.Orchestrator.MaxWorkers,
		MaxSubTasks:      cfg.MultiAgent.Orchestrator.MaxSubTasks,
		WorkerTimeout:    cfg.MultiAgentWorkerTimeout(),
		SummaryMaxTokens: cfg.MultiAgent.Orchestrator.SummaryMaxTokens,
	}

	// 创建编排器并注册 delegate_task 工具
	orch := multiagent.NewOrchestrator(llmClient, roles, registry, orchCfg, toolGate, tracer)
	registry.Register(multiagent.NewDelegateTaskTool(orch), tool.RiskReadOnly, 300*time.Second)

	roleNames := make([]string, 0, len(roles))
	for name := range roles {
		roleNames = append(roleNames, name)
	}
	return roleNames, nil
}

// RegisterRouter 根据配置构建意图路由器，返回 (路由器, 告警列表, error)。
//
// 未启用（router.enabled=false）时返回 (nil, nil, nil)，调用方据此跳过路由注入，
// 行为与旧版完全一致；已启用但未定义 Agent 时返回告警并跳过。
//
// 两类问题区别对待：
//
//   - 配置本身错误 → 返回 error（应拒绝启动，尽早暴露）：
//     Agent 名与键不一致、缺少 description/system_prompt、兜底 Agent 未定义、
//     patterns 正则编译失败。
//   - 工具白名单引用了当前未注册的工具 → 仅告警，不阻断启动。
//     因为 web_search / db_query / kb_* 等工具是**有条件注册**的（分别依赖
//     search / databases / knowledge_bases 配置段），未配相应能力时它们本就不存在。
//     白名单无法凭空授予能力（ToToolDefs 只返回注册表中真实存在的工具），
//     故这不是安全问题；且路由定位为「尽力而为的增强」，不应因此拒绝启动。
//     告警仍会打印，以便发现真正的拼写错误。
func RegisterRouter(
	cfg *config.Config,
	registry *tool.Registry,
	llmClient engine.LLMClient,
) (*router.Router, []string, error) {
	if !cfg.Router.Enabled {
		return nil, nil, nil
	}
	if len(cfg.Router.Agents) == 0 {
		return nil, []string{"意图路由已启用但未定义 Agent，跳过"}, nil
	}

	agents := make(map[string]*router.AgentSpec, len(cfg.Router.Agents))
	for name, entry := range cfg.Router.Agents {
		agents[name] = &router.AgentSpec{
			Name:          name,
			Description:   entry.Description,
			SystemPrompt:  entry.SystemPrompt,
			Tools:         entry.Tools,
			Keywords:      entry.Keywords,
			Patterns:      entry.Patterns,
			Priority:      entry.Priority,
			MaxIterations: entry.MaxIterations,
		}
	}

	// 收集未注册工具的告警（不阻断启动，理由见函数注释）
	var warnings []string
	registered := make(map[string]bool, len(registry.List()))
	for _, m := range registry.List() {
		registered[m.Name()] = true
	}
	for name, spec := range agents {
		missing := make([]string, 0)
		for _, t := range spec.Tools {
			if !registered[t] {
				missing = append(missing, t)
			}
		}
		if len(missing) > 0 {
			warnings = append(warnings, fmt.Sprintf(
				"路由 Agent %q 的工具白名单含当前未注册的工具 %v（可能未配置对应能力，或存在拼写错误）；"+
					"这些工具不会生效，其余工具正常", name, missing))
		}
	}

	r, err := router.NewRouter(agents, router.Config{
		Enabled:             true,
		LLMTimeout:          cfg.RouterLLMTimeout(),
		ConfidenceThreshold: cfg.Router.ConfidenceThreshold,
		FallbackAgent:       cfg.Router.FallbackAgent,
		UseLLM:              cfg.RouterUseLLM(),
	}, llmClient)
	if err != nil {
		return nil, warnings, fmt.Errorf("意图路由配置校验失败: %w", err)
	}

	return r, warnings, nil
}
