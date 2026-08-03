package ctxmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// ============================================================
// 上下文管理器 — 解决 Agent 执行中的上下文爆炸问题
// ============================================================

// CompressionLevel 压缩级别
type CompressionLevel int

const (
	// LevelNone 无压缩，保留完整内容
	LevelNone CompressionLevel = iota
	// LevelTruncate 截断级别：工具输出超长时截断
	LevelTruncate
	// LevelSummarize 摘要级别：用 LLM 对历史轮次生成摘要
	LevelSummarize
	// LevelEvict 驱逐级别：直接丢弃最旧的消息
	LevelEvict
)

func (l CompressionLevel) String() string {
	switch l {
	case LevelNone:
		return "none"
	case LevelTruncate:
		return "truncate"
	case LevelSummarize:
		return "summarize"
	case LevelEvict:
		return "evict"
	default:
		return "unknown"
	}
}

// Config 上下文管理器配置
type Config struct {
	// MaxContextTokens 上下文窗口总 token 预算
	MaxContextTokens int

	// ReserveForResponse 为模型回复预留的 token 数
	ReserveForResponse int

	// SystemPromptBudget 系统提示词的 token 预算上限
	SystemPromptBudget int

	// MaxToolOutputTokens 单条工具输出的最大 token 数（超出则截断）
	MaxToolOutputTokens int

	// CompressionThreshold 触发压缩的使用率阈值 (0.0~1.0)
	// 当已用 token / 可用预算 > 此值时触发压缩
	CompressionThreshold float64

	// KeepRecentMessages 压缩时至少保留最近 N 条消息不压缩
	KeepRecentMessages int

	// SummaryMaxTokens 摘要的最大 token 数
	SummaryMaxTokens int
}

// DefaultConfig 返回默认配置
func DefaultConfig() Config {
	return Config{
		MaxContextTokens:     32768,
		ReserveForResponse:   4096,
		SystemPromptBudget:   2048,
		MaxToolOutputTokens:  4096,
		CompressionThreshold: 0.75,
		KeepRecentMessages:   4,
		SummaryMaxTokens:     1024,
	}
}

// LLMMessage 消息结构（与 llm.Message 兼容）
type LLMMessage struct {
	Role       string
	Content    string
	ToolCallID string // 工具结果对应的调用 ID
	ToolCalls  string // 序列化的工具调用 JSON（避免循环依赖）
}

// Summarizer 摘要生成接口（由外部注入 LLM 实现）
type Summarizer interface {
	// Summarize 对给定文本生成摘要
	Summarize(ctx context.Context, text string, maxTokens int) (string, error)
}

// Manager 上下文管理器，负责 token 预算控制和渐进式压缩
type Manager struct {
	cfg        Config
	estimator  *TokenEstimator
	summarizer Summarizer
	mu         sync.Mutex

	// conversationSummary 历史对话的累积摘要（压缩产物）
	conversationSummary string
	// compressionCount 已执行的压缩次数
	compressionCount int
	// totalTokensSaved 累计节省的 token 数
	totalTokensSaved int
}

// NewManager 创建上下文管理器
func NewManager(cfg Config, summarizer Summarizer) *Manager {
	if cfg.MaxContextTokens <= 0 {
		cfg.MaxContextTokens = 32768
	}
	if cfg.CompressionThreshold <= 0 || cfg.CompressionThreshold > 1.0 {
		cfg.CompressionThreshold = 0.75
	}
	if cfg.KeepRecentMessages <= 0 {
		cfg.KeepRecentMessages = 4
	}
	if cfg.MaxToolOutputTokens <= 0 {
		cfg.MaxToolOutputTokens = 4096
	}

	return &Manager{
		cfg:        cfg,
		estimator:  DefaultEstimator(),
		summarizer: summarizer,
	}
}

// AvailableBudget 计算当前可用于对话历史的 token 预算
func (m *Manager) AvailableBudget(systemPromptTokens int) int {
	budget := m.cfg.MaxContextTokens - m.cfg.ReserveForResponse - systemPromptTokens
	if budget < 1024 {
		budget = 1024 // 最低保底
	}
	return budget
}

// ProcessMessages 对消息列表执行上下文管理，返回压缩后的消息列表。
// 这是核心入口：在每次调用 LLM 前调用此方法。
func (m *Manager) ProcessMessages(ctx context.Context, messages []LLMMessage) []LLMMessage {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(messages) == 0 {
		return messages
	}

	// 分离系统消息和对话消息
	var systemMsgs, dialogMsgs []LLMMessage
	for _, msg := range messages {
		if msg.Role == "system" {
			systemMsgs = append(systemMsgs, msg)
		} else {
			dialogMsgs = append(dialogMsgs, msg)
		}
	}

	// 计算系统提示占用的 token
	systemTokens := m.estimator.EstimateMessages(toEstMessages(systemMsgs))

	// 可用预算
	budget := m.AvailableBudget(systemTokens)

	// 第一步：截断过长的工具输出
	dialogMsgs = m.truncateToolOutputs(dialogMsgs)

	// 计算当前 token 使用量
	currentTokens := m.estimator.EstimateMessages(toEstMessages(dialogMsgs))

	// 如果未超过阈值，直接返回
	threshold := float64(budget) * m.cfg.CompressionThreshold
	if float64(currentTokens) <= threshold {
		return m.assembleMessages(systemMsgs, dialogMsgs)
	}

	// 第二步：渐进式压缩
	dialogMsgs = m.compress(ctx, dialogMsgs, budget)

	return m.assembleMessages(systemMsgs, dialogMsgs)
}

