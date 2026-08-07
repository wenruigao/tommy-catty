// dingtalk.go 实现钉钉企业内部机器人渠道（HTTP 回调模式）：
// 入站经 timestamp+sign（HmacSHA256 加签）验证；出站群聊走
// sessionWebhook，单聊走 oToMessages/batchSend 组织 API。
package channel

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// dingtalkChannelName 钉钉渠道的规范渠道 ID。
const dingtalkChannelName = "dingtalk"

// dingtalkMaxClockSkew 回调时间戳允许的最大偏差（钉钉规定超过 1 小时为非法请求）。
const dingtalkMaxClockSkew = time.Hour

// DingTalkConfig 钉钉渠道配置。
type DingTalkConfig struct {
	// ClientID 应用 appKey（单聊回复时作为 robotCode）。
	ClientID string
	// ClientSecret 应用 appSecret，同时作为回调加签密钥。
	ClientSecret string
	// PathPrefix 接收路由前缀（默认 "/channels/dingtalk"）。
	PathPrefix string
	// APIBase 开放平台 API 基址（默认 https://api.dingtalk.com，可指向代理）。
	APIBase string
}

// dingtalkPayload 钉钉机器人回调请求体（HTTP 模式，仅列用到的字段）。
type dingtalkPayload struct {
	ConversationID   string `json:"conversationId"`
	ConversationType string `json:"conversationType"` // "1" 单聊，"2" 群聊
	MsgID            string `json:"msgId"`
	MsgType          string `json:"msgtype"`
	Text             struct {
		Content string `json:"content"`
	} `json:"text"`
	SenderID       string `json:"senderId"`
	SenderStaffID  string `json:"senderStaffId"`
	RobotCode      string `json:"robotCode"`
	SessionWebhook string `json:"sessionWebhook"`
}

// DingTalkChannel 钉钉渠道（实现 Channel 契约）。
type DingTalkChannel struct {
	cfg    DingTalkConfig
	mux    *http.ServeMux
	client *http.Client
	tokens tokenCache

	mu      sync.Mutex
	inbound chan<- InboundMessage
	status  ChannelStatus
	// 会话元数据：群聊记录 sessionWebhook（出站投递地址），全部记录会话类型
	webhooks map[string]string
	chatType map[string]ChatType
}

// NewDingTalkChannel 创建钉钉渠道（appKey/appSecret 缺失时拒绝创建）。
func NewDingTalkChannel(cfg DingTalkConfig, mux *http.ServeMux) (*DingTalkChannel, error) {
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, fmt.Errorf("钉钉渠道: 必须配置 client_id 与 client_secret")
	}
	if mux == nil {
		return nil, fmt.Errorf("钉钉渠道: 需要 HTTP 路由（mux）")
	}
	if cfg.PathPrefix == "" {
		cfg.PathPrefix = "/channels/dingtalk"
	}
	if cfg.APIBase == "" {
		cfg.APIBase = "https://api.dingtalk.com"
	}
	return &DingTalkChannel{
		cfg:      cfg,
		mux:      mux,
		client:   &http.Client{Timeout: 15 * time.Second},
		status:   StatusStopped,
		webhooks: make(map[string]string),
		chatType: make(map[string]ChatType),
	}, nil
}

// Name 返回规范渠道 ID。
func (c *DingTalkChannel) Name() string { return dingtalkChannelName }

// Start 注册接收路由。
func (c *DingTalkChannel) Start(_ context.Context, in chan<- InboundMessage) error {
	c.mu.Lock()
	c.inbound = in
	c.status = StatusConnected
	c.mu.Unlock()
	c.mux.HandleFunc("POST "+c.cfg.PathPrefix, c.handleInbound)
	return nil
}

// Stop 停止接收。
func (c *DingTalkChannel) Stop() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status = StatusStopped
	c.inbound = nil
	return nil
}

