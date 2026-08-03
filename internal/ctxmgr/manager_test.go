package ctxmgr

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// stubSummarizer 记录输入文本并返回固定摘要的 Summarizer 实现。
type stubSummarizer struct {
	lastText string
	summary  string
	err      error
}

func (s *stubSummarizer) Summarize(ctx context.Context, text string, maxTokens int) (string, error) {
	s.lastText = text
	return s.summary, s.err
}

// assistantWithToolCalls 构造带 ToolCalls 的 assistant 消息（仅 id 字段参与配对）。
func assistantWithToolCalls(content string, ids ...string) LLMMessage {
	calls := make([]map[string]string, len(ids))
	for i, id := range ids {
		calls[i] = map[string]string{"id": id, "type": "function"}
	}
	raw, _ := json.Marshal(calls)
	return LLMMessage{Role: "assistant", Content: content, ToolCalls: string(raw)}
}

// toolMsg 构造一条 tool 结果消息。
func toolMsg(id, content string) LLMMessage {
	return LLMMessage{Role: "tool", ToolCallID: id, Content: content}
}

// parseableToolCallIDs 收集结果中所有 ToolCalls 可解析的 assistant 消息的调用 id。
func parseableToolCallIDs(msgs []LLMMessage) map[string]bool {
	ids := make(map[string]bool)
	for _, m := range msgs {
		if m.Role != "assistant" || m.ToolCalls == "" {
			continue
		}
		if got, ok := toolCallIDs(m.ToolCalls); ok {
			for id := range got {
				ids[id] = true
			}
		}
	}
	return ids
}

// assertNoOrphanToolMessages 断言消息序列中不存在孤儿 tool 消息：
// 每条 tool 消息的 ToolCallID 都必须有序列内某条 assistant 消息的 ToolCalls 与之对应。
func assertNoOrphanToolMessages(t *testing.T, msgs []LLMMessage) {
	t.Helper()
	ids := parseableToolCallIDs(msgs)
	for _, m := range msgs {
		if m.Role == "tool" && !ids[m.ToolCallID] {
			t.Errorf("存在孤儿 tool 消息: ToolCallID=%q 没有对应的 assistant ToolCalls", m.ToolCallID)
		}
	}
}

// hasMessage 判断消息序列中是否包含指定内容的消息。
func hasMessage(msgs []LLMMessage, content string) bool {
	for _, m := range msgs {
		if m.Content == content {
			return true
		}
	}
	return false
}

func TestSummarizeOldMessages_KeepsToolCallGroupIntact(t *testing.T) {
	// keepRecent=3 时朴素切分会把工具组切成两半（tc2 成为孤儿），
	// 分组感知逻辑应整组保留。
	sum := &stubSummarizer{summary: "简短摘要"}
	m := NewManager(Config{KeepRecentMessages: 3, SummaryMaxTokens: 100}, sum)

	msgs := []LLMMessage{
		{Role: "user", Content: "u1"},
		assistantWithToolCalls("调用工具", "c1", "c2"),
		toolMsg("c1", "工具结果一"),
		toolMsg("c2", "工具结果二"),
		{Role: "user", Content: "u2"},
		{Role: "user", Content: "u3"},
	}

	got := m.summarizeOldMessages(context.Background(), msgs, 1024)

	assertNoOrphanToolMessages(t, got)
	// 工具组三条消息应整组保留在结果中
	for _, want := range []string{"调用工具", "工具结果一", "工具结果二"} {
		if !hasMessage(got, want) {
			t.Errorf("工具组消息 %q 应整组保留在结果中", want)
		}
	}
	// 只有 u1 进入摘要
	if !strings.Contains(sum.lastText, "u1") || strings.Contains(sum.lastText, "工具结果一") {
		t.Errorf("摘要输入应只包含 u1，实际: %q", sum.lastText)
	}
}

func TestSummarizeOldMessages_WholeGroupIntoSummary(t *testing.T) {
	// keepRecent=2 时整个工具组都较旧，应整组进入摘要文本。
	sum := &stubSummarizer{summary: "简短摘要"}
	m := NewManager(Config{KeepRecentMessages: 2, SummaryMaxTokens: 100}, sum)

	msgs := []LLMMessage{
		{Role: "user", Content: "u1"},
		assistantWithToolCalls("调用工具", "c1", "c2"),
		toolMsg("c1", "工具结果一"),
		toolMsg("c2", "工具结果二"),
		{Role: "user", Content: "u2"},
		{Role: "assistant", Content: "最终回复"},
	}

	got := m.summarizeOldMessages(context.Background(), msgs, 1024)

	assertNoOrphanToolMessages(t, got)
	// 整组文本（含 tool 消息）一起喂给 Summarizer
	for _, want := range []string{"调用工具", "工具结果一", "工具结果二"} {
		if !strings.Contains(sum.lastText, want) {
			t.Errorf("摘要输入应包含整组内容 %q，实际: %q", want, sum.lastText)
		}
	}
	// 结果 = 摘要消息 + 最近 2 条
	if len(got) != 3 {
		t.Fatalf("结果应为 摘要+2 条最近消息，实际 %d 条", len(got))
	}
}

