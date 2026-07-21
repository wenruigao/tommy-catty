package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/tommy-cat/agent/internal/llm"

	"github.com/google/uuid"
)

// Run 执行 ReAct 循环，处理用户目标并返回完整的执行追踪。
// 循环流程：构建提示 -> 调用 LLM -> 处理工具调用 -> 追加观察 -> 重复直到得出最终答案。
func (e *Engine) Run(ctx context.Context, goal string) (*ExecutionTrace, error) {
	// 初始化执行追踪
	trace := &ExecutionTrace{
		TaskID:    uuid.New().String(),
		Goal:      goal,
		Steps:     make([]StepResult, 0),
		StartTime: time.Now(),
	}

	e.state = StatePlanning

	// 构建初始消息列表
	messages := e.buildInitialMessages(goal)

	// 获取可用工具定义
	toolDefs := e.toolRegistry.ToToolDefs()

	// ReAct 主循环
	for i := 0; i < e.maxIterations; i++ {
		// 检查上下文是否已取消
		select {
		case <-ctx.Done():
			trace.EndTime = time.Now()
			trace.Error = "context cancelled: " + ctx.Err().Error()
			e.state = StateError
			return trace, ctx.Err()
		default:
		}

		// 调用 LLM 获取响应
		e.state = StatePlanning
		resp, err := e.llmGateway.Chat(ctx, messages, toolDefs)
		if err != nil {
			trace.EndTime = time.Now()
			trace.Error = fmt.Sprintf("LLM 调用失败 (迭代 %d): %v", i+1, err)
			e.state = StateError
			return trace, fmt.Errorf("llm chat failed at iteration %d: %w", i+1, err)
		}

		// 累计 token 消耗
		trace.TokenUsage += resp.Usage.TotalTokens

		// 判断是否有工具调用
		if len(resp.ToolCalls) == 0 {
			// 没有工具调用，视为最终答案
			e.state = StateFinishing
			step := StepResult{
				Thought:     resp.Content,
				IsFinal:     true,
				FinalAnswer: resp.Content,
			}
			trace.Steps = append(trace.Steps, step)

			// 将最终对话存入记忆
			e.storeToMemory(messages, resp.Content)

			trace.EndTime = time.Now()
			e.state = StateIdle
			return trace, nil
		}

		// 有工具调用，逐个执行
		e.state = StateExecuting

		// 将 assistant 的回复内容追加到对话历史（如果有思考内容）
		if resp.Content != "" {
			messages = append(messages, llm.Message{
				Role:    "assistant",
				Content: resp.Content,
			})
		}

		// 处理每个工具调用
		for _, tc := range resp.ToolCalls {
			// 解析 JSON 格式的参数
			args := parseToolArgs(tc.Arguments)

			step := StepResult{
				Thought:     resp.Content,
				Action:      tc.Name,
				ActionInput: args,
			}

			// 执行工具调用
			result, err := e.toolRegistry.Call(ctx, tc.Name, args)
			if err != nil {
				step.Observation = fmt.Sprintf("工具调用失败: %v", err)
			} else if result.Error != "" {
				step.Observation = fmt.Sprintf("工具执行错误: %s", result.Error)
			} else {
				step.Observation = result.Output
			}

			trace.Steps = append(trace.Steps, step)

			// 将工具结果追加到对话历史
			e.state = StateObserving
			toolMsg := llm.Message{
				Role:    "tool",
				Content: fmt.Sprintf("[%s] %s", tc.Name, step.Observation),
			}
			messages = append(messages, toolMsg)
		}
	}

	// 超过最大迭代次数
	trace.EndTime = time.Now()
	trace.Error = fmt.Sprintf("超过最大迭代次数 (%d)，未能得出最终答案", e.maxIterations)
	e.state = StateError
	return trace, fmt.Errorf("max iterations (%d) exceeded without final answer", e.maxIterations)
}

// buildInitialMessages 构建初始消息列表，包含系统提示、记忆上下文和用户目标。
func (e *Engine) buildInitialMessages(goal string) []llm.Message {
	messages := make([]llm.Message, 0)

	// 1. 系统提示词
	messages = append(messages, llm.Message{
		Role:    "system",
		Content: e.systemPrompt,
	})

	// 2. 从记忆中获取历史上下文
	if e.memory != nil {
		contextMsgs := e.memory.GetContext(10) // 获取最近 10 条历史消息
		messages = append(messages, contextMsgs...)
	}

	// 3. 用户目标
	messages = append(messages, llm.Message{
		Role:    "user",
		Content: goal,
	})

	return messages
}

// storeToMemory 将本次对话的关键消息存入记忆系统。
func (e *Engine) storeToMemory(messages []llm.Message, finalAnswer string) {
	if e.memory == nil {
		return
	}

	// 存储用户消息和最终回复
	toStore := make([]llm.Message, 0, 2)
	for _, msg := range messages {
		if msg.Role == "user" {
			toStore = append(toStore, msg)
		}
	}
	toStore = append(toStore, llm.Message{
		Role:    "assistant",
		Content: finalAnswer,
	})
	e.memory.Store(toStore)
}

// parseToolArgs 将 JSON 字符串格式的工具参数解析为 map。
// 如果解析失败，返回空 map。
func parseToolArgs(argsJSON string) map[string]interface{} {
	if argsJSON == "" {
		return make(map[string]interface{})
	}
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return make(map[string]interface{})
	}
	return args
}
