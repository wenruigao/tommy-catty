package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/tommy-cat/agent/internal/engine"
	"github.com/tommy-cat/agent/internal/skill"
)

// TestBuildTraceJSON_GenerateSkill 验证 skill.BuildTraceJSON 产出的 JSON 结构
// 与 skill.Generator 期望的 traceData 匹配，能完整跑通 Skill 生成与持久化流程。
func TestBuildTraceJSON_GenerateSkill(t *testing.T) {
	result := &engine.ExecutionTrace{
		TaskID: "task-1",
		Goal:   "搜索 Go 教程并总结",
		Steps: []engine.StepResult{
			{Thought: "先搜索资料", Action: "web_search", ActionInput: map[string]interface{}{"query": "Go 教程"}, Observation: "找到 5 条结果"},
			{Thought: "抓取第一条", Action: "web_fetch", ActionInput: map[string]interface{}{"url": "https://example.com"}, Observation: "页面内容..."},
			{Thought: "总结完成", IsFinal: true, FinalAnswer: "这是 Go 教程总结"},
		},
		StartTime: time.Now(),
		EndTime:   time.Now(),
	}

	traceJSON, err := skill.BuildTraceJSON(result)
	if err != nil {
		t.Fatalf("BuildTraceJSON 失败: %v", err)
	}

	store := skill.NewStore(filepath.Join(t.TempDir(), "skills.json"))
	gen := skill.NewGenerator(store)
	s, err := gen.GenerateFromTrace(traceJSON)
	if err != nil {
		t.Fatalf("GenerateFromTrace 应成功（此前因 JSON 结构不匹配永远失败）: %v", err)
	}
	if err := gen.ValidateSkill(s); err != nil {
		t.Fatalf("生成的 Skill 应通过验证: %v", err)
	}

	// 持久化回归：生成后必须 Save，否则 /skills 永远为空
	if err := gen.Save(s); err != nil {
		t.Fatalf("Save 应成功: %v", err)
	}
	if got := len(store.List()); got != 1 {
		t.Fatalf("保存后 store 应有 1 个 Skill，得到 %d", got)
	}

	if len(s.Steps) != 3 {
		t.Errorf("Skill 步骤数 = %d, want 3", len(s.Steps))
	}
	if len(s.Tools) != 2 {
		t.Errorf("Skill 工具数 = %d, want 2（web_search/web_fetch）", len(s.Tools))
	}
	if s.Steps[0].Action != "call_tool" || s.Steps[0].ToolName != "web_search" {
		t.Errorf("第一步应为 web_search 工具调用，得到: %+v", s.Steps[0])
	}
}
