// Tommy-Cat Agent HTTP Server — 多用户模式入口
// 提供 RESTful API，通过 X-User-ID 或 JWT 认证实现用户隔离。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/tommy-cat/agent/config"
	"github.com/tommy-cat/agent/internal/bootstrap"
	"github.com/tommy-cat/agent/internal/ctxmgr"
	"github.com/tommy-cat/agent/internal/engine"
	"github.com/tommy-cat/agent/internal/llm"
	"github.com/tommy-cat/agent/internal/search"
	"github.com/tommy-cat/agent/internal/security"
	"github.com/tommy-cat/agent/internal/server"
	"github.com/tommy-cat/agent/internal/session"
	"github.com/tommy-cat/agent/internal/skill"
	"github.com/tommy-cat/agent/internal/tool"
	"github.com/tommy-cat/agent/internal/trace"
)

func main() {
	fmt.Println("  Tommy-Cat Agent Server v0.2.0 (multi-user)")

	// 加载配置
	cfgPath := "config/config.yaml"
	if len(os.Args) > 1 {
		cfgPath = os.Args[1]
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Printf("  ⚠️  配置文件加载失败 (%v)，使用默认配置\n", err)
		cfg = config.Default()
	}

	// 初始化 LLM 网关
	gwCfg := cfg.ToGatewayConfig()
	gateway := llm.NewGatewayFromConfig(gwCfg)
	providers := gateway.ListProviders()
	fmt.Printf("  🔌 已加载 %d 个模型供应商\n", len(providers))

	// 初始化工具注册表
	registry := tool.NewRegistry()
	tool.RegisterBuiltinTools(registry, cfg.WorkDir)

	// 初始化搜索工具
	searchMgr := search.NewManager(cfg.Search)
	tool.RegisterSearchTool(registry, &srvSearchAdapter{mgr: searchMgr})
	fmt.Printf("  🔍 搜索引擎: %s\n", strings.Join(searchMgr.ListProviders(), ", "))

	// 构建数据源连接池与知识库，并注册 db_query / kb_* 工具
	dataTools := bootstrap.RegisterDataTools(cfg, registry)
	defer dataTools.Close()
	if dataTools.DBCount > 0 || dataTools.KBCount > 0 {
		fmt.Printf("  🗄️  已加载 %d 个数据源，%d 个知识库\n", dataTools.DBCount, dataTools.KBCount)
	}
	for _, w := range dataTools.Warnings {
		fmt.Printf("  ⚠️  %s\n", w)
	}

	// 连接 MCP Server 并注册远程工具（配置为空时跳过）
	mcpTools := bootstrap.RegisterMCPTools(context.Background(), cfg, registry)
	defer mcpTools.Close()
	if mcpTools.ServerCount > 0 {
		fmt.Printf("  🔗 已连接 %d 个 MCP Server，注册 %d 个远程工具\n", mcpTools.ServerCount, mcpTools.ToolCount)
	}
	for _, w := range mcpTools.Warnings {
		fmt.Printf("  ⚠️  %s\n", w)
	}

	// 初始化安全策略引擎（"YAML 优先、内置模板兜底"，避免同名策略双重加载）
	secEngine := security.NewEngine()
	policyData, _ := os.ReadFile(cfg.PolicyFile)
	if err := secEngine.LoadPolicies(policyData); err != nil {
		log.Printf("警告: 策略文件解析失败，已回退内置默认策略: %v", err)
	}

	// 工具调用安全门禁：HTTP 模式无法交互审批，require_approval 一律自动拒绝
	toolGate := session.NewToolGateAdapter(secEngine, func(_ context.Context, toolName, _, reason string) bool {
		log.Printf("警告: 工具 %q 需要人工审批，HTTP 模式无法交互，已自动拒绝（%s）", toolName, reason)
		return false
	})
	// 透传工具风险等级，使按 tool_risk 匹配的策略能够命中
	toolGate.SetRiskLookup(func(toolName string) int {
		if meta, ok := registry.Get(toolName); ok {
			return int(meta.RiskLevel)
		}
		return 0
	})

	// 最终输出安全门禁（敏感信息脱敏、输出审查）
	outputGate := session.NewOutputGateAdapter(secEngine)

	// 初始化 Skill 系统（store/matcher/generator 均需接入会话，否则整个 Skill 系统失效）
	skillStore := skill.NewStore(cfg.SkillStorePath)
	skillMatcher := skill.NewMatcher(skillStore)
	skillGen := skill.NewGenerator(skillStore)

	// 追踪 JSONL 导出（配置为空则禁用）
	var traceExporter *trace.Exporter
	if cfg.Engine.TraceExportPath != "" {
		exp, err := trace.NewExporter(cfg.Engine.TraceExportPath)
		if err != nil {
			fmt.Printf("  ⚠️  %v（禁用追踪导出）\n", err)
		} else {
			traceExporter = exp
			defer traceExporter.Close()
			fmt.Printf("  📈 追踪导出: %s\n", cfg.Engine.TraceExportPath)
		}
	}

	// 构建 SessionManager
	systemPrompt := cfg.Engine.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = session.DefaultBasePrompt
	}

	llmAdp := &llmAdapter{gateway: gateway}
	summarizer := ctxmgr.NewLLMSummarizer(llmAdp.Chat)

	// 加载 Agent 人格文件（缺失时使用内置兜底文本并警告）
	agentMD, err := session.LoadPersonaFile(cfg.Persona.AgentMDPath, session.DefaultAgentMD)
	if err != nil {
		fmt.Printf("  ⚠️  %v\n", err)
	}
	soulMD, err := session.LoadPersonaFile(cfg.Persona.SoulMDPath, session.DefaultSoulMD)
	if err != nil {
		fmt.Printf("  ⚠️  %v\n", err)
	}

	// 用户画像生成器（每完成 N 次任务用 LLM 更新 user.md，失败静默）
	profiler := session.NewUserProfiler(
		cfg.Persona.UserProfilesDir,
		cfg.Persona.ProfileUpdateIntervalRuns,
		func(ctx context.Context, messages []llm.Message) (string, error) {
			resp, err := llmAdp.Chat(ctx, messages, nil)
			if err != nil {
				return "", err
			}
			return resp.Content, nil
		},
	)

	deps := session.SessionDeps{
		LLM:             llmAdp,
		Tools:           registry,
		MaxIterations:   cfg.Engine.MaxIterations,
		SystemPrompt:    systemPrompt,
		MemorySize:      cfg.Session.MemorySize,
		CtxConfig:       ctxmgr.DefaultConfig(),
		Summarizer:      summarizer,
		Reflection:      cfg.Engine.Reflection.ToReflectionConfig(),
		ToolGate:        toolGate,
		OutputGate:      outputGate,
		TraceExporter:   traceExporter,
		AgentMD:         agentMD,
		SoulMD:          soulMD,
		UserProfilesDir: cfg.Persona.UserProfilesDir,
		Profiler:        profiler,
		// Skill 匹配：命中时将已验证的执行经验拼接到目标之前
		SkillHintProvider: func(input string) string {
			if matched, score := skillMatcher.Match(input); matched != nil && score > 0.6 {
				log.Printf("匹配到 Skill %q（置信度 %.0f%%），将参考其执行流程", matched.Name, score*100)
				return "可参考以下已验证的执行经验：\n" + matched.PromptHints
			}
			return ""
		},
		// 任务完成后自动生成并持久化 Skill（与 CLI 模式口径一致：步骤数 >= 3）
		OnTaskComplete: func(result *engine.ExecutionTrace) {
			if len(result.Steps) < 3 {
				return
			}
			traceJSON, err := skill.BuildTraceJSON(result)
			if err != nil {
				return
			}
			s, err := skillGen.GenerateFromTrace(traceJSON)
			if err != nil {
				return
			}
			if err := skillGen.Save(s); err != nil {
				log.Printf("警告: Skill 持久化失败: %v", err)
				return
			}
			log.Printf("已自动生成并持久化 Skill %q", s.Name)
		},
		RateLimit: session.RateLimitConfig{
			RequestsPerMinute: cfg.Session.RequestsPerMinute,
		},
	}

	smCfg := session.ManagerConfig{
		MaxSessions:     cfg.Session.MaxSessions,
		SessionTTL:      cfg.SessionTTLDuration(),
		CleanupInterval: cfg.SessionCleanupDuration(),
	}
	sessionMgr := session.NewSessionManager(smCfg, deps)
	defer sessionMgr.Shutdown()

	// 构建 HTTP 路由
	mux := http.NewServeMux()
	handler := server.NewHandler(sessionMgr)
	handler.RegisterRoutes(mux)

	// 包装认证中间件（api_key / jwt 模式必须配置密钥，缺失时拒绝启动）
	if cfg.Server.AuthMode == "api_key" && cfg.Server.AuthAPIKey == "" {
		log.Fatal("auth_mode=api_key 但未配置 server.auth_api_key，拒绝启动（不允许静默降级）")
	}
	if cfg.Server.AuthMode == "jwt" && cfg.Server.AuthJWTSecret == "" {
		log.Fatal("auth_mode=jwt 但未配置 server.auth_jwt_secret，拒绝启动（不允许静默降级）")
	}
	authed := server.AuthMiddlewareWithConfig(server.AuthConfig{
		Mode:      cfg.Server.AuthMode,
		APIKey:    cfg.Server.AuthAPIKey,
		JWTSecret: cfg.Server.AuthJWTSecret,
	})(mux)

	// chat 请求进入 handler 前做 task_start 策略评估（deny 直接返回 400）
	guarded := taskStartGuard(secEngine, authed)

	// HTTP 服务
	addr := cfg.Server.Addr
	srv := &http.Server{
		Addr:         addr,
		Handler:      guarded,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 300 * time.Second, // Agent 执行可能较长
		IdleTimeout:  120 * time.Second,
	}

	// 优雅退出
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		fmt.Println("\n  正在关闭服务...")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
		sessionMgr.Shutdown()
	}()

	fmt.Printf("  🚀 HTTP 服务启动于 %s (auth: %s)\n", addr, cfg.Server.AuthMode)
	fmt.Println("  端点: POST /api/v1/chat | GET /api/v1/history | POST /api/v1/clear | GET /api/v1/health")

	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		fmt.Printf("  ❌ 服务异常退出: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("  👋 服务已停止")
}

