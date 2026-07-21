package memory

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// WorkingMemory 是基于内存环形缓冲区的工作记忆实现。
// 当容量满时，最旧的条目会被自动淘汰。
type WorkingMemory struct {
	entries []MemoryEntry // 环形缓冲区存储
	maxSize int           // 最大容量
	mu      sync.RWMutex  // 读写锁，保证并发安全
}

// NewWorkingMemory 创建一个新的工作记忆实例。
// maxSize 指定环形缓冲区的最大容量，必须大于 0。
func NewWorkingMemory(maxSize int) *WorkingMemory {
	if maxSize <= 0 {
		maxSize = 100 // 默认容量
	}
	return &WorkingMemory{
		entries: make([]MemoryEntry, 0, maxSize),
		maxSize: maxSize,
	}
}

// Store 存储一条记忆条目到环形缓冲区。
// 当缓冲区已满时，最旧的条目会被移除。
func (wm *WorkingMemory) Store(_ context.Context, entry MemoryEntry) error {
	wm.mu.Lock()
	defer wm.mu.Unlock()

	// 如果未设置 ID，自动生成
	if entry.ID == "" {
		entry.ID = uuid.New().String()
	}
	// 如果未设置时间戳，使用当前时间
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now()
	}

	// 环形缓冲区：容量满时移除最旧的条目
	if len(wm.entries) >= wm.maxSize {
		// 移除最前面的（最旧的）条目
		wm.entries = wm.entries[1:]
	}

	wm.entries = append(wm.entries, entry)
	return nil
}

// Search 通过简单关键词匹配搜索记忆。
// 这是向量语义搜索的占位实现，后续可替换为真正的向量检索。
func (wm *WorkingMemory) Search(_ context.Context, query string, topK int) ([]MemoryEntry, error) {
	wm.mu.RLock()
	defer wm.mu.RUnlock()

	if topK <= 0 {
		topK = 5
	}

	// 将查询拆分为关键词
	keywords := strings.Fields(strings.ToLower(query))
	if len(keywords) == 0 {
		return nil, nil
	}

	// 简单关键词匹配，按匹配度排序
	type scored struct {
		entry MemoryEntry
		score int
	}
	var results []scored

	for _, entry := range wm.entries {
		contentLower := strings.ToLower(entry.Content)
		tagsLower := strings.ToLower(strings.Join(entry.Tags, " "))
		score := 0

		for _, kw := range keywords {
			if strings.Contains(contentLower, kw) {
				score++
			}
			if strings.Contains(tagsLower, kw) {
				score++
			}
		}

		if score > 0 {
			results = append(results, scored{entry: entry, score: score})
		}
	}

	// 按分数降序排序（简单冒泡，数据量小）
	for i := 0; i < len(results); i++ {
		for j := i + 1; j < len(results); j++ {
			if results[j].score > results[i].score {
				results[i], results[j] = results[j], results[i]
			}
		}
	}

	// 取前 topK 条
	if len(results) > topK {
		results = results[:topK]
	}

	entries := make([]MemoryEntry, len(results))
	for i, r := range results {
		entries[i] = r.entry
	}
	return entries, nil
}

// GetRecent 返回最近的 limit 条记忆条目（按时间从新到旧）。
func (wm *WorkingMemory) GetRecent(_ context.Context, limit int) ([]MemoryEntry, error) {
	wm.mu.RLock()
	defer wm.mu.RUnlock()

	if limit <= 0 {
		limit = 10
	}

	total := len(wm.entries)
	if total == 0 {
		return nil, nil
	}

	// 从末尾取最近的 limit 条
	start := total - limit
	if start < 0 {
		start = 0
	}

	// 返回副本，逆序（最新的在前）
	result := make([]MemoryEntry, 0, total-start)
	for i := total - 1; i >= start; i-- {
		result = append(result, wm.entries[i])
	}
	return result, nil
}

// Clear 清除所有工作记忆。
func (wm *WorkingMemory) Clear(_ context.Context) error {
	wm.mu.Lock()
	defer wm.mu.Unlock()

	wm.entries = make([]MemoryEntry, 0, wm.maxSize)
	return nil
}
