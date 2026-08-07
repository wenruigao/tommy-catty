// Tommy-Cat Agent — 通用任务智能体
// 基于 ReAct 循环的 AI Agent，支持多模型、工具调用、记忆系统、安全策略和 Skill 自动生成
// 支持 CLI（单用户）和 HTTP（多用户）两种运行模式
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/tommy-cat/agent/config"
	"github.com/tommy-cat/agent/internal/bootstrap"
	"github.com/tommy-cat/agent/internal/ctxmgr"
	"github.com/tommy-cat/agent/internal/doctor"
	"github.com/tommy-cat/agent/internal/engine"
	"github.com/tommy-cat/agent/internal/llm"
	"github.com/tommy-cat/agent/internal/search"
	"github.com/tommy-cat/agent/internal/security"
	"github.com/tommy-cat/agent/internal/session"
	"github.com/tommy-cat/agent/internal/skill"
	"github.com/tommy-cat/agent/internal/tool"
	"github.com/tommy-cat/agent/internal/trace"
)

const banner = `
  _____                       _        ____      _
 |_   _|__  _ __ ___  _ __ ___   __ _   / ___|__ _| |_
   | |/ _ \| '_ ` + "`" + ` _ \| '_ ` + "`" + ` _ \ / _` + "`" + ` | | |   / _` + "`" + ` | __|
   | | (_) | | | | | | | | | | | (_| | | |__| (_| | |_
   |_|\___/|_| |_| |_|_| |_| |_|\__,_|  \____\__,_|\__|

  通用任务智能体 v0.2.0 (multi-user)
  输入任务描述开始执行，输入 /quit 退出，/help 查看帮助
`

