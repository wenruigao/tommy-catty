// qq.go 实现 QQ 官方机器人渠道（Webhook HTTP 回调模式，websocket 链路已下线）：
// 入站接收开放平台事件推送（op 13 回调地址验证以 ed25519 应答；
// op 0 消息事件立即 ACK 后入队）；出站经 access_token 调用
// /v2/users|groups/{openid}/messages 接口，携带 msg_id 被动回复。
package channel

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// qqChannelName QQ 渠道的规范渠道 ID。
const qqChannelName = "qq"

// QQConfig QQ 官方机器人配置。
type QQConfig struct {
	// AppID 机器人 AppID（开放平台管理端获得，必填）。
	AppID string
	// AppSecret 机器人 AppSecret（必填，同时派生 ed25519 签名密钥）。
	AppSecret string
	// PathPrefix 接收路由前缀（默认 "/channels/qq"）。
	PathPrefix string
	// APIBase OpenAPI 基址（默认 https://api.bot.qq.com，可指向代理）。
	APIBase string
	// TokenURL access_token 获取端点（默认 https://bots.qq.com/app/getAppAccessToken）。
	TokenURL string
}

// qqPayload 网关通用数据结构（webhook 与 websocket 共用）。
type qqPayload struct {
	Op   int             `json:"op"`
	ID   string          `json:"id"`
	Seq  int             `json:"s"`
	Type string          `json:"t"`
	Data json.RawMessage `json:"d"`
}

// qqValidation op 13 回调地址验证的 d 字段结构。
type qqValidation struct {
	PlainToken string `json:"plain_token"`
	EventTs    string `json:"event_ts"`
}

// qqMessageEvent 单聊/群聊消息事件（仅列用到的字段）。
type qqMessageEvent struct {
	ID        string `json:"id"`
	Content   string `json:"content"`
	Timestamp string `json:"timestamp"`
	Author    struct {
		UserOpenID   string `json:"user_openid"`
		MemberOpenID string `json:"member_openid"`
		UnionOpenID  string `json:"union_openid"`
	} `json:"author"`
	GroupOpenID string `json:"group_openid"`
}

// QQChannel QQ 渠道（实现 Channel 契约）。
type QQChannel struct {
	cfg        QQConfig
	mux        *http.ServeMux
	client     *http.Client
	tokens     tokenCache
	privateKey ed25519.PrivateKey

	mu         sync.Mutex
	inbound    chan<- InboundMessage
	status     ChannelStatus
	lastMsgIDs map[string]string // chatID -> 最近入站消息 ID（被动回复凭证）
	chatTypes  map[string]ChatType
}

// NewQQChannel 创建 QQ 渠道（app_id/app_secret 缺失时拒绝创建）。
func NewQQChannel(cfg QQConfig, mux *http.ServeMux) (*QQChannel, error) {
	if cfg.AppID == "" || cfg.AppSecret == "" {
		return nil, fmt.Errorf("qq 渠道: 必须配置 app_id 与 app_secret")
	}
	if mux == nil {
		return nil, fmt.Errorf("qq 渠道: 需要 HTTP 路由（mux）")
	}
	if cfg.PathPrefix == "" {
		cfg.PathPrefix = "/channels/qq"
	}
	if cfg.APIBase == "" {
		cfg.APIBase = "https://api.bot.qq.com"
	}
	if cfg.TokenURL == "" {
		cfg.TokenURL = "https://bots.qq.com/app/getAppAccessToken"
	}
	return &QQChannel{
		cfg:        cfg,
		mux:        mux,
		client:     &http.Client{Timeout: 15 * time.Second},
		privateKey: qqDerivedKey(cfg.AppSecret),
		status:     StatusStopped,
		lastMsgIDs: make(map[string]string),
		chatTypes:  make(map[string]ChatType),
	}, nil
}

// qqDerivedKey 按官方算法从 AppSecret 派生 ed25519 私钥：
// 将 secret 重复扩展至 32 字节种子长度后取前 32 字节。
func qqDerivedKey(secret string) ed25519.PrivateKey {
	seed := secret
	for len(seed) < ed25519.SeedSize {
		seed += seed
	}
	return ed25519.NewKeyFromSeed([]byte(seed[:ed25519.SeedSize]))
}

// Name 返回规范渠道 ID。
func (c *QQChannel) Name() string { return qqChannelName }

// Start 注册接收路由。
func (c *QQChannel) Start(_ context.Context, in chan<- InboundMessage) error {
	c.mu.Lock()
	c.inbound = in
	c.status = StatusConnected
	c.mu.Unlock()
	c.mux.HandleFunc("POST "+c.cfg.PathPrefix, c.handleInbound)
	return nil
}

// Stop 停止接收。
func (c *QQChannel) Stop() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status = StatusStopped
	c.inbound = nil
	return nil
}

