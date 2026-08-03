package llm

import "testing"

func TestMeterRecordUsage_CachedTokens(t *testing.T) {
	m := NewMeter(0)

	// 带 prompt_tokens_details 的用量：1000 输入 token 中 800 命中缓存
	m.RecordUsage(UsageExecution, "mimo", Usage{
		PromptTokens:     1000,
		CompletionTokens: 200,
		TotalTokens:      1200,
		PromptDetails:    PromptTokenDetails{CachedTokens: 800},
	})
	// 不带缓存明细的用量
	m.RecordUsage(UsagePlanning, "deepseek", Usage{
		PromptTokens:     500,
		CompletionTokens: 100,
		TotalTokens:      600,
	})

	s := m.Summary()
	if s.TotalCachedTokens != 800 {
		t.Errorf("TotalCachedTokens = %d, want 800", s.TotalCachedTokens)
	}
	if s.TotalPromptTokens != 1500 {
		t.Errorf("TotalPromptTokens = %d, want 1500", s.TotalPromptTokens)
	}

	wantRatio := float64(800) / float64(1500)
	if got := s.CacheHitRatio(); got != wantRatio {
		t.Errorf("CacheHitRatio = %v, want %v", got, wantRatio)
	}
}

func TestMeterRecordUsage_ZeroPrompt(t *testing.T) {
	m := NewMeter(0)
	s := m.Summary()
	if got := s.CacheHitRatio(); got != 0 {
		t.Errorf("无输入时 CacheHitRatio = %v, want 0", got)
	}
}
