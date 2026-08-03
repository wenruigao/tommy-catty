// Package security 提供内置的默认安全策略模板。
package security

// DefaultPolicies 返回内置的默认安全策略列表
// 包含 8 条常用安全策略，覆盖破坏性操作拦截、敏感信息保护、成本控制等场景
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
		// 2. 保护敏感信息：对输出中的 API Key、密码等敏感内容进行脱敏
		{
			ID:          "protect-secrets",
			Name:        "保护敏感信息",
			Description: "对输出内容中包含的 API Key、密码、Token 等敏感信息进行脱敏处理",
			Priority:    2,
			Enabled:     true,
			When: PolicyCondition{
				ActionType: []string{"tool_return", "llm_output"},
				Pattern:    `(?i)(api[_-]?key|password|secret|token|credential|private[_-]?key)\s*[:=]\s*\S+`,
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
		// 4. 办公时间限制：高风险工具仅允许在工作时间使用
		{
			ID:          "office-hours",
			Name:        "办公时间限制",
			Description: "L3 高风险工具仅允许在 09:00-18:00 工作时间内使用",
			Priority:    4,
			Enabled:     true,
			When: PolicyCondition{
				ToolRisk:  []string{"L3"},
				TimeRange: "09:00-18:00",
			},
			Then: PolicyAction{
				Effect:         EffectDeny,
				Message:        "高风险工具仅允许在工作时间(09:00-18:00)内使用",
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
		// 7. 作用域围栏：禁止在工作目录外进行文件操作
		{
			ID:          "scope-fence",
			Name:        "作用域围栏",
			Description: "禁止在工作目录范围外进行文件读写操作",
			Priority:    7,
			Enabled:     true,
			When: PolicyCondition{
				ToolNames: []string{"file_read", "file_write", "file_delete"},
				Pattern:   `(?i)(/etc/|/root/|/home/(?!workspace)|C:\\Windows|C:\\Users\\(?!workspace))`,
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
	}
}
