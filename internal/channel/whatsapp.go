// whatsapp.go 实现 WhatsApp 渠道（Cloud API 格式）：
// 入站接收 Meta Webhook 格式的 JSON 回调（Bearer token 验证），
// 出站经 Cloud API /{phone_number_id}/messages 投递文本。
// 也可对接 Twilio 等中转网关（经 api_base 指向其中转端点）。
package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// whatsappChannelName WhatsApp 渠道的规范渠道 ID。
const whatsappChannelName = "whatsapp"

// WhatsAppConfig WhatsApp 渠道配置。
type WhatsAppConfig struct {
	// Token 入站回调验证令牌（调用方经 Authorization: Bearer <token> 携带，必填）。
	Token string
	// APIToken 出站 Cloud API Bearer 令牌（缺省复用 Token）。
	APIToken string
	// PhoneNumberID Cloud API 的 phone_number_id（出站必填）。
	PhoneNumberID string
	// APIBase Cloud API 基址（默认 https://graph.facebook.com/v19.0，可指向中转网关）。
	APIBase string
	// PathPrefix 接收路由前缀（默认 "/channels/whatsapp"）。
	PathPrefix string
}

// whatsappWebhook Meta Cloud API Webhook 回调结构（仅列用到的字段）。
type whatsappWebhook struct {
	Object string `json:"object"`
	Entry  []struct {
		Changes []struct {
			Value struct {
				Metadata struct {
					PhoneNumberID string `json:"phone_number_id"`
				} `json:"metadata"`
				Messages []struct {
					ID   string `json:"id"`
					From string `json:"from"`
					Type string `json:"type"`
					Text struct {
						Body string `json:"body"`
					} `json:"text"`
				} `json:"messages"`
			} `json:"value"`
		} `json:"changes"`
	} `json:"entry"`
}

// WhatsAppChannel WhatsApp 渠道（实现 Channel 契约）。
type WhatsAppChannel struct {
	cfg    WhatsAppConfig
	mux    *http.ServeMux
	client *http.Client

	mu      sync.Mutex
	inbound chan<- InboundMessage
	status  ChannelStatus
}

// NewWhatsAppChannel 创建 WhatsApp 渠道（入站 token 缺失时拒绝创建）。
func NewWhatsAppChannel(cfg WhatsAppConfig, mux *http.ServeMux) (*WhatsAppChannel, error) {
	if cfg.Token == "" {
		return nil, fmt.Errorf("whatsapp 渠道: 必须配置 token（入站回调验证）")
	}
	if mux == nil {
		return nil, fmt.Errorf("whatsapp 渠道: 需要 HTTP 路由（mux）")
	}
	if cfg.PathPrefix == "" {
		cfg.PathPrefix = "/channels/whatsapp"
	}
	if cfg.APIBase == "" {
		cfg.APIBase = "https://graph.facebook.com/v19.0"
	}
	if cfg.APIToken == "" {
		cfg.APIToken = cfg.Token
	}
	return &WhatsAppChannel{
		cfg:    cfg,
		mux:    mux,
		client: &http.Client{Timeout: 15 * time.Second},
		status: StatusStopped,
	}, nil
}

// Name 返回规范渠道 ID。
func (c *WhatsAppChannel) Name() string { return whatsappChannelName }

// Start 注册接收路由。
func (c *WhatsAppChannel) Start(_ context.Context, in chan<- InboundMessage) error {
	c.mu.Lock()
	c.inbound = in
	c.status = StatusConnected
	c.mu.Unlock()
	c.mux.HandleFunc("POST "+c.cfg.PathPrefix, c.handleInbound)
	return nil
}

// Stop 停止接收。
func (c *WhatsAppChannel) Stop() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status = StatusStopped
	c.inbound = nil
	return nil
}

// Status 返回运行状态。
func (c *WhatsAppChannel) Status() ChannelStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// handleInbound 接收 Meta Webhook 回调：Bearer 验证 → 提取文本消息入队 → 200。
func (c *WhatsAppChannel) handleInbound(w http.ResponseWriter, r *http.Request) {
	const prefix = "Bearer "
	auth := r.Header.Get("Authorization")
	if len(auth) <= len(prefix) || !strings.EqualFold(auth[:len(prefix)], prefix) ||
		!ctEqual(auth[len(prefix):], c.cfg.Token) {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}

	var payload whatsappWebhook
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	// 立即 ack（Meta 要求快速响应，否则会重推）
	writeWebhookJSON(w, http.StatusOK, map[string]string{"status": "ok"})

	c.mu.Lock()
	in := c.inbound
	c.mu.Unlock()
	if in == nil {
		return
	}

	for _, entry := range payload.Entry {
		for _, change := range entry.Changes {
			for _, m := range change.Value.Messages {
				if m.Type != "text" || strings.TrimSpace(m.Text.Body) == "" {
					continue
				}
				select {
				case in <- InboundMessage{
					Channel:   whatsappChannelName,
					MessageID: m.ID,
					UserID:    m.From,
					ChatID:    m.From, // WhatsApp 会话均为号码一对一
					ChatType:  ChatTypeDM,
					Text:      strings.TrimSpace(m.Text.Body),
					Timestamp: time.Now(),
				}:
				default:
				}
			}
		}
	}
}

// Send 经 Cloud API 投递文本消息。
func (c *WhatsAppChannel) Send(ctx context.Context, msg OutboundMessage) error {
	if c.cfg.PhoneNumberID == "" {
		return fmt.Errorf("whatsapp 渠道: 未配置 phone_number_id，无法出站")
	}
	payload := map[string]any{
		"messaging_product": "whatsapp",
		"to":                msg.ChatID,
		"type":              "text",
		"text":              map[string]string{"body": msg.Text},
	}
	body, status, err := httpPostJSON(ctx, c.client,
		c.cfg.APIBase+"/"+c.cfg.PhoneNumberID+"/messages",
		map[string]string{"Authorization": "Bearer " + c.cfg.APIToken}, payload)
	if err != nil {
		return err
	}
	if status >= 300 {
		return apiError("whatsapp", status, body)
	}
	return nil
}
