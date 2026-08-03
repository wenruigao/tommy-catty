package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/tommy-cat/agent/internal/ctxmgr"
	"github.com/tommy-cat/agent/internal/llm"

	"github.com/google/uuid"
)

// Run 执行 ReAct 循环，处理用户目标并返回完整的执行追踪。
// 循环流程：构建提示 -> 上下文压缩 -> 调用 LLM -> 处理工具调用 -> 追加观察 -> 反思 -> 重复直到得出最终答案。
// Engine 是无状态的，state 作为局部变量在本次调用内跟踪，支持并发安全。
func (e *Engine) Run(ctx context.Context, goal string) (*ExecutionTrace, error) {
	// 初始化执行追踪
	trace := &ExecutionTrace{
		TaskID:    uuid.New().String(),
		Goal:      goal,
		Steps:     make([]StepResult, 0),
		StartTime: time.Now(),
	}

	// 初始化反思状态
	var replanState *ReplanState
	var reflCfg ReflectionConfig
	if e.reflection != nil {
		reflCfg = *e.reflection
		replanState = &ReplanState{}
	}

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
			return trace, ctx.Err()
		default:
		}

		// ★ 上下文压缩：每次调用 LLM 前执行上下文管理
		messages = e.applyContextManagement(ctx, messages)

		// 调用 LLM 获取响应
		resp, err := e.llmGateway.Chat(ctx, messages, toolDefs)
		if err != nil {
			trace.EndTime = time.Now()
			trace.Error = fmt.Sprintf("LLM 调用失败 (迭代 %d): %v", i+1, err)
			return trace, fmt.Errorf("llm chat failed at iteration %d: %w", i+1, err)
		}

		// 累计 token 消耗
		trace.TokenUsage += resp.Usage.TotalTokens

		// 判断是否有工具调用
		if len(resp.ToolCalls) == 0 {
			// 没有工具调用，视为最终答案
			step := StepResult{
				Thought:     resp.Content,
				IsFinal:     true,
				FinalAnswer: resp.Content,
			}
			trace.Steps = append(trace.Steps, step)

			// 将最终对话存入记忆
			e.storeToMemory(messages, resp.Content)

			trace.EndTime = time.Now()
			return trace, nil
		}

		// 有工具调用，逐个执行

		// 将 assistant 的回复（含 tool_calls）追加到对话历史
		messages = append(messages, llm.Message{
			Role:      "assistant",
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		})

		// 处理每个工具调用
		toolFailed := false  // 本轮是否有工具失败
		emptyResult := false // 本轮是否有工具执行成功但返回空结果
		for _, tc := range resp.ToolCalls {
			// 解析 JSON 格式的参数
			args := parseToolArgs(tc.Arguments)

			step := StepResult{
				Thought:     resp.Content,
				Action:      tc.Name,
				ActionInput: args,
			}

			// ★ 工具调用门禁：执行前进行安全检查（如安全策略评估）。
			// 被拦截时不执行工具，将拦截原因作为该工具的执行结果反馈给 LLM，
			// 保留 tool_call_id 配对；拦截不算执行失败崩溃，循环继续。
			if e.toolGate != nil {
				if gateErr := e.toolGate.CheckToolCall(ctx, tc.Name, tc.Arguments); gateErr != nil {
					step.Observation = fmt.Sprintf("调用被拦截: %v", gateErr)
					trace.Steps = append(trace.Steps, step)
					messages = append(messages, llm.Message{
						Role:       "tool",
						Content:    fmt.Sprintf("[%s] %s", tc.Name, step.Observation),
						ToolCallID: tc.ID,
					})
					continue
				}
			}

			// 执行工具调用
			result, err := e.toolRegistry.Call(ctx, tc.Name, args)
			callFailed := false
			if err != nil {
				step.Observation = fmt.Sprintf("工具调用失败: %v", err)
				callFailed = true
			} else if result.Error != "" {
				step.Observation = fmt.Sprintf("工具执行错误: %s", result.Error)
				callFailed = true
			} else {
				step.Observation = result.Output
			}
			if callFailed {
				toolFailed = true
			} else if result.Output == "" {
				// 执行成功但输出为空，记为空结果（供偏差评估复用，不重复调用工具）
				emptyResult = true
			}

			trace.Steps = append(trace.Steps, step)

			// ★ 工具输出预处理：在追加到消息前进行截断
			observation := e.preprocessToolOutput(tc.Name, step.Observation)

			// 将工具结果追加到对话历史（包含 tool_call_id 以匹配多工具调用）
			toolMsg := llm.Message{
				Role:       "tool",
				Content:    fmt.Sprintf("[%s] %s", tc.Name, observation),
				ToolCallID: tc.ID,
			}
			messages = append(messages, toolMsg)

			// 记录成功的观察结果（按单个工具的执行结果判断，供重规划时使用）
			if replanState != nil && !callFailed && step.Observation != "" {
				replanState.SuccessfulObservations = append(
					replanState.SuccessfulObservations, step.Observation)
			}
		}

		// ★ 反思机制：在工具执行后评估进展
		if replanState != nil {
			// 更新偏差分数（复用上方工具执行时收集的结果，不重复调用工具）
			replanState.updateDeviation(toolFailed, emptyResult)

			// 判断是否触发反思
			if shouldReflect(reflCfg, i, toolFailed) {
				reflection := e.executeReflection(ctx, goal, trace.Steps, trace)

				// 判断是否需要重规划
				if replanState.shouldReplan(reflCfg, reflection, i, e.maxIterations) {
					replanState.ReplanCount++
					// 注入重规划提示，引导 LLM 重新思考。
					// 使用 user 角色而非 system：ctxmgr 会将所有 system 消息
					// 整体前置到对话头部，导致"就近引导"失效；改用 user 角色
					// 并加 [系统反馈] 前缀，使提示停留在当前对话位置。
					replanPrompt := buildReplanPrompt(goal, replanState.SuccessfulObservations)
					messages = append(messages, llm.Message{
						Role:    "user",
						Content: "[系统反馈] " + replanPrompt,
					})
				}
			}
		}
	}

	// 超过最大迭代次数
	trace.EndTime = time.Now()
	trace.Error = fmt.Sprintf("超过最大迭代次数 (%d)，未能得出最终答案", e.maxIterations)
	return trace, fmt.Errorf("max iterations (%d) exceeded without final answer", e.maxIterations)
}

