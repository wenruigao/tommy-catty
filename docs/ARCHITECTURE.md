# Tommy-Cat Agent 项目详细介绍

## 1. 项目概览

Tommy-Cat Agent 是一个基于 Go 语言开发的通用任务智能体（AI Agent），采用 **ReAct（Reasoning + Acting）** 执行循环：思考（Thought）→ 行动（Action）→ 观察（Observation），直到产出最终答案。

### 核心特性

| 特性 | 说明 |
|------|------|
| **ReAct 引擎** | Thought → Action → Observation 循环，支持反思与重规划 |
| **多用户隔离** | CLI 单用户 + HTTP 多用户，会话/记忆/限流完全隔离 |
| **三层记忆** | 工作记忆 + 情景记忆 + 语义记忆，冲突消解与持久化 |
| **安全策略引擎** | Policy-as-Code，9 个检查点，声明式规则 |
| **工具执行沙箱** | none / native / container 三档，OS 级隔离 |
| **多渠道接入** | HTTP / CLI / 8 种 IM 渠道（钉钉/飞书/微信/企微/Telegram/WhatsApp/QQ/Webhook） |
| **声明式配置** | 新增供应商/数据源/策略只需改 YAML，零代码 |
| **零第三方框架** | 标准库 net/http，无 Gin/Echo 等 |

### 运行模式

| 入口 | 模式 | 说明 |
|------|------|------|
| `cmd/agent/main.go` | CLI 交互式 REPL | 单用户（userID 固定 `"local"`），支持斜杠命令 |
| `cmd/server/main.go` | HTTP 多用户服务 | RESTful API，X-User-ID / API Key / JWT 认证 |
| `cmd/memstore/main.go` | 记忆存储服务 | 可选 remote 后端，多实例共享记忆 |

---

## 2. 整体架构图

```
┌─────────────────────────────────────────────────────────────────────┐
│                        用户接入层 (Access Layer)                      │
│  ┌─────────┐  ┌─────────┐  ┌─────────┐  ┌─────────┐  ┌─────────┐   │
│  │ CLI REPL │  │ HTTP API│  │  DingTalk│  │ Feishu  │  │  ...    │   │
│  │cmd/agent │  │cmd/server│  │ Channel  │  │ Channel │  │ 8 types │   │
│  └────┬─────┘  └────┬─────┘  └────┬─────┘  └────┬─────┘  └────┬─────┘   │
│       └──────────────┴──────────────┴──────────────┴──────────────┘   │
└───────────────────────────────────┬─────────────────────────────────┘
                                    │
┌───────────────────────────────────▼─────────────────────────────────┐
│                      会话管理层 (Session Layer)                       │
│  ┌──────────────────────────────────────────────────────────────┐   │
│  │ internal/session (per-user isolated)                         │   │
│  │ ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────────────┐│   │
│  │ │  Engine   │ │ Memory   │ │ CtxMgr   │ │ Persona Assembler││   │
│  │ │ (ReAct)  │ │ (3-tier) │ │ (Context)│ │ (agent+user)     ││   │
│  │ └────┬─────┘ └────┬─────┘ └────┬─────┘ └──────────────────┘│   │
│  │      │             │            │                            │   │
│  │ ┌────▼─────────────▼────────────▼───────────────────────────┐│   │
│  │ │  ToolGate → Security Engine → Tool Registry → Execution   ││   │
│  │ └───────────────────────────────────────────────────────────┘│   │
│  └──────────────────────────────────────────────────────────────┘   │
└───────────────────────────────────┬─────────────────────────────────┘
                                    │
┌───────────────────────────────────▼─────────────────────────────────┐
│                        核心引擎层 (Engine Layer)                      │
│                                                                      │
│  ┌──────────────────────────────────────────────────────────────┐   │
│  │                   ReAct Execution Loop                       │   │
│  │                                                              │   │
│  │   ┌─────────┐    ┌─────────┐    ┌─────────┐    ┌─────────┐  │   │
│  │   │ Thought │───▶│  Action │───▶│  Tool   │───▶│Observe  │  │   │
│  │   │ (LLM)  │    │(LLM)    │    │(Execute)│    │(Result) │  │   │
│  │   └─────────┘    └─────────┘    └─────────┘    └────┬────┘  │   │
│  │        ▲                                            │        │   │
│  │        └────────────────────────────────────────────┘        │   │
│  │                  (loop until final answer)                   │   │
│  └──────────────────────────────────────────────────────────────┘   │
│                                                                      │
│  ┌─────────────┐ ┌─────────────┐ ┌─────────────┐ ┌─────────────┐   │
│  │   LLM 网关   │ │  工具注册表  │ │  安全引擎    │ │  执行沙箱   │   │
│  │ llm/        │ │ tool/       │ │ security/   │ │ sandbox/   │   │
│  │ Provider    │ │ Registry    │ │ Policy      │ │ none/native│   │
│  │ Retry+CB    │ │ +Meta       │ │ Engine      │ │ /container │   │
│  └─────────────┘ └─────────────┘ └─────────────┘ └─────────────┘   │
└─────────────────────────────────────────────────────────────────────┘
                                    │
┌───────────────────────────────────▼─────────────────────────────────┐
│                        基础设施层 (Infrastructure)                    │
│                                                                      │
│  ┌─────────────┐ ┌─────────────┐ ┌─────────────┐ ┌─────────────┐   │
│  │   Memory    │ │  Knowledge  │ │  Search     │ │    MCP      │   │
│  │ memory/     │ │ kb/         │ │ search/     │ │ mcp/        │   │
│  │ Working     │ │ BM25 索引   │ │ DuckDuckGo  │ │ stdio / SSE │   │
│  │ Episodic    │ │ 分块/分词   │ │ Tavily      │ │ 远程工具    │   │
│  │ Semantic    │ │ 倒排索引    │ │             │ │             │   │
│  └─────────────┘ └─────────────┘ └─────────────┘ └─────────────┘   │
│                                                                      │
│  ┌─────────────┐ ┌─────────────┐ ┌─────────────┐ ┌─────────────┐   │
│  │  Database   │ │  Metrics    │ │  Trace      │ │  Doctor     │   │
│  │ tool/dbquery│ │ metrics/    │ │ trace/      │ │ doctor/     │   │
│  │ MySQL/Pg/SL │ │ Prometheus  │ │ JSONL       │ │ 10 项自检   │   │
│  └─────────────┘ └─────────────┘ └─────────────┘ └─────────────┘   │
└─────────────────────────────────────────────────────────────────────┘
```

