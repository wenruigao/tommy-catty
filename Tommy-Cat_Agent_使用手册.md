# Tommy-Cat Agent 使用手册

> 版本：v2（对齐代码当前实现状态，2026-08）
> 本文档为《Tommy-Cat_Agent_使用手册.docx》的 Markdown 同步版。
> 平台：macOS / Linux（Windows 未支持）

---

## 1. 简介

Tommy-Cat Agent 是基于 Go 语言的通用任务智能体：ReAct 执行循环 + 多模型接入 + 工具调用 + 安全策略 + Skill 自动生成。支持三种使用形态：

- **CLI 交互模式**：本地终端对话式使用
- **HTTP 服务模式**：多用户 REST API
- **渠道机器人**：钉钉 / 飞书 / 微信 / 企业微信 / Telegram / WhatsApp / QQ / 通用 webhook

## 2. 安装与构建

### 2.1 环境要求

- Go 1.25+（若不在 PATH，尝试 `export PATH="$HOME/local/go/bin:$PATH"`）
- 构建期可拉取依赖；运行期可访问 LLM API
- 国内网络建议：`export GOPROXY=https://goproxy.cn,direct`

### 2.2 构建

```bash
go mod tidy
go build -o bin/tommy-agent ./cmd/agent     # CLI 交互模式
go build -o bin/tommy-server ./cmd/server   # HTTP / 渠道模式
```

### 2.3 环境变量

config.yaml 中一律写 `${VAR}` 引用，不要把真实 Key 写进配置文件。

| 变量 | 用途 | 必填条件 |
|------|------|----------|
| `MIMO_API_KEY` | 默认 LLM 供应商 | 是（除非改默认供应商） |
| `DEEPSEEK_API_KEY` | 降级供应商 | 建议 |
| `DASHSCOPE_API_KEY` | qwen 供应商 | 可选 |
| `ANTHROPIC_API_KEY` | Anthropic 协议供应商 | 用时必填 |
| `TAVILY_API_KEY` | Tavily 搜索（默认 DuckDuckGo 免 Key） | 可选 |
| `AGENT_API_KEY` | HTTP `auth_mode: api_key` | 该模式必填 |
| `AGENT_JWT_SECRET` | HTTP `auth_mode: jwt`（HS256 密钥） | 该模式必填 |
| `ANALYTICS_DB_DSN` 等 | 数据库数据源 DSN | 配了数据源才需要 |
| `CHANNEL_WEBHOOK_TOKEN` | webhook 渠道认证 | 启用该渠道必填 |
| `DINGTALK_APP_KEY` / `DINGTALK_APP_SECRET` | 钉钉机器人 | 启用时必填 |
| `FEISHU_APP_ID` / `FEISHU_APP_SECRET` / `FEISHU_ENCRYPT_KEY` | 飞书自建应用 | 启用时必填 |
| `WECHAT_APP_ID` / `WECHAT_APP_SECRET` / `WECHAT_TOKEN` | 微信公众号 | 启用时必填 |
| `WECOM_AGENT_SECRET` / `WECOM_TOKEN` / `WECOM_AES_KEY` | 企业微信自建应用 | 启用时必填 |
| `TELEGRAM_BOT_TOKEN` | Telegram Bot | 启用时必填 |
| `WHATSAPP_WEBHOOK_TOKEN` | WhatsApp 入站验证 | 启用时必填 |
| `QQ_APP_ID` / `QQ_APP_SECRET` | QQ 官方机器人 | 启用时必填 |

## 3. CLI 模式

```bash
./bin/tommy-agent                          # 默认读 config/config.yaml
./bin/tommy-agent /path/to/other.yaml      # 指定配置文件
```

启动后进入 REPL（`🐱 >` 提示符），输入自然语言即执行任务。

| 命令 | 作用 |
|------|------|
| `/help` | 显示帮助 |
| `/doctor` | 健康自检（8 项检查 + 自动修复提示） |
| `/skills` | 列出所有 Skill |
| `/skill <id>` | 查看 Skill 详情（id 取 `/skills` 输出的前 8 位） |
| `/policies` | 查看已加载的安全策略 |
| `/config` | 查看/管理配置（`get`/`set`/`unset`/`use`/`path`，见 3.1） |
| `/trace` | 查看最近一次执行的追踪 span |
| `/clear` | 清空会话记忆 |
| `/quit` | 退出 |

启动时自动执行快速自检（仅 Critical 级别），发现问题提示运行 `/doctor`。

### 3.1 `/config` 运行时配置管理

