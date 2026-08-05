package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// anthropicDefaultBaseURL Anthropic Messages API 默认端点
	anthropicDefaultBaseURL = "https://api.anthropic.com/v1/messages"

	// anthropicAPIVersion anthropic-version 请求头指定的 API 版本
	anthropicAPIVersion = "2023-06-01"

	// anthropicDefaultMaxTokens 请求未指定 max_tokens 时的兜底值。
	// Anthropic Messages API 要求 max_tokens 必填，这里取 8192 作为保守上限，
	// 不超过主流 Claude 模型的单次输出上限。
	anthropicDefaultMaxTokens = 8192
)

// ========== Anthropic Messages API 数据结构 ==========

// anthropicRequest 是 Anthropic Messages API 的请求结构
type anthropicRequest struct {
	Model       string             `json:"model"`
	Messages    []anthropicMessage `json:"messages"`
	System      string             `json:"system,omitempty"`
	Tools       []anthropicTool    `json:"tools,omitempty"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature float64            `json:"temperature,omitempty"`
	Stream      bool               `json:"stream,omitempty"`
}

// anthropicMessage 是请求中的消息格式（content 为 content block 列表）
type anthropicMessage struct {
	Role    string                  `json:"role"`
	Content []anthropicContentBlock `json:"content"`
}

// anthropicContentBlock 是消息内容块（请求与响应共用）。
// 请求侧用到 text / tool_use / tool_result 三种类型；响应侧只出现 text / tool_use。
type anthropicContentBlock struct {
	Type string `json:"type"`
	// text 块内容
	Text string `json:"text,omitempty"`
	// tool_use 块字段（assistant 发起的工具调用）
	ID    string                 `json:"id,omitempty"`
	Name  string                 `json:"name,omitempty"`
	Input map[string]interface{} `json:"input,omitempty"`
	// tool_result 块字段（工具执行结果，置于 user 消息中）
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
}

// anthropicTool 是 Anthropic 格式的工具定义
type anthropicTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

// anthropicResponse 是 Anthropic Messages API 的完整响应
type anthropicResponse struct {
	Model      string                  `json:"model"`
	Content    []anthropicContentBlock `json:"content"`
	StopReason string                  `json:"stop_reason"`
	Usage      anthropicUsage          `json:"usage"`
}

// anthropicUsage 是 Anthropic 的 token 用量统计
type anthropicUsage struct {
	InputTokens          int `json:"input_tokens"`
	OutputTokens         int `json:"output_tokens"`
	CacheReadInputTokens int `json:"cache_read_input_tokens"`
}

// anthropicStreamEvent 是 Anthropic SSE 流式事件的通用结构。
// 不同事件类型只填充其中一部分字段，按 type 分发处理。
type anthropicStreamEvent struct {
	Type         string                 `json:"type"`
	Index        int                    `json:"index"`
	ContentBlock *anthropicContentBlock `json:"content_block"`
	Delta        *anthropicStreamDelta  `json:"delta"`
	Message      *anthropicStreamMsg    `json:"message"`
}

// anthropicStreamDelta 是 content_block_delta / message_delta 事件的增量数据
type anthropicStreamDelta struct {
	Type        string `json:"type"` // text_delta | input_json_delta | （message_delta 时为空）
	Text        string `json:"text"`
	PartialJSON string `json:"partial_json"`
	StopReason  string `json:"stop_reason"`
}

// anthropicStreamMsg 是 message_start 事件携带的消息元信息（含初始 usage）
type anthropicStreamMsg struct {
	Usage anthropicUsage `json:"usage"`
}

// AnthropicProvider 是 Anthropic Messages API 协议的 LLM 供应商。
// 通过配置 protocol: "anthropic" 接入 Claude 系列模型。
type AnthropicProvider struct {
	cfg        ProviderConfig
	httpClient *http.Client
}

// NewAnthropicProvider 创建 Anthropic 供应商实例。
// BaseURL 为空时使用默认端点 https://api.anthropic.com/v1/messages。
func NewAnthropicProvider(cfg ProviderConfig) *AnthropicProvider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = anthropicDefaultBaseURL
	}
	if cfg.MaxContextTokens == 0 {
		cfg.MaxContextTokens = 32768
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 120 * time.Second
	}
	return &AnthropicProvider{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: cfg.Timeout,
		},
	}
}

// Name 返回供应商名称标识
func (p *AnthropicProvider) Name() string {
	return p.cfg.Name
}

// Model 返回供应商配置的默认模型名称
func (p *AnthropicProvider) Model() string {
	return p.cfg.Model
}

// MaxTokens 返回该模型支持的最大 token 数
func (p *AnthropicProvider) MaxTokens() int {
	return p.cfg.MaxContextTokens
}

// Chat 发送聊天请求并返回完整响应
func (p *AnthropicProvider) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	body, err := p.buildRequestBody(req, false)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("%s: 构建请求失败: %w", p.cfg.Name, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.BaseURL, bytes.NewReader(body))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("%s: 创建请求失败: %w", p.cfg.Name, err)
	}
	p.setHeaders(httpReq)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("%s: 请求发送失败: %w", p.cfg.Name, err)
	}
	defer resp.Body.Close()

	// 非 200 状态码：返回结构化 APIError（含状态码与响应摘要），供重试机制分类
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		retryAfter := resp.Header.Get("Retry-After")
		return ChatResponse{}, NewAPIError(p.cfg.Name, resp.StatusCode, string(respBody), retryAfter)
	}

	var apiResp anthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return ChatResponse{}, fmt.Errorf("%s: 解析响应失败: %w", p.cfg.Name, err)
	}

	return parseAnthropicResponse(apiResp), nil
}

// ChatStream 发送流式聊天请求，返回 SSE 数据通道
func (p *AnthropicProvider) ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, error) {
	body, err := p.buildRequestBody(req, true)
	if err != nil {
		return nil, fmt.Errorf("%s: 构建流式请求失败: %w", p.cfg.Name, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.BaseURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%s: 创建流式请求失败: %w", p.cfg.Name, err)
	}
	p.setHeaders(httpReq)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%s: 流式请求发送失败: %w", p.cfg.Name, err)
	}

	// 非 200 状态码：返回结构化 APIError（含状态码与响应摘要）
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		retryAfter := resp.Header.Get("Retry-After")
		return nil, NewAPIError(p.cfg.Name, resp.StatusCode, string(respBody), retryAfter)
	}

	ch := make(chan StreamChunk, 32)
	go p.parseSSEStream(ctx, resp.Body, ch)
	return ch, nil
}

// setHeaders 设置请求头（Anthropic 认证 + 版本 + 自定义）
func (p *AnthropicProvider) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	if p.cfg.APIKey != "" {
		req.Header.Set("x-api-key", p.cfg.APIKey)
	}
	req.Header.Set("anthropic-version", anthropicAPIVersion)
	// 注入自定义请求头
	for k, v := range p.cfg.Headers {
		req.Header.Set(k, v)
	}
}

// buildRequestBody 构建 Anthropic Messages API 格式的请求体
func (p *AnthropicProvider) buildRequestBody(req ChatRequest, stream bool) ([]byte, error) {
	model := req.Model
	if model == "" {
		model = p.cfg.Model
	}

	// max_tokens 为必填字段：请求未指定时使用保守兜底值
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = anthropicDefaultMaxTokens
	}

	system, messages := toAnthropicMessages(req.Messages)

	apiReq := anthropicRequest{
		Model:       model,
		Messages:    messages,
		System:      system,
		MaxTokens:   maxTokens,
		Temperature: req.Temperature,
		Stream:      stream,
	}

	// 转换工具定义为 Anthropic 格式（parameters → input_schema）
	if len(req.Tools) > 0 {
		apiReq.Tools = make([]anthropicTool, 0, len(req.Tools))
		for _, t := range req.Tools {
			apiReq.Tools = append(apiReq.Tools, anthropicTool{
				Name:        t.Name,
				Description: t.Description,
				InputSchema: t.Parameters,
			})
		}
	}

	return json.Marshal(apiReq)
}

// toAnthropicMessages 将内部 Message 列表转换为 Anthropic 请求格式。
// 返回顶层 system 字段与消息列表（Anthropic 要求 messages 中不含 system 角色）。
// 关键转换：
//   - system 消息提取合并为顶层 system 字段（多条以换行拼接）
//   - tool 消息转为 user 消息内的 tool_result 块（tool_use_id 取 ToolCallID）
//   - assistant 消息的 ToolCalls 转为 tool_use 块（arguments JSON 反序列化为 input 对象）
//   - 相邻同角色消息合并为一条（Anthropic 要求 user/assistant 角色交替）
func toAnthropicMessages(msgs []Message) (string, []anthropicMessage) {
	var systemParts []string
	result := make([]anthropicMessage, 0, len(msgs))

	for _, m := range msgs {
		switch m.Role {
		case RoleSystem:
			if m.Content != "" {
				systemParts = append(systemParts, m.Content)
			}
			continue
		case RoleTool:
			// 工具结果转为 user 消息中的 tool_result 块
			appendBlock(&result, RoleUser, anthropicContentBlock{
				Type:      "tool_result",
				ToolUseID: m.ToolCallID,
				Content:   m.Content,
			})
		case RoleAssistant:
			if len(m.ToolCalls) > 0 {
				// assistant 的文本与工具调用转为 text + tool_use 块序列
				if m.Content != "" {
					appendBlock(&result, RoleAssistant, anthropicContentBlock{
						Type: "text",
						Text: m.Content,
					})
				}
				for i, tc := range m.ToolCalls {
					id := tc.ID
					if id == "" {
						// 与非流式响应解析一致生成备用 ID
						id = fmt.Sprintf("toolu_%d", i)
					}
					appendBlock(&result, RoleAssistant, anthropicContentBlock{
						Type:  "tool_use",
						ID:    id,
						Name:  tc.Name,
						Input: parseToolArguments(tc.Arguments),
					})
				}
			} else {
				appendBlock(&result, RoleAssistant, anthropicContentBlock{
					Type: "text",
					Text: m.Content,
				})
			}
		default:
			// 普通 user 消息（含未识别角色，按 user 处理）
			appendBlock(&result, RoleUser, anthropicContentBlock{
				Type: "text",
				Text: m.Content,
			})
		}
	}

	return strings.Join(systemParts, "\n\n"), result
}

// appendBlock 向消息列表追加一个内容块；
// 若末尾消息角色相同则合并进该消息（保持 user/assistant 角色交替）。
func appendBlock(msgs *[]anthropicMessage, role string, block anthropicContentBlock) {
	if n := len(*msgs); n > 0 && (*msgs)[n-1].Role == role {
		(*msgs)[n-1].Content = append((*msgs)[n-1].Content, block)
		return
	}
	*msgs = append(*msgs, anthropicMessage{
		Role:    role,
		Content: []anthropicContentBlock{block},
	})
}

// parseToolArguments 将工具调用的 arguments JSON 字符串反序列化为对象。
// 反序列化失败（或结果不是对象）时返回空对象，保证请求体合法。
func parseToolArguments(args string) map[string]interface{} {
	input := make(map[string]interface{})
	if args == "" {
		return input
	}
	if err := json.Unmarshal([]byte(args), &input); err != nil || input == nil {
		return make(map[string]interface{})
	}
	return input
}

// parseAnthropicResponse 将 Anthropic 格式响应转换为统一的 ChatResponse
func parseAnthropicResponse(resp anthropicResponse) ChatResponse {
	result := ChatResponse{
		Model:        resp.Model,
		FinishReason: mapAnthropicStopReason(resp.StopReason),
		Usage: Usage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.InputTokens + resp.Usage.OutputTokens,
			PromptDetails: PromptTokenDetails{
				CachedTokens: resp.Usage.CacheReadInputTokens,
			},
		},
	}

	var texts []string
	for i, block := range resp.Content {
		switch block.Type {
		case "text":
			texts = append(texts, block.Text)
		case "tool_use":
			id := block.ID
			if id == "" {
				// ID 缺失时生成备用 ID
				id = fmt.Sprintf("toolu_%d", i)
			}
			// input 对象序列化回 JSON 字符串，与内部 ToolCall 表示一致
			args, _ := json.Marshal(block.Input)
			result.ToolCalls = append(result.ToolCalls, ToolCall{
				ID:        id,
				Name:      block.Name,
				Arguments: string(args),
			})
		}
	}
	result.Content = strings.Join(texts, "")

	return result
}

// mapAnthropicStopReason 将 Anthropic 的 stop_reason 映射为统一的结束原因
func mapAnthropicStopReason(reason string) string {
	switch reason {
	case "end_turn", "stop_sequence":
		return "stop"
	case "tool_use":
		return "tool_calls"
	case "max_tokens":
		return "length"
	case "":
		return "stop"
	default:
		return reason
	}
}

// parseSSEStream 解析 Anthropic SSE 流式响应并发送到通道。
// 事件类型：message_start / content_block_start / content_block_delta /
// content_block_stop / message_delta / message_stop。
// 工具调用参数以 input_json_delta 分片到达，按 content block index 归并，
// 每个 chunk 携带该工具调用累积至今的完整参数快照与稳定 ID。
func (p *AnthropicProvider) parseSSEStream(ctx context.Context, body io.ReadCloser, ch chan<- StreamChunk) {
	defer close(ch)
	defer body.Close()

	scanner := bufio.NewScanner(body)
	// 设置更大的 buffer 以支持长行（默认 64KB 不够用，模型可能返回长 JSON）
	const maxScanTokenSize = 1 << 20 // 1MB
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, maxScanTokenSize)

	// 按 content block index 跟踪进行中的 tool_use 块，用于跨 chunk 归并参数
	toolBlocks := make(map[int]*ToolCall)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}

		line := scanner.Text()

		// SSE 格式: "event: xxx" + "data: {...}"，事件类型以 data 中的 type 字段为准
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		var event anthropicStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		var sc StreamChunk
		emit := false

		switch event.Type {
		case "content_block_start":
			// tool_use 块开始：登记 ID（空则兜底 toolu_<index>）与名称
			if event.ContentBlock != nil && event.ContentBlock.Type == "tool_use" {
				id := event.ContentBlock.ID
				if id == "" {
					id = fmt.Sprintf("toolu_%d", event.Index)
				}
				tc := &ToolCall{ID: id, Name: event.ContentBlock.Name}
				toolBlocks[event.Index] = tc
				sc.ToolCallDeltas = []ToolCall{*tc}
				emit = true
			}
		case "content_block_delta":
			if event.Delta == nil {
				continue
			}
			switch event.Delta.Type {
			case "text_delta":
				sc.Delta = event.Delta.Text
				emit = true
			case "input_json_delta":
				// 参数分片：归并到对应 content block 的工具调用上
				if tc, ok := toolBlocks[event.Index]; ok {
					tc.Arguments += event.Delta.PartialJSON
					sc.ToolCallDeltas = []ToolCall{*tc}
					emit = true
				}
			}
		case "message_stop":
			sc.Done = true
			emit = true
		}

		if !emit {
			continue
		}
		// 兼容旧消费方：单元素字段指向第一个工具调用
		if len(sc.ToolCallDeltas) > 0 {
			sc.ToolCallDelta = &sc.ToolCallDeltas[0]
		}

		select {
		case ch <- sc:
		case <-ctx.Done():
			return
		}
	}
}
