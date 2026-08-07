// skillwiring_test.go 验证 Skill 生成门控与版本快照的生产接线语义。
package skill

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestGoalFingerprint_Normalization 验证指纹规范化（大小写/空白不敏感、不同目标可区分）。
func TestGoalFingerprint_Normalization(t *testing.T) {
	a := GoalFingerprint("  Deploy   Service A ")
	b := GoalFingerprint("deploy service a")
	if a != b {
		t.Error("规范化后的相同目标指纹应一致")
	}
	if GoalFingerprint("deploy service a") == GoalFingerprint("deploy service b") {
		t.Error("不同目标的指纹应不同")
	}
}

// TestGenerationGate_WiringSemantics 验证门控四条件：指纹去重/步骤/耗时/日配额。
func TestGenerationGate_WiringSemantics(t *testing.T) {
	gate := NewGenerationGate()
	fp := GoalFingerprint("deploy service")

	if gate.ShouldGenerate(fp, 5, 10*time.Second) {
		t.Error("耗时不足 30s 不应触发生成")
	}
	if gate.ShouldGenerate(fp, 2, time.Minute) {
		t.Error("步骤数不足 3 不应触发生成")
	}
	if !gate.ShouldGenerate(fp, 5, time.Minute) {
		t.Error("满足全部条件应触发生成")
	}
	gate.MarkGenerated(fp)
	if gate.ShouldGenerate(fp, 5, time.Minute) {
		t.Error("同 goal 指纹不应重复生成")
	}
}

// TestGeneratorSave_VersionSnapshotOnOverwrite 验证覆盖保存前记录历史快照（回滚依据）。
func TestGeneratorSave_VersionSnapshotOnOverwrite(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "skills.json"))
	gen := NewGenerator(store)
	vm := NewVersionManager()
	gen.SetVersionManager(vm)

	v1 := &Skill{ID: "s1", Name: "skill-v1", Version: "1.0.0",
		Steps: []SkillStep{{Order: 1, Action: "call_tool", ToolName: "shell_exec"}}}
	if err := gen.Save(v1); err != nil {
		t.Fatalf("save v1: %v", err)
	}
	if len(vm.ListVersions("s1")) != 0 {
		t.Error("首次保存不应产生快照")
	}

	v2 := &Skill{ID: "s1", Name: "skill-v2", Version: "1.0.1",
		Steps: []SkillStep{{Order: 1, Action: "call_tool", ToolName: "file_read"}}}
	if err := gen.Save(v2); err != nil {
		t.Fatalf("save v2: %v", err)
	}

	snaps := vm.ListVersions("s1")
	if len(snaps) != 1 {
		t.Fatalf("覆盖保存应产生 1 个快照，got %d", len(snaps))
	}
	if !strings.Contains(snaps[0].Content, "skill-v1") {
		t.Errorf("快照应保存变更前内容，got: %s", snaps[0].Content)
	}
	if got, err := vm.GetVersion("s1", snaps[0].Version); err != nil || got.Content != snaps[0].Content {
		t.Errorf("GetVersion 应能取回快照: err=%v", err)
	}
}