| 命令 | 作用 |
|------|------|
| `/config` | 列出全部可配置键：当前生效值 + 来源标记（`[local]`/`[config]`），密钥自动脱敏；`/config <节名>` 过滤（如 `llm`/`engine`/`search`） |
| `/config get <key>` | 查看单键详情（值、来源、说明） |
| `/config set <key> <值>` | 设置并持久化；秘密键可用 `env:ENV_NAME` 写法引用环境变量（落盘为 `${ENV_NAME}`，不落明文） |
| `/config unset <key>` | 移除覆盖，恢复主配置/默认值 |
| `/config use <provider>` | 快捷切换默认模型供应商 |
| `/config patch <file>` | 按 YAML 补丁文件批量设置（先全量校验，任一非法整体拒绝，原子写入） |
| `/config reset` | 清空覆盖层全部覆盖项（主配置永不触碰） |
| `/config schema` | 打印键注册表：类型、枚举可选值、密钥标记与说明 |
| `/config validate` | 校验主配置 + 覆盖层：YAML 语法、`${ENV}` 引用缺失（警告）、覆盖层键的类型/语义（错误） |
| `/config path` | 显示主配置文件与覆盖层文件路径 |

**持久化机制（overlay 覆盖层）**：变更写入与主配置同目录的 `config.local.yaml`（已加入 `.gitignore`），主配置文件（含注释）永不改动。加载优先级：内置默认 < `config.yaml` < `config.local.yaml`。全部覆盖项清空后覆盖层文件自动删除。

**键白名单**：仅常用标量键可经 `/config` 修改（LLM 供应商/缓存/计量、引擎、搜索、会话、画像、记忆存储、工作目录等约 24 个静态键 + 各供应商的 `model`/`base_url`/`max_tokens`/`timeout`/`api_key` 动态键）；channels/databases/mcp 等复杂结构仍手工编辑主配置。未知键报错并提示最相近键名，类型非法（int/duration/enum）给出中文错误。

**密钥安全**：键名含 api_key/token/secret/dsn/password 的值显示为脱敏形式（`${ENV}` 引用原样展示）；以明文写入秘密键时给出警告（建议改用 `env:ENV_NAME` 或 `${ENV_VAR}` 引用）；set/unset/patch/reset 操作写入审计日志（仅记键名，值不落盘）。

**生效方式**：变更立即写入文件并同步内存中的配置视图，但运行中的组件（Gateway/Engine 等）统一在**重启后生效**。

## 4. HTTP 服务模式

`config/config.yaml` 取消 `server` 段注释：

```yaml
server:
  mode: "http"
  addr: ":8080"
  auth_mode: "header"                    # header | api_key | jwt
  # auth_api_key: "${AGENT_API_KEY}"     # api_key 模式必填（空密钥拒绝启动）
  # auth_jwt_secret: "${AGENT_JWT_SECRET}" # jwt 模式必填（HS256，exp 必须存在）
  # auth_user_id: "agent-bot"            # api_key 模式建议：绑定固定身份，忽略客户端 X-User-ID
```

```bash
./bin/tommy-server
```

### 4.1 API 端点（前缀 /api/v1）

| 方法与路径 | 说明 |
|------------|------|
| `POST /api/v1/chat` | 执行任务，请求体 `{"message": "..."}` |
| `GET /api/v1/history` | 查询当前用户会话历史 |
| `POST /api/v1/clear` | 清空当前用户会话记忆 |
| `GET /api/v1/usage` | Token 用量统计（分模型汇总、缓存命中率、日预算使用情况） |
| `GET /api/v1/health` | 健康检查 |

### 4.2 调用示例（header 模式）

```bash
# 执行任务
curl -X POST http://localhost:8080/api/v1/chat \
  -H "Content-Type: application/json" \
  -H "X-User-ID: alice" \
  -d '{"message": "帮我查一下今天的科技新闻"}'
# 响应：{"task_id":"...","answer":"...","steps":3,"token_usage":1234,"duration_ms":5678,"tools_used":["web_search"]}

# 用量统计
curl -H "X-User-ID: alice" http://localhost:8080/api/v1/usage
# 响应：{"enabled":true,"summary":{...},"cache_hit_ratio":0.0,"daily_budget":{"used":...,"limit":...,"exceeded":false}}

# 历史 / 清空 / 健康
curl -H "X-User-ID: alice" http://localhost:8080/api/v1/history
curl -X POST -H "X-User-ID: alice" http://localhost:8080/api/v1/clear
curl http://localhost:8080/api/v1/health
```