// truncateToolOutputs 截断过长的工具输出消息
func (m *Manager) truncateToolOutputs(messages []LLMMessage) []LLMMessage {
	result := make([]LLMMessage, 0, len(messages))

	for _, msg := range messages {
		if msg.Role == "tool" {
			tokens := m.estimator.EstimateText(msg.Content)
			if tokens > m.cfg.MaxToolOutputTokens {
				// 智能截断：保留头尾
				msg.Content = TruncateText(msg.Content, m.cfg.MaxToolOutputTokens, m.estimator)
			}
		}
		result = append(result, msg)
	}

	return result
}

// compress 渐进式压缩对话历史
func (m *Manager) compress(ctx context.Context, messages []LLMMessage, budget int) []LLMMessage {
	currentTokens := m.estimator.EstimateMessages(toEstMessages(messages))

	// 级别 1: 如果已有累积摘要，注入摘要并减少历史消息
	if m.conversationSummary != "" {
		// 摘要已存在，检查是否需要进一步压缩
		if currentTokens <= budget {
			return messages
		}
	}

	// 级别 2: 对旧消息生成摘要（保留最近 N 条）
	if len(messages) > m.cfg.KeepRecentMessages {
		messages = m.summarizeOldMessages(ctx, messages, budget)
		currentTokens = m.estimator.EstimateMessages(toEstMessages(messages))
	}

	// 级别 3: 如果仍超预算，强制驱逐最旧消息
	if currentTokens > budget && len(messages) > m.cfg.KeepRecentMessages {
		messages = m.evictOldest(messages, budget)
	}

	return messages
}

// summarizeOldMessages 将旧消息压缩为摘要，保留最近的消息
func (m *Manager) summarizeOldMessages(ctx context.Context, messages []LLMMessage, budget int) []LLMMessage {
	keepRecent := m.cfg.KeepRecentMessages
	if keepRecent >= len(messages) {
		return messages
	}

	// 分割：旧消息 | 最近消息。
	// 分割点必须落在原子分组边界上：带 ToolCalls 的 assistant 消息与其 tool 结果
	// 消息要么整组进入摘要、要么整组保留，绝不拆散（否则会产生协议非法的孤儿消息）。
	groups := groupMessages(messages)
	splitGroup := len(groups)
	keepCount := 0
	for splitGroup > 0 && keepCount < keepRecent {
		splitGroup--
		keepCount += len(groups[splitGroup])
	}
	if splitGroup == 0 {
		// 所有消息都属于保留范围（最近一个分组过大），不做摘要
		return messages
	}

	var oldMessages, recentMessages []LLMMessage
	for _, g := range groups[:splitGroup] {
		oldMessages = append(oldMessages, g...)
	}
	for _, g := range groups[splitGroup:] {
		recentMessages = append(recentMessages, g...)
	}

	// 构建需要摘要的文本
	var sb strings.Builder
	if m.conversationSummary != "" {
		sb.WriteString("[之前的摘要] " + m.conversationSummary + "\n\n")
	}
	sb.WriteString("[新增对话]\n")
	for _, msg := range oldMessages {
		role := msg.Role
		content := msg.Content
		// 工具输出只取前 200 字符参与摘要
		if msg.Role == "tool" && len([]rune(content)) > 200 {
			runes := []rune(content)
			content = string(runes[:200]) + "..."
		}
		sb.WriteString(fmt.Sprintf("%s: %s\n", role, content))
	}

	textToSummarize := sb.String()

	// 尝试用 LLM 生成摘要
	var summary string
	if m.summarizer != nil {
		var err error
		summary, err = m.summarizer.Summarize(ctx, textToSummarize, m.cfg.SummaryMaxTokens)
		if err != nil {
			// LLM 摘要失败，降级为简单截断摘要
			summary = m.fallbackSummarize(textToSummarize)
		}
	} else {
		summary = m.fallbackSummarize(textToSummarize)
	}

	// 更新累积摘要
	m.conversationSummary = summary
	m.compressionCount++

	// 计算节省的 token
	oldTokens := m.estimator.EstimateMessages(toEstMessages(oldMessages))
	newTokens := m.estimator.EstimateText(summary)
	m.totalTokensSaved += oldTokens - newTokens

	// 组装：[摘要消息] + [最近消息]
	summaryMsg := LLMMessage{
		Role:    "system",
		Content: "[对话历史摘要] " + summary,
	}

	result := make([]LLMMessage, 0, 1+len(recentMessages))
	result = append(result, summaryMsg)
	result = append(result, recentMessages...)

	return result
}

