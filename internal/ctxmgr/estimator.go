// Package ctxmgr 提供上下文窗口管理，解决 Agent 执行过程中的上下文爆炸问题。
// 核心策略：Token 预算分配 + 渐进式压缩 + 工具输出截断 + LLM 摘要。
package ctxmgr

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// TokenEstimator 估算文本的 token 数量。
// 采用混合估算策略，基于主流 BPE tokenizer（GPT-4/Claude/Qwen/DeepSeek）的实测统计。
type TokenEstimator struct {
	// CJKRatio 中日韩字符的 token 比率（每字符消耗的 token 数）
	// 实测：GPT-4 约 2.0-2.5，Qwen/DeepSeek 约 1.5-2.0，取均值 2.0
	CJKRatio float64
	// LatinRatio 拉丁字符的 token 比率（英文单词平均约 1.3 token/word，约 4-5 字符/token）
	LatinRatio float64
	// OverheadPerMessage 每条消息的固定开销（role 标记、分隔符等）
	OverheadPerMessage int
}

// DefaultEstimator 返回默认的 token 估算器
func DefaultEstimator() *TokenEstimator {
	return &TokenEstimator{
		CJKRatio:           2.0,
		LatinRatio:         0.25,
		OverheadPerMessage: 4,
	}
}

// EstimateText 估算单段文本的 token 数
func (e *TokenEstimator) EstimateText(text string) int {
	if text == "" {
		return 0
	}

	var cjkCount, latinCount, digitCount, spaceCount, otherCount int

	for _, r := range text {
		switch {
		case isCJK(r) || isCJKPunct(r):
			cjkCount++
		case r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z':
			latinCount++
		case r >= '0' && r <= '9':
			digitCount++
		case r == ' ' || r == '\t':
			spaceCount++
		case r == '\n':
			otherCount++ // 换行符约占 1 token
		default:
			otherCount++ // 标点、符号等
		}
	}

	// 拉丁字符：字母和数字合并计算（BPE 通常将字母+数字组合为 token）
	latinTotal := latinCount + digitCount
	tokens := float64(cjkCount)*e.CJKRatio +
		float64(latinTotal)*e.LatinRatio +
		float64(spaceCount)*0.1 + // 空格单独出现时几乎不占 token
		float64(otherCount)*1.0

	// 至少 1 token
	result := int(tokens) + 1
	if result < 1 {
		result = 1
	}
	return result
}

// EstimateMessages 估算消息列表的总 token 数
// assistant 消息的 ToolCalls（序列化的工具调用 JSON）同样计入估算
func (e *TokenEstimator) EstimateMessages(messages []Message) int {
	total := 0
	for _, msg := range messages {
		total += e.EstimateText(msg.Content) + e.EstimateText(msg.ToolCalls) + e.OverheadPerMessage
	}
	return total
}

// isCJK 判断是否为中日韩字符
func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r)
}

// isCJKPunct 判断是否为中文标点符号（这些符号通常单独占 1 个 token）
func isCJKPunct(r rune) bool {
	switch r {
	case '，', '。', '！', '？', '；', '：',
		'「', '」', '『', '』', '（', '）',
		'【', '】', '《', '》',
		'、', '·', '～', '〝', '〞':
		return true
	}
	return false
}

// Message 简化的消息结构（避免循环依赖 llm 包）
type Message struct {
	Role      string
	Content   string
	ToolCalls string // 序列化的工具调用 JSON（assistant 消息）
}

// CharCount 返回文本的字符数
func CharCount(s string) int {
	return utf8.RuneCountInString(s)
}

// TruncateText 按 token 预算截断文本，保留头尾并在中间插入省略标记
func TruncateText(text string, maxTokens int, estimator *TokenEstimator) string {
	if estimator.EstimateText(text) <= maxTokens {
		return text
	}

	// 计算大致的字符预算（取保守估计）
	runes := []rune(text)
	totalRunes := len(runes)

	// 保留头部 60% + 尾部 20%，中间用摘要标记
	headRatio := 0.6
	tailRatio := 0.2
	budgetRatio := float64(maxTokens) / float64(estimator.EstimateText(text)+1)

	headChars := int(float64(totalRunes) * headRatio * budgetRatio)
	tailChars := int(float64(totalRunes) * tailRatio * budgetRatio)

	if headChars < 100 {
		headChars = 100
	}
	if tailChars < 50 {
		tailChars = 50
	}
	if headChars+tailChars >= totalRunes {
		return text
	}

	head := string(runes[:headChars])
	tail := string(runes[totalRunes-tailChars:])
	omitted := totalRunes - headChars - tailChars

	return head + "\n\n... [内容已压缩，省略 " + itoa(omitted) + " 字符] ...\n\n" + tail
}

// TruncateHead 只保留文本尾部（适用于日志类输出，最新内容在末尾）
func TruncateHead(text string, maxTokens int, estimator *TokenEstimator) string {
	if estimator.EstimateText(text) <= maxTokens {
		return text
	}

	runes := []rune(text)
	totalRunes := len(runes)
	budgetRatio := float64(maxTokens) / float64(estimator.EstimateText(text)+1)
	keepChars := int(float64(totalRunes) * budgetRatio * 0.9)

	if keepChars < 100 {
		keepChars = 100
	}
	if keepChars >= totalRunes {
		return text
	}

	omitted := totalRunes - keepChars
	return "... [省略前 " + itoa(omitted) + " 字符] ...\n" + string(runes[totalRunes-keepChars:])
}

// itoa 简单整数转字符串
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// SummarizeKeyPoints 从文本中提取关键行（非空、非注释、包含关键信息的行）
func SummarizeKeyPoints(text string, maxLines int) string {
	lines := strings.Split(text, "\n")
	if len(lines) <= maxLines {
		return text
	}

	// 优先保留：首行、包含关键词的行、末行
	var kept []string
	keywords := []string{"error", "result", "success", "fail", "total", "summary",
		"错误", "结果", "成功", "失败", "总计", "摘要", "结论"}

	// 首行
	kept = append(kept, lines[0])

	// 中间包含关键词的行
	for i := 1; i < len(lines)-1 && len(kept) < maxLines-1; i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		lineLower := strings.ToLower(line)
		for _, kw := range keywords {
			if strings.Contains(lineLower, kw) {
				kept = append(kept, lines[i])
				break
			}
		}
	}

	// 末行
	if len(lines) > 1 {
		kept = append(kept, lines[len(lines)-1])
	}

	// 如果关键词匹配不够，补充前几行
	if len(kept) < maxLines {
		for i := 1; i < len(lines)-1 && len(kept) < maxLines; i++ {
			line := strings.TrimSpace(lines[i])
			if line != "" && !contains(kept, lines[i]) {
				kept = append(kept, lines[i])
			}
		}
	}

	return strings.Join(kept, "\n")
}

func contains(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}
