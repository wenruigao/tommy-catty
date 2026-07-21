// Package trace 提供 Agent 执行的全链路追踪能力
package trace

import (
	"fmt"
	"sync"
	"time"
)

// Span 表示一次追踪区间
type Span struct {
	TraceID   string            `json:"trace_id"`
	SpanID    string            `json:"span_id"`
	Name      string            `json:"name"`
	StartTime time.Time         `json:"start_time"`
	EndTime   time.Time         `json:"end_time"`
	Attrs     map[string]string `json:"attrs"`
	Status    string            `json:"status"` // "ok" | "error"
	Error     string            `json:"error,omitempty"`
}

// Tracer 追踪器，记录 Agent 执行的完整链路
type Tracer struct {
	mu    sync.Mutex
	spans []*Span
	seq   int
}

// NewTracer 创建追踪器
func NewTracer() *Tracer {
	return &Tracer{}
}

// StartSpan 开始一个新的追踪区间
func (t *Tracer) StartSpan(traceID, name string, attrs map[string]string) *Span {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.seq++
	s := &Span{
		TraceID:   traceID,
		SpanID:    fmt.Sprintf("%s-%d", traceID, t.seq),
		Name:      name,
		StartTime: time.Now(),
		Attrs:     attrs,
		Status:    "ok",
	}
	t.spans = append(t.spans, s)
	return s
}

// EndSpan 结束一个追踪区间
func (t *Tracer) EndSpan(s *Span, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s.EndTime = time.Now()
	if err != nil {
		s.Status = "error"
		s.Error = err.Error()
	}
}

// GetSpans 获取所有追踪区间
func (t *Tracer) GetSpans() []*Span {
	t.mu.Lock()
	defer t.mu.Unlock()
	result := make([]*Span, len(t.spans))
	copy(result, t.spans)
	return result
}

// Reset 清空追踪记录
func (t *Tracer) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.spans = nil
	t.seq = 0
}
