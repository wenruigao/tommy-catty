package memory

import (
	"context"
	"time"

	"github.com/wenruigao/tommy-catty/internal/llm"

	"github.com/google/uuid"
)

// PrewarmTag 是会话创建时预热记忆条目的标签：
// 预热条目参与上下文构建，但不会被重复持久化。
const PrewarmTag = "prewarm"

// CombinedMemory 是组合记忆管理器，整合工作记忆和长期记忆。
// 它实现了 engine.MemoryStore 接口，为执行引擎提供统一的记忆访问。
type CombinedMemory struct {
	working  *WorkingMemory // 工作记忆（短期，环形缓冲区）
	longTerm Memory         // 长期记忆（可为 nil，如向量数据库）
}

// NewCombinedMemory 创建组合记忆管理器。
// longTerm 参数可以为 nil，表示仅使用工作记忆。
func NewCombinedMemory(working *WorkingMemory, longTerm Memory) *CombinedMemory {
	if working == nil {
		working = NewWorkingMemory(100)
	}
	return &CombinedMemory{
		working:  working,
		longTerm: longTerm,
	}
}

// GetContext 获取最近的历史消息作为 LLM 上下文。
// 从工作记忆中取出最近条目，转换为 llm.Message 格式。
func (cm *CombinedMemory) GetContext(limit int) []llm.Message {
	if limit <= 0 {
		limit = 10
	}

	ctx := context.Background()
	entries, err := cm.working.GetRecent(ctx, limit)
	if err != nil || len(entries) == 0 {
		return nil
	}

	// 将记忆条目转换为消息格式
	// GetRecent 返回的是从新到旧，需要反转为时间顺序
	messages := make([]llm.Message, 0, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		// 根据标签判断角色，默认为 user 消息
		role := "user"
		for _, tag := range entry.Tags {
			if tag == "assistant" {
				role = "assistant"
				break
			}
		}
		messages = append(messages, llm.Message{
			Role:    role,
			Content: entry.Content,
		})
	}
	return messages
}

// Store 将消息存入记忆系统。
// 同时写入工作记忆，如果长期记忆可用则也写入长期记忆。
// 已存在于工作记忆中的内容（含会话创建时预热的历史回放）不重复落盘。
func (cm *CombinedMemory) Store(messages []llm.Message) {
	ctx := context.Background()
	existing := cm.working.ContentSet()

	for _, msg := range messages {
		if existing[msg.Content] {
			continue // 已在工作记忆中（预热回放 / 历史消息），跳过
		}
		entry := MemoryEntry{
			ID:        uuid.New().String(),
			Content:   msg.Content,
			Timestamp: time.Now(),
			Tags:      []string{msg.Role},
		}

		// 存入工作记忆
		_ = cm.working.Store(ctx, entry)

		// 如果长期记忆可用，也存入长期记忆
		if cm.longTerm != nil {
			_ = cm.longTerm.Store(ctx, entry)
		}
	}
}

// Search 搜索相关记忆，合并工作记忆和长期记忆的结果。
func (cm *CombinedMemory) Search(query string, topK int) []string {
	if topK <= 0 {
		topK = 5
	}

	ctx := context.Background()
	seen := make(map[string]bool) // 去重
	var results []string

	// 搜索工作记忆
	workingResults, err := cm.working.Search(ctx, query, topK)
	if err == nil {
		for _, entry := range workingResults {
			if !seen[entry.ID] {
				seen[entry.ID] = true
				results = append(results, entry.Content)
			}
		}
	}

	// 搜索长期记忆（如果可用）
	if cm.longTerm != nil {
		longTermResults, err := cm.longTerm.Search(ctx, query, topK)
		if err == nil {
			for _, entry := range longTermResults {
				if !seen[entry.ID] {
					seen[entry.ID] = true
					results = append(results, entry.Content)
				}
			}
		}
	}

	// 限制返回数量
	if len(results) > topK {
		results = results[:topK]
	}
	return results
}

// Clear 清除工作记忆（以及长期记忆，如果可用）。
func (cm *CombinedMemory) Clear() error {
	ctx := context.Background()
	if err := cm.working.Clear(ctx); err != nil {
		return err
	}
	if cm.longTerm != nil {
		return cm.longTerm.Clear(ctx)
	}
	return nil
}
