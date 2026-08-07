// conflict.go 实现记忆冲突处理：时间戳优先、置信度衰减、冲突检测。
// 注意（P2）：本模块服务于长期记忆（情景/语义/向量库）；当前 CombinedMemory.longTerm 恒为 nil，
// 这些函数尚未接入生产路径，待长期记忆实现时在记忆写入/检索链路中接线。
package memory

import (
	"math"
	"strings"
	"time"
)

// ConfidenceHalfLife 置信度半衰期（默认 30 天）。
const ConfidenceHalfLife = 30 * 24 * time.Hour

// DecayConfidence 按时间指数衰减置信度。
// confidence(t) = initial * 0.5^(elapsed / halfLife)
func DecayConfidence(initial float64, createdAt time.Time, now time.Time) float64 {
	if initial <= 0 {
		return 0
	}
	elapsed := now.Sub(createdAt)
	if elapsed <= 0 {
		return initial
	}
	halfLives := float64(elapsed) / float64(ConfidenceHalfLife)
	return initial * math.Pow(0.5, halfLives)
}

// ResolveConflict 在一组语义相似的记忆中解决冲突。
// 规则：优先返回最新（UpdatedAt 最晚）的记忆；旧记忆标记为过期。
// 返回排序后的结果（最新在前）和应被标记为过期的条目 ID。
func ResolveConflict(entries []MemoryEntry) (sorted []MemoryEntry, superseded []string) {
	if len(entries) <= 1 {
		return entries, nil
	}

	// 按 Timestamp 降序排序（最新在前）
	sorted = make([]MemoryEntry, len(entries))
	copy(sorted, entries)
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].Timestamp.After(sorted[i].Timestamp) {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	// 最新条目保留，其余标记为过期
	for _, e := range sorted[1:] {
		superseded = append(superseded, e.ID)
	}
	return sorted, superseded
}

// IsSemanticConflict 粗略判断两条记忆是否语义矛盾。
// 简化实现：同一主题关键词但包含否定词（不/没/非/un/dis）差异。
func IsSemanticConflict(a, b MemoryEntry) bool {
	// 计算关键词重叠度
	aWords := extractKeywords(a.Content)
	bWords := extractKeywords(b.Content)
	if len(aWords) == 0 || len(bWords) == 0 {
		return false
	}

	overlap := 0
	for w := range aWords {
		if bWords[w] {
			overlap++
		}
	}
	overlapRatio := float64(overlap) / math.Min(float64(len(aWords)), float64(len(bWords)))

	// 高重叠（> 0.7）但一条含否定、另一条不含 → 矛盾
	if overlapRatio > 0.7 {
		aNeg := hasNegation(a.Content)
		bNeg := hasNegation(b.Content)
		if aNeg != bNeg {
			return true
		}
	}
	return false
}

// extractKeywords 提取内容中的关键词（去停用词，取长度 > 2 的词）。
func extractKeywords(content string) map[string]bool {
	words := strings.Fields(strings.ToLower(content))
	keywords := make(map[string]bool)
	for _, w := range words {
		w = strings.Trim(w, ".,!?;:，。！？；：")
		if len([]rune(w)) > 2 {
			keywords[w] = true
		}
	}
	return keywords
}

// negationWords 否定词列表。
var negationWords = []string{"不", "没", "非", "无", "别", "un", "dis", "not", "no", "never"}

// hasNegation 检测文本是否包含否定词。
func hasNegation(text string) bool {
	lower := strings.ToLower(text)
	for _, neg := range negationWords {
		if strings.Contains(lower, neg) {
			return true
		}
	}
	return false
}
