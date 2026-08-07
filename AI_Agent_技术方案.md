# Tommy-Cat Agent 技术方案

> 版本：v2（对齐代码当前实现状态，2026-08）
> 本文档为《AI_Agent_技术方案.docx》的 Markdown 同步版，以代码实际实现为准。

---

## 1. 概述

Tommy-Cat Agent 是一个用 Go 语言实现的通用任务智能体，采用 ReAct（Reasoning + Acting）执行循环。核心设计目标：

- **配置驱动**：模型接入、工具接入、渠道接入、安全策略均为声明式 YAML 配置，无需改代码
- **安全内建**：四级风险分级 + 五检查点策略引擎 + 审计日志，覆盖输入、工具、输出全链路
- **多入口**：CLI 交互、HTTP API（多用户）、Channel 渠道（钉钉/飞书/微信等 7 平台 + 通用 webhook）
- **可观测**：执行追踪、Token 计量、JSONL 审计日志

技术栈：Go 1.25，零重型框架（标准库 HTTP），依赖仅 `google/uuid`、`gopkg.in/yaml.v3` 等少量库。

## 2. 总体架构

```
┌──────────────────────────── 接入层 ────────────────────────────┐
│  CLI REPL (cmd/agent)                                          │
│  HTTP Server (cmd/server) ── /api/v1/chat|history|clear|usage  │
│  Channel Hub ── webhook / 钉钉 / 飞书 / 微信 / 企微 /          │
│                 Telegram / WhatsApp / QQ                       │
└───────────────┬────────────────────────────────────────────────┘
                │ 统一身份（userID / 渠道会话键 "渠道:用户ID"）
┌───────────────▼──────────── 会话层 ────────────────────────────┐
│  SessionManager（per-user 会话、TTL、上限回收）                 │
│  per-user 限流（token bucket）  task_start 安全门禁             │
│  Persona（agent.md / soul.md / 每用户 user.md 画像）            │
└───────────────┬────────────────────────────────────────────────┘
┌───────────────▼──────────── 引擎层 ────────────────────────────┐
│  ReAct Engine（思考→行动→观察循环，max_iterations 保护）        │
│  上下文压缩 (ctxmgr)   反思与重规划   Skill 匹配/生成门控       │
└───────┬───────────────────────┬────────────────────────────────┘
        │                       │
┌───────▼────── 工具层 ──────┐ ┌▼────────── 模型层 ──────────────┐
│ Registry + 风险等级 L0-L3  │ │ LLM Gateway                     │
│ 内置工具 / db_query / kb   │ │  多供应商路由 + 重试 + 熔断      │
│ MCP 远程工具               │ │  + 降级切换 + 语义缓存(L1)       │
└────────────────────────────┘ │  + Token 计量 (Meter)            │
                               └─────────────────────────────────┘
┌────────────────────────── 横切关注点 ──────────────────────────┐
│  安全策略引擎（五检查点 × 五效果）  审计日志(JSONL)             │
│  执行追踪（内存 + JSONL 导出）      Doctor 健康自检             │
└────────────────────────────────────────────────────────────────┘
```

## 3. 模块设计

### 3.1 LLM 网关（internal/llm）

**协议适配**：`protocol` 字段选择 OpenAI Chat Completions 兼容协议（默认，任何兼容端点均可）或 Anthropic Messages API。供应商声明在 `llm.providers`，支持 `${ENV}` 环境变量引用与自定义 headers。

**可靠性**：
- 指数退避重试（可配最大次数、退避上下限、抖动因子、总超时）
- 熔断器（连续失败阈值 → 打开 → 半开探测 → 恢复）
- 降级切换：主供应商重试耗尽后自动切到 `fallback_provider`

**语义缓存（4.5，L1 已实现）**：
- 缓存键 = SHA-256(model + 排序后工具名列表 + 归一化消息)，工具列表参与键计算，避免工具集变化导致错误命中
- 带工具调用结果（ToolCalls）的响应不进缓存，防止副作用重放
- 流式请求不读写缓存
- 由 Gateway 在成功路径统一回填（`afterChatSuccess`）
- L2 向量相似层：P2 未实现（依赖 embedding 模型），代码中已标注

**Token 计量/预算（4.7，已接线）**：
- `Meter` 由 Gateway 默认创建（计量始终开启，预算可选）：分层（执行/总结/画像）× 分模型统计，含缓存命中率
- Chat 入口预算门禁：日 Token 用量超限返回 `ErrBudgetExceeded`，不发起调用
- 达预算 80% 时日志预警（按天去重）
- 成本口径常量 `llm.CostPerToken`（0.00001 成本单位/token），CLI 与 HTTP 两侧统一用于 task_end 检查点的 `Checkpoint.Cost` 估算，使 cost-guard 类策略具备真实数据源
- 用量经 `GET /api/v1/usage` 暴露

### 3.2 ReAct 执行引擎（internal/engine）