---

## 3. 核心模块技术选型与实现

### 3.1 ReAct 引擎 (`internal/engine/`)

**技术选型**：纯 Go 实现，无状态设计，通过接口解耦。

**核心流程**：

```
┌─────────────────────────────────────────────────────────────┐
│                    Engine.Run(ctx, goal)                     │
│  ┌──────────────────────────────────────────────────────┐   │
│  │ 1. 初始化 ExecutionTrace + Span                      │   │
│  │ 2. 构建系统提示词 (agent.md + persona)               │   │
│  │ 3. 获取工具定义 (toolRegistry.ToToolDefs)            │   │
│  └──────────────────────────────────────────────────────┘   │
│                         │                                    │
│  ┌──────────────────────▼──────────────────────────────┐    │
│  │              ReAct 主循环 (maxIterations)            │    │
│  │                                                      │    │
│  │  ┌─────────┐    ┌─────────┐    ┌─────────┐          │    │
│  │  │ LLM.Call│───▶│ Parse   │───▶│ 如果有  │          │    │
│  │  │(messages)│    │Response │    │tool_call│          │    │
│  │  └─────────┘    └─────────┘    └────┬────┘          │    │
│  │                                      │                │    │
│  │                    ┌─────────────────▼─────────────┐  │    │
│  │                    │         有工具调用？           │  │    │
│  │                    │  ┌───────────┴───────────┐    │  │    │
│  │                    │  Yes                     No   │  │    │
│  │                    │  │                       │    │  │    │
│  │                    │  ▼                       ▼    │  │    │
│  │                    │ ToolGate 检查         输出答案 │  │    │
│  │                    │       │                 结束  │  │    │
│  │                    │       ▼                      │  │    │
│  │                    │ Tool.Execute(ctx)            │  │    │
│  │                    │       │                      │  │    │
│  │                    │       ▼                      │  │    │
│  │                    │ 观察 → 反思 → 追加到 messages │  │    │
│  │                    └──────────────────────────────┘  │    │
│  └──────────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────────┘
```

**关键实现细节**：

```go
// internal/engine/react.go
func (e *Engine) Run(ctx context.Context, goal string) (*ExecutionTrace, error) {
    // 1. 构建初始消息列表（含系统提示词）
    messages := e.buildInitialMessages(goal)

    // 2. 获取工具定义
    toolDefs := e.toolRegistry.ToToolDefs()

    // 3. ReAct 主循环
    for i := 0; i < e.maxIterations; i++ {
        // 上下文压缩（Token 估算 → 超限触发 LLM 摘要）
        messages = e.ctxMgr.EnsureWithinLimit(ctx, messages)

        // 调用 LLM（支持重试/熔断/语义缓存）
        resp, err := e.llmClient.Call(ctx, messages, toolDefs)

        // 处理工具调用
        if resp.HasToolCalls() {
            // ToolGate 策略检查
            for _, tc := range resp.ToolCalls {
                verdict, _ := e.toolGate.Check(ctx, tc)
                if verdict.Action == "deny" {
                    // 拒绝 → 反馈给 LLM
                    continue
                }
                // 执行工具
                result, err := e.toolRegistry.Execute(ctx, tc.Name, tc.Arguments)
                // 追加观察到 messages
                messages = append(messages, ...)
            }
            // 反思检查（每 5 步）
            if e.reflection != nil {
                replanState.IncrementStep()
                if replanState.ShouldReflect() {
                    // LLM 反思 → 可能重新规划
                }
            }
        } else {
            // 最终答案
            return trace, nil
        }
    }
}
```

