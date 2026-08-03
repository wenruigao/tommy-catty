// metering.go 实现分层 Token 用量计量与成本控制。
package llm

import (
	"sync"
	"time"
)

// UsageCategory Token 用途分类。
type UsageCategory string

const (
	UsagePlanning  UsageCategory = "planning"  // 规划
	UsageExecution UsageCategory = "execution" // 执行推理
	UsageMemory    UsageCategory = "memory"    // 记忆压缩/检索
	UsageSkill     UsageCategory = "skill"     // Skill 生成
	UsageSystem    UsageCategory = "system"    // 系统（健康检查等）
)

// TokenRecord 单次调用记录。
type TokenRecord struct {
	Category         UsageCategory
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CachedTokens     int // 命中提示缓存的 token 数（来自 Usage.PromptDetails）
	Model            string
	Timestamp        time.Time
}

// UsageSummary 用量汇总。
type UsageSummary struct {
	TotalPromptTokens     int            `json:"total_prompt_tokens"`
	TotalCompletionTokens int            `json:"total_completion_tokens"`
	TotalTokens           int            `json:"total_tokens"`
	TotalCachedTokens     int            `json:"total_cached_tokens"` // 累计命中提示缓存的 token 数
	ByCategory            map[string]int `json:"by_category"`
	ByModel               map[string]int `json:"by_model"`
	RequestCount          int            `json:"request_count"`
}

// CacheHitRatio 返回整体提示缓存命中率（0-1）。无输入时返回 0。
func (s UsageSummary) CacheHitRatio() float64 {
	if s.TotalPromptTokens <= 0 {
		return 0
	}
	return float64(s.TotalCachedTokens) / float64(s.TotalPromptTokens)
}

// Meter 分层 Token 计量器（并发安全）。
type Meter struct {
	mu      sync.Mutex
	records []TokenRecord
	// 预算控制
	maxDailyTokens int
	dailyReset     time.Time
	dailyTotal     int
}

// NewMeter 创建计量器。maxDailyTokens <= 0 表示不限。
func NewMeter(maxDailyTokens int) *Meter {
	return &Meter{
		records:        make([]TokenRecord, 0, 256),
		maxDailyTokens: maxDailyTokens,
		dailyReset:     time.Now().Truncate(24 * time.Hour),
	}
}

// Record 记录一次 Token 使用。
func (m *Meter) Record(rec TokenRecord) {
	m.mu.Lock()
	defer m.mu.Unlock()

	rec.Timestamp = time.Now()
	m.records = append(m.records, rec)
	m.dailyTotal += rec.TotalTokens

	// 保留最近 10000 条
	if len(m.records) > 10000 {
		m.records = m.records[len(m.records)-5000:]
	}
}

// RecordUsage 以 Usage 结构记录一次调用，透传提示缓存命中明细。
func (m *Meter) RecordUsage(category UsageCategory, model string, usage Usage) {
	m.Record(TokenRecord{
		Category:         category,
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		TotalTokens:      usage.TotalTokens,
		CachedTokens:     usage.PromptDetails.CachedTokens,
		Model:            model,
	})
}

// CheckBudget 检查是否超出日预算。返回 (已用, 上限, 是否超限)。
func (m *Meter) CheckBudget() (used, limit int, exceeded bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.resetDailyIfNeeded()
	if m.maxDailyTokens <= 0 {
		return m.dailyTotal, 0, false
	}
	return m.dailyTotal, m.maxDailyTokens, m.dailyTotal >= m.maxDailyTokens
}

// Summary 返回当前汇总。
func (m *Meter) Summary() UsageSummary {
	m.mu.Lock()
	defer m.mu.Unlock()

	s := UsageSummary{
		ByCategory: make(map[string]int),
		ByModel:    make(map[string]int),
	}
	for _, r := range m.records {
		s.TotalPromptTokens += r.PromptTokens
		s.TotalCompletionTokens += r.CompletionTokens
		s.TotalTokens += r.TotalTokens
		s.TotalCachedTokens += r.CachedTokens
		s.ByCategory[string(r.Category)] += r.TotalTokens
		s.ByModel[r.Model] += r.TotalTokens
		s.RequestCount++
	}
	return s
}

// resetDailyIfNeeded 跨日重置（调用方须持锁）。
func (m *Meter) resetDailyIfNeeded() {
	today := time.Now().Truncate(24 * time.Hour)
	if today.After(m.dailyReset) {
		m.dailyReset = today
		m.dailyTotal = 0
	}
}
