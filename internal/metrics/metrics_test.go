package metrics

import (
	"strings"
	"sync"
	"testing"
)

// ============================================================
// Counter 测试
// ============================================================

func TestCounter_Add(t *testing.T) {
	c := &Counter{}
	c.Add(5)
	c.Add(3)
	if got := c.Value(); got != 8 {
		t.Errorf("Counter.Value() = %v, 期望 8", got)
	}
}

func TestCounter_AddNegative(t *testing.T) {
	c := &Counter{}
	c.Add(5)
	c.Add(-3) // 负值应忽略
	if got := c.Value(); got != 5 {
		t.Errorf("Counter.Value() = %v, 期望 5（负值应忽略）", got)
	}
}

func TestCounter_Concurrent(t *testing.T) {
	c := &Counter{}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Add(1)
		}()
	}
	wg.Wait()
	if got := c.Value(); got != 100 {
		t.Errorf("并发后 Counter.Value() = %v, 期望 100", got)
	}
}

// ============================================================
// Gauge 测试
// ============================================================

func TestGauge_SetIncDec(t *testing.T) {
	g := &Gauge{}
	g.Set(10)
	if got := g.Value(); got != 10 {
		t.Errorf("Gauge.Value() = %v, 期望 10", got)
	}
	g.Inc()
	if got := g.Value(); got != 11 {
		t.Errorf("Inc 后 Gauge.Value() = %v, 期望 11", got)
	}
	g.Dec()
	if got := g.Value(); got != 10 {
		t.Errorf("Dec 后 Gauge.Value() = %v, 期望 10", got)
	}
}

// ============================================================
// CounterVec / GaugeVec 测试
// ============================================================

func TestCounterVec_With(t *testing.T) {
	cv := NewCounterVec()
	cv.With(map[string]string{"provider": "a", "status": "ok"}).Add(3)
	cv.With(map[string]string{"provider": "b", "status": "ok"}).Add(5)
	cv.With(map[string]string{"provider": "a", "status": "ok"}).Add(2)

	all := cv.All()
	if len(all) != 2 {
		t.Fatalf("All() 返回 %d 个条目, 期望 2", len(all))
	}

	// 验证 label 组合 "a,ok" 的值为 5
	for _, item := range all {
		if item.Labels["provider"] == "a" && item.Counter.Value() != 5 {
			t.Errorf("provider=a 的 Counter = %v, 期望 5", item.Counter.Value())
		}
	}
}

func TestGaugeVec_With(t *testing.T) {
	gv := NewGaugeVec()
	gv.With(map[string]string{"provider": "x"}).Set(42)
	if got := gv.With(map[string]string{"provider": "x"}).Value(); got != 42 {
		t.Errorf("GaugeVec value = %v, 期望 42", got)
	}
}

// ============================================================
// Registry 测试
// ============================================================

func TestRegistry_RegisterAndEncode(t *testing.T) {
	r := NewRegistry()

	// 注册标量 Counter
	c := r.RegisterCounter("test_calls_total", "测试调用总数")
	c.Add(10)

	// 注册带 label 的 Counter
	cv := r.RegisterCounterVec("test_errors_total", "测试错误总数")
	cv.With(map[string]string{"type": "timeout"}).Add(3)
	cv.With(map[string]string{"type": "network"}).Add(2)

	// 注册标量 Gauge
	g := r.RegisterGauge("test_active", "活跃数")
	g.Set(7)

	// 注册带 label 的 Gauge
	gv := r.RegisterGaugeVec("test_state", "状态")
	gv.With(map[string]string{"name": "breaker"}).Set(1)

	output := r.Encode()

	// 验证 HELP 和 TYPE 行
	if !strings.Contains(output, "# HELP test_calls_total 测试调用总数") {
		t.Error("缺少 test_calls_total HELP 行")
	}
	if !strings.Contains(output, "# TYPE test_calls_total counter") {
		t.Error("缺少 test_calls_total TYPE 行")
	}
	if !strings.Contains(output, "# TYPE test_errors_total counter") {
		t.Error("缺少 test_errors_total TYPE 行")
	}
	if !strings.Contains(output, "# TYPE test_active gauge") {
		t.Error("缺少 test_active TYPE 行")
	}

	// 验证值
	if !strings.Contains(output, "test_calls_total 10") {
		t.Error("缺少 test_calls_total 值")
	}
	if !strings.Contains(output, `test_errors_total{type="timeout"} 3`) {
		t.Error("缺少 test_errors_total{type=timeout} 值")
	}
	if !strings.Contains(output, `test_errors_total{type="network"} 2`) {
		t.Error("缺少 test_errors_total{type=network} 值")
	}
	if !strings.Contains(output, "test_active 7") {
		t.Error("缺少 test_active 值")
	}
	if !strings.Contains(output, `test_state{name="breaker"} 1`) {
		t.Error("缺少 test_state 值")
	}
}

func TestRegistry_EmptyEncode(t *testing.T) {
	r := NewRegistry()
	output := r.Encode()
	if output != "" {
		t.Errorf("空注册表 Encode() 应返回空串，得到 %q", output)
	}
}