api_key 模式加 `-H "X-API-Key: $AGENT_API_KEY"`；jwt 模式加 `-H "Authorization: Bearer <token>"`。

生成测试用 JWT（HS256）：

```bash
python3 -c "
import hmac,hashlib,base64,json,time,os
b=lambda x:base64.urlsafe_b64encode(x).rstrip(b'=').decode()
h=b(json.dumps({'alg':'HS256','typ':'JWT'}).encode())
p=b(json.dumps({'sub':'alice','exp':int(time.time())+3600}).encode())
s=b(hmac.new(os.environ['AGENT_JWT_SECRET'].encode(),f'{h}.{p}'.encode(),hashlib.sha256).digest())
print(f'{h}.{p}.{s}')"
```

## 5. 渠道接入（Channel）

HTTP 模式下在 `config/config.yaml` 增加 `channels` 段即可接入 IM 平台。会话键为 `"渠道名:用户/群ID"`，与 HTTP X-User-ID 身份天然隔离；per-user 限流、安全门禁、审计按会话键自动生效。

> 约束：启用某渠道但必填凭证缺失会**拒绝启动**（不允许无认证端点）；未配置 `channels` 时接入层完全不启动。

### 5.1 webhook（通用接入，任何系统可用）

```yaml
channels:
  webhook:
    enabled: true
    token: "${CHANNEL_WEBHOOK_TOKEN}"    # 必填，调用方经 Authorization: Bearer <token> 携带
    # callback_url: "http://localhost:9000/callback"  # 默认投递地址（单次请求可覆盖）
    allow_users: ["*"]                   # 空或 ["*"] = 不限制
    group_mode: mention_only             # always | mention_only | never
    ack_message: "收到，处理中…"          # 受理提示（空则不发）
    request_timeout: 120s                # 单条消息执行超时
```

### 5.2 各平台渠道（配置凭证即接入）

| 渠道 | 必填配置 | 说明 |
|------|----------|------|
| `dingtalk` | `client_id` + `client_secret` | 钉钉企业内部机器人；回调加签验证；`path_prefix` 默认 `/channels/dingtalk` |
| `feishu` | `app_id` + `app_secret`（建议 `encrypt_key`） | 飞书自建应用；url_verification 挑战 + X-Lark-Signature 验签 |
| `wechat` | `app_id` + `app_secret` + `token` | 微信公众号；仅支持明文消息模式；配置键也可写中文 `微信` |
| `wecom` | `corp_id` + `agent_id` + `agent_secret` + `token` | 企业微信自建应用；配 43 位 `encoding_aes_key` 后支持 AES 加密回调 |
| `telegram` | `token` | Bot 长轮询，**无需公网回调**；群聊仅响应 @ 消息；`api_base` 可指向代理 |
| `whatsapp` | `token` + `phone_number_id` | Cloud API Webhook；`api_base` 可指向 Twilio 等中转网关 |
| `qq` | `app_id` + `app_secret` | QQ 官方机器人；回调地址 `/channels/qq`；ed25519 验签；勾选 C2C/群 @ 事件 |

完整配置示例（含可选字段）见 `config/config.yaml` 中 `channels` 注释段。

## 6. 模型接入

`llm.providers` 下添加条目即可，支持两种协议：OpenAI Chat Completions 兼容（默认）与 Anthropic Messages API（`protocol: "anthropic"`）。

```yaml
llm:
  default_provider: "mimo"
  fallback_provider: "deepseek"

  providers:
    mimo:
      base_url: "https://.../v1/chat/completions"
      api_key: "${MIMO_API_KEY}"
      model: "mimo-v2.5-pro"
      max_tokens: 32768
      timeout: "120s"

    anthropic:
      protocol: "anthropic"
      api_key: "${ANTHROPIC_API_KEY}"
      model: "claude-sonnet-4-5"
      max_tokens: 200000

    # 本地模型（Ollama / vLLM / LM Studio）只需 base_url 指向本地端点
```

可靠性（均可在 yaml 调整）：指数退避重试（默认 3 次）、熔断器（连续失败 5 次熔断，60s 后半开探测）、降级切换（重试耗尽自动切 `fallback_provider`）。

## 7. 性能与成本

### 7.1 语义缓存（默认关闭，需显式启用）

```yaml
llm:
  cache:
    enabled: true        # 显式启用
    capacity: 500
    ttl: "10m"
```

缓存键包含模型、工具列表与归一化消息；带工具调用的响应不缓存；流式请求不走缓存。向量相似层（L2）为 P2 未实现。

