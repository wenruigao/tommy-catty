// Package skill 提供技能系统的数据结构定义，用于管理和复用 Agent 的工作流程。
package skill

import "time"

// Skill 表示一个可复用的技能，封装了 Agent 执行特定任务的工作流程
type Skill struct {
	// ID 技能唯一标识
	ID string `json:"id"`
	// Name 技能名称
	Name string `json:"name"`
	// Description 技能描述
	Description string `json:"description"`
	// Version 技能版本号
	Version string `json:"version"`
	// Tags 技能标签，用于分类和检索
	Tags []string `json:"tags"`
	// Trigger 触发规则，定义何时激活该技能
	Trigger TriggerRule `json:"trigger"`
	// Steps 技能执行步骤列表
	Steps []SkillStep `json:"steps"`
	// Tools 技能使用的工具列表
	Tools []string `json:"tools"`
	// PromptHints 提示词片段，用于引导 LLM 执行
	PromptHints string `json:"prompt_hints"`
	// CreatedAt 创建时间
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt 最后更新时间
	UpdatedAt time.Time `json:"updated_at"`
	// UsageCount 使用次数统计
	UsageCount int `json:"usage_count"`
	// SuccessRate 成功率（0.0 - 1.0）
	SuccessRate float64 `json:"success_rate"`
	// Source 技能来源信息
	Source SkillSource `json:"source"`
}

// TriggerRule 定义技能的触发规则
type TriggerRule struct {
	// Keywords 触发关键词列表
	Keywords []string `json:"keywords"`
	// IntentPatterns 意图匹配的正则表达式列表
	IntentPatterns []string `json:"intent_patterns"`
	// MinSimilarity 最小相似度阈值（0.0 - 1.0）
	MinSimilarity float64 `json:"min_similarity"`
}

// SkillStep 表示技能中的一个执行步骤
type SkillStep struct {
	// Order 步骤执行顺序
	Order int `json:"order"`
	// Action 动作类型（如 call_tool, prompt, condition, loop）
	Action string `json:"action"`
	// ToolName 要调用的工具名称
	ToolName string `json:"tool_name"`
	// Template 参数模板，支持变量替换
	Template string `json:"template"`
	// Condition 执行条件表达式
	Condition string `json:"condition"`
	// OnError 错误处理策略（skip/retry/abort/fallback）
	OnError string `json:"on_error"`
}

// SkillSource 记录技能的来源信息
type SkillSource struct {
	// Type 来源类型（manual/auto_extract/imported）
	Type string `json:"type"`
	// TraceID 关联的执行追踪ID
	TraceID string `json:"trace_id"`
	// TaskSummary 原始任务摘要
	TaskSummary string `json:"task_summary"`
}
