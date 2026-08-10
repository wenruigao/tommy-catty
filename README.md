# Tommy-Cat Agent

通用任务智能体 — 基于 Go 语言开发的 AI Agent，采用 ReAct（Reasoning + Acting）执行循环，支持多模型接入、工具调用、安全策略、Skill 自动生成与多渠道接入。

## 核心特性

- **配置驱动的模型接入** — 任何兼容 OpenAI Chat Completions API 的服务（及 Anthropic Messages API）均可通过 YAML 配置接入，无需修改代码
- **ReAct 执行引擎** — 思考 → 行动 → 观察循环，支持多步骤复杂任务、上下文自动压缩与可选反思重规划
- **安全策略引擎** — 声明式 YAML 策略（Policy-as-Code），五检查点 × 五效果（拦截、审批、脱敏、限流、放行），附 JSONL 审计日志
- **Skill 自动生成** — 从执行轨迹自动提取可复用技能（生成门控防重复 + 版本快照），相似任务自动匹配
- **Channel 接入层** — 钉钉、飞书、微信、企业微信、Telegram、WhatsApp、QQ 七大平台 + 通用 webhook，声明式配置即接入
- **多用户 HTTP 服务** — 三种认证模式、per-user 会话与限流、每用户画像
- **Token 计量与预算** — 分层分模型计量、日预算门禁与 80% 预警，用量经 API 暴露
- **语义缓存** — L1 精确哈希层（缓存键含工具列表），重复请求免 LLM 调用
- **多供应商故障转移** — 指数退避重试 + 熔断器 + 自动切换备用模型
- **记忆分层存储** — 长期记忆与用户画像多层落盘：remote 全量 + sqlite/file 按保留窗口（未配远端时 sqlite 全量）；首次配远端自动回迁存量；会话创建预热最近历史
- **健康自检（Doctor）** — 内置 9 项环境检查，支持自动修复常见问题

## 项目结构

```
tommy-catty/
├── cmd/
│   ├── agent/main.go            # CLI 入口（交互式 REPL）
│   ├── server/main.go           # HTTP 服务入口（多用户 + Channel 接入层）
│   └── memstore/main.go         # 记忆存储服务（remote 后端）与迁移工具
├── config/
│   ├── config.go                # 配置加载与解析
│   ├── config.yaml              # 主配置文件
│   ├── policy.yaml              # 安全策略配置
│   ├── agent.md / soul.md       # Persona 文件
├── internal/
│   ├── llm/                     # LLM 网关：路由/重试/熔断/降级、语义缓存、Token 计量
│   ├── engine/                  # ReAct 执行引擎
│   ├── tool/                    # 工具注册表与内置工具（含 db_query、kb）
│   ├── security/                # 安全策略引擎（五检查点）+ 审计日志
│   ├── session/                 # 会话管理、per-user 限流、Persona 组装
│   ├── memory/                  # 记忆系统（工作记忆 + 冲突消解）
│   ├── memstore/                # 记忆持久化（file/sqlite/remote 后端 + REST 服务）
│   ├── skill/                   # Skill 生成/匹配、生成门控、版本管理
│   ├── channel/                 # Channel 接入层 Hub 与 8 种渠道 adapter
│   ├── ctxmgr/                  # 上下文压缩
│   ├── search/                  # 搜索引擎（DuckDuckGo/Tavily）
│   ├── kb/                      # 本地知识库（BM25）
│   ├── mcp/                     # MCP 远程工具客户端
│   ├── trace/                   # 执行追踪
│   ├── doctor/                  # 健康自检
│   └── bootstrap/               # 组件装配
├── data/                        # Skill、审计日志、用户画像与长期记忆持久化
├── go.mod
└── go.sum
```

## 快速开始

### 环境要求

- Go 1.25+
- 网络可访问 LLM API 端点

### 编译构建

```bash
# 国内网络建议设置代理
export GOPROXY=https://goproxy.cn,direct

go mod tidy
go build -o bin/tommy-agent ./cmd/agent     # CLI
go build -o bin/tommy-server ./cmd/server   # HTTP / 渠道
go build -o bin/tommy-memstore ./cmd/memstore  # 记忆存储服务（remote 后端，可选）
```

### 配置 API Key

```bash
export MIMO_API_KEY="sk-your-mimo-key"
export DEEPSEEK_API_KEY="sk-your-deepseek-key"
```

### 启动

```bash
./bin/tommy-agent          # CLI 交互模式
./bin/tommy-server         # HTTP 服务模式（需配置 server 段）
```

CLI 启动后进入交互式界面，直接输入自然语言描述即可执行任务：

```
🐱 > 帮我搜索 Go 1.25 的新特性并总结
  ⏳ 正在执行...
  ✅ 完成 (耗时 3.2s, Token: 1847)
  📝 Go 1.25 主要新特性包括...
```

## 模型接入

在 `config/config.yaml` 的 `llm.providers` 下添加条目即可：

```yaml
llm:
  default_provider: "mimo"
  fallback_provider: "deepseek"

  # 语义缓存（缺省关闭）
  # cache: { enabled: true, capacity: 500, ttl: "10m" }
  # Token 日预算（计量始终启用，预算可选）
  # meter: { daily_token_limit: 1000000 }

  providers:
    mimo:
      base_url: "https://.../v1/chat/completions"
      api_key: "${MIMO_API_KEY}"
      model: "mimo-v2.5-pro"

    # Anthropic 协议
    # anthropic:
    #   protocol: "anthropic"
    #   api_key: "${ANTHROPIC_API_KEY}"
    #   model: "claude-sonnet-4-5"

    # 本地模型 (Ollama / vLLM / LM Studio) 只需 base_url 指向本地端点
```

