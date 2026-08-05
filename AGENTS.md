# AGENTS.md — Tommy-Cat Agent 项目指南

本文件面向 AI 编码代理，提供理解、构建、测试和修改本项目所需的全部关键信息。

## 项目概览

Tommy-Cat Agent 是一个基于 Go 语言开发的通用任务智能体（AI Agent），采用 ReAct（Reasoning + Acting）执行循环：思考（Thought）→ 行动（Action）→ 观察（Observation），直到产出最终答案。

- **模块路径**: `github.com/tommy-cat/agent`
- **Go 版本**: go.mod 声明 `go 1.25.0`（README 中写 1.22+，以 go.mod 为准）
- **外部依赖极少**: 直接依赖仅 `github.com/google/uuid` 和 `gopkg.in/yaml.v3`；`modernc.org/sqlite` 仅在测试中作为 sqlite 驱动使用
- **LLM 协议**: 支持两种协议，经供应商配置的 `protocol` 字段选择——OpenAI Chat Completions 兼容协议（默认，支持 tool calling）与 Anthropic Messages API（`protocol: "anthropic"`，对应 `internal/llm/anthropic.go`），任何兼容服务均可通过 YAML 声明式接入，无需改代码
- **主要语言约定**: 代码注释、文档、用户可见提示均使用中文，提交修改时应保持一致

### 两种运行模式

| 入口 | 模式 | 说明 |
|------|------|------|
| `cmd/agent/main.go` | CLI 交互式 REPL | 单用户（userID 固定为 `"local"`），支持斜杠命令 |
| `cmd/server/main.go` | HTTP 多用户服务 | RESTful API，通过 `X-User-ID`、`X-API-Key`（api_key 模式）或 JWT 认证实现用户隔离 |

HTTP 服务端点（`internal/server/handler.go`）：

- `POST /api/v1/chat` — 执行任务
- `GET /api/v1/history` — 查询会话历史
- `POST /api/v1/clear` — 清空会话记忆
- `GET /api/v1/health` — 健康检查

## 构建与测试命令

```bash
# 国内网络建议先设置代理
export GOPROXY=https://goproxy.cn,direct

# 下载依赖
go mod tidy

# 构建 CLI（产物为 bin/tommy-agent）
go build -o bin/tommy-agent ./cmd/agent

# 构建 HTTP 服务
go build -o bin/tommy-server ./cmd/server

# 运行全部测试（当前全部通过，均为离线单元测试，无需 API Key）
go test ./...

# 运行单个包的测试
go test ./internal/security/...

# 静态检查
go vet ./...
```

注意：本环境中 `go` 可能不在默认 PATH 中（例如位于 `~/local/go/bin/go`），执行前请先确认。

### 平台限制

`internal/tool/limits_darwin.go` 与 `internal/tool/limits_linux.go` 分别提供 darwin/linux 的 `resourceLimits()`（均为 `Setpgid` 进程组隔离），被 `internal/tool/builtin.go`（shell_exec / code_run 工具）调用。项目可在 macOS 和 Linux 上编译运行（CI 含双平台交叉编译检查）；**Windows 无对应实现文件**，如需支持须补充 `limits_windows.go`。

### 运行所需环境变量

API Key 通过 `${ENV_VAR}` 语法在 `config/config.yaml` 中引用：

- `MIMO_API_KEY`（当前默认供应商）
- `DEEPSEEK_API_KEY`（当前降级供应商）
- `DASHSCOPE_API_KEY`（qwen，可选）
- `ANTHROPIC_API_KEY`（Anthropic Claude，可选，`protocol: "anthropic"` 时使用）
- `TAVILY_API_KEY`（Tavily 搜索，可选；默认搜索引擎为 DuckDuckGo，无需 Key）
- `AGENT_API_KEY`（HTTP 服务 `auth_mode: api_key` 时必填，客户端经 `X-API-Key` 头携带）
- `AGENT_JWT_SECRET`（HTTP 服务 `auth_mode: jwt` 时必填，HS256 签名密钥）

## 代码组织结构