**反思机制**：
- 每 5 步触发一次反思
- LLM 评估进度并决定是否重新规划
- 避免在简单任务上误触发

---

### 3.2 LLM 网关 (`internal/llm/`)

**技术选型**：统一 Provider 接口，支持 OpenAI 兼容协议 + Anthropic 协议。

**核心设计**：

```go
// Provider 接口（支持多种 LLM 后端）
type Provider interface {
    Name() string
    ChatCompletion(ctx context.Context, req *ChatCompletionRequest) (*ChatCompletionResponse, error)
    StreamChatCompletion(ctx context.Context, req *ChatCompletionRequest) (<-chan StreamChunk, error)
}
```

**故障转移机制**：

```
┌─────────────────────────────────────────────────────────────┐
│                      LLM Provider 选择                        │
│                                                              │
│  ┌──────────┐    失败     ┌──────────┐    失败    ┌────────┐│
│  │  Primary  │──────────▶│  Retry   │──────────▶│Fallback ││
│  │ Provider  │(指数退避)  │ (抖动)   │(3次后)    │Provider ││
│  └────┬─────┘            └──────────┘           └────┬─────┘│
│       │                                              │      │
│       ▼                                              ▼      │
│  ┌──────────┐                                   ┌──────────┐│
│  │Circuit   │                                   │Circuit   ││
│  │Breaker   │                                   │Breaker   ││
│  │(熔断器)  │                                   │(熔断器)  ││
│  └──────────┘                                   └──────────┘│
└─────────────────────────────────────────────────────────────┘
```

**关键特性**：

| 特性 | 实现 |
|------|------|
| **重试** | 指数退避 + 随机抖动，可配置最大重试次数 |
| **熔断器** | 失败计数 → 断路 → 半开 → 恢复，防止雪崩 |
| **语义缓存** | 相同语义问题复用缓存，节省 Token |
| **Token 计量** | 按模型统计，支持日预算限制 |
| **协议适配** | OpenAI 兼容 + Anthropic Messages API |

---

### 3.3 三层记忆系统 (`internal/memory/`)

**技术选型**：分层存储 + 冲突消解，文件持久化。

```
┌─────────────────────────────────────────────────────────────┐
│                     CombinedMemory                           │
│                                                              │
│  ┌───────────────────────────────────────────────────────┐  │
│  │                  Working Memory                        │  │
│  │  • 当前任务上下文（最近 N 轮对话）                       │  │
│  │  • 进行中的工具调用状态                                  │  │
│  │  • 生命周期：单次任务                                    │  │
│  └───────────────────────────────────────────────────────┘  │
│                         │                                    │
│  ┌──────────────────────▼───────────────────────────────┐   │
│  │                  Episodic Memory                      │   │
│  │  • 历史任务记录（目标、步骤、结果）                     │   │
│  │  • 时间索引，按需加载                                   │   │
│  │  • 持久化：data/users/{userID}/episodes/              │   │
│  └───────────────────────────────────────────────────────┘  │
│                         │                                    │
│  ┌──────────────────────▼───────────────────────────────┐   │
│  │                  Semantic Memory                      │   │
│  │  • 用户画像（user.md，由 Profiler 自动生成）           │   │
│  │  • 知识库（BM25 索引，分块检索）                       │   │
│  │  • 持久化：data/users/{userID}/user.md                │   │
│  └───────────────────────────────────────────────────────┘  │
│                                                              │
│  ┌───────────────────────────────────────────────────────┐  │
│  │              冲突消解 (Conflict Resolution)             │  │
│  │  • 三层记忆可能产生矛盾信息                              │  │
│  │  • 按时间衰减 + 来源可信度加权                           │  │
│  │  • 最终输出经过滤（不含敏感信息）                        │  │
│  └───────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
```

**关键实现**：

```go
// internal/memory/combined.go
type CombinedMemory struct {
    working  *WorkingMemory   // 当前任务上下文
    episodic *EpisodicMemory  // 历史任务
    semantic *SemanticMemory  // 用户画像 + 知识库
}

func (m *CombinedMemory) Recall(ctx context.Context, query string) ([]MemoryFragment, error) {
    // 1. 从三层记忆分别召回
    working := m.working.Recall(query)
    episodic := m.episodic.Recall(query)
    semantic := m.semantic.Recall(query)

    // 2. 合并 + 去重
    fragments := mergeAndDedup(working, episodic, semantic)

    // 3. 冲突消解（时间衰减 + 来源权重）
    resolved := m.resolveConflicts(fragments)

    return resolved, nil
}
```

---

### 3.4 工具系统 (`internal/tool/`)

**技术选型**：注册表模式 + 接口驱动，支持内置工具 + MCP 远程工具。

**内置工具清单**：

