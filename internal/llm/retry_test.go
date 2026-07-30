package llm

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// ============================================================
// ClassifyError tests
// ============================================================

func TestClassifyError_Nil(t *testing.T) {
	if cat := ClassifyError(nil); cat != CategoryNonRetryable {
		t.Errorf("nil error should be CategoryNonRetryable, got %v", cat)
	}
}

func TestClassifyError_APIError_Retryable(t *testing.T) {
	err := &APIError{StatusCode: 500, Retryable: true, Provider: "test"}
	if cat := ClassifyError(err); cat != CategoryRetryable {
		t.Errorf("500 Retryable should be CategoryRetryable, got %v", cat)
	}
}

func TestClassifyError_APIError_RateLimited(t *testing.T) {
	err := &APIError{StatusCode: http.StatusTooManyRequests, Retryable: true, Provider: "test"}
	if cat := ClassifyError(err); cat != CategoryRateLimited {
		t.Errorf("429 should be CategoryRateLimited, got %v", cat)
	}
}

func TestClassifyError_APIError_NonRetryable(t *testing.T) {
	err := &APIError{StatusCode: 400, Retryable: false, Provider: "test"}
	if cat := ClassifyError(err); cat != CategoryNonRetryable {
		t.Errorf("400 NonRetryable should be CategoryNonRetryable, got %v", cat)
	}
}

func TestClassifyError_ConnectionRefused(t *testing.T) {
	err := errors.New("dial tcp: connection refused")
	if cat := ClassifyError(err); cat != CategoryRetryable {
		t.Errorf("%q should be CategoryRetryable, got %v", err, cat)
	}
}

func TestClassifyError_ContextDeadlineExceeded(t *testing.T) {
	err := errors.New("context deadline exceeded")
	if cat := ClassifyError(err); cat != CategoryNonRetryable {
		t.Errorf("%q should be CategoryNonRetryable, got %v", err, cat)
	}
}

func TestClassifyError_ContextCanceled(t *testing.T) {
	err := errors.New("context canceled")
	if cat := ClassifyError(err); cat != CategoryNonRetryable {
		t.Errorf("%q should be CategoryNonRetryable, got %v", err, cat)
	}
}

func TestClassifyError_Status502(t *testing.T) {
	err := errors.New("status=502 Bad Gateway")
	if cat := ClassifyError(err); cat != CategoryRetryable {
		t.Errorf("%q should be CategoryRetryable, got %v", err, cat)
	}
}

func TestClassifyError_Status403(t *testing.T) {
	err := errors.New("status=403 Forbidden")
	if cat := ClassifyError(err); cat != CategoryNonRetryable {
		t.Errorf("%q should be CategoryNonRetryable, got %v", err, cat)
	}
}

func TestClassifyError_Status429InString(t *testing.T) {
	err := errors.New("status=429 Too Many Requests")
	if cat := ClassifyError(err); cat != CategoryRateLimited {
		t.Errorf("%q should be CategoryRateLimited, got %v", err, cat)
	}
}

func TestClassifyError_Unknown(t *testing.T) {
	err := errors.New("something completely unexpected")
	if cat := ClassifyError(err); cat != CategoryUnknown {
		t.Errorf("unknown error should be CategoryUnknown, got %v", cat)
	}
}

// ============================================================
// RetryPolicy tests
// ============================================================

func TestDefaultRetryPolicy(t *testing.T) {
	p := DefaultRetryPolicy()
	if p.MaxRetries != 3 {
		t.Errorf("MaxRetries = %d, want 3", p.MaxRetries)
	}
	if p.BaseBackoff != 500*time.Millisecond {
		t.Errorf("BaseBackoff = %v, want 500ms", p.BaseBackoff)
	}
	if p.MaxBackoff != 30*time.Second {
		t.Errorf("MaxBackoff = %v, want 30s", p.MaxBackoff)
	}
	if p.BackoffMultiplier != 2.0 {
		t.Errorf("BackoffMultiplier = %f, want 2.0", p.BackoffMultiplier)
	}
	if p.JitterFactor != 0.2 {
		t.Errorf("JitterFactor = %f, want 0.2", p.JitterFactor)
	}
	if !p.RetryOnUnknown {
		t.Error("RetryOnUnknown should be true")
	}
	if p.MaxTotalTimeout != 2*time.Minute {
		t.Errorf("MaxTotalTimeout = %v, want 2m", p.MaxTotalTimeout)
	}
}

