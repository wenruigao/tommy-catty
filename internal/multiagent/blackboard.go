package multiagent

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// SubTaskResult 记录单个子任务的执行结果。
type SubTaskResult struct {
	// SubTaskID 子任务标识
	SubTaskID string `json:"subtask_id"`
	// Role 执行角色名
	Role string `json:"role"`
	// Status 执行状态：success | failed | timeout
	Status string `json:"status"`
	// Output 执行输出（最终答案）
	Output string `json:"output"`
	// Error 错误信息（失败时）
	Error string `json:"error,omitempty"`
	// TokenUsage token 消耗
	TokenUsage int `json:"token_usage"`
	// Duration 执行耗时
	Duration time.Duration `json:"duration"`
	// Steps 执行步数
	Steps int `json:"steps"`
}

// Blackboard 子任务间共享结果的黑板。
// Worker 执行完毕后将结果写入，下游 Worker 通过依赖查询获取上游结果。
type Blackboard struct {
	mu      sync.RWMutex
	results map[string]*SubTaskResult
}

// NewBlackboard 创建空的黑板。
func NewBlackboard() *Blackboard {
	return &Blackboard{
		results: make(map[string]*SubTaskResult),
	}
}

// Put 写入子任务结果。
func (b *Blackboard) Put(taskID string, result *SubTaskResult) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.results[taskID] = result
}

// Get 获取子任务结果，不存在返回 false。
func (b *Blackboard) Get(taskID string) (*SubTaskResult, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	r, ok := b.results[taskID]
	return r, ok
}

// GatherContext 收集依赖子任务的结果摘要，拼接为上下文文本。
// 仅包含成功完成的任务结果；失败任务标注失败原因。
func (b *Blackboard) GatherContext(dependsOn []string) string {
	if len(dependsOn) == 0 {
		return ""
	}
	b.mu.RLock()
	defer b.mu.RUnlock()

	var sb strings.Builder
	sb.WriteString("以下是上游任务已完成的结果，供你参考：\n\n")
	for _, id := range dependsOn {
		r, ok := b.results[id]
		if !ok {
			sb.WriteString(fmt.Sprintf("### 任务 %s\n状态: 未完成（结果不可用）\n\n", id))
			continue
		}
		sb.WriteString(fmt.Sprintf("### 任务 %s (%s)\n状态: %s\n", id, r.Role, r.Status))
		if r.Error != "" {
			sb.WriteString(fmt.Sprintf("错误: %s\n", r.Error))
		}
		if r.Output != "" {
			// 截断过长输出，避免上下文膨胀
			output := r.Output
			if len(output) > 2000 {
				output = output[:2000] + "\n...（已截断）"
			}
			sb.WriteString(fmt.Sprintf("结果:\n%s\n", output))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// AllResults 返回所有已写入的结果。
func (b *Blackboard) AllResults() map[string]*SubTaskResult {
	b.mu.RLock()
	defer b.mu.RUnlock()
	cp := make(map[string]*SubTaskResult, len(b.results))
	for k, v := range b.results {
		cp[k] = v
	}
	return cp
}
