// sanitizer.go 实现工具返回数据清洗（Data Sanitization）。
// 所有工具返回在注入 LLM 上下文前经过清洗，防止嵌入式 Prompt Injection。
package tool

import (
	"regexp"
	"strings"
)

// TrustLevel 工具信任级别。
type TrustLevel int

const (
	// TrustInternal 内部工具（file_read, db_query），仅做长度截断。
	TrustInternal TrustLevel = 0
	// TrustExternal 外部工具（web_fetch, api_call），执行完整清洗。
	TrustExternal TrustLevel = 1
)

// injectionPatterns 常见注入模式特征（输入层 + 工具返回共用）。
var injectionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)ignore\s+(all\s+)?(previous|prior|above)\s+(instructions|prompts|rules)`),
	regexp.MustCompile(`(?i)disregard\s+(all\s+)?(previous|prior|above)`),
	regexp.MustCompile(`(?i)you\s+are\s+now\s+(a|an|the)`),
	regexp.MustCompile(`(?i)forget\s+(everything|all|your)\s+(above|previous|instructions)`),
	regexp.MustCompile(`(?i)system\s*prompt`),
	regexp.MustCompile(`(?i)repeat\s+(the\s+)?(above|previous|system)`),
	regexp.MustCompile(`(?i)忽略(之前|以上|上面)的(指令|规则|提示)`),
	regexp.MustCompile(`(?i)你现在是`),
	regexp.MustCompile(`(?i)无视(之前|以上)的(要求|设定)`),
}

// htmlScriptRe 匹配 HTML script 标签和事件处理器。
var htmlScriptRe = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>|on\w+\s*=\s*"[^"]*"|on\w+\s*=\s*'[^']*'`)

// SanitizeConfig 清洗配置。
type SanitizeConfig struct {
	// MaxOutputTokens 工具输出最大 token 数（超出截断）
	MaxOutputTokens int
	// Enabled 是否启用清洗
	Enabled bool
}

// DefaultSanitizeConfig 默认清洗配置。
func DefaultSanitizeConfig() SanitizeConfig {
	return SanitizeConfig{
		MaxOutputTokens: 4000,
		Enabled:         true,
	}
}

// Sanitize 对工具返回进行安全清洗。
func Sanitize(output string, trust TrustLevel, cfg SanitizeConfig) string {
	if !cfg.Enabled {
		return output
	}

	// 内部工具：仅截断
	if trust == TrustInternal {
		return truncateToTokens(output, cfg.MaxOutputTokens)
	}

	// 外部工具：完整清洗
	// 1. 剥离 HTML script/event handler
	result := htmlScriptRe.ReplaceAllString(output, "[filtered]")

	// 2. 检测并标记注入模式
	for _, pattern := range injectionPatterns {
		if pattern.MatchString(result) {
			result = pattern.ReplaceAllString(result, "[⚠️ filtered]")
		}
	}

	// 3. 截断
	result = truncateToTokens(result, cfg.MaxOutputTokens)

	return result
}

// DetectInjection 检测文本中是否包含注入模式（用于输入层检测）。
// 返回匹配到的模式数量。
func DetectInjection(text string) int {
	count := 0
	for _, pattern := range injectionPatterns {
		if pattern.MatchString(text) {
			count++
		}
	}
	return count
}

// WrapToolOutput 将工具输出包裹在隔离标签中。
// 注意：工具输出是不可信内容，若其中包含 "</tool_output" 会提前闭合隔离标签、
// 逃逸出包裹边界（注入风险），因此先中和该序列再包裹。
func WrapToolOutput(toolName string, output string) string {
	output = strings.ReplaceAll(output, "</tool_output", "< /tool_output")
	return "<tool_output source=\"" + toolName + "\">\n" + output + "\n</tool_output>"
}

// truncateToTokens 粗略截断到指定 token 数（1 token ≈ 4 字符英文 / 2 字符中文）。
func truncateToTokens(s string, maxTokens int) string {
	if maxTokens <= 0 {
		return s
	}
	// 粗略估算：取 maxTokens * 3 个 rune 作为上限
	maxRunes := maxTokens * 3
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "\n...[输出已截断]"
}

// SanitizeInput 对用户输入做注入检测与清洗（输入层防御）。
// 返回清洗后的文本和是否为可疑输入。
func SanitizeInput(input string) (cleaned string, suspicious bool) {
	count := DetectInjection(input)
	if count == 0 {
		return input, false
	}
	// 标记为可疑但不完全阻断（由安全策略引擎决定后续处理）
	return input, true
}

// DetectOutputLeak 检测 LLM 输出是否泄露 system prompt 关键片段。
// 使用 N-gram 匹配：若输出包含 systemPrompt 中连续 8 个词以上的片段，视为泄露。
func DetectOutputLeak(output string, systemPrompt string) bool {
	if systemPrompt == "" || output == "" {
		return false
	}
	promptWords := strings.Fields(systemPrompt)
	if len(promptWords) < 8 {
		return false
	}
	outputLower := strings.ToLower(output)
	// 滑动窗口检查 8-gram
	for i := 0; i <= len(promptWords)-8; i++ {
		ngram := strings.ToLower(strings.Join(promptWords[i:i+8], " "))
		if strings.Contains(outputLower, ngram) {
			return true
		}
	}
	return false
}