func main() {
	fmt.Print(banner)

	// 加载配置（含覆盖层 config.local.yaml）
	cfg, cfgPath := loadConfig()

	// 安全审计日志（/config 变更与策略决策可追溯；路径为空则禁用）
	auditLogger, aerr := security.NewAuditLogger(cfg.AuditLogPath)
	if aerr != nil {
		fmt.Printf("  ⚠️  审计日志初始化失败: %v\n", aerr)
	}
	defer auditLogger.Close()

	// 初始化 LLM 网关
	gateway := initLLMGateway(cfg)

	// 初始化工具注册表
	registry := tool.NewRegistry()
	tool.RegisterBuiltinTools(registry, cfg.WorkDir)

	// 初始化搜索工具
	searchMgr := search.NewManager(cfg.Search)
	tool.RegisterSearchTool(registry, &searchAdapter{mgr: searchMgr})
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

	// 初始化安全策略引擎
	secEngine := initSecurityEngine(cfg)
	secEngine.SetAuditLogger(auditLogger)

	// /config 命令执行器（覆盖层持久化）
	cfgMgr := newConfigManager(cfgPath, cfg, auditLogger)

	// 工具调用安全门禁（策略评估 + 终端交互式审批）
	toolGate := session.NewToolGateAdapter(secEngine, interactiveApprover)
	// 透传工具风险等级，使按 tool_risk 匹配的策略能够命中
	toolGate.SetRiskLookup(func(toolName string) int {
		if meta, ok := registry.Get(toolName); ok {
			return int(meta.RiskLevel)
		}
		return 0
	})

	// 最终输出安全门禁（敏感信息脱敏、输出审查）
	outputGate := session.NewOutputGateAdapter(secEngine)

	// 初始化 Skill 系统（版本快照 + 生成门控，与 HTTP 模式口径一致）
	skillStore := skill.NewStore(cfg.SkillStorePath)
	skillMatcher := skill.NewMatcher(skillStore)
	skillGen := skill.NewGenerator(skillStore)
	skillGen.SetVersionManager(skill.NewVersionManager())
	genGate := skill.NewGenerationGate()

	// 初始化追踪器（CLI 模式全局）
	tracer := trace.NewTracer()

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

	llmAdapter := &llmClientAdapter{gateway: gateway}

	// 初始化摘要生成器（用于上下文压缩）
	summarizer := ctxmgr.NewLLMSummarizer(llmAdapter.Chat)

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
			resp, err := llmAdapter.Chat(ctx, messages, nil)
			if err != nil {
				return "", err
			}
			return resp.Content, nil
		},
	)

	// 初始化 SessionManager（统一管理用户会话）
	deps := session.SessionDeps{
		LLM:             llmAdapter,
		Tools:           registry,
		MaxIterations:   cfg.Engine.MaxIterations,
		SystemPrompt:    buildSystemPrompt(cfg),
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
				fmt.Printf("  💡 匹配到 Skill「%s」(置信度 %.0f%%)，将参考其执行流程\n", matched.Name, score*100)
				return "可参考以下已验证的执行经验：\n" + matched.PromptHints
			}
			return ""
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

	// 启动时快速自检
	startupQuickCheck(cfg)

	// 交互式 REPL（CLI 模式，userID 固定为 "local"）
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 优雅退出
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\n正在退出...")
		cancel()
		sessionMgr.Shutdown()
		os.Exit(0)
	}()

	// 获取 "local" 用户会话
	localSession := sessionMgr.GetOrCreate("local")

	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("\n🐱 > ")
		if !scanner.Scan() {
			break
		}
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}

		// 命令处理
		if strings.HasPrefix(input, "/") {
			handleCommand(input, cfg, skillStore, skillMatcher, skillGen, secEngine, tracer, localSession, cfgMgr)
			continue
		}

		// 安全策略检查（任务开始）
		decisions := secEngine.Evaluate(security.Checkpoint{
			Type:    "task_start",
			Content: input,
			UserID:  "local",
		})
		if blocked := checkDecisions(decisions); blocked {
			continue
		}

		// 执行任务（通过 session 串行，Skill 匹配提示由 SkillHintProvider 注入）
		fmt.Println("  ⏳ 正在执行...")
		result, err := localSession.Run(ctx, input)
		if err != nil {
			if err == session.ErrRateLimited {
				fmt.Println("  ⏸️  请求过于频繁，请稍后再试。")
			} else {
				fmt.Printf("  ❌ 执行失败: %v\n", err)
			}
			continue
		}

		// 输出结果
		fmt.Printf("\n  ✅ 完成 (耗时 %s, Token: %d)\n",
			result.EndTime.Sub(result.StartTime).Round(1e6), result.TokenUsage)
		if len(result.Steps) > 0 {
			fmt.Printf("  📝 %s\n", result.Steps[len(result.Steps)-1].FinalAnswer)
		}

		// 安全策略检查（任务结束）
		secEngine.Evaluate(security.Checkpoint{
			Type:   "task_end",
			UserID: "local",
			Cost:   float64(result.TokenUsage) * llm.CostPerToken,
		})

		// 尝试自动生成 Skill（经 GenerationGate 门控）
		tryGenerateSkill(skillGen, genGate, result)
	}
}

// loadConfig 加载配置（主配置 + 覆盖层 config.local.yaml），并返回主配置路径
func loadConfig() (*config.Config, string) {
	cfgPath := "config/config.yaml"
	if len(os.Args) > 1 {
		cfgPath = os.Args[1]
	}
	cfg, err := config.LoadWithOverlay(cfgPath)
	if err != nil {
		fmt.Printf("  ⚠️  配置文件加载失败 (%v)，使用默认配置\n", err)
		return config.Default(), cfgPath
	}
	return cfg, cfgPath
}

// initLLMGateway 初始化 LLM 网关
func initLLMGateway(cfg *config.Config) *llm.Gateway {
	gwCfg := cfg.ToGatewayConfig()
	gw := llm.NewGatewayFromConfig(gwCfg)

	providers := gw.ListProviders()
	if len(providers) == 0 {
		fmt.Println("  ⚠️  未配置任何 LLM 供应商，请在 config.yaml 的 llm.providers 中添加")
	} else {
		fmt.Printf("  🔌 已加载 %d 个模型供应商: %s\n", len(providers), strings.Join(providers, ", "))
	}
	return gw
}

