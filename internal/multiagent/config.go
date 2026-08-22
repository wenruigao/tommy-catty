package multiagent

import "time"

// OrchestratorConfig 编排器配置。
type OrchestratorConfig struct {
	// MaxWorkers 最大并发 Worker 数（默认 5）
	MaxWorkers int
	// MaxSubTasks 单次分解最大子任务数（默认 10）
	MaxSubTasks int
	// WorkerTimeout 单 Worker 执行超时（默认 120s）
	WorkerTimeout time.Duration
	// SummaryMaxTokens 汇总阶段最大 token（默认 4096）。
	// 注：当前 engine.LLMClient 接口不支持 per-call MaxTokens，
	// 此字段为预留配置，待接口扩展后生效。
	SummaryMaxTokens int
}

// DefaultOrchestratorConfig 返回编排器默认配置。
func DefaultOrchestratorConfig() OrchestratorConfig {
	return OrchestratorConfig{
		MaxWorkers:       5,
		MaxSubTasks:      10,
		WorkerTimeout:    120 * time.Second,
		SummaryMaxTokens: 4096,
	}
}

// SubTask 编排器分解出的单个执行单元。
type SubTask struct {
	// ID 子任务唯一标识
	ID string `json:"id"`
	// Role 执行角色名
	Role string `json:"role"`
	// Goal 子任务目标描述
	Goal string `json:"goal"`
	// DependsOn 依赖的子任务 ID 列表
	DependsOn []string `json:"depends_on"`
}

// Plan 编排器的执行计划。
type Plan struct {
	// Goal 原始目标
	Goal string `json:"goal"`
	// SubTasks 子任务列表
	SubTasks []SubTask `json:"subtasks"`
	// Strategy 编排策略：parallel | sequential | mixed
	Strategy string `json:"strategy"`
}

// OrchestratorResult 编排器执行的完整结果。
type OrchestratorResult struct {
	// Plan 执行计划
	Plan *Plan `json:"plan"`
	// Results 各子任务结果（key 为 SubTask.ID）
	Results map[string]*SubTaskResult `json:"results"`
	// FinalAnswer 汇总后的最终答案
	FinalAnswer string `json:"final_answer"`
	// TotalTokens 总 token 消耗（含所有 Worker + 编排 LLM 调用）
	TotalTokens int `json:"total_tokens"`
	// Duration 总耗时
	Duration time.Duration `json:"duration"`
}

// WorkerStat 单个 Worker 的统计信息。
type WorkerStat struct {
	SubTaskID  string        `json:"subtask_id"`
	Role       string        `json:"role"`
	Status     string        `json:"status"`
	TokenUsage int           `json:"token_usage"`
	Duration   time.Duration `json:"duration"`
	Steps      int           `json:"steps"`
}
