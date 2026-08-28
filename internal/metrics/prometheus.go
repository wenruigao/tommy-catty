package metrics

import (
	"sort"
	"strings"
)

// Encode 将注册表中所有指标编码为 Prometheus exposition format 文本。
// 输出格式兼容 Prometheus 0.x text format（text/plain; version=0.0.4）。
func (r *Registry) Encode() string {
	var sb strings.Builder

	// 按名称排序输出，保证稳定性
	allNames := make([]string, 0)
	counterOrder := r.CounterNames()
	gaugeOrder := r.GaugeNames()

	// 合并所有指标名并排序
	nameSet := make(map[string]bool)
	for _, n := range counterOrder {
		nameSet[n] = true
		allNames = append(allNames, n)
	}
	for _, n := range gaugeOrder {
		if !nameSet[n] {
			allNames = append(allNames, n)
		}
	}
	sort.Strings(allNames)

	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, name := range allNames {
		if entry, ok := r.counters[name]; ok {
			r.encodeCounter(&sb, entry)
		} else if entry, ok := r.gauges[name]; ok {
			r.encodeGauge(&sb, entry)
		}
	}

	return sb.String()
}

// encodeCounter 编码一个 Counter 指标。
func (r *Registry) encodeCounter(sb *strings.Builder, entry *counterEntry) {
	// HELP 行
	sb.WriteString("# HELP ")
	sb.WriteString(entry.meta.name)
	sb.WriteByte(' ')
	sb.WriteString(entry.meta.help)
	sb.WriteByte('\n')
	// TYPE 行
	sb.WriteString("# TYPE ")
	sb.WriteString(entry.meta.name)
	sb.WriteString(" counter\n")

	if entry.scalar != nil {
		// 无 label 的标量 Counter
		sb.WriteString(entry.meta.name)
		sb.WriteByte(' ')
		sb.WriteString(formatFloat(entry.scalar.Value()))
		sb.WriteByte('\n')
	} else if entry.vec != nil {
		// 带 label 的 CounterVec
		all := entry.vec.All()
		// 按 key 排序保证输出稳定
		keys := make([]string, 0, len(all))
		for k := range all {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			item := all[k]
			sb.WriteString(entry.meta.name)
			sb.WriteString(formatLabels(item.Labels))
			sb.WriteByte(' ')
			sb.WriteString(formatFloat(item.Counter.Value()))
			sb.WriteByte('\n')
		}
	}
	sb.WriteByte('\n')
}

// encodeGauge 编码一个 Gauge 指标。
func (r *Registry) encodeGauge(sb *strings.Builder, entry *gaugeEntry) {
	sb.WriteString("# HELP ")
	sb.WriteString(entry.meta.name)
	sb.WriteByte(' ')
	sb.WriteString(entry.meta.help)
	sb.WriteByte('\n')
	sb.WriteString("# TYPE ")
	sb.WriteString(entry.meta.name)
	sb.WriteString(" gauge\n")

	if entry.scalar != nil {
		sb.WriteString(entry.meta.name)
		sb.WriteByte(' ')
		sb.WriteString(formatFloat(entry.scalar.Value()))
		sb.WriteByte('\n')
	} else if entry.vec != nil {
		all := entry.vec.All()
		keys := make([]string, 0, len(all))
		for k := range all {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			item := all[k]
			sb.WriteString(entry.meta.name)
			sb.WriteString(formatLabels(item.Labels))
			sb.WriteByte(' ')
			sb.WriteString(formatFloat(item.Gauge.Value()))
			sb.WriteByte('\n')
		}
	}
	sb.WriteByte('\n')
}
