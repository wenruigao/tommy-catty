package doctor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ============================================================
// Doctor 核心引擎测试
// ============================================================

func TestDoctor_New(t *testing.T) {
	d := New()
	if d == nil {
		t.Fatal("New() returned nil")
	}
	if len(d.checks) != 0 {
		t.Error("new Doctor should have no checks")
	}
}

func TestDoctor_AddCheck(t *testing.T) {
	d := New()
	d.AddCheck(Check{Name: "a", Run: func(ctx context.Context) (CheckStatus, string) { return StatusOK, "" }})
	d.AddCheck(Check{Name: "b", Run: func(ctx context.Context) (CheckStatus, string) { return StatusOK, "" }})
	if len(d.checks) != 2 {
		t.Errorf("checks len = %d, want 2", len(d.checks))
	}
}

func TestDoctor_RunAll_Aggregation(t *testing.T) {
	d := New()
	d.AddCheck(Check{
		Name: "ok", Severity: SeverityInfo,
		Run: func(ctx context.Context) (CheckStatus, string) { return StatusOK, "fine" },
	})
	d.AddCheck(Check{
		Name: "warn", Severity: SeverityWarning,
		Run: func(ctx context.Context) (CheckStatus, string) { return StatusWarning, "hmm" },
	})
	d.AddCheck(Check{
		Name: "err", Severity: SeverityCritical,
		Run: func(ctx context.Context) (CheckStatus, string) { return StatusError, "bad" },
	})
	d.AddCheck(Check{
		Name: "skip", Severity: SeverityInfo,
		Run: func(ctx context.Context) (CheckStatus, string) { return StatusSkipped, "n/a" },
	})

	report := d.RunAll(context.Background())
	if len(report.Results) != 4 {
		t.Fatalf("results len = %d, want 4", len(report.Results))
	}
	// OK + Skipped 都计入 TotalOK
	if report.TotalOK != 2 {
		t.Errorf("TotalOK = %d, want 2", report.TotalOK)
	}
	if report.TotalWarn != 1 {
		t.Errorf("TotalWarn = %d, want 1", report.TotalWarn)
	}
	if report.TotalError != 1 {
		t.Errorf("TotalError = %d, want 1", report.TotalError)
	}
	if report.EndTime.Before(report.StartTime) {
		t.Error("EndTime should be after StartTime")
	}
}

func TestDoctor_RunAll_Empty(t *testing.T) {
	d := New()
	report := d.RunAll(context.Background())
	if len(report.Results) != 0 {
		t.Errorf("expected 0 results, got %d", len(report.Results))
	}
}

func TestDoctor_RunAll_ParallelExecution(t *testing.T) {
	d := New()
	for i := 0; i < 5; i++ {
		d.AddCheck(Check{
			Name: fmt.Sprintf("check-%d", i), Severity: SeverityInfo,
			Run: func(ctx context.Context) (CheckStatus, string) {
				time.Sleep(10 * time.Millisecond)
				return StatusOK, "done"
			},
		})
	}
	start := time.Now()
	report := d.RunAll(context.Background())
	elapsed := time.Since(start)

	if len(report.Results) != 5 {
		t.Fatalf("results len = %d, want 5", len(report.Results))
	}
	// 并行执行应远小于 5*10ms=50ms（留宽裕余量）
	if elapsed > 200*time.Millisecond {
		t.Errorf("RunAll took %v, expected parallel execution", elapsed)
	}
}

func TestDoctor_RunAll_ResultOrder(t *testing.T) {
	d := New()
	names := []string{"alpha", "beta", "gamma"}
	for _, name := range names {
		n := name
		d.AddCheck(Check{
			Name: n, Severity: SeverityInfo,
			Run: func(ctx context.Context) (CheckStatus, string) { return StatusOK, n },
		})
	}
	report := d.RunAll(context.Background())
	for i, name := range names {
		if report.Results[i].Name != name {
			t.Errorf("Results[%d].Name = %q, want %q", i, report.Results[i].Name, name)
		}
	}
}