- 思考 → 行动（工具调用）→ 观察循环；`max_iterations`（默认 20）兜底防死循环
- 通过 Gate 接口与安全层解耦：`TaskStartGate`（入口拦截）、`ToolGate`（工具调用前审批）、`OutputGate`（最终答案脱敏/拦截）、`ToolReturnGate`（工具返回内容清洗，防间接注入）
- 反思与重规划（默认关闭）：每 N 步 LLM 自评，满意度低于阈值调整策略，累积偏差超阈值触发重规划
- `ExecutionTrace` 记录全过程 span，供 `/trace` 查看与 JSONL 导出

### 3.3 工具系统（internal/tool）

**注册表与风险分级**（L0 只读 → L3 高危）：

| 工具 | 功能 | 风险等级 |
|------|------|----------|
| `web_search` | DuckDuckGo（免 Key）/ Tavily 搜索 | L0 |
| `web_fetch` | 网页抓取，内置 SSRF 防护（私网/回环/元数据地址拒绝） | L0 |
| `file_read` / `file_write` | 文件读写，工作目录沙箱 + 路径穿越防护 | L0 / L2 |
| `code_run` | 代码执行，独立临时目录，输出 1MB 截断 | L3 |
| `shell_exec` | Shell 执行，工具层兜底绝对危险命令，工作目录白名单校验；可争议命令（rm）下沉策略层裁决 | L3 |
| `db_query` | 数据库只读查询（SQL 白名单校验 + 连接池 + 结果缓存） | L1 |
| `kb_search` / `kb_read` / `kb_list` | 本地知识库检索（BM25 倒排索引） | L0 |
| MCP 远程工具 | 经 Model Context Protocol 从外部 server 动态发现注册 | 可配 |

**db_query 结果缓存（17.11，已接线）**：缺省启用（200 条 / 5min TTL），按数据源+规范化 SQL 命中；正确性约束：`max_rows` 覆盖请求不缓存、截断结果不缓存。`db_query_cache` 配置节可关闭。

### 3.4 安全策略引擎（internal/security）

**Policy-as-Code**：`config/policy.yaml` 声明式定义，YAML 策略 + 内置模板策略合并加载。

**五个检查点**（全部已接入运行路径）：

| 检查点 | 时机 | 典型策略 |
|--------|------|----------|
| `task_start` | 任务入口 | prompt-injection 拦截 |
| `tool_call` | 工具调用前 | deny / require_approval / throttle |
| `tool_return` | 工具返回后 | 间接注入清洗（内容包 `<tool_output>` 标签） |
| `llm_output` | 最终答案 | redact-secrets 脱敏、数据外泄检测 |
| `task_end` | 任务结束 | cost-guard 成本评估（Cost 由 Token 用量估算） |

**五种效果**：`deny`（拦截）、`require_approval`（CLI 交互审批；HTTP/渠道模式自动拒绝）、`redact`（脱敏）、`throttle`（限流）、`allow`（放行）。

**内置模板策略**：block-destructive（破坏性命令）、block-rm-recursive-force、scope-fence（/etc 等敏感路径）、prompt-injection、redact-secrets、cost-guard、office-hours（L3 工具仅工作时间可用）。

**审计日志**：JSONL 追加写（`audit_log_path`），记录 L2+ 工具调用与所有策略命中决策，含操作人、检查点、输入内容，保证审批决策可追溯。

**认证层**（HTTP 模式）：`header`（内网，信任 X-User-ID）/ `api_key`（可绑定固定身份 `auth_user_id`，防冒充）/ `jwt`（HS256，exp 必填强制过期）。空密钥配置直接拒绝启动，不静默降级。

### 3.5 会话与多用户（internal/session）

- `SessionManager`：per-user 会话、TTL 过期与上限回收
- per-user 限流：token bucket（`session.requests_per_minute`，默认 30），用户间互不阻塞
- Persona 三段式系统提示词：`agent.md`（权限边界，最高优先级，用户指令不可覆盖）+ `soul.md`（人格）+ `data/users/{userID}/user.md`（每用户画像，每 N 个任务由 LLM 自动总结更新）
- 记忆：工作记忆已接入；长期记忆为 P2（`conflict.go` 冲突检测/消解模块为其预留，代码中已标注）

### 3.6 Skill 系统（internal/skill）

- 从 `ExecutionTrace` 自动提取可复用 Skill，相似任务按匹配注入 PromptHints
- **生成门控 GenerationGate（12.7，已接线）**：四条件同时满足才生成 —— ① goal 指纹（SHA-256 小写+空白折叠）未生成过（防近似重复）② 步骤 ≥ 3 ③ 耗时 ≥ 30s ④ 日配额 10；`MarkGenerated` 仅在 Save 成功后消耗配额
- **版本管理 VersionManager（12.8，已接线）**：覆盖已有 Skill 前先快照变更前内容（changedBy="auto-extract"），最多保留 10 版，可回滚
- 持久化：`skill_store_path`（默认 data/skills.json）

### 3.7 上下文管理（internal/ctxmgr）

长对话自动压缩：消息超限触发，tool_call 消息整组处理（避免孤儿消息导致 API 400），压缩经 LLM 摘要。

### 3.8 Channel 接入层（internal/channel）

对齐 OpenClaw 的 Channel 机制，声明式配置即接入：

