package memstore

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/wenruigao/tommy-catty/internal/memory"
)

func newTestAsyncSQLite(t *testing.T) *AsyncSQLiteStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "async_test.db")
	sq, err := NewSQLiteStore(path, 100)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	a := NewAsyncSQLiteStore(sq, AsyncSQLiteConfig{BufferSize: 64})
	t.Cleanup(func() { a.Close() })
	return a
}

func TestAsyncSQLite_SaveAndRead(t *testing.T) {
	a := newTestAsyncSQLite(t)
	ctx := context.Background()

	entry := memory.MemoryEntry{
		ID: "m1", UserID: "u1", Content: "hello async", Timestamp: time.Now(),
	}
	if err := a.SaveMemory(ctx, entry); err != nil {
		t.Fatalf("SaveMemory: %v", err)
	}
	// 异步写入需要 flush 后才能保证读到
	if err := a.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	entries, err := a.RecentMemories(ctx, "u1", 10)
	if err != nil {
		t.Fatalf("RecentMemories: %v", err)
	}
	if len(entries) != 1 || entries[0].Content != "hello async" {
		t.Errorf("expected 1 entry 'hello async', got %+v", entries)
	}
}

func TestAsyncSQLite_SaveSync(t *testing.T) {
	a := newTestAsyncSQLite(t)
	ctx := context.Background()

	entry := memory.MemoryEntry{
		ID: "m2", UserID: "u1", Content: "sync write", Timestamp: time.Now(),
	}
	if err := a.SaveMemorySync(ctx, entry); err != nil {
		t.Fatalf("SaveMemorySync: %v", err)
	}
	// 同步写入后立即可读（无需 Flush）
	entries, _ := a.RecentMemories(ctx, "u1", 10)
	if len(entries) != 1 || entries[0].Content != "sync write" {
		t.Errorf("expected immediate read after sync write, got %+v", entries)
	}
}

func TestAsyncSQLite_ConcurrentWrites(t *testing.T) {
	a := newTestAsyncSQLite(t)
	ctx := context.Background()

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			entry := memory.MemoryEntry{
				ID: fmt.Sprintf("m-%d", idx), UserID: "u1",
				Content: fmt.Sprintf("entry %d", idx), Timestamp: time.Now(),
			}
			a.SaveMemory(ctx, entry)
		}(i)
	}
	wg.Wait()

	if err := a.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	entries, err := a.RecentMemories(ctx, "u1", 100)
	if err != nil {
		t.Fatalf("RecentMemories: %v", err)
	}
	if len(entries) != n {
		t.Errorf("expected %d entries, got %d", n, len(entries))
	}
}

func TestAsyncSQLite_NonBlocking(t *testing.T) {
	a := newTestAsyncSQLite(t)
	ctx := context.Background()

	// 批量写入不应阻塞调用方
	start := time.Now()
	for i := 0; i < 100; i++ {
		entry := memory.MemoryEntry{
			ID: fmt.Sprintf("perf-%d", i), UserID: "u1",
			Content: "x", Timestamp: time.Now(),
		}
		a.SaveMemory(ctx, entry)
	}
	elapsed := time.Since(start)

	// 100 次异步投递应在 50ms 内完成（实际应 < 1ms）
	if elapsed > 50*time.Millisecond {
		t.Errorf("100 async writes took %v, expected < 50ms", elapsed)
	}
}

func TestAsyncSQLite_Profile(t *testing.T) {
	a := newTestAsyncSQLite(t)
	ctx := context.Background()

	if err := a.SaveProfile(ctx, "u1", "# Profile\nlikes Go"); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if err := a.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	content, err := a.LoadProfile(ctx, "u1")
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	if content != "# Profile\nlikes Go" {
		t.Errorf("LoadProfile = %q", content)
	}
}

func TestAsyncSQLite_Delete(t *testing.T) {
	a := newTestAsyncSQLite(t)
	ctx := context.Background()

	a.SaveMemorySync(ctx, memory.MemoryEntry{ID: "d1", UserID: "u1", Content: "x", Timestamp: time.Now()})
	if err := a.DeleteMemories(ctx, "u1"); err != nil {
		t.Fatalf("DeleteMemories: %v", err)
	}
	if err := a.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	entries, _ := a.RecentMemories(ctx, "u1", 10)
	if len(entries) != 0 {
		t.Errorf("expected 0 entries after delete, got %d", len(entries))
	}
}

func TestAsyncSQLite_Meta(t *testing.T) {
	a := newTestAsyncSQLite(t)
	ctx := context.Background()

	if err := a.SetMetaSync(ctx, "u1", "flag", "1"); err != nil {
		t.Fatalf("SetMetaSync: %v", err)
	}
	v, err := a.GetMeta(ctx, "u1", "flag")
	if err != nil {
		t.Fatalf("GetMeta: %v", err)
	}
	if v != "1" {
		t.Errorf("GetMeta = %q, want '1'", v)
	}
}

func TestAsyncSQLite_PruneBefore(t *testing.T) {
	a := newTestAsyncSQLite(t)
	ctx := context.Background()

	old := memory.MemoryEntry{ID: "old", UserID: "u1", Content: "old", Timestamp: time.Now().Add(-48 * time.Hour)}
	new := memory.MemoryEntry{ID: "new", UserID: "u1", Content: "new", Timestamp: time.Now()}
	a.SaveMemorySync(ctx, old)
	a.SaveMemorySync(ctx, new)

	a.PruneBefore(ctx, "u1", time.Now().Add(-24*time.Hour))
	a.Flush()

	entries, _ := a.RecentMemories(ctx, "u1", 10)
	if len(entries) != 1 || entries[0].ID != "new" {
		t.Errorf("expected only 'new' after prune, got %+v", entries)
	}
}

func TestAsyncSQLite_Close_DrainsQueue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "drain.db")
	sq, _ := NewSQLiteStore(path, 100)
	a := NewAsyncSQLiteStore(sq, AsyncSQLiteConfig{BufferSize: 64})
	ctx := context.Background()

	for i := 0; i < 20; i++ {
		a.SaveMemory(ctx, memory.MemoryEntry{
			ID: fmt.Sprintf("drain-%d", i), UserID: "u1", Content: "x", Timestamp: time.Now(),
		})
	}
	// Close 应排空队列
	a.Close()

	// 重新打开验证数据已落盘
	sq2, _ := NewSQLiteStore(path, 100)
	defer sq2.Close()
	entries, _ := sq2.RecentMemories(ctx, "u1", 100)
	if len(entries) != 20 {
		t.Errorf("expected 20 entries after Close drain, got %d", len(entries))
	}
}

func TestAsyncSQLite_Inner(t *testing.T) {
	a := newTestAsyncSQLite(t)
	if a.Inner() == nil {
		t.Error("Inner() should not be nil")
	}
}