### 7.2 Token 计量与预算

计量**始终启用**（无需配置），预算可选：

```yaml
llm:
  meter:
    daily_token_limit: 1000000   # 每日 Token 预算（<=0 不限），达 80% 日志预警（每天一次）
```

超预算后请求直接被拒（不发起 LLM 调用）。用量随时可查：

```bash
curl -H "X-User-ID: alice" http://localhost:8080/api/v1/usage
```

### 7.3 db_query 结果缓存（默认启用）

```yaml
db_query_cache:
  enabled: true      # 置 false 可禁用
  capacity: 200
  ttl: "5m"
```

按数据源 + 规范化 SQL 命中；指定了自定义 `max_rows` 或结果被截断的查询不缓存（保证正确性）。

## 8. 安全

### 8.1 安全策略

策略文件 `config/policy.yaml`（声明式 YAML），另有一组内置模板策略自动加载。效果类型：

| 效果 | 行为 |
|------|------|
| `deny` | 直接拦截 |
| `require_approval` | CLI 交互审批；HTTP/渠道模式自动拒绝（无人值守不阻塞） |
| `redact` | 脱敏（如 API Key 替换为 `***`） |
| `throttle` | 限流 |
| `allow` | 放行 |

检查点：`task_start`（提示注入拦截）、`tool_call`（工具调用前）、`tool_return`（工具返回清洗，防间接注入）、`llm_output`（最终答案脱敏）、`task_end`（成本评估）。

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

### 8.2 限流与审计

- **per-user 限流**：`session.requests_per_minute`（默认 30，0 不限），token bucket，用户间互不阻塞；渠道消息按会话键同样生效
- **审计日志**：`audit_log_path`（默认 `data/audit.jsonl`），JSONL 追加写，记录 L2+ 工具调用与所有策略命中决策（含操作人、检查点、输入内容）；留空禁用

### 8.3 工具层防护（无需配置，始终生效）

- file_read / file_write：工作目录沙箱 + 路径穿越拦截
- shell_exec：绝对危险命令兜底拒绝 + working_dir 白名单校验
- web_fetch：SSRF 防护（私网/回环/云元数据地址拒绝）
- db_query：仅只读语句（SQL 白名单校验）+ 表/列黑白名单
- code_run：独立临时目录执行 + 输出 1MB 截断

## 9. Skill 系统

- 任务完成后自动从执行轨迹提取 Skill；相似任务自动匹配并注入执行经验
- **生成门控**（防近似重复 Skill）：goal 指纹去重 + 步骤 ≥ 3 + 耗时 ≥ 30s + 日配额 10，四条件同时满足才生成
- **版本快照**：覆盖同名 Skill 前先保存变更前内容（最多 10 版），可追溯
- 持久化于 `skill_store_path`（默认 `data/skills.json`）；CLI 用 `/skills`、`/skill <id>` 查看

## 10. 记忆与 Persona

- **工作记忆**：会话内多轮上下文，`/clear` 或 `POST /clear` 清空（同时清空该用户长期记忆）
- **长期记忆**：对话内容经记忆存储后端持久化，会话重建后仍可检索；语义矛盾的旧条目自动标记失效（sqlite 后端），每用户上限 `memory.max_entries_per_user`（默认 500，超限按时间淘汰）
- **记忆存储后端**（`memory.storage.type`）：`file`（默认，`data/memories/{userID}.jsonl`）/ `sqlite`（`data/memory.db`）/ `remote`（远程记忆服务，需先启动 `go run ./cmd/memstore serve`，配置 `url` 与 `token`）；用户画像随同后端存放（file 后端沿用 `data/users/{userID}/user.md`，路径不变）

  ```yaml
  memory:
    storage:
      type: sqlite            # file / sqlite / remote
      path: data/memory.db    # sqlite 数据库路径（file 时为 JSONL 目录）
      # url: http://mem.internal:9301   # remote 后端服务地址
      # token: ${MEMSTORE_TOKEN}        # remote 鉴权令牌，支持 ${ENV}
      # timeout: 3s
    max_entries_per_user: 500
  ```

- **Persona 三段式**：
  - `config/agent.md` — 职责与权限边界，**最高优先级**，用户指令不得覆盖
  - `config/soul.md` — 人格与对话风格
  - `data/users/{userID}/user.md` — 每用户画像，每 3 个任务（`persona.profile_update_interval_runs`）由 LLM 自动总结更新；可手工修改
