// feishu.go 实现飞书（Lark）自建应用机器人渠道（HTTP 事件订阅模式）：
// 入站支持 url_verification 挑战应答与 X-Lark-Signature 签名验证
// （SHA256(timestamp+nonce+encrypt_key+body)）；出站经 tenant_access_token
// 调用 im/v1/messages 回复。
package channel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// feishuChannelName 飞书渠道的规范渠道 ID。
const feishuChannelName = "feishu"

// FeishuConfig 飞书渠道配置。
type FeishuConfig struct {
	// AppID 自建应用 App ID。
	AppID string
	// AppSecret 自建应用 App Secret（获取 tenant_access_token）。
	AppSecret string
	// VerificationToken 事件订阅验证令牌（校验事件体 header.token，可选）。
	VerificationToken string
	// EncryptKey 事件订阅加密密钥（配置后强制校验 X-Lark-Signature，建议配置）。
	EncryptKey string
	// PathPrefix 接收路由前缀（默认 "/channels/feishu"）。
	PathPrefix string
	// APIBase 开放平台基址（默认 https://open.feishu.cn，可指向代理或 Lark）。
	APIBase string
}

// feishuEnvelope 飞书事件订阅 v2 信封（仅列用到的字段）。
type feishuEnvelope struct {
	Schema    string `json:"schema"`
	Type      string `json:"type"`      // url_verification 时出现
	Challenge string `json:"challenge"` // url_verification 挑战串
	Token     string `json:"token"`     // url_verification 验证令牌
	Header    struct {
		EventID   string `json:"event_id"`
		EventType string `json:"event_type"`
		Token     string `json:"token"`
	} `json:"header"`
	Event struct {
		Sender struct {
			SenderID struct {
				OpenID string `json:"open_id"`
				UserID string `json:"user_id"`
			} `json:"sender_id"`
			SenderType string `json:"sender_type"`
		} `json:"sender"`
		Message struct {
			MessageID   string `json:"message_id"`
			RootID      string `json:"root_id"`
			ChatID      string `json:"chat_id"`
			ChatType    string `json:"chat_type"` // p2p | group
			MessageType string `json:"message_type"`
			Content     string `json:"content"` // JSON 字符串，如 {"text":"@_user_1 你好"}
			Mentions    []struct {
				Key  string `json:"key"` // "@_user_1"
				Name string `json:"name"`
			} `json:"mentions"`
		} `json:"message"`
	} `json:"event"`
}

// FeishuChannel 飞书渠道（实现 Channel 契约）。
type FeishuChannel struct {
	cfg    FeishuConfig
	mux    *http.ServeMux
	client *http.Client
	tokens tokenCache

	mu      sync.Mutex
	inbound chan<- InboundMessage
	status  ChannelStatus
}

// NewFeishuChannel 创建飞书渠道（app_id/app_secret 缺失时拒绝创建）。
func NewFeishuChannel(cfg FeishuConfig, mux *http.ServeMux) (*FeishuChannel, error) {
	if cfg.AppID == "" || cfg.AppSecret == "" {
		return nil, fmt.Errorf("飞书渠道: 必须配置 app_id 与 app_secret")
	}
	if mux == nil {
		return nil, fmt.Errorf("飞书渠道: 需要 HTTP 路由（mux）")
	}
	if cfg.PathPrefix == "" {
		cfg.PathPrefix = "/channels/feishu"
	}
	if cfg.APIBase == "" {
		cfg.APIBase = "https://open.feishu.cn"
	}
	return &FeishuChannel{
		cfg:    cfg,
		mux:    mux,
		client: &http.Client{Timeout: 15 * time.Second},
		status: StatusStopped,
	}, nil
}

// Name 返回规范渠道 ID。
func (c *FeishuChannel) Name() string { return feishuChannelName }

// Start 注册接收路由。
func (c *FeishuChannel) Start(_ context.Context, in chan<- InboundMessage) error {
	c.mu.Lock()
	c.inbound = in
	c.status = StatusConnected
	c.mu.Unlock()
	c.mux.HandleFunc("POST "+c.cfg.PathPrefix, c.handleInbound)
	return nil
}

// Stop 停止接收。
func (c *FeishuChannel) Stop() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status = StatusStopped
	c.inbound = nil
	return nil
}