func TestDoctor_RunQuick_FiltersBySeverity(t *testing.T) {
	d := New()
	d.AddCheck(Check{Name: "info", Severity: SeverityInfo,
		Run: func(ctx context.Context) (CheckStatus, string) { return StatusOK, "" }})
	d.AddCheck(Check{Name: "warning", Severity: SeverityWarning,
		Run: func(ctx context.Context) (CheckStatus, string) { return StatusOK, "" }})
	d.AddCheck(Check{Name: "critical", Severity: SeverityCritical,
		Run: func(ctx context.Context) (CheckStatus, string) { return StatusOK, "" }})

	report := d.RunQuick(context.Background())
	if len(report.Results) != 1 {
		t.Fatalf("RunQuick should only run critical, got %d results", len(report.Results))
	}
	if report.Results[0].Name != "critical" {
		t.Errorf("expected critical, got %s", report.Results[0].Name)
	}
}

func TestDoctor_RunQuick_Empty(t *testing.T) {
	d := New()
	d.AddCheck(Check{Name: "info", Severity: SeverityInfo,
		Run: func(ctx context.Context) (CheckStatus, string) { return StatusOK, "" }})

	report := d.RunQuick(context.Background())
	if len(report.Results) != 0 {
		t.Errorf("expected 0 critical results, got %d", len(report.Results))
	}
}

// ============================================================
// runCheck 自动修复逻辑测试
// ============================================================

func TestRunCheck_FixSuccess(t *testing.T) {
	d := New()
	fixed := false
	d.AddCheck(Check{
		Name: "fixable", Severity: SeverityWarning,
		Run: func(ctx context.Context) (CheckStatus, string) {
			if fixed {
				return StatusOK, "repaired"
			}
			return StatusError, "broken"
		},
		Fix: func(ctx context.Context) (bool, string) {
			fixed = true
			return true, "auto-repaired"
		},
	})

	report := d.RunAll(context.Background())
	r := report.Results[0]
	if r.Status != StatusOK {
		t.Errorf("status = %s, want ok after fix", r.Status)
	}
	if !r.FixApplied {
		t.Error("FixApplied should be true")
	}
	if r.FixMessage != "auto-repaired" {
		t.Errorf("FixMessage = %q", r.FixMessage)
	}
	if report.FixesApplied != 1 {
		t.Errorf("FixesApplied = %d, want 1", report.FixesApplied)
	}
}

func TestRunCheck_FixFailure(t *testing.T) {
	d := New()
	d.AddCheck(Check{
		Name: "unfixable", Severity: SeverityWarning,
		Run: func(ctx context.Context) (CheckStatus, string) { return StatusError, "still broken" },
		Fix: func(ctx context.Context) (bool, string) { return false, "cannot fix" },
	})

	report := d.RunAll(context.Background())
	r := report.Results[0]
	if r.Status != StatusError {
		t.Errorf("status = %s, want error", r.Status)
	}
	if !r.FixApplied {
		t.Error("FixApplied should be true (fix was attempted)")
	}
}

func TestRunCheck_NoFixOnOK(t *testing.T) {
	d := New()
	fixCalled := false
	d.AddCheck(Check{
		Name: "healthy", Severity: SeverityWarning,
		Run: func(ctx context.Context) (CheckStatus, string) { return StatusOK, "fine" },
		Fix: func(ctx context.Context) (bool, string) {
			fixCalled = true
			return true, "should not run"
		},
	})

	d.RunAll(context.Background())
	if fixCalled {
		t.Error("Fix should not be called when status is OK")
	}
}

func TestRunCheck_NoFixFunction(t *testing.T) {
	d := New()
	d.AddCheck(Check{
		Name: "no-fix", Severity: SeverityWarning,
		Run: func(ctx context.Context) (CheckStatus, string) { return StatusWarning, "degraded" },
	})

	report := d.RunAll(context.Background())
	if report.Results[0].FixApplied {
		t.Error("FixApplied should be false when no Fix function")
	}
}

func TestRunCheck_Duration(t *testing.T) {
	d := New()
	d.AddCheck(Check{
		Name: "slow", Severity: SeverityInfo,
		Run: func(ctx context.Context) (CheckStatus, string) {
			time.Sleep(20 * time.Millisecond)
			return StatusOK, "done"
		},
	})

	report := d.RunAll(context.Background())
	if report.Results[0].Duration < 10*time.Millisecond {
		t.Errorf("Duration = %v, expected >= 10ms", report.Results[0].Duration)
	}
}

