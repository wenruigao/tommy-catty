package security

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// auditEntry 单条审计日志记录（JSONL 格式落盘）。
type auditEntry struct {
	Time       string   `json:"time"`
	UserID     string   `json:"user_id"`
	Checkpoint string   `json:"checkpoint"`
	ToolName   string   `json:"tool_name,omitempty"`
	ToolRisk   int      `json:"tool_risk,omitempty"`
	Effect     string   `json:"effect"`
	Policies   []string `json:"policies,omitempty"`
	Content    string   `json:"content"`
}

// AuditLogger 安全审计日志记录器，以 JSONL 追加写方式落盘。
// 并发安全（内部互斥锁保护）；写入失败静默忽略（审计不阻塞主流程）。
type AuditLogger struct {
	mu   sync.Mutex
	file *os.File
}

// NewAuditLogger 创建审计记录器（自动创建所在目录）。
// path 为空时返回 (nil, nil)，表示禁用审计。
func NewAuditLogger(path string) (*AuditLogger, error) {
	if path == "" {
		return nil, nil
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("创建审计日志目录失败: %w", err)
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("打开审计日志失败: %w", err)
	}
	return &AuditLogger{file: f}, nil
}

// Close 关闭审计日志文件。nil 接收者安全（可直接 defer Close）。
func (l *AuditLogger) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file.Close()
}

// Log 写入一条审计记录（内容截断至 500 字符，避免日志膨胀）。
func (l *AuditLogger) Log(entry auditEntry) {
	if l == nil {
		return
	}
	if runes := []rune(entry.Content); len(runes) > 500 {
		entry.Content = string(runes[:500]) + "...[截断]"
	}
	if entry.Time == "" {
		entry.Time = time.Now().Format(time.RFC3339Nano)
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.file.Write(append(data, '\n'))
}

// LogAudit 按审计口径记录一次检查点评估：
//  1. 所有命中策略决策的检查点必须记录，使审批/拒绝/脱敏决策可追溯；
//  2. L2 及以上风险等级的工具调用（tool_call）即使无策略命中也记录，
//     满足"L2+ 操作记录操作人与输入"的审计要求。
func (l *AuditLogger) LogAudit(cp Checkpoint, decisions []Decision) {
	if l == nil {
		return
	}
	if len(decisions) == 0 && !(cp.Type == "tool_call" && cp.ToolRisk >= 2) {
		return
	}

	effect := "allow"
	policies := make([]string, 0, len(decisions))
	for _, d := range decisions {
		policies = append(policies, d.PolicyID+":"+string(d.Effect))
		switch d.Effect {
		case EffectDeny:
			effect = "deny"
		case EffectRequireApproval:
			if effect != "deny" {
				effect = "require_approval"
			}
		case EffectRedact:
			if effect == "allow" {
				effect = "redact"
			}
		case EffectThrottle:
			if effect == "allow" {
				effect = "throttle"
			}
		}
	}

	l.Log(auditEntry{
		UserID:     cp.UserID,
		Checkpoint: cp.Type,
		ToolName:   cp.ToolName,
		ToolRisk:   cp.ToolRisk,
		Effect:     effect,
		Policies:   policies,
		Content:    cp.Content,
	})
}