- agent.md / soul.md 缺失时启动打警告并使用内置兜底文本，不影响运行

## 11. 可选能力

### 11.1 数据库查询（db_query）

```yaml
databases:
  analytics:
    driver: "postgres"                 # sqlite | mysql | postgres
    dsn: "${ANALYTICS_DB_DSN}"
    allowed_tables: ["orders", "users", "analytics_*"]
    denied_columns: ["users.password_hash", "*.secret_key"]
```

Agent 调用 `db_query` 时通过 `datasource` 指定数据源。注意：mysql/postgres 驱动未默认引入，需自行添加依赖并编译。

### 11.2 本地知识库

```yaml
knowledge_bases:
  - name: "project_docs"
    paths: ["./docs", "./README.md"]
    extensions: [".md", ".txt", ".go"]
    strategy: "auto"                   # auto | heading | paragraph
    top_k: 5
```

启动时扫描分块并建立内存 BM25 倒排索引，提供 `kb_search` / `kb_read` / `kb_list` 工具。

### 11.3 MCP 远程工具

```yaml
mcp:
  servers:
    - name: "filesystem"
      transport: "stdio"               # stdio | sse
      command: "npx"
      args: ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]
      risk_level: 1
```

启动时连接并注册外部工具；单个 server 失败只记警告不中断启动。

### 11.4 搜索引擎

`search.default_engine`: `duckduckgo`（默认，免 Key）或 `tavily`（配 `tavily_api_key`，效果更好；Key 错误自动 fallback）。

### 11.5 远程记忆服务（memstore）

多实例部署需要共享长期记忆与用户画像时，在中心机器部署独立的记忆存储服务，各 Agent 实例改用 `remote` 后端（配置见第 10 节）。

**1）启动服务**（落地后端可选 sqlite 或 file）：

```bash
go build -o bin/tommy-memstore ./cmd/memstore
export MEMSTORE_TOKEN=<强随机串>
./bin/tommy-memstore serve -addr :9301 -token $MEMSTORE_TOKEN -backend sqlite -db data/memory.db
```

**2）Agent 实例接入**（`config/config.yaml`）：

```yaml
memory:
  storage:
    type: remote
    url: http://mem.internal:9301
    token: ${MEMSTORE_TOKEN}   # 支持 ${ENV} 引用；token 自动脱敏展示
```

**3）验证**：`/doctor` 中 Memory storage 项应显示 `[OK] <url>`；错误令牌或地址不通时该项报错，Agent 记忆链路自动降级（写入失败仅警告，不阻塞任务）。

**存量迁移**：将本地已生成的用户画像一次性导入目标后端：

```bash
./bin/tommy-memstore migrate -from data/users -to sqlite -db data/memory.db
# 或导入远程服务：-to remote -url http://mem.internal:9301 -token $MEMSTORE_TOKEN
```

> 说明：长期记忆无存量（JSONL/数据库为新增格式），迁移仅针对画像；同一用户跨实例并发写入按 last-write-wins 处理。

## 12. 可观测

| 手段 | 用法 |
|------|------|
| 执行追踪 | CLI `/trace`；配置 `engine.trace_export_path: "data/traces.jsonl"` 导出每 span 一行 JSON |
| 审计日志 | `data/audit.jsonl`（见 8.2） |
| Token 用量 | `GET /api/v1/usage`（分模型汇总、缓存命中率、日预算） |
| 预算预警 | 日 Token 达预算 80% 时日志输出一次预警 |
| 健康自检 | `/doctor`：配置、LLM 连接、策略、工具、Skill 存储、记忆存储、工作目录、网络、系统资源共 9 项 |

## 13. 常见问题

- **`go` 命令找不到**：`export PATH="$HOME/local/go/bin:$PATH"`。
- **LLM 调用 401/403**：检查对应供应商环境变量是否已 export；config.yaml 只写 `${VAR}` 引用。
- **渠道启动失败**：启用渠道但必填凭证缺失会拒绝启动（设计如此）；检查对应环境变量。
- **Tavily 配了没生效**：确认 `tavily_api_key` 已取消注释且环境变量存在；Key 错误会自动 fallback 到 DuckDuckGo。
- **审批提示不出现**：仅策略效果为 `require_approval` 且命中 tool_call 检查点时才询问；HTTP/渠道模式固定自动拒绝。
- **Windows 编译失败**：`internal/tool/limits_*.go` 只有 darwin/linux 实现。
- **预算被拒**：`llm.meter.daily_token_limit` 达上限后新请求直接失败；调大或置 0 取消。