func TestRunCheck_Suggestion(t *testing.T) {
	d := New()
	d.AddCheck(Check{
		Name: "with-suggestion", Severity: SeverityInfo,
		Suggestion: "try this",
		Run:        func(ctx context.Context) (CheckStatus, string) { return StatusError, "fail" },
	})

	report := d.RunAll(context.Background())
	if report.Results[0].Suggestion != "try this" {
		t.Errorf("Suggestion = %q", report.Results[0].Suggestion)
	}
}

// ============================================================
// Report.Format 测试
// ============================================================

func TestReport_Format_Basic(t *testing.T) {
	report := &Report{
		Results: []CheckResult{
			{Name: "check1", Status: StatusOK, Message: "all good", Duration: 10 * time.Millisecond},
			{Name: "check2", Status: StatusWarning, Message: "hmm", Duration: 5 * time.Millisecond},
			{Name: "check3", Status: StatusError, Message: "bad", Suggestion: "fix it manually"},
		},
		TotalOK: 1, TotalWarn: 1, TotalError: 1,
	}

	out := report.Format()
	if !strings.Contains(out, "Tommy-Cat Doctor") {
		t.Error("should contain header")
	}
	if !strings.Contains(out, "check1") || !strings.Contains(out, "check2") || !strings.Contains(out, "check3") {
		t.Error("should contain all check names")
	}
	if !strings.Contains(out, "fix it manually") {
		t.Error("should contain suggestion for error")
	}
	if !strings.Contains(out, "1 OK") {
		t.Error("should contain summary")
	}
}

func TestReport_Format_WithFixes(t *testing.T) {
	report := &Report{
		Results: []CheckResult{
			{Name: "fixed", Status: StatusOK, Message: "ok", FixApplied: true, FixMessage: "auto-fixed it"},
		},
		TotalOK: 1, FixesApplied: 1,
	}

	out := report.Format()
	if !strings.Contains(out, "auto-fixed it") {
		t.Error("should contain fix message")
	}
	if !strings.Contains(out, "1 auto-fixed") {
		t.Error("should contain fix count")
	}
}

func TestReport_Format_NoSuggestions(t *testing.T) {
	report := &Report{
		Results:    []CheckResult{{Name: "ok", Status: StatusOK, Message: "fine"}},
		TotalOK:    1,
	}
	out := report.Format()
	if strings.Contains(out, "Manual action required") {
		t.Error("should not contain manual action section when no errors")
	}
}

// ============================================================
// statusIcon 测试
// ============================================================

func TestStatusIcon(t *testing.T) {
	tests := []struct {
		status CheckStatus
		want   string
	}{
		{StatusOK, "OK"},
		{StatusWarning, "WARN"},
		{StatusError, "ERROR"},
		{StatusSkipped, "SKIP"},
		{CheckStatus("unknown"), "?"},
	}
	for _, tt := range tests {
		if got := statusIcon(tt.status); got != tt.want {
			t.Errorf("statusIcon(%q) = %q, want %q", tt.status, got, tt.want)
		}
	}
}

// ============================================================
// checkConfigFile 测试
// ============================================================

func TestCheckConfigFile_Valid(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	os.WriteFile(p, []byte("llm:\n  providers:\n    test:\n      base_url: http://x\n      api_key: k\n      model: m\n"), 0644)

	check := checkConfigFile(DoctorConfig{ConfigPath: p})
	status, msg := check.Run(context.Background())
	if status != StatusOK {
		t.Errorf("status = %s, want ok; msg = %s", status, msg)
	}
	if !strings.Contains(msg, "1 providers") {
		t.Errorf("msg should mention provider count: %s", msg)
	}
}

func TestCheckConfigFile_Missing(t *testing.T) {
	check := checkConfigFile(DoctorConfig{ConfigPath: "/nonexistent/config.yaml"})
	status, _ := check.Run(context.Background())
	if status != StatusError {
		t.Errorf("status = %s, want error", status)
	}
}

func TestCheckConfigFile_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	os.WriteFile(p, []byte("{{invalid"), 0644)

	check := checkConfigFile(DoctorConfig{ConfigPath: p})
	status, _ := check.Run(context.Background())
	if status != StatusError {
		t.Errorf("status = %s, want error", status)
	}
}