| 工具 | 功能 | 风险等级 | 沙箱隔离 |
|------|------|----------|----------|
| `web_search` | 网络搜索 | L0 (只读) | 无 |
| `web_fetch` | 获取网页内容 | L0 (只读) | 无 |
| `file_read` | 读取文件 | L0 (只读) | work_dir 沙箱 |
| `file_write` | 写入文件 | L2 (写入) | work_dir 沙箱 |
| `db_query` | 数据库只读查询 | L1 | SQL 白名单 |
| `kb_search` | 知识库检索 | L0 (只读) | 无 |
| `code_run` | 执行代码片段 | L3 (高危) | **OS 级沙箱** |
| `shell_exec` | 执行 Shell 命令 | L3 (高危) | **OS 级沙箱** |

**工具注册与执行**：

```go
// internal/tool/registry.go
type Registry struct {
    tools map[string]*ToolMeta  // 工具名 → 元数据
}

type ToolMeta struct {
    Tool    Tool          // 工具实现
    Risk    RiskLevel     // 风险等级
    Timeout time.Duration // 执行超时
}

// 工具接口
type Tool interface {
    Name() string
    Description() string
    Parameters() json.RawMessage
    Execute(ctx context.Context, args map[string]any) (string, error)
}
```

**工具调用流程**：

```
┌─────────────────────────────────────────────────────────────┐
│                    Tool Execution Flow                       │
│                                                              │
│  LLM 输出 tool_call                                         │
│       │                                                      │
│       ▼                                                      │
│  ┌───────────────────────────────────────────────────────┐  │
│  │ ToolGate（策略检查）                                    │  │
│  │ • security.Engine.Evaluate(tool_call)                  │  │
│  │ • deny → 拒绝，反馈 LLM                               │  │
│  │ • require_approval → 审批回调                          │  │
│  │ • allow → 继续                                         │  │
│  └───────────────────────────────────────────────────────┘  │
│       │                                                      │
│       ▼                                                      │
│  ┌───────────────────────────────────────────────────────┐  │
│  │ Sandbox（执行隔离）                                     │  │
│  │ • none: 直通（仅组杀修复）                              │  │
│  │ • native: Linux userns / macOS Seatbelt               │  │
│  │ • container: docker/podman                            │  │
│  └───────────────────────────────────────────────────────┘  │
│       │                                                      │
│       ▼                                                      │
│  Tool.Execute(ctx, args)                                     │
│       │                                                      │
│       ▼                                                      │
│  ┌───────────────────────────────────────────────────────┐  │
│  │ OutputGate（输出脱敏）                                  │  │
│  │ • 检测 API Key / 密码等敏感信息                         │  │
│  │ • 自动替换为 ***                                       │  │
│  └───────────────────────────────────────────────────────┘  │
│       │                                                      │
│       ▼                                                      │
│  返回结果给 LLM                                              │
└─────────────────────────────────────────────────────────────┘
```

---

### 3.5 安全策略引擎 (`internal/security/`)

**技术选型**：Policy-as-Code，声明式 YAML 规则，9 个检查点全覆盖。

**检查点分布**：

```
┌─────────────────────────────────────────────────────────────┐
│                    Security Checkpoints                      │
│                                                              │
│  1. task_start    ──── 任务开始时（提示注入拦截）              │
│  │                                                            │
│  2. tool_call     ──── 工具调用前（ToolGate 策略检查）        │
│  │                                                            │
│  3. tool_return   ──── 工具返回后（间接注入清洗）             │
│  │                                                            │
│  4. llm_output    ──── LLM 输出后（输出脱敏）                │
│  │                                                            │
│  5. task_end      ──── 任务结束时（成本审计）                 │
│                                                              │
│  + ToolGate 策略检查（策略引擎适配）                          │
│  + OutputGate 输出脱敏（Engine.Redact）                      │
│  + Audit 日志记录（JSONL 追加写）                            │
│  + Templates 内置模板（9 条默认规则）                        │
└─────────────────────────────────────────────────────────────┘
```

**策略规则示例**：

```yaml
# config/policy.yaml
- name: deny-dangerous-commands
  priority: 10
  effect: deny
  tool_names: [shell_exec]
  pattern: "rm\\s+-rf\\s+/"
  reason: "危险操作：递归删除根目录"

- name: require-approval-file-write
  priority: 20
  effect: require_approval
  tool_names: [file_write]
  pattern: "config/"
  reason: "配置文件修改需要审批"

- name: office-hours-only
  priority: 30
  effect: deny
  conditions:
    - type: time_range
      start: "22:00"
      end: "06:00"
  tool_names: [shell_exec, code_run]
  reason: "非工作时间禁止执行高危操作"
```

**内置模板**（`internal/security/templates.go`）：

| 模板 | 拦截内容 |
|------|----------|
| `block-rm-rf` | `rm -rf` 递归删除 |
| `block-drop-table` | `DROP TABLE` 数据库操作 |
| `block-fork-bomb` | fork bomb 攻击 |
| `block-write-device` | 写设备文件 |
| `prompt-injection` | 提示注入攻击 |
| `cost-guard` | 成本超限告警 |
| `rate-limiter` | 工具调用限流 |
| `office-hours` | 工作时间限制 |
| `audit-log` | 审计日志记录 |

---