// Status 返回运行状态。
func (c *QQChannel) Status() ChannelStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// handleInbound 处理开放平台事件推送：签名验证 → op 13 验证应答 / op 0 消息 ACK 入队。
// 平台要求 500ms 内响应，因此一律先回包再入队异步处理。
func (c *QQChannel) handleInbound(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxResponseBody))
	if err != nil {
		http.Error(w, "read body failed", http.StatusBadRequest)
		return
	}

	// 请求签名验证（平台携带 x-signature-ed25519 头时强制校验）：
	// signature = ed25519_sign(x-signature-timestamp + body) 的十六进制
	if sigHex := r.Header.Get("x-signature-ed25519"); sigHex != "" {
		ts := r.Header.Get("x-signature-timestamp")
		sig, err := hex.DecodeString(sigHex)
		if err != nil {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
		msg := append([]byte(ts), body...)
		pub := c.privateKey.Public().(ed25519.PublicKey)
		if !ed25519.Verify(pub, msg, sig) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
	}

	var p qqPayload
	if err := json.Unmarshal(body, &p); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}

	switch p.Op {
	case 13:
		// 回调地址验证：返回 plain_token 与 ed25519(event_ts + plain_token) 签名
		var v qqValidation
		if err := json.Unmarshal(p.Data, &v); err != nil {
			http.Error(w, "invalid validation payload", http.StatusBadRequest)
			return
		}
		sig := hex.EncodeToString(ed25519.Sign(c.privateKey, []byte(v.EventTs+v.PlainToken)))
		writeWebhookJSON(w, http.StatusOK, map[string]string{
			"plain_token": v.PlainToken,
			"signature":   sig,
		})

	case 0:
		// 先 ACK（op 12），再异步入队，避免平台判定服务不可用
		writeWebhookJSON(w, http.StatusOK, map[string]any{
			"op":  12,
			"id":  p.ID,
			"seq": p.Seq,
			"d":   struct{}{},
		})
		c.dispatchMessage(p)

	default:
		http.Error(w, "unsupported op", http.StatusBadRequest)
	}
}

// dispatchMessage 解析 op 0 消息事件（单聊/群聊 @）并入队，其他事件忽略。
func (c *QQChannel) dispatchMessage(p qqPayload) {
	chatType := ChatTypeDM
	switch p.Type {
	case "C2C_MESSAGE_CREATE":
	case "GROUP_AT_MESSAGE_CREATE":
		chatType = ChatTypeGroup
	default:
		return
	}

	var ev qqMessageEvent
	if err := json.Unmarshal(p.Data, &ev); err != nil {
		return
	}
	text := strings.TrimSpace(ev.Content)
	if text == "" {
		return
	}

	userID := ev.Author.UserOpenID
	if userID == "" {
		userID = ev.Author.MemberOpenID
	}
	if userID == "" {
		userID = ev.Author.UnionOpenID
	}
	chatID := userID
	if chatType == ChatTypeGroup {
		chatID = ev.GroupOpenID
	}
	if userID == "" || chatID == "" {
		return
	}

	c.mu.Lock()
	c.chatTypes[chatID] = chatType
	if ev.ID != "" {
		c.lastMsgIDs[chatID] = ev.ID
	}
	in := c.inbound
	c.mu.Unlock()
	if in == nil {
		return
	}
	select {
	case in <- InboundMessage{
		Channel:   qqChannelName,
		MessageID: ev.ID,
		UserID:    userID,
		ChatID:    chatID,
		ChatType:  chatType,
		Text:      text,
		Timestamp: time.Now(),
	}:
	default:
	}
}

// Send 经 /v2/users|groups/{openid}/messages 投递文本：
// 携带最近入站消息 msg_id 走被动回复（单聊主动消息每月仅 4 条额度）。
func (c *QQChannel) Send(ctx context.Context, msg OutboundMessage) error {
	token, err := c.accessToken(ctx)
	if err != nil {
		return err
	}
	c.mu.Lock()
	chatType := c.chatTypes[msg.ChatID]
	msgID := c.lastMsgIDs[msg.ChatID]
	c.mu.Unlock()

	path := "/v2/users/" + msg.ChatID + "/messages"
	if chatType == ChatTypeGroup {
		path = "/v2/groups/" + msg.ChatID + "/messages"
	}
	payload := map[string]any{
		"content":  msg.Text,
		"msg_type": 0,
	}
	if msgID != "" {
		payload["msg_id"] = msgID
	}
	body, status, err := httpPostJSON(ctx, c.client, c.cfg.APIBase+path,
		map[string]string{"Authorization": "QQBot " + token}, payload)
	if err != nil {
		return err
	}
	if status >= 300 {
		return apiError("qq", status, body)
	}
	// 业务错误以 err_code 判定（message 文案不稳定，不作为依据）
	var resp struct {
		ErrCode int    `json:"err_code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &resp) == nil && resp.ErrCode != 0 {
		return fmt.Errorf("qq API 调用失败 (err_code %d): %s", resp.ErrCode, resp.Message)
	}
	return nil
}

// accessToken 获取并缓存 OpenAPI access_token。
func (c *QQChannel) accessToken(ctx context.Context) (string, error) {
	return c.tokens.get(func() (string, time.Duration, error) {
		body, status, err := httpPostJSON(ctx, c.client, c.cfg.TokenURL, nil,
			map[string]string{"appId": c.cfg.AppID, "clientSecret": c.cfg.AppSecret})
		if err != nil {
			return "", 0, err
		}
		if status >= 300 {
			return "", 0, apiError("qq", status, body)
		}
		var resp struct {
			AccessToken string          `json:"access_token"`
			ExpiresIn   json.RawMessage `json:"expires_in"`
		}
		if err := json.Unmarshal(body, &resp); err != nil || resp.AccessToken == "" {
			return "", 0, fmt.Errorf("qq 渠道: 获取 access_token 失败: %s", string(body))
		}
		// expires_in 官方返回可能为数字或字符串（如 "7200"），两者均兼容
		var ttlSec int
		if len(resp.ExpiresIn) > 0 {
			ttlSec, _ = strconv.Atoi(strings.Trim(string(resp.ExpiresIn), `"`))
		}
		ttl := time.Duration(ttlSec) * time.Second
		if ttl <= 0 {
			ttl = 30 * time.Minute
		}
		return resp.AccessToken, ttl, nil
	})
}