func TestCheckConfigFile_NoProviders(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	os.WriteFile(p, []byte("llm:\n  providers: {}\n"), 0644)

	check := checkConfigFile(DoctorConfig{ConfigPath: p})
	status, _ := check.Run(context.Background())
	if status != StatusWarning {
		t.Errorf("status = %s, want warning", status)
	}
}

func TestCheckConfigFile_Fix_CreatesDefault(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "config.yaml")

	check := checkConfigFile(DoctorConfig{ConfigPath: p})
	fixed, msg := check.Fix(context.Background())
	if !fixed {
		t.Errorf("Fix should succeed, msg = %s", msg)
	}
	if _, err := os.Stat(p); err != nil {
		t.Error("config file should exist after fix")
	}
}

func TestCheckConfigFile_Fix_ExistingFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	os.WriteFile(p, []byte("bad content"), 0644)

	check := checkConfigFile(DoctorConfig{ConfigPath: p})
	fixed, _ := check.Fix(context.Background())
	if fixed {
		t.Error("Fix should not succeed for existing file with errors")
	}
}

// ============================================================
// checkSecurityPolicy 测试
// ============================================================

func TestCheckSecurityPolicy_Valid(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "policy.yaml")
	os.WriteFile(p, []byte("policies:\n  - id: test\n    name: Test\n"), 0644)

	check := checkSecurityPolicy(DoctorConfig{PolicyPath: p})
	status, msg := check.Run(context.Background())
	if status != StatusOK {
		t.Errorf("status = %s, want ok; msg = %s", status, msg)
	}
	if !strings.Contains(msg, "1 policies") {
		t.Errorf("msg should mention policy count: %s", msg)
	}
}

func TestCheckSecurityPolicy_Missing(t *testing.T) {
	check := checkSecurityPolicy(DoctorConfig{PolicyPath: "/nonexistent/policy.yaml"})
	status, _ := check.Run(context.Background())
	if status != StatusWarning {
		t.Errorf("status = %s, want warning", status)
	}
}

func TestCheckSecurityPolicy_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "policy.yaml")
	os.WriteFile(p, []byte("{{bad"), 0644)

	check := checkSecurityPolicy(DoctorConfig{PolicyPath: p})
	status, _ := check.Run(context.Background())
	if status != StatusError {
		t.Errorf("status = %s, want error", status)
	}
}

func TestCheckSecurityPolicy_Fix(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "policy.yaml")

	check := checkSecurityPolicy(DoctorConfig{PolicyPath: p})
	fixed, _ := check.Fix(context.Background())
	if !fixed {
		t.Error("Fix should create default policy")
	}
	if _, err := os.Stat(p); err != nil {
		t.Error("policy file should exist after fix")
	}
}

// ============================================================
// checkSkillStore 测试
// ============================================================

func TestCheckSkillStore_ValidArray(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "skills.json")
	os.WriteFile(p, []byte(`[{"name":"s1"},{"name":"s2"}]`), 0644)

	check := checkSkillStore(DoctorConfig{SkillStorePath: p})
	status, msg := check.Run(context.Background())
	if status != StatusOK {
		t.Errorf("status = %s, want ok; msg = %s", status, msg)
	}
}

func TestCheckSkillStore_ValidMap(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "skills.json")
	os.WriteFile(p, []byte(`{"s1":{},"s2":{}}`), 0644)

	check := checkSkillStore(DoctorConfig{SkillStorePath: p})
	status, _ := check.Run(context.Background())
	if status != StatusOK {
		t.Errorf("status = %s, want ok for map format", status)
	}
}

func TestCheckSkillStore_Missing(t *testing.T) {
	dir := t.TempDir()
	check := checkSkillStore(DoctorConfig{SkillStorePath: filepath.Join(dir, "skills.json")})
	status, _ := check.Run(context.Background())
	if status != StatusWarning {
		t.Errorf("status = %s, want warning", status)
	}
}