func TestCalculateBackoff_WithRetryAfter(t *testing.T) {
	p := DefaultRetryPolicy()
	ra := 10 * time.Second
	result := p.CalculateBackoff(1, ra)
	if result != ra {
		t.Errorf("CalculateBackoff with retryAfter=%v got %v", ra, result)
	}
}

func TestCalculateBackoff_FirstAttempt(t *testing.T) {
	p := DefaultRetryPolicy()
	p.JitterFactor = 0 // disable jitter for deterministic test
	result := p.CalculateBackoff(1, 0)
	expected := time.Duration(float64(p.BaseBackoff) * 1.0)
	if result != expected {
		t.Errorf("first attempt = %v, want %v", result, expected)
	}
}

func TestCalculateBackoff_ExponentialGrowth(t *testing.T) {
	p := DefaultRetryPolicy()
	p.JitterFactor = 0
	// attempt 3: base * 2^(3-1) = 500ms * 4 = 2s
	result := p.CalculateBackoff(3, 0)
	expected := time.Duration(500 * 4 * float64(time.Millisecond))
	if result != expected {
		t.Errorf("attempt 3 = %v, want %v", result, expected)
	}
}

func TestCalculateBackoff_MaxCapped(t *testing.T) {
	p := DefaultRetryPolicy()
	p.JitterFactor = 0
	// attempt 20: base * 2^19 would exceed max, should cap
	result := p.CalculateBackoff(20, 0)
	if result > p.MaxBackoff {
		t.Errorf("CalculateBackoff should cap at MaxBackoff=%v, got %v", p.MaxBackoff, result)
	}
}

func TestCalculateBackoff_WithJitter(t *testing.T) {
	p := DefaultRetryPolicy()
	p.JitterFactor = 1.0
	result := p.CalculateBackoff(1, 0)
	// with jitter=1, the result should be in range [0, 2*base]
	maxNoJitter := float64(p.BaseBackoff) * 2.0
	if float64(result) > maxNoJitter {
		t.Errorf("CalculateBackoff with jitter=1 at max: got %v > %v", result, maxNoJitter)
	}
}

func TestShouldRetry_AttemptExceedsMax(t *testing.T) {
	p := RetryPolicy{MaxRetries: 3, RetryOnUnknown: true}
	err := errors.New("connection refused")
	if p.ShouldRetry(err, 3) {
		t.Error("ShouldRetry should be false when attempt >= MaxRetries")
	}
}

func TestShouldRetry_RetryableError(t *testing.T) {
	p := RetryPolicy{MaxRetries: 3, RetryOnUnknown: true}
	err := errors.New("connection refused")
	if !p.ShouldRetry(err, 1) {
		t.Error("ShouldRetry should be true for retryable error")
	}
}

func TestShouldRetry_NonRetryableError(t *testing.T) {
	p := RetryPolicy{MaxRetries: 3, RetryOnUnknown: true}
	err := &APIError{StatusCode: 400, Retryable: false}
	if p.ShouldRetry(err, 1) {
		t.Error("ShouldRetry should be false for non-retryable error")
	}
}

func TestShouldRetry_UnknownError_RetryOn(t *testing.T) {
	p := RetryPolicy{MaxRetries: 3, RetryOnUnknown: true}
	err := errors.New("mysterious error")
	if !p.ShouldRetry(err, 1) {
		t.Error("ShouldRetry should be true for unknown error when RetryOnUnknown=true")
	}
}

func TestShouldRetry_UnknownError_RetryOff(t *testing.T) {
	p := RetryPolicy{MaxRetries: 3, RetryOnUnknown: false}
	err := errors.New("mysterious error")
	if p.ShouldRetry(err, 1) {
		t.Error("ShouldRetry should be false for unknown error when RetryOnUnknown=false")
	}
}

// ============================================================
// APIError tests
// ============================================================

func TestAPIError_Error(t *testing.T) {
	err := &APIError{Provider: "deepseek", StatusCode: 500, Message: "internal error"}
	expected := "[deepseek] status=500: internal error"
	if err.Error() != expected {
		t.Errorf("Error() = %q, want %q", err.Error(), expected)
	}
}