### 3.6 工具执行沙箱 (`internal/sandbox/`)

**技术选型**：接口驱动，三档实现，OS 级隔离。

**架构设计**：

```
┌─────────────────────────────────────────────────────────────┐
│                      Sandbox Interface                       │
│                                                              │
│  type Sandbox interface {                                    │
│      Name() string                                          │
│      Available() error                                      │
│      Compile(ctx, ExecSpec) (*exec.Cmd, func(), error)     │
│  }                                                          │
│                                                              │
│  ┌───────────────────────────────────────────────────────┐  │
│  │                    New(type, config)                    │  │
│  │       │               │               │                │  │
│  │       ▼               ▼               ▼                │  │
│  │  ┌─────────┐    ┌─────────┐    ┌─────────┐           │  │
│  │  │  none   │    │ native  │    │container│           │  │
│  │  │ 直通    │    │原生沙箱 │    │容器沙箱 │           │  │
│  │  └─────────┘    └─────────┘    └─────────┘           │  │
│  └───────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
```

**三档实现对比**：

| 特性 | none | native | container |
|------|------|--------|-----------|
| **隔离级别** | 无（仅组杀） | OS 命名空间 | 容器 |
| **网络隔离** | ❌ | ✅ netns | ✅ --network none |
| **文件系统** | ❌ | ❌ | ✅ --read-only |
| **资源限制** | ❌ | ✅ rlimit | ✅ cgroup |
| **进程限制** | ❌ | ❌ | ✅ --pids-limit |
| **依赖** | 无 | Linux 内核/Seatbelt | Docker/Podman |

**native 沙箱实现**：

```go
// internal/sandbox/native_linux.go
func (n *nativeSandbox) compile(ctx context.Context, spec ExecSpec) (*exec.Cmd, func(), error) {
    cmd := exec.CommandContext(ctx, spec.Argv[0], spec.Argv[1:]...)
    cmd.SysProcAttr = &syscall.SysProcAttr{
        // 非特权用户命名空间隔离
        Cloneflags: syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS |
                    syscall.CLONE_NEWPID | syscall.CLONE_NEWIPC | syscall.CLONE_NEWUTS,
        // 挂载命名空间独立 /tmp
        Unshareflags: syscall.CLONE_NEWNS,
        // 安全：agent 死亡后终止子进程
        Pdeathsig: syscall.SIGKILL,
    }
    // 资源限制
    rlimits := []syscall.Rlimit{
        {Cur: uint64(config.MemoryLimitMB << 20), Max: uint64(config.MemoryLimitMB << 20)}, // RLIMIT_AS
        {Cur: uint64(config.CPULimitSeconds), Max: uint64(config.CPULimitSeconds)},         // RLIMIT_CPU
    }
    // 应用 rlimit
    applyRLimits(cmd, rlimits)
    return cmd, cleanup, nil
}
```

**container 沙箱实现**：

```go
// internal/sandbox/container.go
func (c *containerSandbox) buildRunArgs() []string {
    args := []string{
        "run", "--rm", "-i",
        "--read-only",                          // 只读根文件系统
        "--tmpfs", "/tmp:rw,noexec,nosuid",     // 独立 /tmp
        "--network", "none",                    // 禁网
        "--cap-drop", "ALL",                    // 裁剪所有能力
        "--security-opt", "no-new-privileges",  // 禁止提权
        "--pids-limit", "64",                   // 进程数限制
        "--memory", "512m",                     // 内存限制
        "--cpus", "0.5",                        // CPU 限制
        "--pull", "never",                      // 禁止自动拉取
        "-e", "HOME=/root",
        "-e", "TMPDIR=/tmp",
    }
    return args
}
```

---

### 3.7 多用户会话管理 (`internal/session/`)

**技术选型**：per-user 隔离，互斥锁串行执行。

**架构设计**：

```
┌─────────────────────────────────────────────────────────────┐
│                    Session Manager                           │
│                                                              │
│  ┌───────────────────────────────────────────────────────┐  │
│  │                   用户会话 Map                         │  │
│  │  ┌─────────┐ ┌─────────┐ ┌─────────┐ ┌─────────┐    │  │
│  │  │ user:1  │ │ user:2  │ │ user:3  │ │  ...    │    │  │
│  │  └────┬────┘ └────┬────┘ └────┬────┘ └─────────┘    │  │
│  │       │           │           │                        │  │
│  │       ▼           ▼           ▼                        │  │
│  │  ┌──────────────────────────────────────────────┐    │  │
│  │  │              Session (per-user)              │    │  │
│  │  │  • Engine (ReAct 引擎)                       │    │  │
│  │  │  • Memory (三层记忆)                         │    │  │
│  │  │  • CtxManager (上下文管理)                   │    │  │
│  │  │  • Tracer (执行追踪)                         │    │  │
│  │  │  • mu sync.Mutex (串行执行)                  │    │  │
│  │  └──────────────────────────────────────────────┘    │  │
│  └───────────────────────────────────────────────────────┘  │
│                                                              │
│  ┌───────────────────────────────────────────────────────┐  │
│  │                   共享组件                             │  │
│  │  • ToolRegistry (工具注册表)                          │  │
│  │  • SecurityEngine (安全策略引擎)                      │  │
│  │  • SearchEngine (搜索引擎)                            │  │
│  │  • KnowledgeBases (知识库)                            │  │
│  └───────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
```

