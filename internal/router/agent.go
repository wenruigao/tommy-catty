package router

import (
	"fmt"
	"regexp"
)

// AgentSpec 定义一个预设专职 Agent：独立的系统提示词与工具白名单。
type AgentSpec struct {
	// Name Agent 唯一标识（如 "coder"、"researcher"）
	Name string `yaml:"name" json:"name"`
	// Description 能力描述，供 LLM 分类时判断该选哪个 Agent
	Description string `yaml:"description" json:"description"`
	// SystemPrompt Agent 专属系统提示词（仅替换 base prompt，
	// 仍经 session.BuildSystemPrompt 与 agent.md/soul.md/用户画像组装）
	SystemPrompt string `yaml:"system_prompt" json:"system_prompt"`
	// Tools 工具白名单；空表示不限制（兜底 Agent 通常留空）
	Tools []string `yaml:"tools" json:"tools"`
	// Keywords 规则触发词（大小写不敏感的子串匹配）
	Keywords []string `yaml:"keywords" json:"keywords"`
	// Patterns 规则触发的正则表达式（bootstrap 期预编译，编译失败即报错）
	Patterns []string `yaml:"patterns" json:"patterns"`
	// Priority 规则优先级，数值大者先匹配（默认 0）
	Priority int `yaml:"priority" json:"priority"`
	// MaxIterations 可选，覆盖会话默认的最大 ReAct 迭代次数（<=0 不覆盖）
	MaxIterations int `yaml:"max_iterations" json:"max_iterations,omitempty"`

	// compiled 预编译的正则（非配置字段，由 compilePatterns 填充）
	compiled []*regexp.Regexp
}

// Validate 校验 Agent 定义的合法性。
//
// 兜底 Agent 允许没有触发词（它靠回落命中，不参与规则匹配），
// 其余 Agent 必须至少有 description + system_prompt。
func (a *AgentSpec) Validate(isFallback bool) error {
	if a.Name == "" {
		return fmt.Errorf("Agent 名称不能为空")
	}
	if a.Description == "" {
		return fmt.Errorf("Agent %q 缺少描述（description），LLM 分类无法判断其适用场景", a.Name)
	}
	if a.SystemPrompt == "" {
		return fmt.Errorf("Agent %q 缺少系统提示词（system_prompt）", a.Name)
	}
	if !isFallback && len(a.Keywords) == 0 && len(a.Patterns) == 0 {
		return fmt.Errorf("Agent %q 未配置任何触发规则（keywords 或 patterns），"+
			"将只能靠 LLM 分类命中；若为有意设计请至少配置一个 keyword", a.Name)
	}
	return nil
}

// compilePatterns 预编译 Patterns 中的正则，失败返回错误（配置错误应尽早暴露）。
func (a *AgentSpec) compilePatterns() error {
	if len(a.compiled) > 0 {
		return nil // 已编译
	}
	a.compiled = make([]*regexp.Regexp, 0, len(a.Patterns))
	for _, p := range a.Patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return fmt.Errorf("Agent %q 的正则 %q 编译失败: %w", a.Name, p, err)
		}
		a.compiled = append(a.compiled, re)
	}
	return nil
}

// ValidateAgents 校验一组 Agent 定义：名称与键一致、各自合法、兜底 Agent 存在。
func ValidateAgents(agents map[string]*AgentSpec, fallback string) error {
	if len(agents) == 0 {
		return fmt.Errorf("未定义任何路由 Agent")
	}
	if _, ok := agents[fallback]; !ok {
		return fmt.Errorf("兜底 Agent %q 未在 agents 中定义", fallback)
	}
	for name, spec := range agents {
		if spec.Name != name {
			return fmt.Errorf("Agent 键名 %q 与 Name %q 不一致", name, spec.Name)
		}
		if err := spec.Validate(name == fallback); err != nil {
			return err
		}
		if err := spec.compilePatterns(); err != nil {
			return err
		}
	}
	return nil
}