// initSecurityEngine 初始化安全策略引擎。
// 采用"YAML 优先、内置模板兜底"策略：policy.yaml 存在时仅加载 YAML 配置，
// 缺失或无策略时才回退到内置默认模板，避免同名策略双重加载。
func initSecurityEngine(cfg *config.Config) *security.Engine {
	eng := security.NewEngine()

	data, _ := os.ReadFile(cfg.PolicyFile)
	if err := eng.LoadPolicies(data); err != nil {
		fmt.Printf("  ⚠️  策略文件解析失败，已回退内置默认策略: %v\n", err)
	}

	return eng
}

// buildSystemPrompt 构建系统提示词
func buildSystemPrompt(cfg *config.Config) string {
	if cfg.Engine.SystemPrompt != "" {
		return cfg.Engine.SystemPrompt
	}
	return session.DefaultBasePrompt
}

// handleCommand 处理斜杠命令
func handleCommand(input string, cfg *config.Config, store *skill.Store, matcher *skill.Matcher, gen *skill.Generator, sec *security.Engine, tracer *trace.Tracer, sess *session.UserSession, cfgMgr *configManager) {
	cmd := strings.Fields(input)
	switch cmd[0] {
	case "/quit", "/exit", "/q":
		fmt.Println("  👋 再见！")
		os.Exit(0)
	case "/help":
		fmt.Println(`  可用命令:
    /help          显示帮助
    /quit          退出程序
    /doctor        健康自检（检测问题并自动修复）
    /skills        列出所有 Skill
    /skill <id>    查看 Skill 详情
    /policies      列出安全策略
    /trace         查看最近一次执行的追踪信息
    /clear         清空记忆
    /config        查看/管理配置（get/set/unset/use/path）`)
	case "/doctor":
		runDoctor(cfg)
	case "/skills":
		skills := store.List()
		if len(skills) == 0 {
			fmt.Println("  暂无 Skill，完成任务后会自动生成。")
		}
		for _, s := range skills {
			fmt.Printf("  [%s] %s (v%s, 使用%d次, 成功率%.0f%%)\n",
				s.ID[:8], s.Name, s.Version, s.UsageCount, s.SuccessRate*100)
		}
	case "/policies":
		fmt.Println("  安全策略已加载（使用内置模板 + 自定义配置）")
	case "/trace":
		spans := sess.Tracer().GetSpans()
		if len(spans) == 0 {
			fmt.Println("  暂无追踪数据。")
		}
		for _, s := range spans {
			fmt.Printf("  [%s] %s (%s) %s\n",
				s.SpanID, s.Name, s.EndTime.Sub(s.StartTime).Round(1e6), s.Status)
		}
	case "/config":
		handleConfigCommand(cmd[1:], cfgMgr)
	case "/clear":
		sess.ClearMemory()
		fmt.Println("  记忆已清空。")
	default:
		fmt.Printf("  未知命令: %s，输入 /help 查看帮助\n", cmd[0])
	}
}

// interactiveApprover 交互式审批回调：在终端打印工具名/参数摘要/原因，
// 提示用户输入 y/n 决定是否放行。审批时新建 reader 读取 stdin，
// 避免与主输入循环的 bufio.Scanner 产生缓冲冲突。
func interactiveApprover(_ context.Context, toolName, argsSummary, reason string) bool {
	fmt.Printf("  ⚠️  工具调用需要审批\n")
	fmt.Printf("      工具: %s\n", toolName)
	fmt.Printf("      参数: %s\n", argsSummary)
	fmt.Printf("      原因: %s\n", reason)
	fmt.Print("  是否允许执行？(y/N): ")
	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	approved := strings.EqualFold(strings.TrimSpace(answer), "y")
	if !approved {
		fmt.Println("  已拒绝本次工具调用。")
	}
	return approved
}

