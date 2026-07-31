package dbquery

import (
	"testing"
	"time"
)

func TestQueryCacheHitMiss(t *testing.T) {
	cache := NewQueryCache(10, time.Minute)
	result := &QueryResult{Columns: []string{"id"}, Rows: [][]string{{"1"}}, RowCount: 1}

	// Miss
	_, hit := cache.Get("ds", "SELECT * FROM users")
	if hit {
		t.Error("expected miss")
	}

	// Put and hit
	cache.Put("ds", "SELECT * FROM users", result)
	got, hit := cache.Get("ds", "SELECT * FROM users")
	if !hit {
		t.Error("expected hit")
	}
	if got.RowCount != 1 {
		t.Errorf("unexpected row count: %d", got.RowCount)
	}
}

func TestQueryCacheNormalization(t *testing.T) {
	cache := NewQueryCache(10, time.Minute)
	result := &QueryResult{RowCount: 1}
	cache.Put("ds", "select  *  from users", result)
	// 规范化后应命中
	_, hit := cache.Get("ds", "SELECT * FROM users")
	if !hit {
		t.Error("normalized SQL should hit cache")
	}
}

func TestQueryCacheTruncatedNotCached(t *testing.T) {
	cache := NewQueryCache(10, time.Minute)
	result := &QueryResult{RowCount: 500, Truncated: true}
	cache.Put("ds", "SELECT * FROM big", result)
	_, hit := cache.Get("ds", "SELECT * FROM big")
	if hit {
		t.Error("truncated results should not be cached")
	}
}

func TestQueryCacheTTL(t *testing.T) {
	cache := NewQueryCache(10, 1*time.Millisecond)
	cache.Put("ds", "SELECT 1", &QueryResult{RowCount: 1})
	time.Sleep(5 * time.Millisecond)
	_, hit := cache.Get("ds", "SELECT 1")
	if hit {
		t.Error("expected miss after TTL")
	}
}
