// tokenizer.go 提供轻量级文本分词，供倒排索引与 BM25 使用。
// 纯 Go 实现：英文按非字母数字切分并小写化；中文按单字（unigram）切分。
package kb

import (
	"strings"
	"unicode"
)

// 停用词表（英文常见词），减少噪声匹配。
var stopWords = map[string]bool{
	"the": true, "a": true, "an": true, "and": true, "or": true,
	"of": true, "to": true, "in": true, "on": true, "for": true,
	"is": true, "are": true, "was": true, "were": true, "be": true,
	"with": true, "as": true, "by": true, "at": true, "this": true,
	"that": true, "it": true, "from": true, "but": true, "not": true,
	"的": true, "了": true, "是": true, "在": true, "和": true,
	"与": true, "或": true, "及": true, "等": true, "一个": true,
}

// tokenize 将文本切分为词项（term）序列。
// 英文/数字：连续字母数字作为一个词项并转小写；
// CJK 字符：每个字符单独作为一个词项（unigram）。
func tokenize(text string) []string {
	text = strings.ToLower(text)
	var tokens []string
	var buf strings.Builder

	flush := func() {
		if buf.Len() > 0 {
			w := buf.String()
			if !stopWords[w] && len(w) > 1 {
				tokens = append(tokens, w)
			}
			buf.Reset()
		}
	}

	for _, r := range text {
		switch {
		case isCJK(r):
			flush()
			// 单字 unigram（过滤纯标点类 CJK）
			if unicode.IsLetter(r) {
				tokens = append(tokens, string(r))
			}
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			buf.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	return tokens
}

// isCJK 判断是否为中日韩统一表意文字。
func isCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || // CJK 基本区
		(r >= 0x3400 && r <= 0x4DBF) || // CJK 扩展 A
		(r >= 0x3040 && r <= 0x30FF) || // 日文假名
		(r >= 0xAC00 && r <= 0xD7AF) // 韩文音节
}
