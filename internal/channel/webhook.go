// webhook.go 实现 webhook 通用渠道：标准 HTTP 接收 + callback_url 异步投递。
// 零平台依赖，任何系统（包括 OpenClaw 类网关）都可以通过标准 HTTP 接入，
// 也用于在实现具体 IM adapter 之前验证 Channel 契约。
package channel

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// webhookChannelName webhook 渠道的规范渠道 ID。
const webhookChannelName = "webhook"

// WebhookConfig webhook 渠道配置。
type WebhookConfig struct {
	// Token Bearer 令牌（必填）：入站校验 Authorization 头，出站回调原样携带。
	Token string
	// CallbackURL 默认投递地址（可被单次请求的 callback_url 字段覆盖）。
	CallbackURL string
	// PathPrefix 接收路由前缀（默认 "/channels/webhook"，完整路由为 POST <前缀>）。
	PathPrefix string
}

// webhookPayload 入站请求体。
type webhookPayload struct {
	MessageID   string `json:"message_id"`
	UserID      string `json:"user_id"`
	ChatID      string `json:"chat_id"`      // 缺省时用 user_id
	ChatType    string `json:"chat_type"`    // dm | group（缺省 dm）
	ThreadID    string `json:"thread_id"`    // 可选
	Text        string `json:"text"`         // 必填
	CallbackURL string `json:"callback_url"` // 覆盖默认投递地址（可选）
}

// webhookCallback 出站回调请求体。
type webhookCallback struct {
	Channel  string `json:"channel"`
	ChatID   string `json:"chat_id"`
	ThreadID string `json:"thread_id,omitempty"`
	Text     string `json:"text"`
}

// WebhookChannel webhook 通用渠道（实现 Channel 契约）。
type WebhookChannel struct {
	cfg    WebhookConfig
	mux    *http.ServeMux
	client *http.Client

	mu      sync.Mutex
	inbound chan<- InboundMessage
	status  ChannelStatus
}

// NewWebhookChannel 创建 webhook 渠道。token 未配置时拒绝创建（安全底线，
// 与 api_key 认证"缺失即拒绝启动"的口径一致）。
func NewWebhookChannel(cfg WebhookConfig, mux *http.ServeMux) (*WebhookChannel, error) {
	if cfg.Token == "" {
		return nil, fmt.Errorf("webhook 渠道: 必须配置 token（不允许无认证端点）")
	}
	if mux == nil {
		return nil, fmt.Errorf("webhook 渠道: 需要 HTTP 路由（mux）")
	}
	if cfg.PathPrefix == "" {
		cfg.PathPrefix = "/channels/webhook"
	}
	return &WebhookChannel{
		cfg:    cfg,
		mux:    mux,
		client: &http.Client{Timeout: 10 * time.Second},
		status: StatusStopped,
	}, nil
}

// Name 返回规范渠道 ID。
func (c *WebhookChannel) Name() string { return webhookChannelName }

// Start 注册接收路由并进入可接收状态。
func (c *WebhookChannel) Start(ctx context.Context, in chan<- InboundMessage) error {
	c.mu.Lock()
	c.inbound = in
	c.status = StatusConnected
	c.mu.Unlock()
	c.mux.HandleFunc("POST "+c.cfg.PathPrefix, c.handleInbound)
	return nil
}

// Stop 停止接收（路由保留但返回 503）。
func (c *WebhookChannel) Stop() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status = StatusStopped
	c.inbound = nil
	return nil
}

// Status 返回运行状态。
func (c *WebhookChannel) Status() ChannelStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// MaxChunk 实现 Chunker：webhook 以 JSON 投递，单条上限放宽到 8000 字符。
func (c *WebhookChannel) MaxChunk() int { return 8000 }

// handleInbound 接收入站消息：Bearer 校验通过后立即回 202（异步执行，
// 不阻塞调用方），消息写入 Hub 入站队列。
func (c *WebhookChannel) handleInbound(w http.ResponseWriter, r *http.Request) {
	if !c.checkToken(r) {
		writeWebhookJSON(w, http.StatusUnauthorized, map[string]string{"error": "token 缺失或无效"})
		return
	}

	var p webhookPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil || strings.TrimSpace(p.Text) == "" {
		writeWebhookJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request: text 必填"})
		return
	}

	chatType := ChatTypeDM
	if p.ChatType == string(ChatTypeGroup) {
		chatType = ChatTypeGroup
	}
	chatID := p.ChatID
	if chatID == "" {
		chatID = p.UserID
	}

	msg := InboundMessage{
		Channel:   webhookChannelName,
		MessageID: p.MessageID,
		UserID:    p.UserID,
		ChatID:    chatID,
		ChatType:  chatType,
		ThreadID:  p.ThreadID,
		Text:      p.Text,
		ReplyTo:   p.CallbackURL,
		Timestamp: time.Now(),
	}

	c.mu.Lock()
	in := c.inbound
	c.mu.Unlock()
	if in == nil {
		writeWebhookJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "channel 已停止"})
		return
	}

	select {
	case in <- msg:
		writeWebhookJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
	default:
		// 队列满：背压保护，明确告知调用方重试
		writeWebhookJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "queue full, please retry later"})
	}
}

// checkToken 恒定时间比较 Bearer 令牌。
func (c *WebhookChannel) checkToken(r *http.Request) bool {
	const prefix = "Bearer "
	auth := r.Header.Get("Authorization")
	if len(auth) <= len(prefix) || !strings.EqualFold(auth[:len(prefix)], prefix) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(auth[len(prefix):]), []byte(c.cfg.Token)) == 1
}

// Send 将回复 POST 到 ReplyTo（单次请求指定）或默认 CallbackURL。
func (c *WebhookChannel) Send(ctx context.Context, msg OutboundMessage) error {
	target := msg.ReplyTo
	if target == "" {
		target = c.cfg.CallbackURL
	}
	if target == "" {
		return fmt.Errorf("webhook 渠道: 无投递地址（请求未携带 callback_url 且未配置默认地址）")
	}

	body, err := json.Marshal(webhookCallback{
		Channel:  webhookChannelName,
		ChatID:   msg.ChatID,
		ThreadID: msg.ThreadID,
		Text:     msg.Text,
	})
	if err != nil {
		return fmt.Errorf("webhook 渠道: 序列化回调体失败: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("webhook 渠道: 构造回调请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+c.cfg.Token)

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook 渠道: 投递失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook 渠道: 投递返回非成功状态码 %d", resp.StatusCode)
	}
	return nil
}

// writeWebhookJSON 回写 JSON 响应。
func writeWebhookJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
