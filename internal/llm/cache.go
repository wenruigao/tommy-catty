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
}

// SemanticCache 精确哈希 + TTL 的 L1 语义缓存（并发安全）。
type SemanticCache struct {
	mu       sync.RWMutex
	entries  map[string]CacheEntry
	capacity int
	ttl      time.Duration
	hits     int64
	misses   int64
}

// NewSemanticCache 创建缓存。capacity <= 0 默认 500，ttl <= 0 默认 10 分钟。
func NewSemanticCache(capacity int, ttl time.Duration) *SemanticCache {
	if capacity <= 0 {
		capacity = 500
	}
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &SemanticCache{
		entries:  make(map[string]CacheEntry, capacity),
		capacity: capacity,
		ttl:      ttl,
	}
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
		delete(c.entries, key)
		c.misses++
		c.mu.Unlock()
		return ChatResponse{}, false
	}

	c.mu.Lock()
	c.hits++
	c.mu.Unlock()
	return entry.Response, true
}

// Put 写入缓存。
func (c *SemanticCache) Put(req ChatRequest, resp ChatResponse) {
	key := cacheKey(req)
	c.mu.Lock()
	defer c.mu.Unlock()

	// LRU 简化：超容量时清除最旧条目
	if len(c.entries) >= c.capacity {
		c.evictOldest()
	}

	c.entries[key] = CacheEntry{
		Response:  resp,
		CreatedAt: time.Now(),
	}
}

// Clear 清空缓存。
func (c *SemanticCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]CacheEntry, c.capacity)
}

// Stats 返回缓存统计。
func (c *SemanticCache) Stats() (hits, misses int64, size int) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.hits, c.misses, len(c.entries)
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
		delete(c.entries, oldestKey)
	}
}
