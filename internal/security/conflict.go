// conflict.go 实现安全策略冲突检测与优先级仲裁。
// 仲裁语义：Deny-Override（deny 优先于 allow）。
package security

// ResolveConflict 对多条匹配决策执行冲突仲裁，返回最终生效的决策。
// 规则：
//  1. 任何 deny 决策存在 → 最终为 deny（Deny-Override）
//  2. 同级别冲突时，priority 数字小的策略优先
//  3. require_approval 优先于 allow
func ResolveConflict(decisions []Decision, policies []Policy) Decision {
	if len(decisions) == 0 {
		return Decision{Effect: EffectAllow, Message: "no matching policy"}
	}
	if len(decisions) == 1 {
		return decisions[0]
	}

	// 构建 policyID -> priority 映射
	priorityMap := make(map[string]int)
	for _, p := range policies {
		priorityMap[p.ID] = p.Priority
	}

	// Deny-Override：检查是否存在 deny
	for _, d := range decisions {
		if d.Effect == EffectDeny {
			return d
		}
	}

	// 检查 require_approval
	for _, d := range decisions {
		if d.Effect == EffectRequireApproval {
			return d
		}
	}

	// 均为 allow/throttle 时，按优先级数字排序（小的优先）
	best := decisions[0]
	bestPriority := getPriority(best.PolicyID, priorityMap)
	for _, d := range decisions[1:] {
		p := getPriority(d.PolicyID, priorityMap)
		if p < bestPriority {
			best = d
			bestPriority = p
		}
	}
	return best
}

// DetectConflicts 静态冲突检测：分析所有策略对，找出可能对同一输入产生不同决策的组合。
// 返回冲突对列表（用于启动时 Warning 日志）。
func DetectConflicts(policies []Policy) []ConflictPair {
	var conflicts []ConflictPair
	for i := 0; i < len(policies); i++ {
		for j := i + 1; j < len(policies); j++ {
			a, b := policies[i], policies[j]
			if !a.Enabled || !b.Enabled {
				continue
			}
			// 如果两条策略可能匹配同一输入且决策不同，标记为潜在冲突
			if a.Then.Effect != b.Then.Effect && conditionsMayOverlap(a.When, b.When) {
				conflicts = append(conflicts, ConflictPair{
					PolicyA: a.ID,
					PolicyB: b.ID,
					EffectA: a.Then.Effect,
					EffectB: b.Then.Effect,
				})
			}
		}
	}
	return conflicts
}

// ConflictPair 描述一对潜在冲突的策略。
type ConflictPair struct {
	PolicyA string
	PolicyB string
	EffectA Effect
	EffectB Effect
}

// conditionsMayOverlap 粗略判断两个条件是否可能同时匹配。
// 简化实现：如果 ActionType 有交集或任一为空（通配），则认为可能重叠。
func conditionsMayOverlap(a, b PolicyCondition) bool {
	// 任一为空条件（匹配所有）→ 可能重叠
	if len(a.ActionType) == 0 || len(b.ActionType) == 0 {
		return true
	}
	// 检查 ActionType 交集
	for _, at := range a.ActionType {
		for _, bt := range b.ActionType {
			if at == bt {
				return true
			}
		}
	}
	// ToolNames 交集
	if len(a.ToolNames) > 0 && len(b.ToolNames) > 0 {
		for _, at := range a.ToolNames {
			for _, bt := range b.ToolNames {
				if at == bt {
					return true
				}
			}
		}
		return false
	}
	return len(a.ToolNames) == 0 || len(b.ToolNames) == 0
}

func getPriority(policyID string, m map[string]int) int {
	if p, ok := m[policyID]; ok {
		return p
	}
	return 100 // 默认优先级
}
