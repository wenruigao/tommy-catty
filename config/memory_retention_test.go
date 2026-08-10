package config

import (
	"testing"
	"time"
)

// TestMemoryRetentionDefaults 验证保留窗口默认值随"是否配置远端"切换。
func TestMemoryRetentionDefaults(t *testing.T) {
	// 无远端：sqlite 全量（0），file 7 天
	c := &Config{}
	if got := c.MemorySQLiteRetention(); got != 0 {
		t.Fatalf("无远端 sqlite 应默认全量(0)，实际 %v", got)
	}
	if got := c.MemoryFileRetention(); got != 168*time.Hour {
		t.Fatalf("无远端 file 应默认 168h，实际 %v", got)
	}

	// 有远端：sqlite 7 天，file 3 天
	c.Memory.Storage.URL = "http://mem.internal:9301"
	if got := c.MemorySQLiteRetention(); got != 168*time.Hour {
		t.Fatalf("有远端 sqlite 应默认 168h，实际 %v", got)
	}
	if got := c.MemoryFileRetention(); got != 72*time.Hour {
		t.Fatalf("有远端 file 应默认 72h，实际 %v", got)
	}

	// 显式配置优先（0 = 全量）
	c.Memory.Storage.SQLiteRetention = "0s"
	c.Memory.Storage.FileRetention = "48h"
	if got := c.MemorySQLiteRetention(); got != 0 {
		t.Fatalf("显式 0s 应为全量，实际 %v", got)
	}
	if got := c.MemoryFileRetention(); got != 48*time.Hour {
		t.Fatalf("显式 48h 应生效，实际 %v", got)
	}
}
