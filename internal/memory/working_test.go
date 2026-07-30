package memory

import (
	"context"
	"testing"
)

func TestNewWorkingMemory_PositiveSize(t *testing.T) {
	wm := NewWorkingMemory(50)
	if wm.maxSize != 50 {
		t.Errorf("maxSize = %d, want 50", wm.maxSize)
	}
}

func TestNewWorkingMemory_ZeroSize(t *testing.T) {
	wm := NewWorkingMemory(0)
	if wm.maxSize != 100 {
		t.Errorf("maxSize = %d, want 100 (default)", wm.maxSize)
	}
}

func TestNewWorkingMemory_NegativeSize(t *testing.T) {
	wm := NewWorkingMemory(-5)
	if wm.maxSize != 100 {
		t.Errorf("maxSize = %d, want 100 (default)", wm.maxSize)
	}
}

func TestWorkingMemory_StoreAndGetRecent(t *testing.T) {
	wm := NewWorkingMemory(10)
	ctx := context.Background()

	wm.Store(ctx, MemoryEntry{Content: "entry1"})
	wm.Store(ctx, MemoryEntry{Content: "entry2"})
	wm.Store(ctx, MemoryEntry{Content: "entry3"})

	entries, err := wm.GetRecent(ctx, 2)
	if err != nil {
		t.Fatalf("GetRecent: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("GetRecent len = %d, want 2", len(entries))
	}
	// newest first
	if entries[0].Content != "entry3" || entries[1].Content != "entry2" {
		t.Error("GetRecent should return newest first")
	}
}

func TestWorkingMemory_GetRecent_LimitZero(t *testing.T) {
	wm := NewWorkingMemory(10)
	ctx := context.Background()
	wm.Store(ctx, MemoryEntry{Content: "a"})
	wm.Store(ctx, MemoryEntry{Content: "b"})

	entries, _ := wm.GetRecent(ctx, 0)
	if len(entries) != 2 { // default 10, but only 2 entries
		t.Errorf("GetRecent with limit=0 should use default, got %d", len(entries))
	}
}

func TestWorkingMemory_GetRecent_Empty(t *testing.T) {
	wm := NewWorkingMemory(10)
	entries, err := wm.GetRecent(context.Background(), 5)
	if err != nil {
		t.Fatalf("GetRecent: %v", err)
	}
	if entries != nil {
		t.Error("GetRecent on empty should return nil")
	}
}

func TestWorkingMemory_Store_AutoGeneratesID(t *testing.T) {
	wm := NewWorkingMemory(10)
	ctx := context.Background()
	wm.Store(ctx, MemoryEntry{Content: "test"})
	entries, _ := wm.GetRecent(ctx, 1)
	if entries[0].ID == "" {
		t.Error("Store should auto-generate ID")
	}
}

func TestWorkingMemory_Store_AutoTimestamp(t *testing.T) {
	wm := NewWorkingMemory(10)
	ctx := context.Background()
	wm.Store(ctx, MemoryEntry{Content: "test"})
	entries, _ := wm.GetRecent(ctx, 1)
	if entries[0].Timestamp.IsZero() {
		t.Error("Store should auto-set timestamp")
	}
}

func TestWorkingMemory_Overflow(t *testing.T) {
	wm := NewWorkingMemory(3)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		wm.Store(ctx, MemoryEntry{Content: "entry"})
	}

	entries, _ := wm.GetRecent(ctx, 10)
	if len(entries) != 3 {
		t.Errorf("overflow should keep maxSize entries, got %d", len(entries))
	}
}

func TestWorkingMemory_Search(t *testing.T) {
	wm := NewWorkingMemory(10)
	ctx := context.Background()

	wm.Store(ctx, MemoryEntry{Content: "learn about Go", Tags: []string{"programming"}})
	wm.Store(ctx, MemoryEntry{Content: "read about Python", Tags: []string{"programming"}})
	wm.Store(ctx, MemoryEntry{Content: "cooking recipe"})

	results, err := wm.Search(ctx, "go programming", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Error("Search should find matching entries")
	}
	// "learn about Go" matches "go" in content and "programming" in tags -> score 2
	// "read about Python" matches "programming" in tags only -> score 1
	if len(results) >= 2 && results[0].Content != "learn about Go" {
		t.Errorf("highest score should be 'learn about Go', got %q", results[0].Content)
	}
}

func TestWorkingMemory_Search_EmptyQuery(t *testing.T) {
	wm := NewWorkingMemory(10)
	ctx := context.Background()
	wm.Store(ctx, MemoryEntry{Content: "test"})

	results, err := wm.Search(ctx, "", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if results != nil {
		t.Error("Search with empty query should return nil")
	}
}

func TestWorkingMemory_Search_TopK(t *testing.T) {
	wm := NewWorkingMemory(10)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		wm.Store(ctx, MemoryEntry{Content: "test match"})
	}

	results, err := wm.Search(ctx, "test", 2)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("Search topK=2 should return 2 results, got %d", len(results))
	}
}

func TestWorkingMemory_Search_TopK_Zero(t *testing.T) {
	wm := NewWorkingMemory(10)
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		wm.Store(ctx, MemoryEntry{Content: "test"})
	}

	results, _ := wm.Search(ctx, "test", 0)
	if len(results) > 5 {
		t.Errorf("Search with topK=0 should default to 5, got %d", len(results))
	}
}

func TestWorkingMemory_Clear(t *testing.T) {
	wm := NewWorkingMemory(10)
	ctx := context.Background()
	wm.Store(ctx, MemoryEntry{Content: "test"})
	wm.Clear(ctx)

	entries, _ := wm.GetRecent(ctx, 10)
	if entries != nil {
		t.Error("Clear should empty all entries")
	}
}