故障转移：主供应商失败时指数退避重试（默认 3 次），仍失败则熔断并切换 `fallback_provider`。

## HTTP API（前缀 /api/v1）

| 方法与路径 | 说明 |
|------------|------|
| `POST /api/v1/chat` | 执行任务 `{"message": "..."}` |
| `GET /api/v1/history` | 查询会话历史 |
| `POST /api/v1/clear` | 清空会话记忆 |
| `GET /api/v1/usage` | Token 用量统计（分模型、缓存命中率、日预算） |
| `GET /api/v1/health` | 健康检查 |

认证模式：`header`（内网信任 X-User-ID）/ `api_key` / `jwt`（HS256，exp 必填）。

## 渠道接入（Channel）

`config/config.yaml` 增加 `channels` 段即可接入，会话键 `"渠道名:用户/群ID"` 与 HTTP 身份隔离，限流/门禁/审计自动生效：

| 渠道 | 说明 |
|------|------|
| `webhook` | 通用 HTTP 接收 + callback_url 异步投递（任何系统可直接接入） |
| `dingtalk` | 钉钉企业内部机器人（回调加签验证） |
| `feishu` | 飞书自建应用（url_verification + 事件签名验证） |
| `wechat` | 微信公众号（echostr 校验 + XML 消息，配置键也可写 `微信`） |
| `wecom` | 企业微信自建应用（明文/AES 加密回调） |
| `telegram` | Bot 长轮询（无需公网回调） |
| `whatsapp` | Cloud API Webhook + 出站消息接口 |
| `qq` | QQ 官方机器人（ed25519 验签） |

启用渠道但必填凭证缺失会拒绝启动；未配置时接入层完全不启动。

## 命令行命令

| 命令 | 功能 |
|------|------|
| `/help` | 显示帮助 |
| `/quit` | 退出程序 |
| `/doctor` | 完整健康自检（检测 + 自动修复） |
| `/skills` | 列出所有已生成的 Skill |
| `/skill <id>` | 查看 Skill 详情 |
| `/policies` | 查看已加载的安全策略 |
| `/config` | 查看/管理配置（get/set/unset/use/path/schema/validate/patch/reset，持久化到 `config.local.yaml` 覆盖层） |
| `/trace` | 查看最近一次执行追踪 |
| `/clear` | 清空会话记忆 |

## 安全策略

策略文件位于 `config/policy.yaml`，采用声明式 YAML 定义（另有内置模板策略自动加载）：

```yaml
policies:
  - id: block-destructive
    priority: 1
    enabled: true
    when:
      tool_names: [shell_exec, code_run]
      pattern: "(?i)(rm\\s+-rf|drop\\s+table)"
    then:
      effect: deny
      message: "检测到破坏性操作，已拦截。"
```

- **检查点**：`task_start`（提示注入）、`tool_call`（工具调用前）、`tool_return`（工具返回清洗）、`llm_output`（输出脱敏）、`task_end`（成本评估）
- **效果**：`deny`、`require_approval`（CLI 交互审批；HTTP/渠道自动拒绝）、`redact`、`throttle`、`allow`
- **审计**：L2+ 工具调用与策略决策写入 `data/audit.jsonl`（JSONL 追加，可追溯）

## 内置工具

| 工具 | 功能 | 风险等级 |
|------|------|----------|
| `web_search` | 网络搜索（DuckDuckGo/Tavily） | L0 (只读) |
| `web_fetch` | 获取网页内容（内置 SSRF 防护） | L0 (只读) |
| `file_read` | 读取文件（工作目录沙箱） | L0 (只读) |
| `file_write` | 写入文件（工作目录沙箱） | L2 (写入) |
| `db_query` | 数据库只读查询（SQL 白名单 + 结果缓存） | L1 |
| `kb_search` 等 | 本地知识库检索（BM25） | L0 (只读) |
| `code_run` | 执行代码片段（临时目录 + 输出截断） | L3 (高危) |
| `shell_exec` | 执行 Shell 命令（危险命令兜底 + 目录白名单） | L3 (高危) |
| MCP 远程工具 | 经 Model Context Protocol 动态注册 | 可配 |

## 健康自检

运行 `/doctor` 执行完整环境检查：

```
🔍 Tommy-Cat Agent 健康检查
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
✅ 配置文件检查        通过
✅ LLM 供应商连接     通过 (2/2)
✅ 安全策略加载       通过
✅ 工具注册表         通过
✅ Skill 存储         通过
✅ 工作目录           通过
✅ 网络连通性         通过
✅ 系统资源           通过
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
```

启动时自动执行快速自检（仅 Critical 级别），发现问题会提示运行 `/doctor`。

## 文档

- 技术方案：`AI_Agent_技术方案.md`
- 使用手册：`Tommy-Cat_Agent_使用手册.md`
- 手动测试清单：`手动运行手册.md`

## 技术栈

- **语言**: Go 1.25
- **依赖**: google/uuid, gopkg.in/yaml.v3 等（HTTP 用标准库）
- **架构**: 模块化 internal 包，接口驱动设计
- **协议**: OpenAI Chat Completions API（SSE 流式）+ Anthropic Messages API
- **平台**: macOS / Linux（Windows 未支持）

## License

MIT