// applyContextManagement 执行上下文管理（压缩/截断/摘要）
func (e *Engine) applyContextManagement(ctx context.Context, messages []llm.Message) []llm.Message {
	if e.ctxManager == nil {
		return messages
	}

	// 转换为 ctxmgr 格式（保留 tool_call_id 和 tool_calls）
	mgrMsgs := make([]ctxmgr.LLMMessage, len(messages))
	for i, msg := range messages {
		mgr := ctxmgr.LLMMessage{Role: msg.Role, Content: msg.Content, ToolCallID: msg.ToolCallID}
		if len(msg.ToolCalls) > 0 {
			if tcJSON, err := json.Marshal(msg.ToolCalls); err == nil {
				mgr.ToolCalls = string(tcJSON)
			}
		}
		mgrMsgs[i] = mgr
	}

	// 执行上下文管理
	compressed := e.ctxManager.ProcessMessages(ctx, mgrMsgs)

	// 转换回 llm.Message 格式（恢复 tool_call_id 和 tool_calls）
	result := make([]llm.Message, len(compressed))
	for i, msg := range compressed {
		m := llm.Message{Role: msg.Role, Content: msg.Content, ToolCallID: msg.ToolCallID}
		if msg.ToolCalls != "" {
			json.Unmarshal([]byte(msg.ToolCalls), &m.ToolCalls)
		}
		result[i] = m
	}

	return result
}

// preprocessToolOutput 对工具输出进行预处理（截断过长内容）
func (e *Engine) preprocessToolOutput(toolName string, output string) string {
	if e.ctxManager == nil {
		return output
	}

	estimator := ctxmgr.DefaultEstimator()
	maxTokens := e.ctxManager.Config().MaxToolOutputTokens

	tokens := estimator.EstimateText(output)
	if tokens <= maxTokens {
		return output
	}

	// 根据工具类型选择不同的截断策略
	switch toolName {
	case "shell_exec", "code_run":
		// 命令输出：保留尾部（最新输出更重要）
		return ctxmgr.TruncateHead(output, maxTokens, estimator)
	case "web_fetch", "file_read":
		// 网页/文件：保留头尾
		return ctxmgr.TruncateText(output, maxTokens, estimator)
	default:
		return ctxmgr.TruncateText(output, maxTokens, estimator)
	}
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
