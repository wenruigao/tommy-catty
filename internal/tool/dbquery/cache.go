// cache.go 实现 db_query 查询结果短期缓存。
// 缓存键：SHA-256(datasource + normalized_sql)，LRU + TTL。
package dbquery

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"sync"
	"time"
)

// QueryCache 查询结果缓存（并发安全）。
type QueryCache struct {
	mu       sync.RWMutex
	entries  map[string]cacheEntry
	capacity int
	ttl      time.Duration
	hits     int64
	misses   int64
}

type cacheEntry struct {
	result    *QueryResult
	createdAt time.Time
}

// NewQueryCache 创建查询缓存。capacity 默认 200，ttl 默认 5 分钟。
func NewQueryCache(capacity int, ttl time.Duration) *QueryCache {
	if capacity <= 0 {
		capacity = 200
	}
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return &QueryCache{
		entries:  make(map[string]cacheEntry, capacity),
		capacity: capacity,
		ttl:      ttl,
	}
}

// whitespaceRe 用于 SQL 规范化。
var whitespaceRe = regexp.MustCompile(`\s+`)

// normalizeSQL 规范化 SQL（去多余空白、统一关键词大写）。
func normalizeSQL(sql string) string {
	s := whitespaceRe.ReplaceAllString(strings.TrimSpace(sql), " ")
	// 前后补空格，确保首尾关键词也能被匹配
	s = " " + s + " "
	for _, kw := range []string{"select", "from", "where", "join", "left", "right", "inner", "outer", "on", "and", "or", "not", "in", "like", "order", "by", "group", "having", "limit", "offset", "as", "is", "null", "between", "exists", "case", "when", "then", "else", "end", "with"} {
		s = strings.ReplaceAll(s, " "+kw+" ", " "+strings.ToUpper(kw)+" ")
	}
	return strings.TrimSpace(s)
}

// CacheKey 计算缓存键。
func CacheKey(datasource, sql string) string {
	normalized := datasource + "|" + normalizeSQL(sql)
	h := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(h[:])
}

// Get 查询缓存。
func (c *QueryCache) Get(datasource, sql string) (*QueryResult, bool) {
	key := CacheKey(datasource, sql)
	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()

	if !ok {
		c.mu.Lock()
		c.misses++
		c.mu.Unlock()
		return nil, false
	}

	if time.Since(entry.createdAt) > c.ttl {
		c.mu.Lock()
		delete(c.entries, key)
		c.misses++
		c.mu.Unlock()
		return nil, false
	}

	c.mu.Lock()
	c.hits++
	c.mu.Unlock()
	return entry.result, true
}

// Put 写入缓存（仅缓存成功且未截断的结果）。
func (c *QueryCache) Put(datasource, sql string, result *QueryResult) {
	if result == nil || result.Truncated {
		return // 截断结果不缓存
	}
	key := CacheKey(datasource, sql)
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.entries) >= c.capacity {
		c.evictOldest()
	}
	c.entries[key] = cacheEntry{result: result, createdAt: time.Now()}
}

// Invalidate 清空指定数据源的所有缓存。
// 由于缓存键为 hash 无法按数据源前缀筛选，简化为全量清空。
func (c *QueryCache) Invalidate(datasource string) {
	_ = datasource // 保留参数以备后续按源失效优化
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]cacheEntry, c.capacity)
}

// Clear 清空全部缓存。
func (c *QueryCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]cacheEntry, c.capacity)
}

// Stats 返回缓存统计。
func (c *QueryCache) Stats() (hits, misses int64, size int) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.hits, c.misses, len(c.entries)
}

func (c *QueryCache) evictOldest() {
	var oldestKey string
	var oldestTime time.Time
	first := true
	for k, v := range c.entries {
		if first || v.createdAt.Before(oldestTime) {
			oldestKey = k
			oldestTime = v.createdAt
			first = false
		}
	}
	if oldestKey != "" {
		delete(c.entries, oldestKey)
	}
}
