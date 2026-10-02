// cache.go 实现语义缓存 L1（精确哈希层）。
// 对规范化后的 Prompt 计算 SHA-256 哈希，命中则直接返回缓存结果。
package llm

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"sync"
	"time"
)

// CacheEntry 缓存条目。
type CacheEntry struct {
	Response  ChatResponse
	CreatedAt time.Time
	SizeBytes int // 该条目的估算字节占用
}

// SemanticCache 精确哈希 + TTL + 双上界（条数 & 字节）的 L1 语义缓存（并发安全）。
//
// 淘汰策略：条数超限或字节超限时，按 CreatedAt 从旧到新逐条淘汰直到两个维度均满足。
// 参考 OpenClaw 的 capacity + byte-bound 双上界淘汰设计。
type SemanticCache struct {
	mu        sync.RWMutex
	entries   map[string]CacheEntry
	capacity  int
	maxBytes  int64 // 字节预算上限；<= 0 表示不限
	curBytes  int64 // 当前总字节占用
	ttl       time.Duration
	hits      int64
	misses    int64
	evictions int64
}

// NewSemanticCache 创建缓存。
//   - capacity <= 0 默认 500
//   - ttl <= 0 默认 10 分钟
//   - maxBytes <= 0 表示不限字节（仅条数约束）
func NewSemanticCache(capacity int, ttl time.Duration, maxBytes int64) *SemanticCache {
	if capacity <= 0 {
		capacity = 500
	}
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &SemanticCache{
		entries:  make(map[string]CacheEntry, capacity),
		capacity: capacity,
		maxBytes: maxBytes,
		ttl:      ttl,
	}
}

// estimateSize 估算一个缓存条目的字节占用：
// 键（64B hex）+ Content + ToolCalls 序列化 + 固定元数据开销。
func estimateSize(key string, resp ChatResponse) int {
	size := len(key) + len(resp.Content) + len(resp.Model) + 64 // 64B 固定开销（时间戳、计数器等）
	for _, tc := range resp.ToolCalls {
		size += len(tc.ID) + len(tc.Name) + len(tc.Arguments)
	}
	return size
}

// cacheKey 计算请求的缓存键（SHA-256 of model + tools + normalized prompt）。
func cacheKey(req ChatRequest) string {
	var sb strings.Builder
	sb.WriteString(req.Model)
	sb.WriteByte('|')
	// 工具列表参与缓存键：不同工具集会改变模型可用动作，
	// 不计入键会在切换工具集时产生错误命中
	if len(req.Tools) > 0 {
		names := make([]string, 0, len(req.Tools))
		for _, td := range req.Tools {
			names = append(names, td.Name)
		}
		sort.Strings(names)
		sb.WriteString(strings.Join(names, ","))
	}
	sb.WriteByte('|')
	for _, msg := range req.Messages {
		sb.WriteString(msg.Role)
		sb.WriteByte(':')
		// 规范化：去除多余空白
		sb.WriteString(normalizeWhitespace(msg.Content))
		sb.WriteByte(';')
	}
	h := sha256.Sum256([]byte(sb.String()))
	return hex.EncodeToString(h[:])
}

// normalizeWhitespace 去除多余空白（连续空格/换行合并为单空格）。
func normalizeWhitespace(s string) string {
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

// Get 查询缓存。返回 (response, hit)。
func (c *SemanticCache) Get(req ChatRequest) (ChatResponse, bool) {
	key := cacheKey(req)
	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()

	if !ok {
		c.mu.Lock()
		c.misses++
		c.mu.Unlock()
		return ChatResponse{}, false
	}

	// TTL 检查
	if time.Since(entry.CreatedAt) > c.ttl {
		c.mu.Lock()
		c.removeEntry(key, entry)
		c.misses++
		c.mu.Unlock()
		return ChatResponse{}, false
	}

	c.mu.Lock()
	c.hits++
	c.mu.Unlock()
	return entry.Response, true
}

// Put 写入缓存。条数或字节任一超限时，按时间从旧到新淘汰。
func (c *SemanticCache) Put(req ChatRequest, resp ChatResponse) {
	key := cacheKey(req)
	size := estimateSize(key, resp)

	c.mu.Lock()
	defer c.mu.Unlock()

	// 如果键已存在，先扣除旧条目的字节占用
	if old, ok := c.entries[key]; ok {
		c.curBytes -= int64(old.SizeBytes)
		delete(c.entries, key)
	}

	// 淘汰：条数超限 或 字节超限（maxBytes > 0 时）
	for len(c.entries) >= c.capacity || (c.maxBytes > 0 && c.curBytes+int64(size) > c.maxBytes && len(c.entries) > 0) {
		c.evictOldest()
	}

	c.entries[key] = CacheEntry{
		Response:  resp,
		CreatedAt: time.Now(),
		SizeBytes: size,
	}
	c.curBytes += int64(size)
}

// Clear 清空缓存。
func (c *SemanticCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]CacheEntry, c.capacity)
	c.curBytes = 0
}

// Stats 返回缓存统计：命中数、未命中数、当前条目数、当前字节占用、累计淘汰数。
func (c *SemanticCache) Stats() (hits, misses int64, size int, bytes int64, evictions int64) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.hits, c.misses, len(c.entries), c.curBytes, c.evictions
}

// removeEntry 删除条目并扣减字节计数（调用方须持写锁）。
func (c *SemanticCache) removeEntry(key string, entry CacheEntry) {
	delete(c.entries, key)
	c.curBytes -= int64(entry.SizeBytes)
}

// evictOldest 清除最旧条目（调用方须持写锁）。
func (c *SemanticCache) evictOldest() {
	var oldestKey string
	var oldestTime time.Time
	first := true
	for k, v := range c.entries {
		if first || v.CreatedAt.Before(oldestTime) {
			oldestKey = k
			oldestTime = v.CreatedAt
			first = false
		}
	}
	if oldestKey != "" {
		c.curBytes -= int64(c.entries[oldestKey].SizeBytes)
		delete(c.entries, oldestKey)
		c.evictions++
	}
}
