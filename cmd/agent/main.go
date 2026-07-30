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

	// 加载配置
	cfg := loadConfig()

	// 初始化 LLM 网关
	gateway := initLLMGateway(cfg)

	// 初始化工具注册表
	registry := tool.NewRegistry()
	tool.RegisterBuiltinTools(registry)

	// 构建数据源连接池与知识库，并注册 db_query / kb_* 工具
	dataTools := bootstrap.RegisterDataTools(cfg, registry)
	defer dataTools.Close()
	if dataTools.DBCount > 0 || dataTools.KBCount > 0 {
		fmt.Printf("  🗄️  已加载 %d 个数据源，%d 个知识库\n", dataTools.DBCount, dataTools.KBCount)
	}
	for _, w := range dataTools.Warnings {
		fmt.Printf("  ⚠️  %s\n", w)
	}

	// 初始化安全策略引擎
	secEngine := initSecurityEngine(cfg)

	// 初始化 Skill 系统
	skillStore := skill.NewStore(cfg.SkillStorePath)
	skillMatcher := skill.NewMatcher(skillStore)
	skillGen := skill.NewGenerator(skillStore)

	// 初始化追踪器（CLI 模式全局）
	tracer := trace.NewTracer()

	// 构建共享依赖
	defaultModel := ""
	if entry, ok := cfg.LLM.Providers[cfg.LLM.DefaultProvider]; ok {
		defaultModel = entry.Model
	}

	llmAdapter := &llmClientAdapter{gateway: gateway, model: defaultModel}

	// 初始化 SessionManager（统一管理用户会话）
	deps := session.SessionDeps{
		LLM:           llmAdapter,
		Tools:         registry,
		MaxIterations: cfg.Engine.MaxIterations,
		SystemPrompt:  buildSystemPrompt(cfg),
		MemorySize:    cfg.Session.MemorySize,
		CtxConfig:     ctxmgr.DefaultConfig(),
		Summarizer:    nil,
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
			handleCommand(input, cfg, skillStore, skillMatcher, skillGen, secEngine, tracer, localSession)
			continue
		}

		// 安全策略检查（任务开始）
		decisions := secEngine.Evaluate(security.Checkpoint{
			Type:    "task_start",
			Content: input,
		})
		if blocked := checkDecisions(decisions); blocked {
			continue
		}

		// Skill 匹配
		if matched, score := skillMatcher.Match(input); matched != nil && score > 0.6 {
			fmt.Printf("  💡 匹配到 Skill「%s」(置信度 %.0f%%)，将参考其执行流程\n", matched.Name, score*100)
		}

		// 执行任务（通过 session 串行）
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
			Type: "task_end",
			Cost: float64(result.TokenUsage) * 0.00001,
		})

		// 尝试自动生成 Skill
		tryGenerateSkill(skillGen, result)
	}
}

// loadConfig 加载配置
func loadConfig() *config.Config {
	cfgPath := "config/config.yaml"
	if len(os.Args) > 1 {
		cfgPath = os.Args[1]
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Printf("  ⚠️  配置文件加载失败 (%v)，使用默认配置\n", err)
		return config.Default()
	}
	return cfg
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

// initSecurityEngine 初始化安全策略引擎
func initSecurityEngine(cfg *config.Config) *security.Engine {
	eng := security.NewEngine()

	for _, p := range security.DefaultPolicies() {
		eng.AddPolicy(p)
	}

	if data, err := os.ReadFile(cfg.PolicyFile); err == nil {
		if err := eng.LoadFromYAML(data); err != nil {
			fmt.Printf("  ⚠️  策略文件解析失败: %v\n", err)
		}
	}

	return eng
}

// buildSystemPrompt 构建系统提示词
func buildSystemPrompt(cfg *config.Config) string {
	if cfg.Engine.SystemPrompt != "" {
		return cfg.Engine.SystemPrompt
	}
	return `你是 Tommy-Cat，一个通用任务智能体。你可以通过工具调用来完成用户的任务。
执行任务时遵循 ReAct 模式：思考(Thought) -> 行动(Action) -> 观察(Observation) -> 循环。
当任务完成时，直接输出最终答案，不再调用工具。
回答使用中文，保持简洁专业。`
}

// handleCommand 处理斜杠命令
func handleCommand(input string, cfg *config.Config, store *skill.Store, matcher *skill.Matcher, gen *skill.Generator, sec *security.Engine, tracer *trace.Tracer, sess *session.UserSession) {
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
    /clear         清空记忆`)
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
	case "/clear":
		sess.ClearMemory()
		fmt.Println("  记忆已清空。")
	default:
		fmt.Printf("  未知命令: %s，输入 /help 查看帮助\n", cmd[0])
	}
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

// tryGenerateSkill 尝试从执行结果自动生成 Skill
func tryGenerateSkill(gen *skill.Generator, result *engine.ExecutionTrace) {
	if len(result.Steps) < 3 {
		return
	}
	traceJSON := fmt.Sprintf(`{"goal":"%s","steps":%d,"tools_used":true}`, result.Goal, len(result.Steps))
	s, err := gen.GenerateFromTrace(traceJSON)
	if err != nil {
		return
	}
	if err := gen.ValidateSkill(s); err != nil {
		return
	}
	fmt.Printf("  🧠 已自动生成 Skill「%s」\n", s.Name)
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
	model   string
}

func (a *llmClientAdapter) Chat(ctx context.Context, messages []llm.Message, tools []llm.ToolDef) (llm.ChatResponse, error) {
	req := llm.ChatRequest{
		Model:    a.model,
		Messages: messages,
		Tools:    tools,
	}
	return a.gateway.Chat(ctx, req)
}
