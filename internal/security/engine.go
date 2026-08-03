// Package security 提供安全策略引擎的核心评估逻辑。
package security

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Engine 是安全策略评估引擎，负责管理和评估所有安全策略
type Engine struct {
	// policies 存储所有已加载的策略
	policies []Policy
	// mu 保护 policies 的读写锁
	mu sync.RWMutex
}

// NewEngine 创建一个新的策略引擎实例
func NewEngine() *Engine {
	return &Engine{
		policies: make([]Policy, 0),
	}
}

// AddPolicy 向引擎中添加一条策略（自动预编译正则）
func (e *Engine) AddPolicy(p Policy) {
	p.When.compilePattern()
	e.mu.Lock()
	defer e.mu.Unlock()
	e.policies = append(e.policies, p)
}

// RemovePolicy 根据策略ID移除一条策略
func (e *Engine) RemovePolicy(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	filtered := make([]Policy, 0, len(e.policies))
	for _, p := range e.policies {
		if p.ID != id {
			filtered = append(filtered, p)
		}
	}
	e.policies = filtered
}

// yamlPolicyFile 用于解析 YAML 策略文件的内部结构
type yamlPolicyFile struct {
	Policies []Policy `yaml:"policies"`
}

// LoadFromYAML 从 YAML 数据中加载策略列表（自动预编译正则）
func (e *Engine) LoadFromYAML(data []byte) error {
	var file yamlPolicyFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("解析YAML策略文件失败: %w", err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	for _, p := range file.Policies {
		p.When.compilePattern()
		e.policies = append(e.policies, p)
	}
	return nil
}

// Evaluate 评估检查点，返回匹配的策略决策（按优先级排序，deny 短路）
func (e *Engine) Evaluate(cp Checkpoint) []Decision {
	e.mu.RLock()
	defer e.mu.RUnlock()

	// 收集所有匹配的策略
	var matched []Policy
	for _, p := range e.policies {
		if !p.Enabled {
			continue
		}
		if matchesCondition(p.When, cp) {
			matched = append(matched, p)
		}
	}

	if len(matched) == 0 {
		return nil
	}

	// 按优先级排序（数值越小优先级越高）
	sort.Slice(matched, func(i, j int) bool {
		return matched[i].Priority < matched[j].Priority
	})

	// 生成决策列表，deny 效果短路（不再评估后续策略）
	decisions := make([]Decision, 0, len(matched))
	for _, p := range matched {
		decisions = append(decisions, Decision{
			Effect:   p.Then.Effect,
			PolicyID: p.ID,
			Message:  p.Then.Message,
		})
		if p.Then.Effect == EffectDeny {
			break // deny 是最终决策，不再继续
		}
	}

	return decisions
}

// matchesCondition 检查检查点是否匹配策略条件
func matchesCondition(cond PolicyCondition, cp Checkpoint) bool {
	// 检查工具名称匹配
	if len(cond.ToolNames) > 0 {
		if !containsStr(cond.ToolNames, cp.ToolName) {
			return false
		}
	}

	// 检查工具风险等级匹配
	if len(cond.ToolRisk) > 0 {
		riskStr := fmt.Sprintf("L%d", cp.ToolRisk)
		if !containsStr(cond.ToolRisk, riskStr) {
			return false
		}
	}

	// 检查操作类型匹配
	if len(cond.ActionType) > 0 {
		if !containsStr(cond.ActionType, cp.Type) {
			return false
		}
	}

	// 检查正则表达式匹配（使用预编译正则）
	if cond.Pattern != "" {
		if cond.compiledPattern == nil {
			// 正则无效，视为不匹配
			return false
		}
		if !cond.compiledPattern.MatchString(cp.Content) {
			return false
		}
	}

	// 检查敏感关键词匹配
	if len(cond.Sensitive) > 0 {
		found := false
		for _, keyword := range cond.Sensitive {
			if strings.Contains(strings.ToLower(cp.Content), strings.ToLower(keyword)) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	// 检查时间范围
	if cond.TimeRange != "" {
		if !inTimeRange(cond.TimeRange, cp.Timestamp) {
			return false
		}
	}

	// 检查成本阈值
	if cond.MaxCost > 0 {
		if cp.Cost <= cond.MaxCost {
			return false
		}
	}

	return true
}

// inTimeRange 检查给定时间是否在指定时间范围内
// timeRange 格式为 "HH:MM-HH:MM"，例如 "09:00-18:00"
func inTimeRange(timeRange string, t time.Time) bool {
	parts := strings.Split(timeRange, "-")
	if len(parts) != 2 {
		return false
	}

	startStr := strings.TrimSpace(parts[0])
	endStr := strings.TrimSpace(parts[1])

	startH, startM, err1 := parseTime(startStr)
	endH, endM, err2 := parseTime(endStr)
	if err1 != nil || err2 != nil {
		return false
	}

	currentMinutes := t.Hour()*60 + t.Minute()
	startMinutes := startH*60 + startM
	endMinutes := endH*60 + endM

	// 处理跨午夜的情况（如 "22:00-06:00"）
	if startMinutes <= endMinutes {
		return currentMinutes >= startMinutes && currentMinutes <= endMinutes
	}
	// 跨午夜：在开始时间之后或在结束时间之前
	return currentMinutes >= startMinutes || currentMinutes <= endMinutes
}

// parseTime 解析 "HH:MM" 格式的时间字符串
func parseTime(s string) (int, int, error) {
	var h, m int
	_, err := fmt.Sscanf(s, "%d:%d", &h, &m)
	if err != nil {
		return 0, 0, err
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, fmt.Errorf("无效的时间: %s", s)
	}
	return h, m, nil
}

// containsStr 检查字符串切片中是否包含指定字符串
func containsStr(slice []string, s string) bool {
	for _, item := range slice {
		if strings.EqualFold(item, s) {
			return true
		}
	}
	return false
}