// taskStartGuard 在 chat 请求进入 handler 前做 task_start 安全策略评估：
// 命中 deny 策略时直接返回 400 中文错误，不再进入会话执行；
// 其他情况原样放行（请求体读完后会重新装回）。
func taskStartGuard(secEngine *security.Engine, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/chat" {
			body, err := io.ReadAll(r.Body)
			if err == nil {
				var req struct {
					Message string `json:"message"`
				}
				if json.Unmarshal(body, &req) == nil && req.Message != "" {
					decisions := secEngine.Evaluate(security.Checkpoint{
						Type:      "task_start",
						Content:   req.Message,
						Timestamp: time.Now(),
					})
					for _, d := range decisions {
						if d.Effect == security.EffectDeny {
							w.Header().Set("Content-Type", "application/json; charset=utf-8")
							w.WriteHeader(http.StatusBadRequest)
							_ = json.NewEncoder(w).Encode(map[string]string{
								"error": fmt.Sprintf("请求被安全策略拦截 [%s]: %s", d.PolicyID, d.Message),
							})
							return
						}
					}
				}
				// 重新装回请求体，交给后续 handler 正常解析
				r.Body = io.NopCloser(bytes.NewReader(body))
			}
		}
		next.ServeHTTP(w, r)
	})
}

// llmAdapter 将 llm.Gateway 适配为 engine.LLMClient 接口
type llmAdapter struct {
	gateway *llm.Gateway
}

func (a *llmAdapter) Chat(ctx context.Context, messages []llm.Message, tools []llm.ToolDef) (llm.ChatResponse, error) {
	req := llm.ChatRequest{
		Messages: messages,
		Tools:    tools,
	}
	return a.gateway.Chat(ctx, req)
}

// srvSearchAdapter 将 search.Manager 适配为 tool.Searcher 接口
type srvSearchAdapter struct {
	mgr *search.Manager
}

func (a *srvSearchAdapter) Search(ctx context.Context, query string, maxResults int) ([]tool.SearchResult, error) {
	results, err := a.mgr.Search(ctx, query, maxResults)
	if err != nil {
		return nil, err
	}
	out := make([]tool.SearchResult, len(results))
	for i, r := range results {
		out[i] = tool.SearchResult{
			Title:   r.Title,
			URL:     r.URL,
			Snippet: r.Snippet,
		}
	}
	return out, nil
}