// Status 返回运行状态。
func (c *FeishuChannel) Status() ChannelStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// feishuSignature 计算飞书事件签名：SHA256(timestamp + nonce + encrypt_key + body) 的十六进制。
func feishuSignature(timestamp, nonce, encryptKey string, body []byte) string {
	h := sha256.New()
	h.Write([]byte(timestamp))
	h.Write([]byte(nonce))
	h.Write([]byte(encryptKey))
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// handleInbound 接收飞书事件：签名验证 → url_verification 挑战 → 消息事件入队。
func (c *FeishuChannel) handleInbound(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxResponseBody))
	if err != nil {
		http.Error(w, "read body failed", http.StatusBadRequest)
		return
	}

	// 签名验证（配置了 encrypt_key 时强制）
	if c.cfg.EncryptKey != "" {
		ts := r.Header.Get("X-Lark-Request-Timestamp")
		nonce := r.Header.Get("X-Lark-Request-Nonce")
		sign := r.Header.Get("X-Lark-Signature")
		if !ctEqual(feishuSignature(ts, nonce, c.cfg.EncryptKey, body), sign) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
	}

	var env feishuEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}

	// URL 验证挑战：首次配置事件订阅时飞书要求原样返回 challenge
	if env.Type == "url_verification" {
		if c.cfg.VerificationToken != "" && !ctEqual(env.Token, c.cfg.VerificationToken) {
			http.Error(w, "invalid verification token", http.StatusUnauthorized)
			return
		}
		writeWebhookJSON(w, http.StatusOK, map[string]string{"challenge": env.Challenge})
		return
	}

	// 事件验证令牌校验（可选的二次验证）
	if c.cfg.VerificationToken != "" && !ctEqual(env.Header.Token, c.cfg.VerificationToken) {
		http.Error(w, "invalid verification token", http.StatusUnauthorized)
		return
	}

	// 立即 ack，避免飞书重推
	writeWebhookJSON(w, http.StatusOK, map[string]string{"status": "ok"})

	if env.Header.EventType != "im.message.receive_v1" || env.Event.Message.MessageType != "text" {
		return
	}
	// 机器人自身发送的消息不回环处理
	if env.Event.Sender.SenderType == "app" {
		return
	}

	var content struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(env.Event.Message.Content), &content); err != nil {
		return
	}
	text := stripFeishuMentions(content.Text, env.Event.Message.Mentions)
	if strings.TrimSpace(text) == "" {
		return
	}

	userID := env.Event.Sender.SenderID.OpenID
	if userID == "" {
		userID = env.Event.Sender.SenderID.UserID
	}
	chatType := ChatTypeDM
	if env.Event.Message.ChatType == "group" {
		chatType = ChatTypeGroup
	}

	c.mu.Lock()
	in := c.inbound
	c.mu.Unlock()
	if in == nil {
		return
	}
	select {
	case in <- InboundMessage{
		Channel:   feishuChannelName,
		MessageID: env.Event.Message.MessageID,
		UserID:    userID,
		ChatID:    env.Event.Message.ChatID,
		ChatType:  chatType,
		ThreadID:  env.Event.Message.RootID,
		Text:      strings.TrimSpace(text),
		Timestamp: time.Now(),
	}:
	default:
	}
}

// stripFeishuMentions 剥离文本中的 @_user_N mention 占位符。
func stripFeishuMentions(text string, mentions []struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}) string {
	for _, m := range mentions {
		if m.Key != "" {
			text = strings.ReplaceAll(text, m.Key, "")
		}
	}
	return text
}

// Send 经 im/v1/messages 以 chat_id 回复文本消息。
func (c *FeishuChannel) Send(ctx context.Context, msg OutboundMessage) error {
	token, err := c.tenantToken(ctx)
	if err != nil {
		return err
	}
	content, _ := json.Marshal(struct {
		Text string `json:"text"`
	}{Text: msg.Text})
	payload := map[string]any{
		"receive_id": msg.ChatID,
		"msg_type":   "text",
		"content":    string(content),
	}
	body, status, err := httpPostJSON(ctx, c.client,
		c.cfg.APIBase+"/open-apis/im/v1/messages?receive_id_type=chat_id",
		map[string]string{"Authorization": "Bearer " + token}, payload)
	if err != nil {
		return err
	}
	if status >= 300 {
		return apiError("飞书", status, body)
	}
	// 飞书业务错误码在 200 响应内（code != 0）
	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if json.Unmarshal(body, &resp) == nil && resp.Code != 0 {
		return fmt.Errorf("飞书 API 调用失败 (code %d): %s", resp.Code, resp.Msg)
	}
	return nil
}

// tenantToken 获取并缓存 tenant_access_token。
func (c *FeishuChannel) tenantToken(ctx context.Context) (string, error) {
	return c.tokens.get(func() (string, time.Duration, error) {
		body, status, err := httpPostJSON(ctx, c.client,
			c.cfg.APIBase+"/open-apis/auth/v3/tenant_access_token/internal", nil,
			map[string]string{"app_id": c.cfg.AppID, "app_secret": c.cfg.AppSecret})
		if err != nil {
			return "", 0, err
		}
		if status >= 300 {
			return "", 0, apiError("飞书", status, body)
		}
		var resp struct {
			Code              int    `json:"code"`
			Msg               string `json:"msg"`
			TenantAccessToken string `json:"tenant_access_token"`
			Expire            int    `json:"expire"`
		}
		if err := json.Unmarshal(body, &resp); err != nil || resp.Code != 0 || resp.TenantAccessToken == "" {
			return "", 0, fmt.Errorf("飞书渠道: 获取 tenant_access_token 失败: %s", string(body))
		}
		ttl := time.Duration(resp.Expire) * time.Second
		if ttl <= 0 {
			ttl = 30 * time.Minute
		}
		return resp.TenantAccessToken, ttl, nil
	})
}
