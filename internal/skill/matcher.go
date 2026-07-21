// Package skill 提供技能匹配功能，根据用户查询找到最相关的技能。
package skill

import (
	"strings"
)

// Matcher 负责将用户查询与已存储的技能进行匹配
type Matcher struct {
	// store 技能存储实例
	store *Store
}

// NewMatcher 创建一个新的技能匹配器
func NewMatcher(store *Store) *Matcher {
	return &Matcher{
		store: store,
	}
}

// Match 将用户查询与所有技能进行匹配，返回最佳匹配的技能及其得分
// 如果没有匹配的技能或得分为0，返回 nil 和 0
func (m *Matcher) Match(query string) (*Skill, float64) {
	skills := m.store.List()
	if len(skills) == 0 || query == "" {
		return nil, 0
	}

	var bestSkill *Skill
	var bestScore float64

	for _, sk := range skills {
		score := m.scoreSkill(sk, query)
		if score > bestScore {
			bestScore = score
			bestSkill = sk
		}
	}

	// 得分过低时不返回匹配结果
	if bestScore < 0.1 {
		return nil, 0
	}

	return bestSkill, bestScore
}

// scoreSkill 计算查询与技能的匹配得分
// 评分公式：关键词命中 * 0.4 + 标签重叠 * 0.3 + 描述重叠 * 0.3
func (m *Matcher) scoreSkill(s *Skill, query string) float64 {
	queryLower := strings.ToLower(query)
	queryWords := tokenize(queryLower)

	// 1. 关键词匹配得分（权重 0.4）
	keywordScore := 0.0
	if len(s.Trigger.Keywords) > 0 {
		hits := 0
		for _, kw := range s.Trigger.Keywords {
			if strings.Contains(queryLower, strings.ToLower(kw)) {
				hits++
			}
		}
		keywordScore = float64(hits) / float64(len(s.Trigger.Keywords))
	}

	// 2. 标签重叠得分（权重 0.3）
	tagScore := 0.0
	if len(s.Tags) > 0 {
		hits := 0
		for _, tag := range s.Tags {
			tagLower := strings.ToLower(tag)
			for _, word := range queryWords {
				if strings.Contains(tagLower, word) || strings.Contains(word, tagLower) {
					hits++
					break
				}
			}
		}
		tagScore = float64(hits) / float64(len(s.Tags))
	}

	// 3. 描述重叠得分（权重 0.3）
	descScore := 0.0
	if s.Description != "" {
		descWords := tokenize(strings.ToLower(s.Description))
		if len(descWords) > 0 {
			hits := 0
			for _, dw := range descWords {
				for _, qw := range queryWords {
					if dw == qw {
						hits++
						break
					}
				}
			}
			// 使用查询词在描述中的命中率
			if len(queryWords) > 0 {
				descScore = float64(hits) / float64(len(queryWords))
			}
		}
	}

	// 综合得分
	totalScore := keywordScore*0.4 + tagScore*0.3 + descScore*0.3
	return totalScore
}

// tokenize 将文本按空格和标点符号分词
func tokenize(text string) []string {
	// 替换常见标点为空格
	replacer := strings.NewReplacer(
		",", " ", ".", " ", "!", " ", "?", " ",
		";", " ", ":", " ", "(", " ", ")", " ",
		"[", " ", "]", " ", "{", " ", "}", " ",
		"/", " ", "\\", " ", "-", " ", "_", " ",
	)
	cleaned := replacer.Replace(text)
	words := strings.Fields(cleaned)

	// 过滤过短的词
	result := make([]string, 0, len(words))
	for _, w := range words {
		if len(w) >= 2 {
			result = append(result, w)
		}
	}
	return result
}
