package memory

import (
	"testing"
	"time"
)

func TestDecayConfidence(t *testing.T) {
	now := time.Now()
	// 刚创建：置信度不变
	c := DecayConfidence(1.0, now, now)
	if c < 0.99 || c > 1.01 {
		t.Errorf("expected ~1.0, got %f", c)
	}
	// 30 天后：衰减到 0.5
	c = DecayConfidence(1.0, now.Add(-30*24*time.Hour), now)
	if c < 0.45 || c > 0.55 {
		t.Errorf("expected ~0.5 after 30 days, got %f", c)
	}
	// 60 天后：衰减到 0.25
	c = DecayConfidence(1.0, now.Add(-60*24*time.Hour), now)
	if c < 0.2 || c > 0.3 {
		t.Errorf("expected ~0.25 after 60 days, got %f", c)
	}
}

func TestResolveConflict(t *testing.T) {
	older := MemoryEntry{ID: "old", Content: "喜欢 A", Timestamp: time.Now().Add(-time.Hour)}
	newer := MemoryEntry{ID: "new", Content: "不喜欢 A", Timestamp: time.Now()}

	sorted, superseded := ResolveConflict([]MemoryEntry{older, newer})
	if sorted[0].ID != "new" {
		t.Errorf("expected newest first, got %s", sorted[0].ID)
	}
	if len(superseded) != 1 || superseded[0] != "old" {
		t.Errorf("expected old to be superseded, got %v", superseded)
	}
}

func TestIsSemanticConflict(t *testing.T) {
	a := MemoryEntry{Content: "I like apple banana orange grape as daily fruit"}
	b := MemoryEntry{Content: "I not like apple banana orange grape as daily fruit"}
	if !IsSemanticConflict(a, b) {
		t.Error("expected semantic conflict (negation difference)")
	}

	c := MemoryEntry{Content: "today weather good for walking outside park"}
	if IsSemanticConflict(a, c) {
		t.Error("should not detect conflict for unrelated content")
	}
}
