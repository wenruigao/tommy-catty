// Package memory 实现了 Agent 的记忆系统，
// 包括工作记忆（短期）和长期记忆的管理。
package memory

import (
	"context"
	"time"
)

// MemoryEntry 表示一条记忆条目。
type MemoryEntry struct {
	ID        string    // 唯一标识
	Content   string    // 记忆内容
	Timestamp time.Time // 创建时间
	Tags      []string  // 标签，用于分类和检索
	Embedding []float32 // 向量嵌入（用于语义搜索）
}

// Memory 定义记忆存储的核心接口。
// 不同的实现可以是内存、数据库或向量存储。
type Memory interface {
	// Store 存储一条记忆条目。
	Store(ctx context.Context, entry MemoryEntry) error

	// Search 根据查询语义搜索最相关的 topK 条记忆。
	Search(ctx context.Context, query string, topK int) ([]MemoryEntry, error)

	// GetRecent 获取最近的 limit 条记忆条目。
	GetRecent(ctx context.Context, limit int) ([]MemoryEntry, error)

	// Clear 清除所有记忆。
	Clear(ctx context.Context) error
}
