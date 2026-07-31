package skill

import (
	"testing"
	"time"
)

func TestVersionManager(t *testing.T) {
	vm := NewVersionManager()
	vm.Snapshot("sk1", 1, `{"name":"v1"}`, "manual-edit", "initial")
	vm.Snapshot("sk1", 2, `{"name":"v2"}`, "auto-optimize", "improved")

	// 获取版本
	snap, err := vm.GetVersion("sk1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Content != `{"name":"v1"}` {
		t.Errorf("unexpected content: %s", snap.Content)
	}

	// 最新版本
	if vm.LatestVersion("sk1") != 2 {
		t.Errorf("expected latest=2, got %d", vm.LatestVersion("sk1"))
	}

	// 不存在的版本
	_, err = vm.GetVersion("sk1", 99)
	if err == nil {
		t.Error("expected error for missing version")
	}
}

func TestVersionHistoryLimit(t *testing.T) {
	vm := NewVersionManager()
	for i := 1; i <= 15; i++ {
		vm.Snapshot("sk1", i, "content", "test", "")
	}
	versions := vm.ListVersions("sk1")
	if len(versions) > MaxVersionHistory {
		t.Errorf("expected max %d versions, got %d", MaxVersionHistory, len(versions))
	}
}

func TestGenerationGate(t *testing.T) {
	gate := NewGenerationGate()

	// 步骤不足
	if gate.ShouldGenerate("goal1", 2, time.Minute) {
		t.Error("should not generate with < 3 steps")
	}
	// 耗时不足
	if gate.ShouldGenerate("goal1", 5, 10*time.Second) {
		t.Error("should not generate with < 30s duration")
	}
	// 满足条件
	if !gate.ShouldGenerate("goal1", 5, time.Minute) {
		t.Error("expected generation allowed")
	}
	// 标记后不再重复
	gate.MarkGenerated("goal1")
	if gate.ShouldGenerate("goal1", 5, time.Minute) {
		t.Error("should not generate same goal twice")
	}
	// 不同 goal 可以
	if !gate.ShouldGenerate("goal2", 5, time.Minute) {
		t.Error("different goal should be allowed")
	}
}
