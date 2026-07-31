package engine

import (
	"testing"
)

func TestShouldReflect(t *testing.T) {
	cfg := DefaultReflectionConfig()

	// 工具失败时触发
	if !shouldReflect(cfg, 1, true) {
		t.Error("expected reflection on tool failure")
	}
	// 非间隔步不触发
	if shouldReflect(cfg, 3, false) {
		t.Error("should not reflect at step 3")
	}
	// 间隔步触发（每 5 步）
	if !shouldReflect(cfg, 5, false) {
		t.Error("expected reflection at step 5")
	}
	// 禁用时不触发
	cfg.Enabled = false
	if shouldReflect(cfg, 5, true) {
		t.Error("should not reflect when disabled")
	}
}

func TestParseReflection(t *testing.T) {
	tests := []struct {
		input      string
		wantAdj    string
		wantSatMin float64
	}{
		{`{"satisfaction": 0.9, "issues": [], "adjustment": "continue"}`, "continue", 0.8},
		{`{"satisfaction": 0.3, "issues": ["missing data"], "adjustment": "replan"}`, "replan", 0.2},
		{`{"satisfaction": 0.5, "issues": [], "adjustment": "revise"}`, "revise", 0.4},
		{`invalid json`, "continue", 0.7}, // 解析失败默认继续
	}
	for _, tt := range tests {
		r := parseReflection(tt.input)
		if r.Adjustment != tt.wantAdj {
			t.Errorf("input=%q: got adjustment=%q, want %q", tt.input, r.Adjustment, tt.wantAdj)
		}
		if r.Satisfaction < tt.wantSatMin {
			t.Errorf("input=%q: satisfaction=%f < min %f", tt.input, r.Satisfaction, tt.wantSatMin)
		}
	}
}

func TestReplanState(t *testing.T) {
	cfg := DefaultReflectionConfig()
	rs := &ReplanState{}

	// 连续失败 3 次触发重规划
	rs.updateDeviation(true, false)
	rs.updateDeviation(true, false)
	if rs.shouldReplan(cfg, nil, 2, 20) {
		t.Error("should not replan after 2 failures")
	}
	rs.updateDeviation(true, false)
	if !rs.shouldReplan(cfg, nil, 3, 20) {
		t.Error("expected replan after 3 consecutive failures")
	}

	// 重规划次数上限
	rs.ReplanCount = cfg.MaxReplans
	if rs.shouldReplan(cfg, nil, 3, 20) {
		t.Error("should not replan when at max replans")
	}
}

func TestDeviationAccumulation(t *testing.T) {
	cfg := DefaultReflectionConfig()
	rs := &ReplanState{}

	// 空结果累积偏差
	rs.updateDeviation(false, true) // +0.3
	rs.updateDeviation(false, true) // +0.3
	rs.updateDeviation(false, true) // +0.3
	rs.updateDeviation(false, true) // +0.3
	rs.updateDeviation(false, true) // +0.3 = 1.5
	if !rs.shouldReplan(cfg, nil, 5, 20) {
		t.Errorf("expected replan at deviation=%.1f (threshold=%.1f)", rs.DeviationScore, cfg.DeviationThreshold)
	}
}

func TestBuildReplanPrompt(t *testing.T) {
	prompt := buildReplanPrompt("分析销售数据", []string{"已获取 2024 年 Q1 数据", "用户表有 1000 条记录"})
	if len(prompt) == 0 {
		t.Error("expected non-empty replan prompt")
	}
}
