// Package multiagent 实现 Orchestrator-Worker 多 Agent 协作模式。
// 编排者（Orchestrator）接收复杂任务，通过 LLM 分解为子任务，
// 分配给拥有不同角色和工具子集的 Worker Agent 执行，最终汇总结果。
package multiagent

import "fmt"

// RoleDef 定义一个 Agent 角色，描述其能力、系统提示词和可用工具。
type RoleDef struct {
	// Name 角色唯一标识（如 "researcher"、"coder"）
	Name string `yaml:"name" json:"name"`
	// Description 角色能力描述，供 Orchestrator 的 LLM 选择角色
	Description string `yaml:"description" json:"description"`
	// SystemPrompt 角色专属系统提示词
	SystemPrompt string `yaml:"system_prompt" json:"system_prompt"`
	// Tools 可用工具白名单（如 ["web_search", "web_fetch"]）
	Tools []string `yaml:"tools" json:"tools"`
	// Model 可选，指定使用的 LLM 供应商名称（空则继承主 Agent）。
	// 注：当前 engine.LLMClient 接口不支持 per-call 模型选择，
	// 此字段为预留配置，待接口扩展后生效。
	Model string `yaml:"model" json:"model,omitempty"`
	// MaxIterations Worker 的最大 ReAct 迭代次数（<=0 使用默认 15）
	MaxIterations int `yaml:"max_iterations" json:"max_iterations,omitempty"`
	// MaxConcurrent 该角色最大并发实例数（<=0 使用默认 3）
	MaxConcurrent int `yaml:"max_concurrent" json:"max_concurrent,omitempty"`
}

// DefaultWorkerIterations Worker 默认最大迭代次数。
const DefaultWorkerIterations = 15

// DefaultMaxConcurrent 单角色默认最大并发数。
const DefaultMaxConcurrent = 3

// EffectiveMaxIterations 返回角色实际使用的最大迭代次数。
func (r *RoleDef) EffectiveMaxIterations() int {
	if r.MaxIterations <= 0 {
		return DefaultWorkerIterations
	}
	return r.MaxIterations
}

// EffectiveMaxConcurrent 返回角色实际使用的最大并发数。
func (r *RoleDef) EffectiveMaxConcurrent() int {
	if r.MaxConcurrent <= 0 {
		return DefaultMaxConcurrent
	}
	return r.MaxConcurrent
}

// Validate 校验角色定义的合法性。
func (r *RoleDef) Validate() error {
	if r.Name == "" {
		return fmt.Errorf("角色名称不能为空")
	}
	if r.Description == "" {
		return fmt.Errorf("角色 %q 缺少描述（description）", r.Name)
	}
	if r.SystemPrompt == "" {
		return fmt.Errorf("角色 %q 缺少系统提示词（system_prompt）", r.Name)
	}
	if len(r.Tools) == 0 {
		return fmt.Errorf("角色 %q 未配置任何工具（tools）", r.Name)
	}
	return nil
}

// ValidateRoles 校验一组角色定义的合法性，确保名称唯一且每个角色均有效。
func ValidateRoles(roles map[string]*RoleDef) error {
	if len(roles) == 0 {
		return fmt.Errorf("未定义任何 Agent 角色")
	}
	for name, role := range roles {
		if role.Name != name {
			return fmt.Errorf("角色键名 %q 与角色 Name %q 不一致", name, role.Name)
		}
		if err := role.Validate(); err != nil {
			return err
		}
	}
	return nil
}