// checkDecisions 检查安全策略决策，返回是否被阻止
func checkDecisions(decisions []security.Decision) bool {
	for _, d := range decisions {
		switch d.Effect {
		case security.EffectDeny:
			fmt.Printf("  🚫 安全策略拦截 [%s]: %s\n", d.PolicyID, d.Message)
			return true
		case security.EffectRequireApproval:
			fmt.Printf("  ⚠️  需要确认 [%s]: %s\n", d.PolicyID, d.Message)
			fmt.Print("  是否继续？(y/N): ")
			var answer string
			fmt.Scanln(&answer)
			if strings.ToLower(answer) != "y" {
				fmt.Println("  已取消执行。")
				return true
			}
		case security.EffectThrottle:
			fmt.Printf("  ⏸️  限流 [%s]: %s\n", d.PolicyID, d.Message)
		}
	}
	return false
}

// tryGenerateSkill 尝试从执行结果自动生成 Skill 并持久化。
// 追踪转换逻辑复用 skill.BuildTraceJSON（与 HTTP 模式一致）。
// 经 GenerationGate 门控：goal 指纹去重 + 步骤数 >= 3 + 耗时 >= 30s + 日配额 10，
// 避免相似任务重复执行时持续产出近似重复的 Skill。
func tryGenerateSkill(gen *skill.Generator, gate *skill.GenerationGate, result *engine.ExecutionTrace) {
	fingerprint := skill.GoalFingerprint(result.Goal)
	if !gate.ShouldGenerate(fingerprint, len(result.Steps), result.EndTime.Sub(result.StartTime)) {
		return
	}
	traceJSON, err := skill.BuildTraceJSON(result)
	if err != nil {
		return
	}
	s, err := gen.GenerateFromTrace(traceJSON)
	if err != nil {
		return
	}
	// 持久化：缺少 Save 会导致技能丢失，/skills 永远为空、匹配器无技能可用
	if err := gen.Save(s); err != nil {
		fmt.Printf("  ⚠️  Skill 持久化失败: %v\n", err)
		return
	}
	gate.MarkGenerated(fingerprint)
	fmt.Printf("  🧠 已自动生成并持久化 Skill「%s」\n", s.Name)
}

// runDoctor 执行完整健康自检
func runDoctor(cfg *config.Config) {
	d := doctor.New()
	doctor.RegisterAllChecks(d, buildDoctorConfig(cfg))
	report := d.RunAll(context.Background())
	fmt.Print(report.Format())
}

// startupQuickCheck 启动时快速自检
func startupQuickCheck(cfg *config.Config) {
	d := doctor.New()
	doctor.RegisterAllChecks(d, buildDoctorConfig(cfg))
	report := d.RunQuick(context.Background())

	if report.TotalError > 0 {
		fmt.Printf("  ⚠️  启动自检发现 %d 个严重问题，建议运行 /doctor 查看详情\n", report.TotalError)
	}
}

// buildDoctorConfig 从应用配置构建 Doctor 检查配置
func buildDoctorConfig(cfg *config.Config) doctor.DoctorConfig {
	providers := make(map[string]doctor.ProviderCheckInfo)
	for name, entry := range cfg.LLM.Providers {
		providers[name] = doctor.ProviderCheckInfo{
			BaseURL: entry.BaseURL,
			APIKey:  entry.APIKey,
			Model:   entry.Model,
		}
	}
	return doctor.DoctorConfig{
		ConfigPath:     "config/config.yaml",
		PolicyPath:     cfg.PolicyFile,
		SkillStorePath: cfg.SkillStorePath,
		WorkDir:        cfg.WorkDir,
		Providers:      providers,
	}
}

// llmClientAdapter 将 llm.Gateway 适配为 engine.LLMClient 接口
type llmClientAdapter struct {
	gateway *llm.Gateway
}

func (a *llmClientAdapter) Chat(ctx context.Context, messages []llm.Message, tools []llm.ToolDef) (llm.ChatResponse, error) {
	req := llm.ChatRequest{
		Messages: messages,
		Tools:    tools,
	}
	return a.gateway.Chat(ctx, req)
}

// searchAdapter 将 search.Manager 适配为 tool.Searcher 接口
type searchAdapter struct {
	mgr *search.Manager
}

func (a *searchAdapter) Search(ctx context.Context, query string, maxResults int) ([]tool.SearchResult, error) {
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
