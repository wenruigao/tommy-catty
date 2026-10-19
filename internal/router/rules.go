package router

import (
	"fmt"
	"sort"
	"strings"
)

// 规则匹配置信度：正则比关键词更精确，故给更高分。
const (
	confidencePattern  = 0.90 // 命中正则
	confidencePerHit   = 0.15 // 每命中一个关键词的增量
	confidenceBaseHit  = 0.60 // 命中至少一个关键词的基线
	confidenceMaxRules = 0.95 // 规则匹配上限
)

// ruleMatch 单个 Agent 的规则匹配结果。
type ruleMatch struct {
	agent      *AgentSpec
	hits       int      // 命中的关键词数
	patternHit bool     // 是否命中正则
	matched    []string // 命中的具体词/正则（进 Reason 供排查）
	confidence float64
}

// matchRules 对输入做规则匹配，返回最优命中（无命中返回 nil）。
//
// agents 须已按 priority 降序排列（由 NewRouter 保证）。
// 择优规则：置信度高者胜；置信度相同时 priority 高者胜（即切片中靠前者）。
func matchRules(agents []*AgentSpec, input string) *ruleMatch {
	if strings.TrimSpace(input) == "" {
		return nil
	}
	lowered := strings.ToLower(input)

	var best *ruleMatch
	for _, a := range agents {
		m := matchOne(a, lowered)
		if m == nil {
			continue
		}
		// 严格大于才替换：保证同分时 priority 高者（靠前者）胜出
		if best == nil || m.confidence > best.confidence {
			best = m
		}
	}
	return best
}

// matchOne 计算单个 Agent 对输入的匹配情况，无命中返回 nil。
// lowered 为已转小写的输入。
func matchOne(a *AgentSpec, lowered string) *ruleMatch {
	m := &ruleMatch{agent: a}

	for _, kw := range a.Keywords {
		if kw == "" {
			continue
		}
		if strings.Contains(lowered, strings.ToLower(kw)) {
			m.hits++
			m.matched = append(m.matched, kw)
		}
	}

	for _, re := range a.compiled {
		if re.MatchString(lowered) {
			m.patternHit = true
			m.matched = append(m.matched, re.String())
		}
	}

	if m.hits == 0 && !m.patternHit {
		return nil
	}

	switch {
	case m.patternHit:
		m.confidence = confidencePattern
		// 同时命中关键词时略微加分（多重证据）
		if m.hits > 0 {
			m.confidence = minFloat(confidencePattern+confidencePerHit, confidenceMaxRules)
		}
	default:
		m.confidence = minFloat(confidenceBaseHit+confidencePerHit*float64(m.hits), confidenceMaxRules)
	}
	return m
}

// reason 生成人类可读的决策依据（进日志与 trace span）。
func (m *ruleMatch) reason() string {
	if m == nil {
		return ""
	}
	shown := m.matched
	if len(shown) > 5 { // 避免 Reason 过长
		shown = shown[:5]
	}
	return fmt.Sprintf("命中 %d 项规则: %s", len(m.matched), strings.Join(shown, ", "))
}

// sortByPriority 按 priority 降序稳定排序（同 priority 保持插入顺序）。
func sortByPriority(agents []*AgentSpec) {
	sort.SliceStable(agents, func(i, j int) bool {
		return agents[i].Priority > agents[j].Priority
	})
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