func TestSummarizeOldMessages_CorruptToolCallsJSON(t *testing.T) {
	// ToolCalls JSON 损坏时采取保守策略：assistant 与后续连续 tool 消息不可拆分。
	sum := &stubSummarizer{summary: "简短摘要"}
	m := NewManager(Config{KeepRecentMessages: 2, SummaryMaxTokens: 100}, sum)

	msgs := []LLMMessage{
		{Role: "user", Content: "u1"},
		{Role: "assistant", Content: "损坏的调用", ToolCalls: "{这不是合法JSON"},
		toolMsg("cX", "工具结果"),
		{Role: "user", Content: "u2"},
	}

	got := m.summarizeOldMessages(context.Background(), msgs, 1024)

	// 保守行为：assistant 与其 tool 消息必须同进同出
	hasAssistant := hasMessage(got, "损坏的调用")
	hasTool := hasMessage(got, "工具结果")
	if hasAssistant != hasTool {
		t.Errorf("ToolCalls 损坏时 assistant 与 tool 消息应整组保留或整组摘要，实际 assistant=%v tool=%v",
			hasAssistant, hasTool)
	}
	// 本例中应整组保留（keepRecent=2 的切分点不允许拆开它们）
	if !hasAssistant || !hasTool {
		t.Errorf("损坏组应整组保留在结果中，实际: %+v", got)
	}
}

func TestEvictOldest_EvictsWholeGroup(t *testing.T) {
	// 驱逐必须整组进行：不能只驱逐 assistant 而留下孤儿 tool 消息。
	m := NewManager(Config{KeepRecentMessages: 1, SummaryMaxTokens: 100}, nil)

	big := strings.Repeat("很长的中文内容。", 100)
	msgs := []LLMMessage{
		assistantWithToolCalls(big, "c1"),
		toolMsg("c1", big),
		{Role: "user", Content: big},
	}

	got := m.evictOldest(msgs, 1)

	assertNoOrphanToolMessages(t, got)
	// 预算为 1 时工具组应被整组驱逐，只剩最近的用户消息
	if len(got) != 1 || got[0].Role != "user" {
		t.Errorf("整组驱逐后应只剩最近的用户消息，实际: %+v", got)
	}
}

func TestEvictOldest_KeepsSummaryMessage(t *testing.T) {
	m := NewManager(Config{KeepRecentMessages: 1, SummaryMaxTokens: 100}, nil)

	big := strings.Repeat("很长的中文内容。", 100)
	msgs := []LLMMessage{
		{Role: "system", Content: "[对话历史摘要] 历史内容"},
		assistantWithToolCalls(big, "c1"),
		toolMsg("c1", big),
		{Role: "user", Content: big},
	}

	got := m.evictOldest(msgs, 1700)

	assertNoOrphanToolMessages(t, got)
	// 摘要消息不驱逐，工具组整组驱逐，最近的用户消息保留
	if len(got) != 2 || !isSummaryMessage(got[0]) || got[1].Role != "user" {
		t.Errorf("应保留摘要消息并整组驱逐工具组，实际: %+v", got)
	}
}

func TestProcessMessages_NoOrphanToolMessages(t *testing.T) {
	// 端到端：小预算触发摘要+驱逐后，输出中不得存在孤儿 tool 消息。
	sum := &stubSummarizer{summary: "简短摘要"}
	m := NewManager(Config{
		MaxContextTokens:     2048,
		ReserveForResponse:   512,
		CompressionThreshold: 0.75,
		KeepRecentMessages:   4,
		SummaryMaxTokens:     100,
		MaxToolOutputTokens:  100000,
	}, sum)

	big := strings.Repeat("汉", 400)
	msgs := []LLMMessage{
		{Role: "system", Content: "系统提示"},
		{Role: "user", Content: big},
		assistantWithToolCalls("调用工具", "c1", "c2"),
		toolMsg("c1", big),
		toolMsg("c2", big),
		{Role: "user", Content: big},
		{Role: "assistant", Content: big},
		{Role: "user", Content: big},
		{Role: "assistant", Content: big},
	}

	got := m.ProcessMessages(context.Background(), msgs)

	assertNoOrphanToolMessages(t, got)
	if !m.Stats().HasSummary {
		t.Error("应已生成累积摘要")
	}
}