**会话隔离保证**：

```go
// internal/session/session.go
type Session struct {
    engine   *engine.Engine
    memory   *memory.CombinedMemory
    ctxMgr   *ctxmgr.Manager
    tracer   *trace.Tracer
    mu       sync.Mutex  // 同一用户请求串行执行
}

func (s *Session) Run(ctx context.Context, goal string) (*engine.ExecutionTrace, error) {
    s.mu.Lock()
    defer s.mu.Unlock()

    // 执行 ReAct 循环
    return s.engine.Run(ctx, goal)
}
```

**Persona 组装**：

```go
// internal/session/persona.go
func (s *Session) buildSystemPrompt() string {
    var sb strings.Builder

    // 1. agent.md（职责与权限边界，最高优先级）
    sb.WriteString(loadAgentMD())

    // 2. user.md（用户画像，由 Profiler 自动生成）
    sb.WriteString(s.loadUserProfile())

    // 3. soul.md（人格与对话风格）
    sb.WriteString(loadSoulMD())

    return sb.String()
}
```

---

### 3.8 多渠道接入 (`internal/channel/`)

**技术选型**：Hub + Adapter 模式，声明式配置。

**支持的渠道**：

| 渠道 | 实现文件 | 认证方式 |
|------|----------|----------|
| **Webhook** | `webhook.go` | Bearer Token |
| **DingTalk** | `dingtalk.go` | HMAC-SHA256 签名 |
| **Feishu** | `feishu.go` | 签名验证 |
| **WeChat** | `wechat.go` | 消息加解密 |
| **WeCom** | `wecom.go` | 回调 URL 验证 |
| **Telegram** | `telegram.go` | Bot Token |
| **WhatsApp** | `whatsapp.go` | Webhook 验证 |
| **QQ** | `qq.go` | 签名验证 |

**架构设计**：

```
┌─────────────────────────────────────────────────────────────┐
│                      Channel Hub                             │
│                                                              │
│  ┌───────────────────────────────────────────────────────┐  │
│  │                    Adapter Registry                    │  │
│  │  ┌─────────┐ ┌─────────┐ ┌─────────┐ ┌─────────┐    │  │
│  │  │ DingTalk│ │ Feishu  │ │ WeChat  │ │Telegram │    │  │
│  │  └────┬────┘ └────┬────┘ └────┬────┘ └────┬────┘    │  │
│  └───────┼───────────┼───────────┼───────────┼──────────┘  │
│          │           │           │           │               │
│          ▼           ▼           ▼           ▼               │
│  ┌───────────────────────────────────────────────────────┐  │
│  │              统一消息格式 (Message)                     │  │
│  │  • SenderID                                           │  │
│  │  • Content                                            │  │
│  │  • ChannelType                                        │  │
│  │  • RawPayload                                         │  │
│  └───────────────────────────────────────────────────────┘  │
│          │                                                   │
│          ▼                                                   │
│  ┌───────────────────────────────────────────────────────┐  │
│  │              会话键 (Session Key)                      │  │
│  │  格式: "{channel}:{senderID}"                          │  │
│  │  例: "dingtalk:1234567890"                            │  │
│  └───────────────────────────────────────────────────────┘  │
│          │                                                   │
│          ▼                                                   │
│  ┌───────────────────────────────────────────────────────┐  │
│  │              Session Manager                          │  │
│  │  • 按 Session Key 获取/创建会话                        │  │
│  │  • 自动应用限流/门禁/审计                              │  │
│  └───────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
```

---

### 3.9 MCP 远程工具 (`internal/mcp/`)

**技术选型**：Model Context Protocol 客户端，支持 stdio / SSE 传输。

**架构设计**：

```
┌─────────────────────────────────────────────────────────────┐
│                      MCP Client                              │
│                                                              │
│  ┌───────────────────────────────────────────────────────┐  │
│  │                   传输层 (Transport)                   │  │
│  │  ┌─────────────┐           ┌─────────────┐           │  │
│  │  │    stdio    │           │     SSE     │           │  │
│  │  │ (子进程通信) │           │ (HTTP 流)   │           │  │
│  │  └─────────────┘           └─────────────┘           │  │
│  └───────────────────────────────────────────────────────┘  │
│                         │                                    │
│  ┌──────────────────────▼───────────────────────────────┐   │
│  │                工具发现与注册                          │   │
│  │  • tools/list → 获取远程工具定义                       │   │
│  │  • 自动注册到 ToolRegistry                            │   │
│  │  • 工具名前缀: "{server}:"                            │   │
│  └───────────────────────────────────────────────────────┘  │
│                         │                                    │
│  ┌──────────────────────▼───────────────────────────────┐   │
│  │                工具调用代理                            │   │
│  │  • tools/call → 转发到远程服务器                       │   │
│  │  • 结果清洗（防间接注入）                              │   │
│  │  • 超时控制                                           │   │
│  └───────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
```

