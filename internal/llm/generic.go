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

// ProviderConfig 通用模型接入配置
// 任何兼容 OpenAI Chat Completions API 的模型服务均可通过此配置接入
type ProviderConfig struct {
	// Name 供应商唯一标识（用于路由和日志）
	Name string

	// BaseURL API 端点地址（完整的 chat/completions 路径）
	// 示例:
	//   DeepSeek:  https://api.deepseek.com/chat/completions
	//   通义千问:  https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions
	//   OpenAI:    https://api.openai.com/v1/chat/completions
	//   Ollama:    http://localhost:11434/v1/chat/completions
	//   vLLM:      http://localhost:8000/v1/chat/completions
	//   LM Studio: http://localhost:1234/v1/chat/completions
	BaseURL string

	// APIKey 认证密钥（Bearer Token）
	APIKey string

	// Model 默认模型名称（请求中未指定 model 时使用）
	Model string

	// MaxContextTokens 模型最大上下文 token 数
	MaxContextTokens int

	// Timeout 单次请求超时时间（默认 120s）
	Timeout time.Duration

	// Headers 额外自定义请求头（可选）
	Headers map[string]string
}

// GenericProvider 通用 OpenAI 兼容协议的 LLM 供应商
// 通过配置即可接入任何兼容 OpenAI Chat Completions API 的模型服务，
// 无需为每个供应商编写独立代码。
type GenericProvider struct {
	cfg        ProviderConfig
	httpClient *http.Client
}

// NewGenericProvider 创建通用供应商实例
func NewGenericProvider(cfg ProviderConfig) *GenericProvider {
	if cfg.MaxContextTokens == 0 {
		cfg.MaxContextTokens = 32768
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 120 * time.Second
	}
	return &GenericProvider{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: cfg.Timeout,
		},
	}
}

// Name 返回供应商名称标识
func (p *GenericProvider) Name() string {
	return p.cfg.Name
}

// Model 返回供应商配置的默认模型名称
func (p *GenericProvider) Model() string {
	return p.cfg.Model
}

// MaxTokens 返回该模型支持的最大 token 数
func (p *GenericProvider) MaxTokens() int {
	return p.cfg.MaxContextTokens
}

// Chat 发送聊天请求并返回完整响应
func (p *GenericProvider) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	body, err := p.buildRequestBody(req, false)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("%s: build request: %w", p.cfg.Name, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.BaseURL, bytes.NewReader(body))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("%s: create request: %w", p.cfg.Name, err)
	}
	p.setHeaders(httpReq)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("%s: request failed: %w", p.cfg.Name, err)
	}
	defer resp.Body.Close()

	// 非 200 状态码：返回结构化 APIError，供重试机制分类
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		retryAfter := resp.Header.Get("Retry-After")
		return ChatResponse{}, NewAPIError(p.cfg.Name, resp.StatusCode, string(respBody), retryAfter)
	}

	var apiResp openAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return ChatResponse{}, fmt.Errorf("%s: decode response: %w", p.cfg.Name, err)
	}

	return parseOpenAIResponse(apiResp), nil
}

// ChatStream 发送流式聊天请求，返回 SSE 数据通道
func (p *GenericProvider) ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, error) {
	body, err := p.buildRequestBody(req, true)
	if err != nil {
		return nil, fmt.Errorf("%s: build stream request: %w", p.cfg.Name, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.BaseURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%s: create stream request: %w", p.cfg.Name, err)
	}
	p.setHeaders(httpReq)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%s: stream request failed: %w", p.cfg.Name, err)
	}

	// 非 200 状态码：返回结构化 APIError
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

// setHeaders 设置请求头（标准 + 自定义）
func (p *GenericProvider) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	if p.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	}
	// 注入自定义请求头
	for k, v := range p.cfg.Headers {
		req.Header.Set(k, v)
	}
}

// buildRequestBody 构建 OpenAI 兼容格式的请求体
func (p *GenericProvider) buildRequestBody(req ChatRequest, stream bool) ([]byte, error) {
	model := req.Model
	if model == "" {
		model = p.cfg.Model
	}

	apiReq := openAIRequest{
		Model:       model,
		Messages:    toOpenAIMessages(req.Messages),
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Stream:      stream,
	}

	// 转换工具定义为 OpenAI function calling 格式
	if len(req.Tools) > 0 {
		apiReq.Tools = make([]openAITool, 0, len(req.Tools))
		for _, t := range req.Tools {
			apiReq.Tools = append(apiReq.Tools, openAITool{
				Type: "function",
				Function: openAIFunction{
					Name:        t.Name,
					Description: t.Description,
					Parameters:  t.Parameters,
				},
			})
		}
	}

	return json.Marshal(apiReq)
}

// parseSSEStream 解析 SSE 流式响应并发送到通道
func (p *GenericProvider) parseSSEStream(ctx context.Context, body io.ReadCloser, ch chan<- StreamChunk) {
	defer close(ch)
	defer body.Close()

	scanner := bufio.NewScanner(body)
	// 设置更大的 buffer 以支持长行（默认 64KB 不够用，模型可能返回长 JSON）
	const maxScanTokenSize = 1 << 20 // 1MB
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, maxScanTokenSize)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}

		line := scanner.Text()

		// SSE 格式: "data: {...}" 或 "data: [DONE]"
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			ch <- StreamChunk{Done: true}
			return
		}

		var chunk openAIStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		delta := chunk.Choices[0].Delta
		sc := StreamChunk{
			Delta: delta.Content,
		}

		// 解析增量工具调用（遍历全部元素，支持单个 chunk 携带多个并发 tool call）
		if len(delta.ToolCalls) > 0 {
			sc.ToolCallDeltas = make([]ToolCall, 0, len(delta.ToolCalls))
			for i, tc := range delta.ToolCalls {
				// 优先使用协议携带的 index（跨 chunk 归并保持稳定），缺省时退回切片位置
				idx := i
				if tc.Index != nil {
					idx = *tc.Index
				}
				id := tc.ID
				if id == "" {
					// 某些 API（如 MiMo）流式响应不返回 tool call ID，与非流式路径一致生成备用 ID
					id = fmt.Sprintf("call_%d", idx)
				}
				sc.ToolCallDeltas = append(sc.ToolCallDeltas, ToolCall{
					ID:        id,
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				})
			}
			// 兼容旧消费方：单元素字段指向第一个工具调用
			sc.ToolCallDelta = &sc.ToolCallDeltas[0]
		}

		// 检查是否结束
		if chunk.Choices[0].FinishReason != "" {
			sc.Done = true
		}

		select {
		case ch <- sc:
		case <-ctx.Done():
			return
		}
	}
}
