package ctxmgr

import (
	"testing"
)

// ============================================================
// isCJK tests
// ============================================================

func TestIsCJK_Chinese(t *testing.T) {
	if !isCJK('你') {
		t.Error("Chinese character should be CJK")
	}
	if !isCJK('中') {
		t.Error("Chinese character should be CJK")
	}
}

func TestIsCJK_Japanese(t *testing.T) {
	if !isCJK('あ') {
		t.Error("Hiragana should be CJK")
	}
	if !isCJK('カ') {
		t.Error("Katakana should be CJK")
	}
}

func TestIsCJK_Korean(t *testing.T) {
	if !isCJK('한') {
		t.Error("Hangul should be CJK")
	}
}

func TestIsCJK_Latin(t *testing.T) {
	if isCJK('a') {
		t.Error("Latin should not be CJK")
	}
	if isCJK('Z') {
		t.Error("Uppercase Latin should not be CJK")
	}
}

func TestIsCJK_Digit(t *testing.T) {
	if isCJK('0') {
		t.Error("Digit should not be CJK")
	}
}

// ============================================================
// CharCount tests
// ============================================================

func TestCharCount_Empty(t *testing.T) {
	if CharCount("") != 0 {
		t.Error("empty string should have 0 chars")
	}
}

func TestCharCount_ASCII(t *testing.T) {
	if CharCount("hello") != 5 {
		t.Errorf("hello = %d, want 5", CharCount("hello"))
	}
}

func TestCharCount_CJK(t *testing.T) {
	if CharCount("你好世界") != 4 {
		t.Errorf("你好世界 = %d, want 4", CharCount("你好世界"))
	}
}

func TestCharCount_Mixed(t *testing.T) {
	if CharCount("hello你好") != 7 {
		t.Errorf("hello你好 = %d, want 7", CharCount("hello你好"))
	}
}

// ============================================================
// itoa tests
// ============================================================

func TestItoa_Zero(t *testing.T) {
	if itoa(0) != "0" {
		t.Errorf("itoa(0) = %q", itoa(0))
	}
}

func TestItoa_Positive(t *testing.T) {
	if itoa(42) != "42" {
		t.Errorf("itoa(42) = %q", itoa(42))
	}
}

func TestItoa_Negative(t *testing.T) {
	if itoa(-1) != "-1" {
		t.Errorf("itoa(-1) = %q", itoa(-1))
	}
}

func TestItoa_Large(t *testing.T) {
	if itoa(123456789) != "123456789" {
		t.Errorf("itoa(123456789) = %q", itoa(123456789))
	}
}

// ============================================================
// TokenEstimator tests
// ============================================================

func TestTokenEstimator_EstimateText_Empty(t *testing.T) {
	est := DefaultEstimator()
	if est.EstimateText("") != 0 {
		t.Error("empty text should have 0 tokens")
	}
}

func TestTokenEstimator_EstimateText_ASCII(t *testing.T) {
	est := DefaultEstimator()
	result := est.EstimateText("hello world this is a test")
	if result <= 0 {
		t.Error("ASCII text should have positive tokens")
	}
}

func TestTokenEstimator_EstimateText_CJK(t *testing.T) {
	est := DefaultEstimator()
	asciiResult := est.EstimateText("hello")
	cjkResult := est.EstimateText("你好")
	if cjkResult <= asciiResult {
		t.Errorf("CJK text should have more tokens per char than ASCII: ascii=%d, cjk=%d", asciiResult, cjkResult)
	}
}

func TestTokenEstimator_AlwaysAtLeastOne(t *testing.T) {
	est := DefaultEstimator()
	result := est.EstimateText("a")
	if result < 1 {
		t.Errorf("even short text should estimate at least 1 token, got %d", result)
	}
}

func TestTokenEstimator_EstimateMessages(t *testing.T) {
	est := DefaultEstimator()
	result := est.EstimateMessages([]Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi there"},
	})
	if result <= 8 {
		t.Errorf("messages estimate should be > 8 (overhead), got %d", result)
	}
}

func TestTokenEstimator_EstimateMessages_Empty(t *testing.T) {
	est := DefaultEstimator()
	if est.EstimateMessages([]Message{}) != 0 {
		t.Error("empty messages should have 0 tokens")
	}
}

// ============================================================
// TruncateText tests
// ============================================================

func TestTruncateText_Short(t *testing.T) {
	est := DefaultEstimator()
	result := TruncateText("short text", 100, est)
	if result != "short text" {
		t.Errorf("short text should not be truncated, got %q", result)
	}
}

func TestTruncateText_Long(t *testing.T) {
	est := DefaultEstimator()
	longText := ""
	for i := 0; i < 500; i++ {
		longText += "word "
	}
	result := TruncateText(longText, 10, est)
	if result == longText {
		t.Error("long text should be truncated")
	}
}

func TestTruncateText_Empty(t *testing.T) {
	est := DefaultEstimator()
	result := TruncateText("", 10, est)
	if result != "" {
		t.Errorf("empty text should remain empty, got %q", result)
	}
}

// ============================================================
// TruncateHead tests
// ============================================================

func TestTruncateHead_Short(t *testing.T) {
	est := DefaultEstimator()
	result := TruncateHead("short", 100, est)
	if result != "short" {
		t.Errorf("short text should not be truncated, got %q", result)
	}
}

func TestTruncateHead_Long(t *testing.T) {
	est := DefaultEstimator()
	longText := ""
	for i := 0; i < 500; i++ {
		longText += "word "
	}
	result := TruncateHead(longText, 10, est)
	if result == longText {
		t.Error("long text should be truncated")
	}
}

// ============================================================
// SummarizeKeyPoints tests
// ============================================================

func TestSummarizeKeyPoints_Empty(t *testing.T) {
	result := SummarizeKeyPoints("", 5)
	if result != "" {
		t.Errorf("empty text should return empty, got %q", result)
	}
}

func TestSummarizeKeyPoints_Short(t *testing.T) {
	result := SummarizeKeyPoints("short\ntext", 10)
	if result != "short\ntext" {
		t.Errorf("short text should be returned as-is, got %q", result)
	}
}

// ============================================================
// CompressionLevel String tests
// ============================================================

func TestCompressionLevel_String(t *testing.T) {
	tests := []struct {
		level    CompressionLevel
		expected string
	}{
		{LevelNone, "none"},
		{LevelTruncate, "truncate"},
		{LevelSummarize, "summarize"},
		{LevelEvict, "evict"},
	}
	for _, tt := range tests {
		if got := tt.level.String(); got != tt.expected {
			t.Errorf("CompressionLevel(%d).String() = %q, want %q", tt.level, got, tt.expected)
		}
	}
}

// ============================================================
// DefaultConfig tests
// ============================================================

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.MaxContextTokens <= 0 {
		t.Error("MaxContextTokens should be positive")
	}
	if cfg.CompressionThreshold <= 0 || cfg.CompressionThreshold >= 1 {
		t.Error("CompressionThreshold should be between 0 and 1")
	}
}
