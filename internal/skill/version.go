// version.go 实现 Skill 版本管理与回滚机制。
package skill

import (
	"fmt"
	"sync"
	"time"
)

// MaxVersionHistory 每个 Skill 保留的最大历史版本数。
const MaxVersionHistory = 10

// VersionSnapshot Skill 历史版本快照。
type VersionSnapshot struct {
	Version    int       `json:"version"`
	Content    string    `json:"content"`     // Skill 完整定义（JSON）
	ChangedBy  string    `json:"changed_by"`  // auto-optimize | manual-edit | rollback
	ChangedAt  time.Time `json:"changed_at"`
	Note       string    `json:"note"`
}

// VersionManager 管理 Skill 的版本历史（内存，随 Store 持久化）。
type VersionManager struct {
	mu       sync.RWMutex
	history  map[string][]VersionSnapshot // skillID -> 版本历史（升序）
}

// NewVersionManager 创建版本管理器。
func NewVersionManager() *VersionManager {
	return &VersionManager{
		history: make(map[string][]VersionSnapshot),
	}
}

// Snapshot 保存当前版本快照（在变更前调用）。
func (vm *VersionManager) Snapshot(skillID string, version int, content string, changedBy string, note string) {
	vm.mu.Lock()
	defer vm.mu.Unlock()

	snap := VersionSnapshot{
		Version:   version,
		Content:   content,
		ChangedBy: changedBy,
		ChangedAt: time.Now(),
		Note:      note,
	}

	vm.history[skillID] = append(vm.history[skillID], snap)

	// 保留最近 N 个版本
	if len(vm.history[skillID]) > MaxVersionHistory {
		vm.history[skillID] = vm.history[skillID][len(vm.history[skillID])-MaxVersionHistory:]
	}
}

// GetVersion 获取指定版本快照。
func (vm *VersionManager) GetVersion(skillID string, version int) (*VersionSnapshot, error) {
	vm.mu.RLock()
	defer vm.mu.RUnlock()

	for _, snap := range vm.history[skillID] {
		if snap.Version == version {
			return &snap, nil
		}
	}
	return nil, fmt.Errorf("skill %q version %d not found", skillID, version)
}

// ListVersions 返回 Skill 的所有历史版本（升序）。
func (vm *VersionManager) ListVersions(skillID string) []VersionSnapshot {
	vm.mu.RLock()
	defer vm.mu.RUnlock()
	return vm.history[skillID]
}

// LatestVersion 返回最新版本号（无历史则返回 0）。
func (vm *VersionManager) LatestVersion(skillID string) int {
	vm.mu.RLock()
	defer vm.mu.RUnlock()
	snaps := vm.history[skillID]
	if len(snaps) == 0 {
		return 0
	}
	return snaps[len(snaps)-1].Version
}

// ============================================================
// Skill 生成成本控制
// ============================================================

// GenerationGate 控制 Skill 自动生成的触发条件，避免频繁 LLM 调用。
type GenerationGate struct {
	mu             sync.Mutex
	dailyCount     int
	dailyReset     time.Time
	MaxDaily       int           // 每日最大生成次数（默认 10）
	MinSteps       int           // 最少执行步骤（默认 3）
	MinDuration    time.Duration // 最短耗时（默认 30s）
	GeneratedGoals map[string]bool // 已生成过 Skill 的 goal 指纹
}

// NewGenerationGate 创建生成门控。
func NewGenerationGate() *GenerationGate {
	return &GenerationGate{
		MaxDaily:       10,
		MinSteps:       3,
		MinDuration:    30 * time.Second,
		dailyReset:     time.Now().Truncate(24 * time.Hour),
		GeneratedGoals: make(map[string]bool),
	}
}

// ShouldGenerate 判断是否应触发 Skill 生成。
// 条件须同时满足：(1) 首次成功 (2) 步骤 >= MinSteps (3) 耗时 >= MinDuration (4) 未超日配额。
func (g *GenerationGate) ShouldGenerate(goalFingerprint string, steps int, duration time.Duration) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.resetDailyIfNeeded()

	// 已生成过
	if g.GeneratedGoals[goalFingerprint] {
		return false
	}
	// 步骤不足
	if steps < g.MinSteps {
		return false
	}
	// 耗时不足
	if duration < g.MinDuration {
		return false
	}
	// 超日配额
	if g.dailyCount >= g.MaxDaily {
		return false
	}
	return true
}

// MarkGenerated 标记已生成（消耗配额）。
func (g *GenerationGate) MarkGenerated(goalFingerprint string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.GeneratedGoals[goalFingerprint] = true
	g.dailyCount++
}

func (g *GenerationGate) resetDailyIfNeeded() {
	today := time.Now().Truncate(24 * time.Hour)
	if today.After(g.dailyReset) {
		g.dailyReset = today
		g.dailyCount = 0
	}
}