func TestNewAPIError_RateLimited(t *testing.T) {
	err := NewAPIError("test", http.StatusTooManyRequests, "rate limited", "120")
	if !err.Retryable {
		t.Error("429 should be Retryable")
	}
	if err.RetryAfter != 120*time.Second {
		t.Errorf("RetryAfter = %v, want 120s", err.RetryAfter)
	}
}

func TestNewAPIError_500(t *testing.T) {
	err := NewAPIError("test", 500, "server error", "")
	if !err.Retryable {
		t.Error("500 should be Retryable")
	}
}

func TestNewAPIError_400(t *testing.T) {
	err := NewAPIError("test", 400, "bad request", "")
	if err.Retryable {
		t.Error("400 should not be Retryable")
	}
}

func TestNewAPIError_LongBodyTruncation(t *testing.T) {
	longBody := ""
	for i := 0; i < 300; i++ {
		longBody += "x"
	}
	err := NewAPIError("test", 500, longBody, "")
	if len(err.Message) > 256+3 { // 256 chars + "..."
		t.Errorf("long body should be truncated, got %d chars", len(err.Message))
	}
	if len(err.Message) != 256+3 {
		t.Errorf("expected truncated length 259, got %d", len(err.Message))
	}
}

func TestNewAPIError_ShortBody(t *testing.T) {
	err := NewAPIError("test", 500, "short", "")
	if err.Message != "short" {
		t.Errorf("short body should not be truncated, got %q", err.Message)
	}
}

// ============================================================
// parseRetryAfter tests
// ============================================================

func TestParseRetryAfter_Empty(t *testing.T) {
	if d := parseRetryAfter(""); d != 0 {
		t.Errorf("empty header should return 0, got %v", d)
	}
}

func TestParseRetryAfter_Seconds(t *testing.T) {
	if d := parseRetryAfter("120"); d != 120*time.Second {
		t.Errorf("120 should return 120s, got %v", d)
	}
}

func TestParseRetryAfter_ZeroSeconds(t *testing.T) {
	if d := parseRetryAfter("0"); d != 5*time.Second {
		t.Errorf("0 seconds should fallback to 5s, got %v", d)
	}
}

func TestParseRetryAfter_InvalidFormat(t *testing.T) {
	if d := parseRetryAfter("not-a-number"); d != 5*time.Second {
		t.Errorf("invalid format should fallback to 5s, got %v", d)
	}
}

func TestParseRetryAfter_HTTPDate_Future(t *testing.T) {
	future := time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat)
	d := parseRetryAfter(future)
	if d <= 0 {
		t.Errorf("future HTTP date should return positive duration, got %v", d)
	}
}

func TestParseRetryAfter_HTTPDate_Past(t *testing.T) {
	past := time.Now().Add(-1 * time.Hour).UTC().Format(http.TimeFormat)
	d := parseRetryAfter(past)
	if d != 5*time.Second {
		t.Errorf("past HTTP date should fallback to 5s, got %v", d)
	}
}

// ============================================================
// truncate tests
// ============================================================

func TestTruncate_Short(t *testing.T) {
	result := truncate("hello", 10)
	if result != "hello" {
		t.Errorf("truncate short = %q, want %q", result, "hello")
	}
}

func TestTruncate_Exact(t *testing.T) {
	result := truncate("hello", 5)
	if result != "hello" {
		t.Errorf("truncate exact = %q, want %q", result, "hello")
	}
}

func TestTruncate_Long(t *testing.T) {
	result := truncate("hello world this is long", 10)
	if result != "hello worl..." {
		t.Errorf("truncate long = %q, want %q", result, "hello worl...")
	}
}

func TestTruncate_Empty(t *testing.T) {
	result := truncate("", 10)
	if result != "" {
		t.Errorf("truncate empty = %q, want %q", result, "")
	}
}

// ============================================================
// CircuitBreaker tests
// ============================================================

func TestCircuitBreaker_InitialState(t *testing.T) {
	cb := NewCircuitBreaker(DefaultCircuitBreakerConfig())
	if cb.State() != CircuitClosed {
		t.Error("initial state should be CircuitClosed")
	}
	if !cb.Allow() {
		t.Error("Allow should return true when closed")
	}
}

