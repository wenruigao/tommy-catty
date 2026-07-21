// Package security 提供安全策略引擎，用于在 Agent 执行过程中进行安全检查和访问控制。
package security

import "time"

// Effect 表示策略执行的效果类型
type Effect string

const (
	// EffectAllow 允许操作通过
	EffectAllow Effect = "allow"
	// EffectDeny 拒绝操作
	EffectDeny Effect = "deny"
	// EffectRequireApproval 需要人工审批
	EffectRequireApproval Effect = "require_approval"
	// EffectRedact 对敏感内容进行脱敏处理
	EffectRedact Effect = "redact"
	// EffectThrottle 对操作进行限流
	EffectThrottle Effect = "throttle"
)

// PolicyCondition 定义策略的匹配条件
type PolicyCondition struct {
	// UserRoles 匹配的用户角色列表
	UserRoles []string `yaml:"user_roles" json:"user_roles"`
	// ToolNames 匹配的工具名称列表
	ToolNames []string `yaml:"tool_names" json:"tool_names"`
	// ToolRisk 匹配的工具风险等级列表（如 "L1", "L2", "L3"）
	ToolRisk []string `yaml:"tool_risk" json:"tool_risk"`
	// ActionType 匹配的操作类型列表
	ActionType []string `yaml:"action_type" json:"action_type"`
	// Pattern 正则表达式，用于匹配内容
	Pattern string `yaml:"pattern" json:"pattern"`
	// Sensitive 敏感关键词列表
	Sensitive []string `yaml:"sensitive" json:"sensitive"`
	// TimeRange 时间范围限制，格式如 "09:00-18:00"
	TimeRange string `yaml:"time_range" json:"time_range"`
	// MaxCost 最大允许成本阈值
	MaxCost float64 `yaml:"max_cost" json:"max_cost"`
}

// PolicyAction 定义策略匹配后执行的动作
type PolicyAction struct {
	// Effect 策略效果
	Effect Effect `yaml:"effect" json:"effect"`
	// Message 策略触发时的提示信息
	Message string `yaml:"message" json:"message"`
	// NotifyChannel 通知渠道（如 webhook 地址）
	NotifyChannel string `yaml:"notify_channel" json:"notify_channel"`
	// NotifySeverity 通知严重级别（info/warning/critical）
	NotifySeverity string `yaml:"notify_severity" json:"notify_severity"`
}

// Policy 表示一条完整的安全策略
type Policy struct {
	// ID 策略唯一标识
	ID string `yaml:"id" json:"id"`
	// Name 策略名称
	Name string `yaml:"name" json:"name"`
	// Description 策略描述
	Description string `yaml:"description" json:"description"`
	// Priority 优先级，数值越小优先级越高
	Priority int `yaml:"priority" json:"priority"`
	// Enabled 是否启用
	Enabled bool `yaml:"enabled" json:"enabled"`
	// When 匹配条件
	When PolicyCondition `yaml:"when" json:"when"`
	// Then 执行动作
	Then PolicyAction `yaml:"then" json:"then"`
}

// Checkpoint 表示一个安全检查点，在 Agent 执行流程的关键节点触发
type Checkpoint struct {
	// Type 检查点类型：task_start/tool_call/tool_return/llm_output/task_end
	Type string `json:"type"`
	// ToolName 当前调用的工具名称
	ToolName string `json:"tool_name"`
	// ToolRisk 工具风险等级（1-3，3为最高风险）
	ToolRisk int `json:"tool_risk"`
	// Content 当前操作的内容（命令、输出等）
	Content string `json:"content"`
	// UserID 当前用户ID
	UserID string `json:"user_id"`
	// Cost 当前操作的成本
	Cost float64 `json:"cost"`
	// Timestamp 检查点触发时间
	Timestamp time.Time `json:"timestamp"`
}

// Decision 表示策略引擎对某个检查点的评估结果
type Decision struct {
	// Effect 策略效果
	Effect Effect `json:"effect"`
	// PolicyID 触发该决策的策略ID
	PolicyID string `json:"policy_id"`
	// Message 决策说明信息
	Message string `json:"message"`
}
