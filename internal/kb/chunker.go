// Package kb 实现本地知识库的索引与检索。
// chunker.go 负责将文档切分为适合检索的片段（chunk）。
package kb

import (
	"regexp"
	"strings"
)

// ChunkStrategy 分块策略。
type ChunkStrategy string

const (
	// StrategyAuto 根据文件类型自动选择（代码/Markdown 用标题，其余用段落）。
	StrategyAuto ChunkStrategy = "auto"
	// StrategyHeading 按标题/函数声明切分。
	StrategyHeading ChunkStrategy = "heading"
	// StrategyParagraph 按段落（空行）切分。
	StrategyParagraph ChunkStrategy = "paragraph"
)

// Chunk 表示一个文档片段。
type Chunk struct {
	DocID     int    // 所属文档 ID
	Seq       int    // 在文档中的序号
	Content   string // 片段内容
	StartLine int    // 原文起始行（1-based）
	EndLine   int    // 原文结束行（1-based）
	Heading   string // 所属标题/上下文
}

// ChunkConfig 分块配置。
type ChunkConfig struct {
	Strategy  ChunkStrategy
	MaxTokens int // 每块最大 token 数（粗略按字符数/2 估算）
	Overlap   int // 滑动窗口重叠 token 数
}

// DefaultChunkConfig 返回默认分块配置。
func DefaultChunkConfig() ChunkConfig {
	return ChunkConfig{
		Strategy:  StrategyAuto,
		MaxTokens: 500,
		Overlap:   100,
	}
}

// markdownHeadingRe 匹配 Markdown 标题行。
var markdownHeadingRe = regexp.MustCompile(`^(#{1,6})\s+(.+)$`)

// codeHeadingRe 匹配 Go 函数/类型声明行。
var codeHeadingRe = regexp.MustCompile(`^(func|type|const|var)\s+`)

// ChunkDocument 将文档内容切分为片段。
// path 用于判断文件类型（auto 策略）。
func ChunkDocument(content, path string, cfg ChunkConfig) []Chunk {
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 500
	}

	strategy := cfg.Strategy
	if strategy == StrategyAuto || strategy == "" {
		strategy = autoStrategy(path)
	}

	lines := strings.Split(content, "\n")

	var chunks []Chunk
	switch strategy {
	case StrategyHeading:
		chunks = chunkByHeading(lines, cfg)
	default: // paragraph
		chunks = chunkByParagraph(lines, cfg)
	}

	// 对过长的片段再做滑动窗口二次切分
	var result []Chunk
	seq := 0
	for _, c := range chunks {
		if estimateTokens(c.Content) > cfg.MaxTokens {
			for _, sub := range slidingWindow(c.Content, cfg) {
				sub.DocID = c.DocID
				sub.StartLine = c.StartLine
				sub.EndLine = c.EndLine
				sub.Heading = c.Heading
				sub.Seq = seq
				seq++
				result = append(result, sub)
			}
		} else {
			c.Seq = seq
			seq++
			result = append(result, c)
		}
	}
	return result
}

// autoStrategy 根据文件扩展名选择分块策略。
func autoStrategy(path string) ChunkStrategy {
	lower := strings.ToLower(path)
	if strings.HasSuffix(lower, ".md") || strings.HasSuffix(lower, ".markdown") {
		return StrategyHeading
	}
	if strings.HasSuffix(lower, ".go") || strings.HasSuffix(lower, ".py") ||
		strings.HasSuffix(lower, ".js") || strings.HasSuffix(lower, ".ts") ||
		strings.HasSuffix(lower, ".java") || strings.HasSuffix(lower, ".rs") {
		return StrategyHeading
	}
	return StrategyParagraph
}

// chunkByHeading 按标题/声明行切分。
func chunkByHeading(lines []string, cfg ChunkConfig) []Chunk {
	var chunks []Chunk
	var current []string
	currentHeading := ""
	startLine := 1

	flush := func(endLine int) {
		if len(current) == 0 {
			return
		}
		text := strings.TrimSpace(strings.Join(current, "\n"))
		if text != "" {
			chunks = append(chunks, Chunk{
				Content:   text,
				StartLine: startLine,
				EndLine:   endLine,
				Heading:   currentHeading,
			})
		}
		current = nil
	}

	for i, line := range lines {
		lineNo := i + 1
		isHeading := false
		headingText := ""

		if m := markdownHeadingRe.FindStringSubmatch(line); m != nil {
			isHeading = true
			headingText = strings.TrimSpace(m[2])
		} else if codeHeadingRe.MatchString(line) {
			isHeading = true
			headingText = strings.TrimSpace(line)
		}

		if isHeading && len(current) > 0 {
			flush(lineNo - 1)
			startLine = lineNo
			currentHeading = headingText
		} else if isHeading {
			startLine = lineNo
			currentHeading = headingText
		}
		current = append(current, line)
	}
	flush(len(lines))
	return chunks
}

// chunkByParagraph 按空行分段，并在超过 MaxTokens 时聚合/切分。
func chunkByParagraph(lines []string, cfg ChunkConfig) []Chunk {
	var chunks []Chunk
	var para []string
	startLine := 1

	flushPara := func(endLine int) {
		if len(para) == 0 {
			return
		}
		text := strings.TrimSpace(strings.Join(para, "\n"))
		if text != "" {
			chunks = append(chunks, Chunk{
				Content:   text,
				StartLine: startLine,
				EndLine:   endLine,
			})
		}
		para = nil
	}

	for i, line := range lines {
		lineNo := i + 1
		if strings.TrimSpace(line) == "" {
			flushPara(lineNo - 1)
			startLine = lineNo + 1
			continue
		}
		if len(para) == 0 {
			startLine = lineNo
		}
		para = append(para, line)
	}
	flushPara(len(lines))
	return chunks
}

// slidingWindow 对超长文本做滑动窗口切分。
func slidingWindow(content string, cfg ChunkConfig) []Chunk {
	words := strings.Fields(content)
	if len(words) == 0 {
		return nil
	}

	// 粗略：1 token ≈ 1 个空白分隔单元（中文按字符更准，此处简化）
	windowSize := cfg.MaxTokens
	if windowSize <= 0 {
		windowSize = 500
	}
	step := windowSize - cfg.Overlap
	if step <= 0 {
		step = windowSize
	}

	var chunks []Chunk
	for start := 0; start < len(words); start += step {
		end := start + windowSize
		if end > len(words) {
			end = len(words)
		}
		chunks = append(chunks, Chunk{
			Content: strings.Join(words[start:end], " "),
		})
		if end == len(words) {
			break
		}
	}
	return chunks
}

// estimateTokens 粗略估算 token 数。
// 中文按字符数，英文按词数；此处用字符数/2 + 词数的折中。
func estimateTokens(s string) int {
	runes := len([]rune(s))
	words := len(strings.Fields(s))
	// 取较大者，避免低估中文
	if runes > words*2 {
		return runes / 2
	}
	return words
}