func TestCircuitBreaker_SuccessKeepsClosed(t *testing.T) {
	cb := NewCircuitBreaker(DefaultCircuitBreakerConfig())
	cb.RecordSuccess()
	if cb.State() != CircuitClosed {
		t.Error("state should remain Closed after success")
	}
}

func TestCircuitBreaker_OpensAfterFailures(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{
		FailureThreshold: 3,
		SuccessThreshold: 2,
		OpenTimeout:      60 * time.Second,
	})
	for i := 0; i < 3; i++ {
		cb.RecordFailure()
	}
	if cb.State() != CircuitOpen {
		t.Error("state should be Open after threshold failures")
	}
	if cb.Allow() {
		t.Error("Allow should be false when open")
	}
}

func TestCircuitBreaker_HalfOpenAfterTimeout(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{
		FailureThreshold: 1,
		SuccessThreshold: 2,
		OpenTimeout:      1 * time.Nanosecond,
	})
	cb.RecordFailure() // open
	// OpenTimeout is 1ns, so it should immediately allow half-open
	if !cb.Allow() {
		t.Error("Allow should be true after OpenTimeout (half-open)")
	}
}

func TestCircuitBreaker_HalfOpenSuccessFullyCloses(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{
		FailureThreshold: 1,
		SuccessThreshold: 2,
		OpenTimeout:      1 * time.Nanosecond,
	})
	cb.RecordFailure() // open
	cb.Allow()         // transition to half-open
	cb.RecordSuccess()
	if cb.State() != CircuitHalfOpen {
		t.Error("first success in half-open should keep it half-open")
	}
	cb.RecordSuccess()
	if cb.State() != CircuitClosed {
		t.Error("second success should close the circuit")
	}
}

func TestCircuitBreaker_HalfOpenFailureReopens(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{
		FailureThreshold: 1,
		SuccessThreshold: 2,
		OpenTimeout:      1 * time.Nanosecond,
	})
	cb.RecordFailure() // open
	cb.Allow()         // transition to half-open
	cb.RecordFailure() // failure should reopen
	if cb.State() != CircuitOpen {
		t.Error("failure in half-open should reopen")
	}
}

func TestCircuitBreaker_String(t *testing.T) {
	tests := []struct {
		state    CircuitState
		expected string
	}{
		{CircuitClosed, "closed"},
		{CircuitOpen, "open"},
		{CircuitHalfOpen, "half-open"},
		{CircuitState(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.state.String(); got != tt.expected {
			t.Errorf("CircuitState(%d).String() = %q, want %q", tt.state, got, tt.expected)
		}
	}
}

func TestDefaultCircuitBreakerConfig(t *testing.T) {
	cfg := DefaultCircuitBreakerConfig()
	if cfg.FailureThreshold != 5 {
		t.Errorf("FailureThreshold = %d, want 5", cfg.FailureThreshold)
	}
	if cfg.SuccessThreshold != 2 {
		t.Errorf("SuccessThreshold = %d, want 2", cfg.SuccessThreshold)
	}
	if cfg.OpenTimeout != 60*time.Second {
		t.Errorf("OpenTimeout = %v, want 60s", cfg.OpenTimeout)
	}
}

// ============================================================
// Category constants test
// ============================================================

func TestErrorCategory_Values(t *testing.T) {
	// Ensure constants are distinct
	cats := []ErrorCategory{CategoryRetryable, CategoryRateLimited, CategoryNonRetryable, CategoryUnknown}
	seen := make(map[ErrorCategory]bool)
	for _, c := range cats {
		if seen[c] {
			t.Errorf("duplicate ErrorCategory value: %d", c)
		}
		seen[c] = true
	}
}

// ============================================================
// Edge case: retryable patterns
// ============================================================

func TestClassifyError_AllRetryablePatterns(t *testing.T) {
	patterns := []string{
		"connection refused",
		"connection reset",
		"connection timed out",
		"i/o timeout",
		"no such host",
		"broken pipe",
		"network is unreachable",
		"temporary failure",
	}
	for _, p := range patterns {
		t.Run(fmt.Sprintf("pattern=%s", p), func(t *testing.T) {
			err := errors.New(p)
			if cat := ClassifyError(err); cat != CategoryRetryable {
				t.Errorf("%q should be CategoryRetryable, got %v", p, cat)
			}
		})
	}
}
