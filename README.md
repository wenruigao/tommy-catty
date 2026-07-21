# Tommy-Cat Agent

通用任务智能体 — 基于 Go 语言开发的 AI Agent，采用 ReAct（Reasoning + Acting）执行循环，支持多模型接入、工具调用、安全策略和 Skill 自动生成。

## 核心特性

- **配置驱动的模型接入** — 任何兼容 OpenAI Chat Completions API 的服务均可通过 YAML 配置接入，无需修改代码
- **ReAct 执行引擎** — 思考 → 行动 → 观察循环，支持多步骤复杂任务
- **安全策略引擎** — 声明式 YAML 策略（Policy-as-Code），支持拦截、脱敏、审批、限流等多种效果
- **Skill 自动生成** — 从执行轨迹中自动提取可复用技能，后续相似任务自动匹配
- **三层记忆系统** — 工作记忆、情景记忆、语义记忆
- **健康自检（Doctor）** — 内置 8 项环境检查，支持自动修复常见问题
- **多供应商故障转移** — 主供应商失败时自动重试并切换备用模型

## 项目结构

```
tommy-cat/
├── cmd/agent/main.go          # CLI 入口（交互式 REPL）
├── config/
│   ├── config.go              # 配置加载与解析
│   ├── config.yaml            # 主配置文件（模型供应商声明）
│   └── policy.yaml            # 安全策略配置
├── internal/
│   ├── llm/                   # LLM 网关与通用 Provider
│   ├── engine/                # ReAct 执行引擎
│   ├── tool/                  # 工具注册表与内置工具
│   ├── memory/                # 三层记忆系统
│   ├── security/              # 安全策略引擎
│   ├── skill/                 # Skill 自动生成与匹配
│   ├── doctor/                # 健康自检系统
│   └── trace/                 # 执行追踪
├── data/                      # Skill 持久化存储
├── go.mod
└── go.sum
```

## 快速开始

### 环境要求

- Go 1.22+
- 网络可访问 LLM API 端点

### 编译构建

```bash
# 国内网络建议设置代理
export GOPROXY=https://goproxy.cn,direct

# 下载依赖并编译
go mod tidy
go build -o bin/tommy-agent ./cmd/agent
```

### 配置 API Key

```bash
export DEEPSEEK_API_KEY="sk-your-deepseek-key"
export DASHSCOPE_API_KEY="sk-your-dashscope-key"
```

### 启动

```bash
./bin/tommy-agent
```

启动后进入交互式界面，直接输入自然语言描述即可执行任务：

```
🐱 > 帮我搜索 Go 1.22 的新特性并总结
  ⏳ 正在执行...
  ✅ 完成 (耗时 3.2s, Token: 1847)
  📝 Go 1.22 主要新特性包括...
```

## 模型接入

在 `config/config.yaml` 的 `llm.providers` 下添加条目即可接入任意 OpenAI 兼容服务：

```yaml
llm:
  default_provider: "deepseek"
  fallback_provider: "qwen"

  providers:
    deepseek:
      base_url: "https://api.deepseek.com/chat/completions"
      api_key: "${DEEPSEEK_API_KEY}"
      model: "deepseek-chat"
      max_tokens: 65536
      timeout: "120s"

    qwen:
      base_url: "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions"
      api_key: "${DASHSCOPE_API_KEY}"
      model: "qwen-max"
      max_tokens: 32768

    # 本地模型 (Ollama)
    # ollama:
    #   base_url: "http://localhost:11434/v1/chat/completions"
    #   api_key: "ollama"
    #   model: "qwen2.5:72b"
    #   timeout: "300s"

    # 自部署推理服务 (vLLM)
    # vllm:
    #   base_url: "http://localhost:8000/v1/chat/completions"
    #   api_key: ""
    #   model: "Qwen/Qwen2.5-72B-Instruct"
```

故障转移：主供应商失败时自动重试 3 次（指数退避），仍失败则切换到 `fallback_provider`。

## 命令行命令

| 命令 | 功能 |
|------|------|
| `/help` | 显示帮助 |
| `/quit` | 退出程序 |
| `/doctor` | 完整健康自检（检测 + 自动修复） |
| `/skills` | 列出所有已生成的 Skill |
| `/skill <id>` | 查看 Skill 详情 |
| `/policies` | 查看已加载的安全策略 |
| `/trace` | 查看最近一次执行追踪 |
| `/clear` | 清空工作记忆 |

## 安全策略

策略文件位于 `config/policy.yaml`，采用声明式 YAML 定义：

```yaml
policies:
  - id: block-destructive
    name: "阻止破坏性操作"
    priority: 1
    enabled: true
    when:
      tool_names: [shell_exec, code_run]
      pattern: "(?i)(rm\\s+-rf|drop\\s+table)"
    then:
      effect: deny
      message: "检测到破坏性操作，已拦截。"
```

支持的效果类型：`deny`（拦截）、`require_approval`（需确认）、`redact`（脱敏）、`throttle`（限流）、`allow`（放行）。

## 内置工具

| 工具 | 功能 | 风险等级 |
|------|------|----------|
| `web_search` | 网络搜索 | L0 (只读) |
| `web_fetch` | 获取网页内容 | L0 (只读) |
| `file_read` | 读取文件 | L0 (只读) |
| `file_write` | 写入文件 | L2 (写入) |
| `code_run` | 执行代码片段 | L3 (高危) |
| `shell_exec` | 执行 Shell 命令 | L3 (高危) |

## 健康自检

运行 `/doctor` 执行完整环境检查：

```
🔍 Tommy-Cat Agent 健康检查
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
✅ 配置文件检查        通过
✅ LLM 供应商连接     通过 (2/2)
✅ 安全策略加载       通过 (7条策略)
✅ 工具注册表         通过 (6个工具)
✅ Skill 存储         通过
✅ 工作目录           通过
✅ 网络连通性         通过
✅ 系统资源           通过
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
检查完成: 8通过 / 0警告 / 0错误
```

启动时自动执行快速自检（仅 Critical 级别），发现问题会提示运行 `/doctor`。

## 技术栈

- **语言**: Go 1.22
- **依赖**: google/uuid, gopkg.in/yaml.v3
- **架构**: 模块化 internal 包，接口驱动设计
- **协议**: OpenAI Chat Completions API（SSE 流式支持）

## License

MIT
