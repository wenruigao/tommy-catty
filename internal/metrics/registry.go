// Package metrics 实现轻量级指标采集与 Prometheus exposition format 输出。
// 不依赖 prometheus/client_golang，自行实现 Counter / Gauge / Vec / Registry。
// 通过 /metrics 端点暴露文本格式指标，供 Prometheus Server 抓取 → Grafana 展示。
package metrics

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Counter 只增计数器（调用数、Token 数、错误数）。
type Counter struct {
	mu    sync.Mutex
	value float64
}

// Add 增加计数值。负值忽略。
func (c *Counter) Add(v float64) {
	if v < 0 {
		return
	}
	c.mu.Lock()
	c.value += v
	c.mu.Unlock()
}

// Value 返回当前值。
func (c *Counter) Value() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.value
}

// Gauge 可增可减的瞬时值（活跃会话数、缓存大小、熔断器状态）。
type Gauge struct {
	mu    sync.Mutex
	value float64
}

// Set 设置 Gauge 值。
func (g *Gauge) Set(v float64) {
	g.mu.Lock()
	g.value = v
	g.mu.Unlock()
}

// Inc 增加 1。
func (g *Gauge) Inc() {
	g.mu.Lock()
	g.value++
	g.mu.Unlock()
}

// Dec 减少 1。
func (g *Gauge) Dec() {
	g.mu.Lock()
	g.value--
	g.mu.Unlock()
}

// Value 返回当前值。
func (g *Gauge) Value() float64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.value
}

// labelsKey 将 label map 转换为排序后的稳定字符串键。
func labelsKey(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for i, k := range keys {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(k)
		sb.WriteByte('=')
		sb.WriteString(labels[k])
	}
	return sb.String()
}

// CounterVec 带 label 维度的 Counter 集合。
type CounterVec struct {
	mu       sync.RWMutex
	counters map[string]*Counter
	labels   map[string]map[string]string // key → labels
}

// NewCounterVec 创建带 label 维度的 Counter。
func NewCounterVec() *CounterVec {
	return &CounterVec{
		counters: make(map[string]*Counter),
		labels:   make(map[string]map[string]string),
	}
}

// With 返回指定 label 组合的 Counter。
func (cv *CounterVec) With(labels map[string]string) *Counter {
	key := labelsKey(labels)
	cv.mu.RLock()
	c, ok := cv.counters[key]
	cv.mu.RUnlock()
	if ok {
		return c
	}
	cv.mu.Lock()
	defer cv.mu.Unlock()
	// double-check
	if c, ok = cv.counters[key]; ok {
		return c
	}
	c = &Counter{}
	cv.counters[key] = c
	cv.labels[key] = labels
	return c
}

// All 返回所有 Counter 及其 labels。
func (cv *CounterVec) All() map[string]struct {
	Counter *Counter
	Labels  map[string]string
} {
	cv.mu.RLock()
	defer cv.mu.RUnlock()
	result := make(map[string]struct {
		Counter *Counter
		Labels  map[string]string
	}, len(cv.counters))
	for k, c := range cv.counters {
		result[k] = struct {
			Counter *Counter
			Labels  map[string]string
		}{Counter: c, Labels: cv.labels[k]}
	}
	return result
}

// GaugeVec 带 label 维度的 Gauge 集合。
type GaugeVec struct {
	mu     sync.RWMutex
	gauges map[string]*Gauge
	labels map[string]map[string]string
}

// NewGaugeVec 创建带 label 维度的 Gauge。
func NewGaugeVec() *GaugeVec {
	return &GaugeVec{
		gauges: make(map[string]*Gauge),
		labels: make(map[string]map[string]string),
	}
}

// With 返回指定 label 组合的 Gauge。
func (gv *GaugeVec) With(labels map[string]string) *Gauge {
	key := labelsKey(labels)
	gv.mu.RLock()
	g, ok := gv.gauges[key]
	gv.mu.RUnlock()
	if ok {
		return g
	}
	gv.mu.Lock()
	defer gv.mu.Unlock()
	if g, ok = gv.gauges[key]; ok {
		return g
	}
	g = &Gauge{}
	gv.gauges[key] = g
	gv.labels[key] = labels
	return g
}

// All 返回所有 Gauge 及其 labels。
func (gv *GaugeVec) All() map[string]struct {
	Gauge  *Gauge
	Labels map[string]string
} {
	gv.mu.RLock()
	defer gv.mu.RUnlock()
	result := make(map[string]struct {
		Gauge  *Gauge
		Labels map[string]string
	}, len(gv.gauges))
	for k, g := range gv.gauges {
		result[k] = struct {
			Gauge  *Gauge
			Labels map[string]string
		}{Gauge: g, Labels: gv.labels[k]}
	}
	return result
}