```
cmd/agent/            CLI 入口（交互式 REPL、斜杠命令、启动自检）
cmd/server/           HTTP 服务入口（多用户模式）
config/
  config.go           配置加载与解析（支持 ${ENV_VAR} 展开）
  config.yaml         主配置：LLM 供应商、引擎、搜索、数据库、知识库
  policy.yaml         安全策略（声明式 YAML）
  agent.md            Agent 职责与权限边界（最高优先级，注入系统提示词）
  soul.md             Agent 人格与对话风格（注入系统提示词）
internal/
  engine/             ReAct 执行引擎（执行追踪、反思机制、ToolGate 工具调用门禁、OutputGate 输出门禁）
  llm/                LLM 网关：通用 Provider、重试（指数退避+抖动）、熔断器、缓存、计量
  session/            多用户会话隔离：每用户独立 Engine/Memory/CtxManager/Tracer，带限流与 TTL；含 ToolGate/OutputGate 适配、persona 组装（persona.go）与 user.md 画像生成（profiler.go）
  tool/               工具接口、注册表与内置工具
    dbquery/          数据库只读查询工具（连接池、SQL 校验、结果缓存、格式化）
    kbtools/          知识库工具（kb_search / kb_read / kb_list）
  security/           安全策略引擎（Policy-as-Code，内置模板 + YAML 自定义）
  memory/             三层记忆：工作记忆、情景记忆、语义记忆（CombinedMemory）
  ctxmgr/             上下文管理：token 估算、LLM 摘要压缩
  skill/              Skill 自动生成、匹配、版本化与持久化（data/skills.json）
  search/             搜索引擎抽象（DuckDuckGo / Tavily）
  kb/                 本地知识库：分块、分词、内存倒排索引（BM25 检索）
  mcp/                MCP 客户端（stdio / SSE 传输），发现并调用远程工具（经 config 的 `mcp.servers` 段装配，由 bootstrap.RegisterMCPTools 注册）
  server/             HTTP handler 与认证中间件
  bootstrap/          启动装配：根据配置构建数据源连接池与知识库，注册 db_query / kb_* 工具
  doctor/             健康自检（8 项检查，支持自动修复）
  trace/              执行追踪（span 记录，可选 JSONL 导出，配置 engine.trace_export_path）
data/                 运行时数据（Skill 持久化等）
bin/                  构建产物
```

### 架构要点

- **接口驱动设计**: 引擎通过 `engine.LLMClient`、`engine.ToolCaller`、`engine.MemoryStore` 等接口解耦；入口处用适配器（如 `llmClientAdapter`、`searchAdapter`）把具体实现接入接口。
- **会话隔离**: `internal/session` 是组装核心——每个用户持有独立的有状态组件，同一用户的请求通过互斥锁串行执行，不同用户之间无共享指针。
- **故障转移**: 主 LLM 供应商失败时按配置重试（指数退避 + 抖动），仍失败则切换到 `fallback_provider`；另有熔断器防止雪崩。
- **配置即代码**: 新增模型供应商、数据库数据源、知识库、安全策略都只需改 YAML，不改 Go 代码。

## 内置工具与风险等级

工具实现 `tool.Tool` 接口（Name / Description / Parameters / Execute），在注册表中绑定 `ToolMeta`（风险等级 + 超时）。

| 工具 | 功能 | 风险等级 |
|------|------|----------|
| `web_search` | 网络搜索 | L0 (只读) |
| `web_fetch` | 获取网页内容 | L0 (只读) |
| `file_read` | 读取文件 | L0 (只读) |
| `file_write` | 写入文件 | L2 (写入) |
| `code_run` | 执行代码片段 | L3 (高危) |
| `shell_exec` | 执行 Shell 命令 | L3 (高危) |
| `db_query` | 数据库只读查询 | 需配置数据源 |
| `kb_search` / `kb_read` / `kb_list` | 本地知识库检索 | 需配置知识库 |

## 安全注意事项