**配置示例**：

```yaml
# config/config.yaml
mcp:
  servers:
    filesystem:
      command: npx
      args: ["-y", "@modelcontextprotocol/server-filesystem", "/path/to/allowed/dir"]
      transport: stdio
      timeout: 30s
    web-search:
      url: "http://localhost:3001/sse"
      transport: sse
      timeout: 15s
```

---

## 4. 数据流架构

```
┌─────────────────────────────────────────────────────────────────────┐
│                         完整数据流                                   │
│                                                                      │
│  用户输入                                                            │
│    │                                                                 │
│    ▼                                                                 │
│  ┌───────────────────────────────────────────────────────────────┐  │
│  │ 1. 接入层 (CLI/HTTP/Channel)                                  │  │
│    │ • 认证（header/api_key/jwt）                                │  │
│    │ • 限流（per-user token bucket）                              │  │
│    └───────────────────────────────────────────────────────────────┘  │
│    │                                                                 │
│    ▼                                                                 │
│  ┌───────────────────────────────────────────────────────────────┐  │
│  │ 2. 会话层 (Session Manager)                                   │  │
│    │ • 获取/创建会话                                              │  │
│    │ • Persona 组装（agent.md + user.md + soul.md）              │  │
│    └───────────────────────────────────────────────────────────────┘  │
│    │                                                                 │
│    ▼                                                                 │
│  ┌───────────────────────────────────────────────────────────────┐  │
│  │ 3. 引擎层 (ReAct Engine)                                     │  │
│    │ • 构建消息列表                                               │  │
│    │ • 上下文压缩（Token 估算 → 超限触发摘要）                    │  │
│    │ • 调用 LLM（重试/熔断/缓存）                                 │  │
│    └───────────────────────────────────────────────────────────────┘  │
│    │                                                                 │
│    ▼                                                                 │
│  ┌───────────────────────────────────────────────────────────────┐  │
│  │ 4. 工具层 (Tool Execution)                                    │  │
│    │ • ToolGate 策略检查                                         │  │
│    │ • Sandbox 执行隔离                                          │  │
│    │ • Tool.Execute()                                            │  │
│    │ • OutputGate 输出脱敏                                       │  │
│    └───────────────────────────────────────────────────────────────┘  │
│    │                                                                 │
│    ▼                                                                 │
│  ┌───────────────────────────────────────────────────────────────┐  │
│  │ 5. 记忆层 (Memory)                                            │  │
│    │ • 工作记忆更新                                               │  │
│    │ • 情景记忆持久化                                             │  │
│    │ • 用户画像更新（每 N 次任务）                                 │  │
│    └───────────────────────────────────────────────────────────────┘  │
│    │                                                                 │
│    ▼                                                                 │
│  ┌───────────────────────────────────────────────────────────────┐  │
│  │ 6. 输出层 (Response)                                          │  │
│    │ • 审计日志（JSONL）                                          │  │
│    │ • 指标采集（Prometheus）                                     │  │
│    │ • 执行追踪（Span）                                           │  │
│    └───────────────────────────────────────────────────────────────┘  │
│    │                                                                 │
│    ▼                                                                 │
│  最终答案                                                           │
└─────────────────────────────────────────────────────────────────────┘
```

---

## 5. 技术选型总结

### 5.1 语言与运行时

| 选型 | 说明 |
|------|------|
| **Go 1.26** | 静态类型，编译型，高性能，优秀的并发支持 |
| **标准库优先** | net/http, encoding/json, os/exec, syscall 等 |
| **零第三方框架** | 无 Gin/Echo/Fiber，标准库 net/http 足够 |

### 5.2 外部依赖（仅 5 个直接依赖）

| 依赖 | 用途 |
|------|------|
| `github.com/google/uuid` | 生成唯一任务 ID |
| `gopkg.in/yaml.v3` | YAML 配置解析 |
| `github.com/go-sql-driver/mysql` | MySQL 驱动（db_query） |
| `github.com/lib/pq` | PostgreSQL 驱动（db_query） |
| `modernc.org/sqlite` | SQLite 驱动（db_query，纯 Go 实现） |

### 5.3 架构模式

| 模式 | 应用 |
|------|------|
| **接口驱动** | Engine, Tool, Sandbox, Memory 均通过接口解耦 |
| **注册表模式** | 工具、策略、渠道均通过 Registry 注册 |
| **组合模式** | CombinedMemory, Session 等组合多个组件 |
| **策略模式** | 安全策略、沙箱类型、认证模式均可配置 |
| **适配器模式** | LLM Provider, Channel Adapter, MCP Transport |

### 5.4 安全设计

| 层级 | 机制 |
|------|------|
| **策略层** | Policy-as-Code，声明式规则，9 个检查点 |
| **执行层** | OS 级沙箱（native/container），网络隔离 |
| **输出层** | 敏感信息脱敏（API Key/密码） |
| **审计层** | JSONL 日志，全链路追踪 |

