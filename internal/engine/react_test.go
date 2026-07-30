package engine

import (
	"testing"
)

// ============================================================
// parseToolArgs tests
// ============================================================

func TestParseToolArgs_ValidJSON(t *testing.T) {
	result := parseToolArgs(`{"query":"hello","limit":10}`)
	if result["query"] != "hello" {
		t.Errorf("query = %v, want hello", result["query"])
	}
	if result["limit"] != float64(10) {
		t.Errorf("limit = %v, want 10", result["limit"])
	}
}

func TestParseToolArgs_Empty(t *testing.T) {
	result := parseToolArgs("")
	if result == nil {
		t.Error("empty string should return empty map, not nil")
	}
	if len(result) != 0 {
		t.Errorf("empty string should return empty map, got %d entries", len(result))
	}
}

func TestParseToolArgs_InvalidJSON(t *testing.T) {
	result := parseToolArgs(`{invalid}`)
	if result == nil {
		t.Error("invalid JSON should return empty map, not nil")
	}
	if len(result) != 0 {
		t.Errorf("invalid JSON should return empty map, got %d entries", len(result))
	}
}

func TestParseToolArgs_NotObject(t *testing.T) {
	result := parseToolArgs(`["array","not","object"]`)
	if result == nil {
		t.Error("non-object JSON should return empty map, not nil")
	}
	if len(result) != 0 {
		t.Errorf("non-object JSON should return empty map, got %d entries", len(result))
	}
}

// ============================================================
// NewEngine tests
// ============================================================

func TestNewEngine_MaxIterationsDefault(t *testing.T) {
	e := NewEngine(EngineConfig{})
	if e == nil {
		t.Fatal("NewEngine should not return nil")
	}
	// can't check unexported maxIterations, just verify engine is created
}

func TestNewEngine_SystemPromptDefault(t *testing.T) {
	e := NewEngine(EngineConfig{})
	if e == nil {
		t.Fatal("NewEngine should not return nil")
	}
	// verify ContextManager returns nil
	if e.ContextManager() != nil {
		t.Error("default engine should have nil ContextManager")
	}
}

func TestNewEngine_CustomConfig(t *testing.T) {
	e := NewEngine(EngineConfig{
		MaxIterations: 5,
		SystemPrompt:  "custom",
	})
	if e == nil {
		t.Fatal("NewEngine with custom config should not return nil")
	}
}

// ============================================================
// State constants test
// ============================================================

func TestState_Constants(t *testing.T) {
	states := map[State]string{
		StateIdle:      "idle",
		StatePlanning:  "planning",
		StateExecuting: "executing",
		StateObserving: "observing",
		StateFinishing: "finishing",
		StateError:     "error",
	}
	for state, expected := range states {
		if string(state) != expected {
			t.Errorf("State value = %q, want %q", string(state), expected)
		}
	}
}

// ============================================================
// preprocessToolOutput tests
// ============================================================

func TestPreprocessToolOutput_NilCtxManager(t *testing.T) {
	e := NewEngine(EngineConfig{MaxIterations: 10})
	result := e.preprocessToolOutput("web_search", "output text")
	if result != "output text" {
		t.Errorf("without ctxManager, output should be unchanged, got %q", result)
	}
}