- **安全策略引擎**: 策略定义于 `config/policy.yaml`，按 `priority` 升序评估。效果类型：`deny`（拦截）、`require_approval`（需确认）、`redact`（脱敏）、`throttle`（限流，每会话令牌桶 30 次/分钟）、`allow`（放行）。内置模板见 `internal/security/templates.go`（9 条，含 task_start 提示注入拦截），默认拦截 `rm -rf`、`DROP TABLE` 等破坏性操作；输出中的 API Key / 密码经 OutputGate（llm_output 检查点 + `Engine.Redact`）自动脱敏。
- **间接注入防线**: 不可信工具输出（web_search/web_fetch/kb_*/MCP 工具）经 `internal/tool/sanitizer.go` 清洗（script 剥离、注入模式打标）并以 `<tool_output>` 隔离标签包裹后进入上下文；system prompt（含 agent.md）中声明外部内容仅为数据。
- **persona 体系**: `config/agent.md`（职责与权限边界，最高优先级）+ `config/soul.md`（人格风格）经 `SystemPromptProvider` 组装进系统提示词；`data/users/{userID}/user.md` 为每用户画像，由 UserProfiler 每 N 次任务（默认 3，`persona.profile_update_interval_runs`）经 LLM 总结生成。
- **db_query 多重防御**: 仅放行 SELECT/SHOW/DESCRIBE/EXPLAIN/WITH，含语句白名单、危险模式拦截、表名白名单、列黑名单、LIMIT 注入、结果行数硬上限。注意：go.mod 中只有 `modernc.org/sqlite`（且仅测试导入），mysql/postgres 驱动未引入，生产使用需在相应构建中注册驱动。
- **子进程隔离**: shell_exec / code_run 通过 `Setpgid` 创建独立进程组，便于超时时按组终止整个子进程树（并非 rlimit 资源限制，内存/CPU 无硬性限额）。
- **工作目录沙箱**: `work_dir` 配置作为 `file_read`/`file_write` 的 `AllowedDirs`（`RegisterBuiltinTools(reg, workDir)` 接线），目录外路径与独立 `..` 路径段均被拒绝。
- **HTTP 认证**: `auth_mode` 支持 `header`（内网信任 `X-User-ID`）、`api_key`（校验 `X-API-Key`，密钥必须配置，为空拒绝启动）、`jwt`（HS256 签名校验，`auth_jwt_secret` 必填，取 `sub` 作为 userID，校验 `exp`）。
- **工具调用门禁**: 引擎在每次工具执行前经 `engine.ToolGate` 接口做策略检查（`internal/session/gate.go` 适配 security 引擎）：`deny` 直接拦截并把原因反馈给 LLM；`require_approval` 走审批回调——CLI 交互式询问（y/N），HTTP 模式自动拒绝并记日志。`rm -rf` 等破坏性命令的裁决在策略层（policy.yaml + 内置模板），工具层黑名单只兜底 fork bomb、写设备等绝对危险命令，`rm file.txt` 等常规用法不受影响。
- **不要提交密钥**: API Key 一律用 `${ENV_VAR}` 引用，禁止硬编码到 `config.yaml`。

## 代码风格约定

- 标准 Go 风格：`gofmt` 格式化，包注释采用 `// Package xxx ...` 形式。
- **所有注释、错误消息、用户可见输出均使用中文**，与现有代码保持一致（例如错误消息 `"不支持的数据库驱动: %s（支持 mysql/postgres/sqlite）"`）。
- 导出标识符必须有中文文档注释，注释以标识符名称开头（如 `// NewManager 创建空管理器。`）。
- 新功能应放入对应的 `internal/` 包，通过接口与引擎解耦，避免在 `cmd/` 中堆积业务逻辑。
- 配置优先：能通过 YAML 声明的能力（供应商、数据源、策略）不要写死到代码里。

## 测试策略

- 测试文件与源码同包（`*_test.go`），使用标准 `testing` 包，无第三方测试框架。
- 所有测试均为离线单元测试，不依赖真实 LLM API 或网络，可直接运行 `go test ./...`。
- 集成测试通过导入 `modernc.org/sqlite` 驱动 + 内存数据库实现（见 `internal/tool/dbquery/tool_integration_test.go`、`internal/bootstrap/bootstrap_test.go`）。
- 已有测试覆盖：config、llm（重试/缓存/类型）、engine、security、memory、skill、session、ctxmgr、tool（含 sanitizer）、dbquery、kbtools、kb、mcp、trace、bootstrap、registry。
- 尚无测试的包：cmd/*、doctor——修改这些包时建议补充测试。
- 修改代码后务必运行 `go build ./... && go vet ./... && go test ./...` 验证。

## 其他说明

- 项目根目录有两份中文 Word 文档（`AI_Agent_技术方案.docx`、`Tommy-Cat_Agent_使用手册.docx`），包含更详细的设计与使用说明。
- `data/skills.json` 与 `data/users/`（每用户 user.md 画像）为运行时生成内容，不要手工编辑（user.md 允许用户手工修改，profiler 更新时会保留仍有效的旧偏好）。
- 无 Makefile、无 lint 配置文件——构建测试均直接使用 `go` 命令。CI 配置为 `.github/workflows/ci.yml`（gofmt 检查、vet、test、linux/darwin 交叉编译）。
- `go` 工具链若不在 PATH，可尝试 `~/local/go/bin/go` 或 `~/go-sdk/go/bin/go`（本机已验证 go 1.25 工具链可正常构建并通过全部测试）。