// ============================================================
// Prometheus 格式编码测试
// ============================================================

func TestFormatLabels(t *testing.T) {
	tests := []struct {
		labels map[string]string
		want   string
	}{
		{nil, ""},
		{map[string]string{}, ""},
		{map[string]string{"a": "1"}, `{a="1"}`},
		{map[string]string{"b": "2", "a": "1"}, `{a="1",b="2"}`},
		{map[string]string{"k": `val"with"quotes`}, `{k="val\"with\"quotes"}`},
	}

	for _, tt := range tests {
		got := formatLabels(tt.labels)
		if got != tt.want {
			t.Errorf("formatLabels(%v) = %q, 期望 %q", tt.labels, got, tt.want)
		}
	}
}

func TestEscapeLabel(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`hello`, `hello`},
		{`say "hi"`, `say \"hi\"`},
		{"line\nbreak", `line\nbreak`},
		{`back\slash`, `back\\slash`},
	}

	for _, tt := range tests {
		got := escapeLabel(tt.input)
		if got != tt.want {
			t.Errorf("escapeLabel(%q) = %q, 期望 %q", tt.input, got, tt.want)
		}
	}
}

func TestFormatFloat(t *testing.T) {
	if got := formatFloat(42); got != "42" {
		t.Errorf("formatFloat(42) = %q, 期望 42", got)
	}
	if got := formatFloat(3.14); got != "3.14" {
		t.Errorf("formatFloat(3.14) = %q, 期望 3.14", got)
	}
	if got := formatFloat(0); got != "0" {
		t.Errorf("formatFloat(0) = %q, 期望 0", got)
	}
}

// ============================================================
// CollectAll 测试
// ============================================================

func TestCollectAll_RuntimeMetrics(t *testing.T) {
	ensureRegistered()
	CollectAll()

	mem := DefaultRegistry().GetGauge(MetricRuntimeMemory)
	if mem == nil {
		t.Fatal("runtime memory gauge 未注册")
	}
	if mem.Value() <= 0 {
		t.Error("runtime memory 应大于 0")
	}

	goroutines := DefaultRegistry().GetGauge(MetricRuntimeGoroutines)
	if goroutines == nil {
		t.Fatal("runtime goroutines gauge 未注册")
	}
	if goroutines.Value() <= 0 {
		t.Error("goroutine 数应大于 0")
	}
}

func TestCollectAll_CustomCollector(t *testing.T) {
	ensureRegistered()

	called := false
	RegisterCollector(func() {
		called = true
	})
	CollectAll()

	if !called {
		t.Error("自定义 collector 应被调用")
	}
}

// ============================================================
// 全局访问器测试
// ============================================================

func TestGlobalAccessors(t *testing.T) {
	// 验证所有访问器返回非 nil
	if LLMCalls() == nil {
		t.Error("LLMCalls() 返回 nil")
	}
	if LLMRetries() == nil {
		t.Error("LLMRetries() 返回 nil")
	}
	if LLMCircuitState() == nil {
		t.Error("LLMCircuitState() 返回 nil")
	}
	if LLMTokens() == nil {
		t.Error("LLMTokens() 返回 nil")
	}
	if LLMTokensCached() == nil {
		t.Error("LLMTokensCached() 返回 nil")
	}
	if SessionActive() == nil {
		t.Error("SessionActive() 返回 nil")
	}
	if SessionCreated() == nil {
		t.Error("SessionCreated() 返回 nil")
	}
	if ToolCalls() == nil {
		t.Error("ToolCalls() 返回 nil")
	}
	if SecurityEvents() == nil {
		t.Error("SecurityEvents() 返回 nil")
	}
	if ChannelMessages() == nil {
		t.Error("ChannelMessages() 返回 nil")
	}
	if AgentDelegations() == nil {
		t.Error("AgentDelegations() 返回 nil")
	}
	if AgentWorkers() == nil {
		t.Error("AgentWorkers() 返回 nil")
	}
}

// ============================================================
// 端到端：Encode 输出完整性
// ============================================================

func TestEncode_FullOutput(t *testing.T) {
	ensureRegistered()

	// 写入一些测试数据
	LLMCalls().With(map[string]string{"provider": "test", "status": "success"}).Add(5)
	SessionActive().Set(3)
	CollectAll()

	output := DefaultRegistry().Encode()

	// 验证关键指标存在
	required := []string{
		"tommy_llm_calls_total",
		"tommy_session_active",
		"tommy_runtime_memory_bytes",
		"tommy_runtime_goroutines",
	}
	for _, name := range required {
		if !strings.Contains(output, name) {
			t.Errorf("输出中缺少指标 %q", name)
		}
	}

	// 验证 Prometheus 格式：每行要么是注释（#）要么是 metric_line
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		// 指标行应包含至少一个空格（name value 或 name{labels} value）
		if !strings.ContainsAny(line, " ") {
			t.Errorf("非法指标行: %q", line)
		}
	}
}