// fallbackSummarize 无 LLM 时的降级摘要策略：提取关键行
func (m *Manager) fallbackSummarize(text string) string {
	maxTokens := m.cfg.SummaryMaxTokens
	// 先按行提取关键点
	summarized := SummarizeKeyPoints(text, 20)
	// 如果仍超长，截断
	if m.estimator.EstimateText(summarized) > maxTokens {
		summarized = TruncateText(summarized, maxTokens, m.estimator)
	}
	return summarized
}

// evictOldest 强制驱逐最旧的消息直到满足预算。
// 驱逐以原子分组为单位：带 ToolCalls 的 assistant 消息与其 tool 结果消息整组驱逐，
// 不会只驱逐其中一部分而产生孤儿 tool 消息。
func (m *Manager) evictOldest(messages []LLMMessage, budget int) []LLMMessage {
	for len(messages) > m.cfg.KeepRecentMessages {
		currentTokens := m.estimator.EstimateMessages(toEstMessages(messages))
		if currentTokens <= budget {
			break
		}
		groups := groupMessages(messages)
		// 定位第一个可驱逐的分组（开头的摘要消息不驱逐）
		evictIdx := 0
		if isSummaryMessage(groups[0][0]) {
			if len(groups) > 1 {
				evictIdx = 1
			} else {
				break
			}
		}
		// 计算该分组在消息切片中的起始下标
		start := 0
		for k := 0; k < evictIdx; k++ {
			start += len(groups[k])
		}
		// 整组移除
		messages = append(messages[:start], messages[start+len(groups[evictIdx]):]...)
	}
	return messages
}

// isSummaryMessage 判断是否为压缩产生的摘要消息（摘要不参与驱逐）
func isSummaryMessage(msg LLMMessage) bool {
	return msg.Role == "system" && strings.HasPrefix(msg.Content, "[对话历史摘要]")
}

// toolCallIDs 解析 assistant 消息 ToolCalls JSON 中的调用 id 集合。
// 第二个返回值表示解析是否成功；失败时调用方应采取保守策略（整组不可拆分）。
func toolCallIDs(raw string) (map[string]bool, bool) {
	var calls []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(raw), &calls); err != nil {
		return nil, false
	}
	ids := make(map[string]bool, len(calls))
	for _, c := range calls {
		if c.ID != "" {
			ids[c.ID] = true
		}
	}
	return ids, true
}

// groupMessages 将消息序列划分为原子分组，压缩/驱逐必须以组为单位进行：
//   - 带 ToolCalls 的 assistant 消息与其后续连续且 ToolCallID 匹配的 tool 消息构成一组；
//   - ToolCalls JSON 解析失败时，保守地将后续所有连续 tool 消息都并入该组（不可拆分）；
//   - 其余消息各自单独成组。
func groupMessages(messages []LLMMessage) [][]LLMMessage {
	groups := make([][]LLMMessage, 0, len(messages))
	i := 0
	for i < len(messages) {
		msg := messages[i]
		if msg.Role == "assistant" && msg.ToolCalls != "" {
			ids, ok := toolCallIDs(msg.ToolCalls)
			j := i + 1
			if ok {
				for j < len(messages) && messages[j].Role == "tool" && ids[messages[j].ToolCallID] {
					j++
				}
			} else {
				// 解析失败：保守策略，后续连续 tool 消息全部并入本组
				for j < len(messages) && messages[j].Role == "tool" {
					j++
				}
			}
			groups = append(groups, messages[i:j])
			i = j
		} else {
			groups = append(groups, messages[i:i+1])
			i++
		}
	}
	return groups
}

// assembleMessages 组装最终消息列表
func (m *Manager) assembleMessages(systemMsgs, dialogMsgs []LLMMessage) []LLMMessage {
	result := make([]LLMMessage, 0, len(systemMsgs)+len(dialogMsgs))
	result = append(result, systemMsgs...)
	result = append(result, dialogMsgs...)
	return result
}

// Stats 返回压缩统计信息
func (m *Manager) Stats() Stats {
	m.mu.Lock()
	defer m.mu.Unlock()
	return Stats{
		CompressionCount: m.compressionCount,
		TotalTokensSaved: m.totalTokensSaved,
		HasSummary:       m.conversationSummary != "",
	}
}

// Stats 压缩统计
type Stats struct {
	CompressionCount int  // 压缩执行次数
	TotalTokensSaved int  // 累计节省 token
	HasSummary       bool // 是否有累积摘要
}

// Reset 重置管理器状态（清空摘要）
func (m *Manager) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.conversationSummary = ""
	m.compressionCount = 0
	m.totalTokensSaved = 0
}

// toEstMessages 转换消息格式供估算器使用（透传 ToolCalls 以计入估算）
func toEstMessages(msgs []LLMMessage) []Message {
	result := make([]Message, len(msgs))
	for i, m := range msgs {
		result[i] = Message{Role: m.Role, Content: m.Content, ToolCalls: m.ToolCalls}
	}
	return result
}

// Config 返回管理器的配置
func (m *Manager) Config() Config {
	return m.cfg
}
