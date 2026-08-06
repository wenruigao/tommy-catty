// Package security 提供内置的默认安全策略模板。
package security

// DefaultPolicies 返回内置的默认安全策略列表
// 包含 9 条常用安全策略，覆盖破坏性操作拦截、敏感信息保护、成本控制、提示注入防护等场景
func DefaultPolicies() []Policy {
	return []Policy{
		// 1. 拦截破坏性操作：禁止执行 rm -rf、DROP TABLE 等危险命令。
		// rm 子模式覆盖合并旗标（-rf/-fr/-Rf）、分离旗标（-r -f、-f -r，允许夹杂
		// 其他旗标）以及长旗标（--recursive --force）等形式，目标可以是 /、/*、~
		// 或引号包裹的路径；普通的 rm file.txt / rm -f file 不会命中。
		{
			ID:          "block-destructive",
			Name:        "拦截破坏性操作",
			Description: "禁止执行包含 rm -rf（含 -fr、-r -f、--recursive --force 等变体）、DROP TABLE 等破坏性命令的操作",
			Priority:    1,
			Enabled:     true,
			When: PolicyCondition{
				ToolNames: []string{"shell_exec", "code_run"},
				Pattern:   `(?i)(\brm\s+((-[a-z]+\s+)*-[a-z]*[rf][a-z]*[rf][a-z]*\b|(-[a-z]+\s+)*-[a-z]*r[a-z]*(\s+-[a-z]+)*\s+-[a-z]*f[a-z]*\b|(-[a-z]+\s+)*-[a-z]*f[a-z]*(\s+-[a-z]+)*\s+-[a-z]*r[a-z]*\b|--(recursive|force)[a-z]*(\s+--?[a-z]+)*\s+--(recursive|force)[a-z]*)|DROP\s+TABLE|DROP\s+DATABASE|TRUNCATE\s+TABLE|mkfs|dd\s+if=)`,
			},
			Then: PolicyAction{
				Effect:         EffectDeny,
				Message:        "检测到破坏性操作，已拦截",
				NotifySeverity: "critical",
			},
		},
		// 2. 保护敏感信息：对输出中的 API Key、密码等敏感内容进行脱敏。
		// 覆盖两类形式：sk- 开头的裸密钥（如 sk-xxxxxxxxxxxxxxxxxxxx），
		// 以及 "key: value" / "key=value" 形式的敏感字段。
		{
			ID:          "protect-secrets",
			Name:        "保护敏感信息",
			Description: "对输出内容中包含的 API Key、密码、Token 等敏感信息进行脱敏处理",
			Priority:    2,
			Enabled:     true,
			When: PolicyCondition{
				ActionType: []string{"tool_return", "llm_output"},
				Pattern:    `(?i)(sk-[a-zA-Z0-9]{20,}|(api[_-]?key|password|secret|token|credential|private[_-]?key)\s*[:=]\s*\S+)`,
			},
			Then: PolicyAction{
				Effect:         EffectRedact,
				Message:        "输出中检测到敏感信息，已进行脱敏处理",
				NotifySeverity: "warning",
			},
		},
		// 3. 成本控制：当单次操作成本超过阈值时需要审批
		{
			ID:          "cost-guard",
			Name:        "成本控制",
			Description: "当操作成本超过 10 时，需要人工审批确认",
			Priority:    3,
			Enabled:     true,
			When: PolicyCondition{
				MaxCost: 10,
			},
			Then: PolicyAction{
				Effect:         EffectRequireApproval,
				Message:        "操作成本超过阈值(10)，需要人工审批",
				NotifySeverity: "warning",
			},
		},
		// 4. 办公时间限制：高风险工具仅允许在工作时间使用。
		// 引擎语义为"条件命中即执行效果"，因此条件必须描述"禁止窗口"：
		// 非工作时间 18:00-09:00（跨午夜写法）命中即 deny。
		// 若条件写成工作时间 09:00-18:00，会导致白天封禁、夜间放行（语义反转）。
		{
			ID:          "office-hours",
			Name:        "办公时间限制",
			Description: "L3 高风险工具仅允许在 09:00-18:00 工作时间内使用，非工作时间禁止",
			Priority:    4,
			Enabled:     true,
			When: PolicyCondition{
				ToolRisk:  []string{"L3"},
				TimeRange: "18:00-09:00",
			},
			Then: PolicyAction{
				Effect:         EffectDeny,
				Message:        "当前为非工作时间，高风险工具已禁止（仅允许 09:00-18:00 工作时间使用）",
				NotifySeverity: "warning",
			},
		},
		// 5. 频率限制：防止工具调用过于频繁
		{
			ID:          "rate-limiter",
			Name:        "调用频率限制",
			Description: "当工具调用频率超过每分钟 20 次时进行限流",
			Priority:    5,
			Enabled:     true,
			When: PolicyCondition{
				ActionType: []string{"tool_call"},
				MaxCost:    20, // 复用 MaxCost 字段表示频率阈值（次/分钟）
			},
			Then: PolicyAction{
				Effect:         EffectThrottle,
				Message:        "工具调用频率超过限制(20次/分钟)，已进行限流",
				NotifySeverity: "info",
			},
		},
		// 6. 数据外泄防护：对外部 IP 地址的访问需要审批
		{
			ID:          "data-exfil",
			Name:        "数据外泄防护",
			Description: "检测到向外部 IP 地址发送数据时，需要人工审批",
			Priority:    6,
			Enabled:     true,
			When: PolicyCondition{
				ActionType: []string{"tool_call"},
				Pattern:    `\b(?:(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\.){3}(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\b`,
			},
			Then: PolicyAction{
				Effect:         EffectRequireApproval,
				Message:        "检测到向外部IP地址发送数据，需要人工审批",
				NotifySeverity: "critical",
			},
		},
		// 7. 作用域围栏：禁止在工作目录外进行文件操作。
		// 仅匹配实际存在的 file_read / file_write 工具；pattern 与
		// config/policy.yaml 中的 scope-fence 写法保持一致（RE2 不支持
		// 负向先行断言 (?!...)，旧写法会导致正则编译失败、策略永不命中）。
		{
			ID:          "scope-fence",
			Name:        "作用域围栏",
			Description: "禁止在工作目录范围外进行文件读写操作",
			Priority:    7,
			Enabled:     true,
			When: PolicyCondition{
				ToolNames: []string{"file_read", "file_write"},
				Pattern:   `(/etc/|/usr/|/System/|/var/|~/.ssh)`,
			},
			Then: PolicyAction{
				Effect:         EffectDeny,
				Message:        "禁止在工作目录范围外进行文件操作",
				NotifySeverity: "critical",
			},
		},
		// 8. 输出审查：对超长输出进行审查
		{
			ID:          "output-review",
			Name:        "输出长度审查",
			Description: "当输出内容超过 5000 字符时，需要人工审批确认",
			Priority:    8,
			Enabled:     true,
			When: PolicyCondition{
				ActionType: []string{"llm_output", "tool_return"},
				// 使用 Pattern 匹配超长内容（通过引擎侧额外检查长度）
				Pattern: `(?s)^.{5000,}$`,
			},
			Then: PolicyAction{
				Effect:         EffectRequireApproval,
				Message:        "输出内容超过5000字符，需要人工审查",
				NotifySeverity: "info",
			},
		},
		// 9. 提示注入防护：在任务开始时检测输入中的明确指令注入短语。
		// 与 internal/tool/sanitizer.go 的注入特征同源，但只收录明确的注入指令
		// 短语（ignore/disregard/forget previous instructions、忽略/无视之前的指令等），
		// 不收录 "system prompt"、"你现在是" 这类容易误伤正常请求的宽泛特征。
		{
			ID:          "prompt-injection",
			Name:        "提示注入防护",
			Description: "检测任务输入中的提示注入短语（如 ignore previous instructions、忽略之前的指令），命中即拒绝",
			Priority:    10,
			Enabled:     true,
			When: PolicyCondition{
				ActionType: []string{"task_start"},
				Pattern: `(?i)(ignore\s+(all\s+)?(previous|prior|above)\s+(instructions|prompts|rules)` +
					`|disregard\s+(all\s+)?(previous|prior|above)` +
					`|forget\s+(everything|all|your)\s+(above|previous|instructions)` +
					`|忽略(之前|以上|上面)的(指令|规则|提示)` +
					`|无视(之前|以上)的(要求|设定))`,
			},
			Then: PolicyAction{
				Effect:         EffectDeny,
				Message:        "检测到提示注入尝试，已拒绝执行",
				NotifySeverity: "critical",
			},
		},
	}
}
