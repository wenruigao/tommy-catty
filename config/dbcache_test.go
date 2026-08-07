// dbcache_test.go 验证 db_query_cache 配置解析。
package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDBQueryCacheConfig_Parse 验证 db_query_cache 段的 YAML 解析。
func TestDBQueryCacheConfig_Parse(t *testing.T) {
	content := "db_query_cache:\n  enabled: true\n  capacity: 50\n  ttl: \"2m\"\n"
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.DBQueryCache == nil {
		t.Fatal("db_query_cache 应被解析")
	}
	if !cfg.DBQueryCache.Enabled || cfg.DBQueryCache.Capacity != 50 || cfg.DBQueryCache.TTL != "2m" {
		t.Errorf("解析结果不符: %+v", cfg.DBQueryCache)
	}
}

// TestDBQueryCacheConfig_DefaultNil 未配置时为 nil（bootstrap 层按缺省启用处理）。
func TestDBQueryCacheConfig_DefaultNil(t *testing.T) {
	cfg := Default()
	if cfg.DBQueryCache != nil {
		t.Errorf("默认配置不应显式设置 db_query_cache: %+v", cfg.DBQueryCache)
	}
}

// TestLLMCacheMeterConfig_Parse 验证 llm.cache / llm.meter 段解析并透传至网关配置。
func TestLLMCacheMeterConfig_Parse(t *testing.T) {
	content := "llm:\n  cache:\n    enabled: true\n    capacity: 100\n    ttl: \"5m\"\n  meter:\n    daily_token_limit: 2000\n"
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	gw := cfg.ToGatewayConfig()
	if gw.Cache == nil || !gw.Cache.Enabled || gw.Cache.Capacity != 100 || gw.Cache.TTL != "5m" {
		t.Errorf("cache 透传不符: %+v", gw.Cache)
	}
	if gw.Meter == nil || gw.Meter.DailyTokenLimit != 2000 {
		t.Errorf("meter 透传不符: %+v", gw.Meter)
	}
}