func TestCheckSkillStore_Corrupted(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "skills.json")
	os.WriteFile(p, []byte(`{invalid`), 0644)

	check := checkSkillStore(DoctorConfig{SkillStorePath: p})
	status, _ := check.Run(context.Background())
	if status != StatusError {
		t.Errorf("status = %s, want error", status)
	}
}

func TestCheckSkillStore_Fix_CreatesEmpty(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "skills.json")

	check := checkSkillStore(DoctorConfig{SkillStorePath: p})
	fixed, _ := check.Fix(context.Background())
	if !fixed {
		t.Error("Fix should create empty skill store")
	}
	data, _ := os.ReadFile(p)
	if string(data) != "[]" {
		t.Errorf("expected [], got %s", data)
	}
}

func TestCheckSkillStore_Fix_ResetsCorrupted(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "skills.json")
	os.WriteFile(p, []byte(`{bad json`), 0644)

	check := checkSkillStore(DoctorConfig{SkillStorePath: p})
	fixed, msg := check.Fix(context.Background())
	if !fixed {
		t.Errorf("Fix should reset corrupted file, msg = %s", msg)
	}
	if _, err := os.Stat(p + ".bak"); err != nil {
		t.Error("backup file should exist")
	}
	data, _ := os.ReadFile(p)
	if string(data) != "[]" {
		t.Errorf("expected [], got %s", data)
	}
}

// ============================================================
// checkWorkDirectory 测试
// ============================================================

func TestCheckWorkDirectory_Valid(t *testing.T) {
	dir := t.TempDir()
	check := checkWorkDirectory(DoctorConfig{WorkDir: dir})
	status, msg := check.Run(context.Background())
	if status != StatusOK && status != StatusWarning {
		t.Errorf("status = %s, want ok or warning; msg = %s", status, msg)
	}
	if !strings.Contains(msg, "writable") {
		t.Errorf("msg should mention writable: %s", msg)
	}
}

func TestCheckWorkDirectory_Missing(t *testing.T) {
	check := checkWorkDirectory(DoctorConfig{WorkDir: "/nonexistent/dir"})
	status, _ := check.Run(context.Background())
	if status != StatusError {
		t.Errorf("status = %s, want error", status)
	}
}

func TestCheckWorkDirectory_NotADirectory(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "file.txt")
	os.WriteFile(f, []byte("x"), 0644)

	check := checkWorkDirectory(DoctorConfig{WorkDir: f})
	status, _ := check.Run(context.Background())
	if status != StatusError {
		t.Errorf("status = %s, want error for file path", status)
	}
}

func TestCheckWorkDirectory_Fix(t *testing.T) {
	dir := t.TempDir()
	newDir := filepath.Join(dir, "workdir")
	check := checkWorkDirectory(DoctorConfig{WorkDir: newDir})
	fixed, _ := check.Fix(context.Background())
	if !fixed {
		t.Error("Fix should create directory")
	}
	info, err := os.Stat(newDir)
	if err != nil || !info.IsDir() {
		t.Error("directory should exist after fix")
	}
}

// ============================================================
// checkSandbox 测试
// ============================================================

func TestCheckSandbox_None(t *testing.T) {
	check := checkSandbox(DoctorConfig{SandboxType: "none"})
	status, msg := check.Run(context.Background())
	if status != StatusOK {
		t.Errorf("status = %s, want ok; msg = %s", status, msg)
	}
}

func TestCheckSandbox_Empty(t *testing.T) {
	check := checkSandbox(DoctorConfig{})
	status, _ := check.Run(context.Background())
	if status != StatusOK {
		t.Errorf("status = %s, want ok for empty type", status)
	}
}

func TestCheckSandbox_Native_ProbeOK(t *testing.T) {
	check := checkSandbox(DoctorConfig{
		SandboxType:  "native",
		SandboxProbe: func() error { return nil },
	})
	status, _ := check.Run(context.Background())
	if status != StatusOK {
		t.Errorf("status = %s, want ok", status)
	}
}

func TestCheckSandbox_Native_ProbeFail(t *testing.T) {
	check := checkSandbox(DoctorConfig{
		SandboxType:  "native",
		SandboxProbe: func() error { return fmt.Errorf("no namespace") },
	})
	status, msg := check.Run(context.Background())
	if status != StatusWarning {
		t.Errorf("status = %s, want warning; msg = %s", status, msg)
	}
}

