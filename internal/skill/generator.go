// Package skill 提供技能自动生成功能，从执行追踪中提炼可复用的技能。
package skill

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Generator 负责从执行追踪中自动生成技能
type Generator struct {
	// store 技能存储实例
	store *Store
}

// NewGenerator 创建一个新的技能生成器
func NewGenerator(store *Store) *Generator {
	return &Generator{
		store: store,
	}
}

// Save 将技能持久化到生成器关联的存储（委托 Store.Save）。
// GenerateFromTrace 生成并通过校验的技能必须调用 Save，
// 否则技能不会落盘，匹配器永远无技能可用。
func (g *Generator) Save(s *Skill) error {
	return g.store.Save(s)
}

// traceData 表示执行追踪的 JSON 结构
type traceData struct {
	// TraceID 追踪唯一标识
	TraceID string `json:"trace_id"`
	// TaskSummary 任务摘要描述
	TaskSummary string `json:"task_summary"`
	// Steps 执行步骤列表
	Steps []traceStep `json:"steps"`
	// Tools 使用的工具列表
	Tools []string `json:"tools"`
	// Keywords 相关关键词
	Keywords []string `json:"keywords"`
}

// traceStep 表示追踪中的单个执行步骤
type traceStep struct {
	// Order 步骤顺序
	Order int `json:"order"`
	// Action 动作类型
	Action string `json:"action"`
	// ToolName 工具名称
	ToolName string `json:"tool_name"`
	// Input 输入参数
	Input string `json:"input"`
	// Output 输出结果
	Output string `json:"output"`
	// Success 是否成功
	Success bool `json:"success"`
}

// GenerateFromTrace 从 JSON 格式的执行追踪中自动生成技能
// 解析追踪数据，提取工作流模式，创建技能结构
func (g *Generator) GenerateFromTrace(traceJSON string) (*Skill, error) {
	// 解析追踪 JSON
	var trace traceData
	if err := json.Unmarshal([]byte(traceJSON), &trace); err != nil {
		return nil, fmt.Errorf("解析执行追踪失败: %w", err)
	}

	if len(trace.Steps) == 0 {
		return nil, fmt.Errorf("执行追踪中没有步骤数据")
	}

	// 生成技能名称和描述
	name := generateSkillName(trace.TaskSummary)
	description := trace.TaskSummary
	if description == "" {
		description = fmt.Sprintf("从追踪 %s 自动生成的技能", trace.TraceID)
	}

	// 提取执行步骤
	steps := make([]SkillStep, 0, len(trace.Steps))
	for _, ts := range trace.Steps {
		step := SkillStep{
			Order:    ts.Order,
			Action:   ts.Action,
			ToolName: ts.ToolName,
			Template: ts.Input,
			OnError:  "retry", // 默认错误处理策略为重试
		}
		steps = append(steps, step)
	}

	// 提取工具列表
	tools := trace.Tools
	if len(tools) == 0 {
		// 从步骤中提取工具名称
		toolSet := make(map[string]bool)
		for _, ts := range trace.Steps {
			if ts.ToolName != "" {
				toolSet[ts.ToolName] = true
			}
		}
		for t := range toolSet {
			tools = append(tools, t)
		}
	}

	// 提取触发关键词
	keywords := trace.Keywords
	if len(keywords) == 0 {
		keywords = extractKeywords(trace.TaskSummary)
	}

	now := time.Now()
	sk := &Skill{
		ID:          uuid.New().String(),
		Name:        name,
		Description: description,
		Version:     "1.0.0",
		Tags:        tools, // 使用工具名作为标签
		Trigger: TriggerRule{
			Keywords:      keywords,
			MinSimilarity: 0.6,
		},
		Steps:       steps,
		Tools:       tools,
		PromptHints: buildPromptHints(trace.TaskSummary, tools),
		CreatedAt:   now,
		UpdatedAt:   now,
		UsageCount:  0,
		SuccessRate: 1.0, // 新生成的技能默认成功率为1
		Source: SkillSource{
			Type:        "auto_extract",
			TraceID:     trace.TraceID,
			TaskSummary: trace.TaskSummary,
		},
	}

	// 验证生成的技能
	if err := g.ValidateSkill(sk); err != nil {
		return nil, fmt.Errorf("生成的技能验证失败: %w", err)
	}

	return sk, nil
}

// ValidateSkill 验证技能结构的完整性和合法性
func (g *Generator) ValidateSkill(s *Skill) error {
	if s == nil {
		return fmt.Errorf("技能不能为空")
	}
	if s.Name == "" {
		return fmt.Errorf("技能名称不能为空")
	}
	if len(s.Steps) == 0 {
		return fmt.Errorf("技能至少需要一个执行步骤")
	}

	// 验证步骤的合法性
	for i, step := range s.Steps {
		if step.Action == "" {
			return fmt.Errorf("步骤 %d 的动作类型不能为空", i+1)
		}
		// 如果是工具调用步骤，必须有工具名称
		if step.Action == "call_tool" && step.ToolName == "" {
			return fmt.Errorf("步骤 %d 为工具调用但缺少工具名称", i+1)
		}
	}

	// 验证步骤顺序的连续性
	for i := 1; i < len(s.Steps); i++ {
		if s.Steps[i].Order <= s.Steps[i-1].Order {
			return fmt.Errorf("步骤顺序不连续: 步骤 %d 的顺序号应大于步骤 %d", i+1, i)
		}
	}

	return nil
}

// generateSkillName 根据任务摘要生成技能名称
func generateSkillName(summary string) string {
	if summary == "" {
		return "unnamed-skill"
	}
	// 取摘要的前几个词作为名称
	words := strings.Fields(summary)
	if len(words) > 5 {
		words = words[:5]
	}
	name := strings.Join(words, "-")
	// 移除特殊字符
	name = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		return '-'
	}, name)
	return strings.ToLower(strings.Trim(name, "-"))
}

// extractKeywords 从文本中提取关键词
func extractKeywords(text string) []string {
	if text == "" {
		return nil
	}
	words := strings.Fields(strings.ToLower(text))
	// 过滤停用词和过短的词
	stopWords := map[string]bool{
		"的": true, "是": true, "在": true, "了": true, "和": true,
		"the": true, "is": true, "a": true, "an": true, "in": true,
		"to": true, "for": true, "of": true, "and": true, "or": true,
	}

	var keywords []string
	seen := make(map[string]bool)
	for _, w := range words {
		w = strings.Trim(w, ".,!?;:()[]{}\"'")
		if len(w) >= 2 && !stopWords[w] && !seen[w] {
			keywords = append(keywords, w)
			seen[w] = true
		}
	}

	// 最多取10个关键词
	if len(keywords) > 10 {
		keywords = keywords[:10]
	}
	return keywords
}

// buildPromptHints 构建提示词片段
func buildPromptHints(summary string, tools []string) string {
	var sb strings.Builder
	sb.WriteString("执行以下任务: ")
	sb.WriteString(summary)
	sb.WriteString("\n可用工具: ")
	sb.WriteString(strings.Join(tools, ", "))
	sb.WriteString("\n请按照步骤顺序执行，遇到错误时进行重试。")
	return sb.String()
}
