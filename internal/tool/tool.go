package tool

import (
	"context"
	"time"
)

// Tool 是所有工具必须实现的核心接口。
// 每个工具提供名称、描述、参数 schema 以及执行逻辑。
type Tool interface {
	// Name 返回工具的唯一标识名称
	Name() string
	// Description 返回工具的功能描述，供 LLM 理解工具用途
	Description() string
	// Parameters 返回工具的 JSON Schema 参数定义
	Parameters() JSONSchema
	// Execute 执行工具逻辑，args 为经过验证的参数字典
	Execute(ctx context.Context, args map[string]interface{}) (Result, error)
}

// JSONSchema 描述工具参数的 JSON Schema 结构
type JSONSchema struct {
	Type       string              `json:"type"`
	Properties map[string]Property `json:"properties"`
	Required   []string            `json:"required,omitempty"`
}

// Property 描述单个参数的类型和约束
type Property struct {
	Type        string      `json:"type"`
	Description string      `json:"description"`
	Enum        []string    `json:"enum,omitempty"`
	Default     interface{} `json:"default,omitempty"`
}

// Result 是工具执行后的返回结果
type Result struct {
	// Output 工具的正常输出内容
	Output string `json:"output"`
	// Error 工具执行中的错误信息（非致命）
	Error string `json:"error,omitempty"`
	// Metadata 附加元数据，如耗时、来源等
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// RiskLevel 表示工具的风险等级，用于权限控制和审批流程
type RiskLevel int

const (
	// RiskReadOnly 只读操作，无副作用
	RiskReadOnly RiskLevel = 0
	// RiskLowWrite 低风险写操作，如写入临时文件
	RiskLowWrite RiskLevel = 1
	// RiskHighWrite 高风险写操作，如修改用户文件
	RiskHighWrite RiskLevel = 2
	// RiskDangerous 危险操作，如执行任意代码或 shell 命令
	RiskDangerous RiskLevel = 3
)

// String 返回风险等级的可读名称
func (r RiskLevel) String() string {
	switch r {
	case RiskReadOnly:
		return "read_only"
	case RiskLowWrite:
		return "low_write"
	case RiskHighWrite:
		return "high_write"
	case RiskDangerous:
		return "dangerous"
	default:
		return "unknown"
	}
}

// ToolMeta 将工具与其元信息（风险等级、超时时间）绑定
type ToolMeta struct {
	Tool
	RiskLevel RiskLevel
	Timeout   time.Duration
}