func TestCheckSandbox_Container_NoProbe(t *testing.T) {
	check := checkSandbox(DoctorConfig{SandboxType: "container"})
	status, _ := check.Run(context.Background())
	if status != StatusWarning {
		t.Errorf("status = %s, want warning when no probe", status)
	}
}

// ============================================================
// checkMemoryStorage 测试
// ============================================================

func TestCheckMemoryStorage_File(t *testing.T) {
	dir := t.TempDir()
	memDir := filepath.Join(dir, "memories")
	check := checkMemoryStorage(DoctorConfig{MemoryType: "file", MemoryPath: memDir})
	status, _ := check.Run(context.Background())
	if status != StatusOK {
		t.Errorf("status = %s, want ok", status)
	}
	if _, err := os.Stat(memDir); err != nil {
		t.Error("memory dir should be created")
	}
}

func TestCheckMemoryStorage_File_Default(t *testing.T) {
	check := checkMemoryStorage(DoctorConfig{MemoryType: "file"})
	status, _ := check.Run(context.Background())
	// 默认路径 data/memories，可能成功也可能失败（取决于工作目录权限）
	if status != StatusOK && status != StatusError {
		t.Errorf("status = %s, unexpected", status)
	}
}

func TestCheckMemoryStorage_Sqlite(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "sub", "memory.db")
	check := checkMemoryStorage(DoctorConfig{MemoryType: "sqlite", MemoryPath: dbPath})
	status, _ := check.Run(context.Background())
	if status != StatusOK {
		t.Errorf("status = %s, want ok", status)
	}
}

func TestCheckMemoryStorage_Remote_NoURL(t *testing.T) {
	check := checkMemoryStorage(DoctorConfig{MemoryType: "remote"})
	status, _ := check.Run(context.Background())
	if status != StatusError {
		t.Errorf("status = %s, want error", status)
	}
}

// ============================================================
// checkResources 测试
// ============================================================

func TestCheckResources(t *testing.T) {
	check := checkResources()
	status, msg := check.Run(context.Background())
	if status != StatusOK && status != StatusWarning {
		t.Errorf("status = %s; msg = %s", status, msg)
	}
	if !strings.Contains(msg, "mem=") {
		t.Errorf("msg should contain memory info: %s", msg)
	}
	if !strings.Contains(msg, "goroutines=") {
		t.Errorf("msg should contain goroutine count: %s", msg)
	}
}

// ============================================================
// checkToolAvailability 测试
// ============================================================

func TestCheckToolAvailability(t *testing.T) {
	check := checkToolAvailability()
	status, msg := check.Run(context.Background())
	if status != StatusOK && status != StatusWarning {
		t.Errorf("status = %s; msg = %s", status, msg)
	}
	// sh 在 unix 系统上应该总是可用的
	if !strings.Contains(msg, "sh") {
		t.Errorf("msg should mention sh: %s", msg)
	}
}

// ============================================================
// RegisterAllChecks 集成测试
// ============================================================

func TestRegisterAllChecks(t *testing.T) {
	d := New()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfgPath, []byte("llm:\n  providers:\n    test:\n      base_url: http://x\n      api_key: k\n      model: m\n"), 0644)
	policyPath := filepath.Join(dir, "policy.yaml")
	os.WriteFile(policyPath, []byte("policies: []\n"), 0644)

	RegisterAllChecks(d, DoctorConfig{
		ConfigPath:     cfgPath,
		PolicyPath:     policyPath,
		SkillStorePath: filepath.Join(dir, "skills.json"),
		WorkDir:        dir,
		Providers: map[string]ProviderCheckInfo{
			"test": {BaseURL: "http://127.0.0.1:1", APIKey: "k", Model: "m"},
		},
		MemoryType:  "file",
		MemoryPath:  filepath.Join(dir, "memories"),
		SandboxType: "none",
	})

	if len(d.checks) != 10 {
		t.Errorf("expected 10 checks, got %d", len(d.checks))
	}

	// 验证检查项名称不重复
	seen := make(map[string]bool)
	for _, c := range d.checks {
		if seen[c.Name] {
			t.Errorf("duplicate check name: %s", c.Name)
		}
		seen[c.Name] = true
	}
}