// Status 返回运行状态。
func (c *DingTalkChannel) Status() ChannelStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// dingtalkSign 计算钉钉加签：Base64(HmacSHA256(timestamp+"\n"+secret, secret))。
func dingtalkSign(timestamp, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "\n" + secret))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// handleInbound 接收钉钉回调：验证 timestamp/sign → 转换消息 → 立即回 200。
func (c *DingTalkChannel) handleInbound(w http.ResponseWriter, r *http.Request) {
	// 加签验证：timestamp 偏差超过 1 小时或 sign 不一致均视为非法请求
	ts := r.Header.Get("timestamp")
	sign := r.Header.Get("sign")
	tsMillis, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || time.Since(time.UnixMilli(tsMillis)).Abs() > dingtalkMaxClockSkew {
		http.Error(w, "invalid timestamp", http.StatusUnauthorized)
		return
	}
	if !ctEqual(dingtalkSign(ts, c.cfg.ClientSecret), sign) {
		http.Error(w, "invalid sign", http.StatusUnauthorized)
		return
	}

	var p dingtalkPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	// 仅处理文本消息（其他类型回复提示）
	if p.MsgType != "" && p.MsgType != "text" {
		writeWebhookJSON(w, http.StatusOK, map[string]string{"status": "unsupported msgtype"})
		return
	}
	text := strings.TrimSpace(p.Text.Content)
	if text == "" {
		writeWebhookJSON(w, http.StatusOK, map[string]string{"status": "empty"})
		return
	}

	chatType := ChatTypeDM
	if p.ConversationType == "2" {
		chatType = ChatTypeGroup
	}
	// 群聊会话键用群 ID，单聊用发送者 staffId（oTo 回复接口需要）
	chatID := p.ConversationID
	userID := p.SenderStaffID
	if userID == "" {
		userID = p.SenderID
	}
	if chatType == ChatTypeDM {
		chatID = userID
	}

	c.mu.Lock()
	c.chatType[chatID] = chatType
	if p.SessionWebhook != "" {
		c.webhooks[chatID] = p.SessionWebhook
	}
	in := c.inbound
	c.mu.Unlock()

	// 立即 ack（钉钉回调有超时约束），消息异步入队
	writeWebhookJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	if in == nil {
		return
	}
	select {
	case in <- InboundMessage{
		Channel:   dingtalkChannelName,
		MessageID: p.MsgID,
		UserID:    userID,
		ChatID:    chatID,
		ChatType:  chatType,
		Text:      text,
		Timestamp: time.Now(),
	}:
	default:
	}
}

// Send 投递回复：群聊走 sessionWebhook；单聊走 oToMessages/batchSend。
func (c *DingTalkChannel) Send(ctx context.Context, msg OutboundMessage) error {
	c.mu.Lock()
	chatType := c.chatType[msg.ChatID]
	webhook := c.webhooks[msg.ChatID]
	c.mu.Unlock()

	if webhook != "" {
		// 群聊（或带 sessionWebhook 的会话）：直接 POST 文本消息
		payload := map[string]any{
			"msgtype": "text",
			"text":    map[string]string{"content": msg.Text},
		}
		body, status, err := httpPostJSON(ctx, c.client, webhook, nil, payload)
		if err != nil {
			return err
		}
		if status >= 300 {
			return apiError("钉钉", status, body)
		}
		return nil
	}

	if chatType != ChatTypeDM {
		return fmt.Errorf("钉钉渠道: 会话 %s 无可用投递方式（sessionWebhook 已过期且非单聊）", msg.ChatID)
	}

	// 单聊：组织 API批量发送（robotCode + userIds）
	token, err := c.accessToken(ctx)
	if err != nil {
		return err
	}
	payload := map[string]any{
		"robotCode": c.cfg.ClientID,
		"userIds":   []string{msg.ChatID},
		"msgKey":    "sampleText",
		"msgParam":  fmt.Sprintf(`{"content":%s}`, mustJSONString(msg.Text)),
	}
	body, status, err := httpPostJSON(ctx, c.client, c.cfg.APIBase+"/v1.0/robot/oToMessages/batchSend",
		map[string]string{"x-acs-dingtalk-access-token": token}, payload)
	if err != nil {
		return err
	}
	if status >= 300 {
		return apiError("钉钉", status, body)
	}
	return nil
}

// accessToken 获取并缓存组织 API access token。
func (c *DingTalkChannel) accessToken(ctx context.Context) (string, error) {
	return c.tokens.get(func() (string, time.Duration, error) {
		body, status, err := httpPostJSON(ctx, c.client, c.cfg.APIBase+"/v1.0/oauth2/accessToken", nil,
			map[string]string{"appKey": c.cfg.ClientID, "appSecret": c.cfg.ClientSecret})
		if err != nil {
			return "", 0, err
		}
		if status >= 300 {
			return "", 0, apiError("钉钉", status, body)
		}
		var resp struct {
			AccessToken string `json:"accessToken"`
			ExpireIn    int    `json:"expireIn"`
		}
		if err := json.Unmarshal(body, &resp); err != nil || resp.AccessToken == "" {
			return "", 0, fmt.Errorf("钉钉渠道: 获取 access token 失败: %s", string(body))
		}
		ttl := time.Duration(resp.ExpireIn) * time.Second
		if ttl <= 0 {
			ttl = 30 * time.Minute
		}
		return resp.AccessToken, ttl, nil
	})
}

// mustJSONString 将文本转为 JSON 字符串字面量（含引号，转义特殊字符）。
func mustJSONString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
