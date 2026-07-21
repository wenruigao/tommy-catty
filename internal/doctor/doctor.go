// Package doctor 提供 Agent 健康自检能力，自动检测问题并尝试修复
package doctor

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// CheckStatus 检查状态
type CheckStatus string

const (
	StatusOK      CheckStatus = "ok"      // 健康
	StatusWarning CheckStatus = "warning" // 降级但可用
	StatusError   CheckStatus = "error"   // 异常，需要关注
	StatusSkipped CheckStatus = "skipped" // 不适用
)

// Severity 严重级别
type Severity int

const (
	SeverityInfo     Severity = 0 // 信息性
	SeverityWarning  Severity = 1 // 警告
	SeverityCritical Severity = 2 // 严重
)

// CheckResult 单项检查结果
type CheckResult struct {
	Name       string        `json:"name"`        // 检查项名称
	Category   string        `json:"category"`    // 分类
	Status     CheckStatus   `json:"status"`      // 结果状态
	Severity   Severity      `json:"severity"`    // 严重级别
	Message    string        `json:"message"`     // 人类可读描述
	Duration   time.Duration `json:"duration"`    // 检查耗时
	FixApplied bool          `json:"fix_applied"` // 是否执行了自动修复
	FixMessage string        `json:"fix_message"` // 修复结果描述
	Suggestion string        `json:"suggestion"`  // 手动修复建议
}

// Check 定义一个检查项
type Check struct {
	Name     string
	Category string
	Severity Severity
	// Run 执行检查，返回状态和消息
	Run func(ctx context.Context) (CheckStatus, string)
	// Fix 尝试自动修复（可选），返回是否修复成功和描述
	Fix func(ctx context.Context) (bool, string)
	// Suggestion 手动修复建议
	Suggestion string
}

// Report 完整诊断报告
type Report struct {
	Results    []CheckResult `json:"results"`
	StartTime  time.Time     `json:"start_time"`
	EndTime    time.Time     `json:"end_time"`
	TotalOK    int           `json:"total_ok"`
	TotalWarn  int           `json:"total_warn"`
	TotalError int           `json:"total_error"`
	FixesApplied int         `json:"fixes_applied"`
}

// Doctor 健康自检引擎
type Doctor struct {
	checks []Check
}

// New 创建 Doctor 实例
func New() *Doctor {
	return &Doctor{}
}

// AddCheck 注册一个检查项
func (d *Doctor) AddCheck(check Check) {
	d.checks = append(d.checks, check)
}

// RunAll 并行执行所有检查项，生成诊断报告
func (d *Doctor) RunAll(ctx context.Context) *Report {
	report := &Report{
		StartTime: time.Now(),
		Results:   make([]CheckResult, len(d.checks)),
	}

	var wg sync.WaitGroup
	for i, check := range d.checks {
		wg.Add(1)
		go func(idx int, c Check) {
			defer wg.Done()
			result := d.runCheck(ctx, c)
			report.Results[idx] = result
		}(i, check)
	}
	wg.Wait()

	// 汇总统计
	for _, r := range report.Results {
		switch r.Status {
		case StatusOK, StatusSkipped:
			report.TotalOK++
		case StatusWarning:
			report.TotalWarn++
		case StatusError:
			report.TotalError++
		}
		if r.FixApplied {
			report.FixesApplied++
		}
	}

	report.EndTime = time.Now()
	return report
}

// RunQuick 仅执行 Critical 级别的检查（启动时快速自检）
func (d *Doctor) RunQuick(ctx context.Context) *Report {
	report := &Report{StartTime: time.Now()}

	for _, check := range d.checks {
		if check.Severity < SeverityCritical {
			continue
		}
		result := d.runCheck(ctx, check)
		report.Results = append(report.Results, result)
		switch result.Status {
		case StatusOK, StatusSkipped:
			report.TotalOK++
		case StatusWarning:
			report.TotalWarn++
		case StatusError:
			report.TotalError++
		}
	}

	report.EndTime = time.Now()
	return report
}

// runCheck 执行单个检查项
func (d *Doctor) runCheck(ctx context.Context, check Check) CheckResult {
	start := time.Now()

	status, message := check.Run(ctx)

	result := CheckResult{
		Name:       check.Name,
		Category:   check.Category,
		Status:     status,
		Severity:   check.Severity,
		Message:    message,
		Duration:   time.Since(start),
		Suggestion: check.Suggestion,
	}

	// 如果检查失败且有修复函数，尝试自动修复
	if (status == StatusError || status == StatusWarning) && check.Fix != nil {
		fixed, fixMsg := check.Fix(ctx)
		result.FixApplied = true
		result.FixMessage = fixMsg
		if fixed {
			// 修复后重新检查
			newStatus, newMsg := check.Run(ctx)
			if newStatus == StatusOK {
				result.Status = StatusOK
				result.Message = newMsg
			}
		}
	}

	return result
}

// Format 格式化报告为终端友好的输出
func (r *Report) Format() string {
	var sb strings.Builder

	sb.WriteString("\n  Tommy-Cat Doctor v0.1.0\n")
	sb.WriteString("  ========================\n\n")

	for _, result := range r.Results {
		icon := statusIcon(result.Status)
		sb.WriteString(fmt.Sprintf("  [%s] %-22s %s", icon, result.Name, result.Message))
		if result.Duration > 0 {
			sb.WriteString(fmt.Sprintf(" (%s)", result.Duration.Round(time.Millisecond)))
		}
		sb.WriteString("\n")

		if result.FixApplied && result.FixMessage != "" {
			sb.WriteString(fmt.Sprintf("         ↳ Fixed: %s\n", result.FixMessage))
		}
	}

	sb.WriteString(fmt.Sprintf("\n  Summary: %d OK, %d Warning, %d Error",
		r.TotalOK, r.TotalWarn, r.TotalError))
	if r.FixesApplied > 0 {
		sb.WriteString(fmt.Sprintf(", %d auto-fixed", r.FixesApplied))
	}
	sb.WriteString("\n")

	// 输出需要手动处理的建议
	var suggestions []string
	for _, result := range r.Results {
		if result.Status == StatusError && result.Suggestion != "" {
			suggestions = append(suggestions, result.Suggestion)
		}
	}
	if len(suggestions) > 0 {
		sb.WriteString("\n  Manual action required:\n")
		for _, s := range suggestions {
			sb.WriteString(fmt.Sprintf("    - %s\n", s))
		}
	}

	sb.WriteString("\n")
	return sb.String()
}

// statusIcon 返回状态对应的图标
func statusIcon(s CheckStatus) string {
	switch s {
	case StatusOK:
		return "OK"
	case StatusWarning:
		return "WARN"
	case StatusError:
		return "ERROR"
	case StatusSkipped:
		return "SKIP"
	default:
		return "?"
	}
}