| 渠道 | 协议要点 |
|------|----------|
| `webhook` | 通用 HTTP 接收 + callback_url 异步投递（任何系统可直接接入），Bearer token 认证 |
| `dingtalk` | 企业内部机器人，回调加签验证 + sessionWebhook/oTo 回复 |
| `feishu` | 自建应用，url_verification 挑战 + X-Lark-Signature 事件签名验证 |
| `wechat` | 公众号，echostr 接入校验 + XML 消息 + 客服消息回复（明文模式） |
| `wecom` | 企业微信自建应用，明文/AES 加密回调 + 应用消息回复 |
| `telegram` | Bot 长轮询（无需公网回调），群聊仅响应 @ 消息 |
| `whatsapp` | Cloud API Webhook 格式 + 出站消息接口 |
| `qq` | 官方机器人 Webhook + ed25519 签名验证 + REST 回复 |

设计要点：
- Hub 统一调度：入站消息 → 渠道 adapter 解析/验签 → 以会话键 `"渠道名:用户/群ID"` 进入与 HTTP 相同的会话/限流/安全链路 → 回复经 adapter 出站
- 启用时必填凭证缺失直接拒绝启动（不允许无认证端点）
- 未配置 `channels` 时接入层不启动，行为与旧版完全一致

## 4. 关键数据流（HTTP chat 为例）

```
POST /api/v1/chat
  → 认证（header/api_key/jwt）→ task_start 门禁（注入拦截）
  → SessionManager 取/建会话 → per-user 限流
  → Persona 组装系统提示词 → Skill 匹配（命中则注入 PromptHints）
  → ReAct 循环：LLM Gateway（缓存→供应商→重试/熔断/降级）
      ├─ 工具调用：tool_call 策略评估 → 执行 → tool_return 清洗
      └─ 上下文超限时自动压缩
  → llm_output 脱敏/拦截 → 写会话记忆 → 用户画像按需更新
  → task_end 成本评估 → 响应 + Token 计量入账
```

## 5. 配置体系一览（config/config.yaml）

| 配置节 | 作用 |
|--------|------|
| `llm` | 供应商、默认/降级、重试、熔断、`cache`（语义缓存）、`meter`（日预算） |
| `engine` | 最大迭代、trace 导出路径、反思参数 |
| `server` | HTTP 模式：地址、认证模式与密钥 |
| `session` | per-user 限流 |
| `persona` | agent.md / soul.md / 画像更新间隔 |
| `databases` | db_query 数据源（表/列白名单、DSN） |
| `db_query_cache` | db_query 结果缓存（默认启用，可关） |
| `knowledge_bases` | 本地知识库（分块、BM25） |
| `search` | 搜索引擎（duckduckgo / tavily） |
| `mcp.servers` | MCP 远程工具（stdio / sse） |
| `channels` | 渠道接入层（8 种渠道声明式配置） |
| `policy_file` / `audit_log_path` / `skill_store_path` / `work_dir` | 安全与持久化路径 |

**覆盖层（overlay）**：与主配置同目录的 `config.local.yaml` 作为本地覆盖层，加载优先级 内置默认 < `config.yaml` < `config.local.yaml`。CLI `/config set/unset` 仅读写覆盖层（白名单键、类型校验、密钥脱敏），主配置文件永不改动；HTTP 与 CLI 共用同一加载入口 `config.LoadWithOverlay`。

## 6. 部署形态

1. **CLI 单机**：`cmd/agent`，交互式 REPL，适合本地使用与调试
2. **HTTP 多用户**：`cmd/server`，`/api/v1/*` REST API，三种认证模式
3. **渠道机器人**：HTTP 模式 + `channels` 配置，IM 平台消息直达 Agent

## 7. 实现状态与路线图

### 已实现（截至当前）

- 全部核心链路：ReAct、多供应商、工具系统、安全五检查点、会话多用户、Persona
- 本次接线完成的五项子系統：语义缓存 L1、Token 计量/预算、db_query 结果缓存、Skill 生成门控与版本快照、记忆模块 P2 标注
- Channel 接入层与 7 平台 adapter + 通用 webhook
- 观测面：trace、审计 JSONL、`GET /api/v1/usage`
- CLI `/config` 运行时配置管理（overlay 覆盖层持久化 + 键白名单校验 + 脱敏与审计）

### P2 路线图（未实现，代码中已标注）

| 项 | 说明 |
|----|------|
| 语义缓存 L2 | 向量相似层，依赖 embedding 模型 |
| 长期记忆 | longTerm 记忆体 + conflict.go 冲突检测/消解接线 |
| 进度流式推送 | 需 session 暴露中间步骤事件 |
| 渠道状态端点 | GET /api/v1/channels 状态管理 |

## 8. 测试与工程

- 全量 `go test ./...`（21 包）离线可跑；关键接线均有针对性单测（缓存键含工具、预算门禁、usage 端点、门控四条件、版本快照、db 缓存正确性约束等）
- CI：gofmt / vet / test / 双平台编译
- 平台：macOS / Linux（`internal/tool/limits_*.go` 未提供 Windows 实现）