---

## 6. 部署架构

```
┌─────────────────────────────────────────────────────────────────────┐
│                        部署模式                                      │
│                                                                      │
│  ┌───────────────────────────────────────────────────────────────┐  │
│  │  单机模式 (CLI)                                               │  │
│  │  • cmd/agent → bin/tommy-agent                               │  │
│  │  • 单用户，本地文件存储                                        │  │
│  │  • 适合开发/测试                                              │  │
│  └───────────────────────────────────────────────────────────────┘  │
│                                                                      │
│  ┌───────────────────────────────────────────────────────────────┐  │
│  │  服务模式 (HTTP)                                              │  │
│  │  • cmd/server → bin/tommy-server                             │  │
│  │  • 多用户，JWT/API Key 认证                                   │  │
│  │  • 适合生产部署                                               │  │
│  └───────────────────────────────────────────────────────────────┘  │
│                                                                      │
│  ┌───────────────────────────────────────────────────────────────┐  │
│  │  分布式模式 (Remote Memory)                                   │  │
│  │  • cmd/server × N（无状态水平扩展）                           │  │
│  │  • cmd/memstore（共享记忆后端）                               │  │
│  │  • config: memory.storage.type: remote                       │  │
│  └───────────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────────┘
```

---

## 7. 监控与可观测性

### 7.1 指标采集 (`internal/metrics/`)

```
┌─────────────────────────────────────────────────────────────┐
│                    Prometheus Metrics                        │
│                                                              │
│  GET /metrics                                               │
│                                                              │
│  # 请求指标                                                  │
│  http_requests_total{method, path, status}                  │
│  http_request_duration_seconds{method, path}                │
│                                                              │
│  # LLM 指标                                                  │
│  llm_requests_total{provider, model}                        │
│  llm_tokens_total{provider, model, type}                    │
│  llm_request_duration_seconds{provider, model}              │
│                                                              │
│  # 工具指标                                                  │
│  tool_calls_total{tool, risk}                               │
│  tool_call_duration_seconds{tool}                           │
│                                                              │
│  # 安全指标                                                  │
│  security_violations_total{checkpoint, effect}               │
│                                                              │
│  # 沙箱指标                                                  │
│  sandbox_executions_total{type, result}                     │
└─────────────────────────────────────────────────────────────┘
```

### 7.2 执行追踪 (`internal/trace/`)

```
┌─────────────────────────────────────────────────────────────┐
│                    Execution Trace                           │
│                                                              │
│  Task Span (task_id)                                        │
│    ├── LLM Call Span (llm_call_1)                          │
│    │     └── Duration: 234ms                                │
│    ├── Tool Call Span (tool_call_1)                         │
│    │     ├── Tool: web_search                               │
│    │     ├── Duration: 1.2s                                 │
│    │     └── Result: 5 results                              │
│    ├── LLM Call Span (llm_call_2)                          │
│    │     └── Duration: 189ms                                │
│    └── Final Answer Span                                    │
│          └── Duration: 45ms                                 │
│                                                              │
│  输出格式：JSONL（每行一个 Span）                            │
│  配置：engine.trace_export_path: "data/traces.jsonl"       │
└─────────────────────────────────────────────────────────────┘
```

### 7.3 健康检查 (`internal/doctor/`)

```
┌─────────────────────────────────────────────────────────────┐
│                    Doctor Checks (10 项)                     │
│                                                              │
│  1. config        配置文件校验                               │
│  2. llm           LLM 供应商连通性                           │
│  3. security      安全策略加载                               │
│  4. tool          工具注册完整性                             │
│  5. skill         Skill 存储目录                             │
│  6. workdir       工作目录权限                               │
│  7. memory        记忆存储目录                               │
│  8. network       网络连通性                                 │
│  9. resources     系统资源（磁盘/内存）                      │
│  10. sandbox      沙箱可用性                                 │
│                                                              │
│  访问：CLI /doctor 命令                                     │
│       HTTP GET /api/v1/doctor                               │
└─────────────────────────────────────────────────────────────┘
```

---

## 8. 总结

Tommy-Cat Agent 是一个**生产级**的 AI Agent 框架，具备：

| 维度 | 能力 |
|------|------|
| **功能完整性** | ReAct 引擎 + 多用户 + 多渠道 + 多工具 |
| **安全性** | 策略引擎 + OS 沙箱 + 输出脱敏 + 审计日志 |
| **可扩展性** | 接口驱动 + 声明配置 + MCP 远程工具 |
| **可观测性** | 指标采集 + 执行追踪 + 健康检查 |
| **工程质量** | 离线测试 + 交叉编译 + 零第三方框架 |

**核心设计哲学**：

1. **配置即代码**：新增供应商/数据源/策略只需改 YAML
2. **接口驱动解耦**：组件间通过接口交互，易于替换和测试
3. **安全纵深防御**：策略层 + 执行层 + 输出层 + 审计层
4. **最小依赖原则**：仅 5 个直接依赖，标准库优先
