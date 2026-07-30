package trace

import (
	"testing"
)

// ============================================================
// NewTracer tests
// ============================================================

func TestNewTracer(t *testing.T) {
	tr := NewTracer()
	if tr == nil {
		t.Fatal("NewTracer should not return nil")
	}
}

// ============================================================
// StartSpan / EndSpan tests
// ============================================================

func TestTracer_StartAndEndSpan(t *testing.T) {
	tr := NewTracer()
	span := tr.StartSpan("trace-1", "test-span", map[string]string{"key": "value"})

	if span == nil {
		t.Fatal("StartSpan should not return nil")
	}
	if span.TraceID != "trace-1" {
		t.Errorf("TraceID = %q, want trace-1", span.TraceID)
	}
	if span.Name != "test-span" {
		t.Errorf("Name = %q, want test-span", span.Name)
	}
	if span.Attrs["key"] != "value" {
		t.Errorf("Attrs = %v", span.Attrs)
	}

	tr.EndSpan(span, nil)
	spans := tr.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("GetSpans len = %d, want 1", len(spans))
	}
	if spans[0].Status != "ok" {
		t.Errorf("Status = %q, want ok", spans[0].Status)
	}
}

func TestTracer_EndSpan_WithError(t *testing.T) {
	tr := NewTracer()
	span := tr.StartSpan("trace-1", "errored-span", nil)
	tr.EndSpan(span, &testError{})

	spans := tr.GetSpans()
	if spans[0].Status != "error" {
		t.Errorf("Status = %q, want error", spans[0].Status)
	}
}

// ============================================================
// GetSpans tests
// ============================================================

func TestTracer_GetSpans_Empty(t *testing.T) {
	tr := NewTracer()
	spans := tr.GetSpans()
	if len(spans) != 0 {
		t.Errorf("empty tracer should have 0 spans, got %d", len(spans))
	}
}

func TestTracer_GetSpans_Multiple(t *testing.T) {
	tr := NewTracer()
	s1 := tr.StartSpan("t1", "span1", nil)
	s2 := tr.StartSpan("t2", "span2", nil)
	tr.EndSpan(s1, nil)
	tr.EndSpan(s2, nil)

	spans := tr.GetSpans()
	if len(spans) != 2 {
		t.Errorf("should have 2 spans, got %d", len(spans))
	}
}

// ============================================================
// Reset tests
// ============================================================

func TestTracer_Reset(t *testing.T) {
	tr := NewTracer()
	span := tr.StartSpan("t", "s", nil)
	tr.EndSpan(span, nil)
	tr.Reset()

	spans := tr.GetSpans()
	if len(spans) != 0 {
		t.Errorf("Reset should clear all spans, got %d", len(spans))
	}
}

func TestTracer_SpanID_Increment(t *testing.T) {
	tr := NewTracer()
	s1 := tr.StartSpan("t", "s1", nil)
	s2 := tr.StartSpan("t", "s2", nil)

	if s1.SpanID == s2.SpanID {
		t.Error("SpanIDs should be unique")
	}
	tr.EndSpan(s1, nil)
	tr.EndSpan(s2, nil)
}

// ============================================================
// EndSpan error test (using a mock error)
// ============================================================

type testError struct{}

func (e *testError) Error() string { return "test error" }

func TestTracer_EndSpan_WithErrorInterface(t *testing.T) {
	tr := NewTracer()
	span := tr.StartSpan("trace-err", "error-span", nil)
	tr.EndSpan(span, &testError{})

	spans := tr.GetSpans()
	if spans[0].Status != "error" {
		t.Errorf("Status = %q, want error", spans[0].Status)
	}
	if spans[0].EndTime.IsZero() {
		t.Error("EndTime should be set")
	}
}