// metricMeta 指标元信息（名称、帮助文本、类型）。
type metricMeta struct {
	name string
	help string
	typ  string // "counter" | "gauge"
}

// Registry 全局指标注册表。
type Registry struct {
	mu       sync.RWMutex
	counters map[string]*counterEntry
	gauges   map[string]*gaugeEntry
}

type counterEntry struct {
	meta   metricMeta
	scalar *Counter    // 无 label 时使用
	vec    *CounterVec // 有 label 时使用
}

type gaugeEntry struct {
	meta   metricMeta
	scalar *Gauge
	vec    *GaugeVec
}

// globalRegistry 全局默认注册表。
var globalRegistry = NewRegistry()

// DefaultRegistry 返回全局默认注册表。
func DefaultRegistry() *Registry {
	return globalRegistry
}

// NewRegistry 创建新的注册表。
func NewRegistry() *Registry {
	return &Registry{
		counters: make(map[string]*counterEntry),
		gauges:   make(map[string]*gaugeEntry),
	}
}

// RegisterCounter 注册一个无 label 的 Counter。
func (r *Registry) RegisterCounter(name, help string) *Counter {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := &Counter{}
	r.counters[name] = &counterEntry{
		meta:   metricMeta{name: name, help: help, typ: "counter"},
		scalar: c,
	}
	return c
}

// RegisterCounterVec 注册一个带 label 的 Counter。
func (r *Registry) RegisterCounterVec(name, help string) *CounterVec {
	r.mu.Lock()
	defer r.mu.Unlock()
	cv := NewCounterVec()
	r.counters[name] = &counterEntry{
		meta: metricMeta{name: name, help: help, typ: "counter"},
		vec:  cv,
	}
	return cv
}

// RegisterGauge 注册一个无 label 的 Gauge。
func (r *Registry) RegisterGauge(name, help string) *Gauge {
	r.mu.Lock()
	defer r.mu.Unlock()
	g := &Gauge{}
	r.gauges[name] = &gaugeEntry{
		meta:   metricMeta{name: name, help: help, typ: "gauge"},
		scalar: g,
	}
	return g
}

// RegisterGaugeVec 注册一个带 label 的 Gauge。
func (r *Registry) RegisterGaugeVec(name, help string) *GaugeVec {
	r.mu.Lock()
	defer r.mu.Unlock()
	gv := NewGaugeVec()
	r.gauges[name] = &gaugeEntry{
		meta: metricMeta{name: name, help: help, typ: "gauge"},
		vec:  gv,
	}
	return gv
}

// CounterNames 返回所有已注册 Counter 名称（排序）。
func (r *Registry) CounterNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.counters))
	for name := range r.counters {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// GaugeNames 返回所有已注册 Gauge 名称（排序）。
func (r *Registry) GaugeNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.gauges))
	for name := range r.gauges {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// GetCounter 获取已注册的 Counter（nil 表示未注册）。
func (r *Registry) GetCounter(name string) *Counter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if e, ok := r.counters[name]; ok {
		return e.scalar
	}
	return nil
}

// GetCounterVec 获取已注册的 CounterVec。
func (r *Registry) GetCounterVec(name string) *CounterVec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if e, ok := r.counters[name]; ok {
		return e.vec
	}
	return nil
}

// GetGauge 获取已注册的 Gauge。
func (r *Registry) GetGauge(name string) *Gauge {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if e, ok := r.gauges[name]; ok {
		return e.scalar
	}
	return nil
}

// GetGaugeVec 获取已注册的 GaugeVec。
func (r *Registry) GetGaugeVec(name string) *GaugeVec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if e, ok := r.gauges[name]; ok {
		return e.vec
	}
	return nil
}

// formatLabels 将 label map 格式化为 Prometheus 标签字符串。
// 输出格式：{key1="val1",key2="val2"}，空 labels 返回空串。
func formatLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	sb.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(k)
		sb.WriteString(`="`)
		sb.WriteString(escapeLabel(labels[k]))
		sb.WriteByte('"')
	}
	sb.WriteByte('}')
	return sb.String()
}

// escapeLabel 转义 label 值中的特殊字符（反斜杠、双引号、换行）。
func escapeLabel(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return s
}

// formatFloat 格式化浮点数为 Prometheus 兼容格式。
func formatFloat(v float64) string {
	if v == float64(int64(v)) {
		return fmt.Sprintf("%d", int64(v))
	}
	return fmt.Sprintf("%g", v)
}
