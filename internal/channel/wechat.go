// wechat.go 实现微信公众号渠道（"wechat"/"微信"）：
// 入站为公众平台消息回调（GET 用于接入校验 echostr，POST 为 XML 消息，
// sha1(sort(token,timestamp,nonce)) 签名验证）；因被动回复有 5 秒窗口限制，
// Agent 结果经客服消息接口异步投递。仅支持明文模式（加密模式回调将被跳过）。
package channel

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// wechatChannelName 微信公众号渠道的规范渠道 ID。
const wechatChannelName = "wechat"

// WeChatConfig 微信公众号配置。
type WeChatConfig struct {
	// AppID 公众号 appid。
	AppID string
	// AppSecret 公众号 appsecret（获取 access_token）。
	AppSecret string
	// Token 公众平台后台配置的回调校验 Token。
	Token string
	// PathPrefix 接收路由前缀（默认 "/channels/wechat"）。
	PathPrefix string
	// APIBase 公众平台 API 基址（默认 https://api.weixin.qq.com，可指向代理）。
	APIBase string
}

// wechatXMLMessage 公众平台文本消息 XML 结构。
type wechatXMLMessage struct {
	XMLName      xml.Name `xml:"xml"`
	ToUserName   string   `xml:"ToUserName"`
	FromUserName string   `xml:"FromUserName"`
	CreateTime   int64    `xml:"CreateTime"`
	MsgType      string   `xml:"MsgType"`
	Content      string   `xml:"Content"`
	MsgID        int64    `xml:"MsgId"`
}

// WeChatChannel 微信公众号渠道（实现 Channel 契约）。
type WeChatChannel struct {
	cfg    WeChatConfig
	mux    *http.ServeMux
	client *http.Client
	tokens tokenCache

	mu      sync.Mutex
	inbound chan<- InboundMessage
	status  ChannelStatus
}

// NewWeChatChannel 创建微信公众号渠道（appid/appsecret/token 缺失时拒绝创建）。
func NewWeChatChannel(cfg WeChatConfig, mux *http.ServeMux) (*WeChatChannel, error) {
	if cfg.AppID == "" || cfg.AppSecret == "" || cfg.Token == "" {
		return nil, fmt.Errorf("微信公众号渠道: 必须配置 app_id、app_secret 与 token")
	}
	if mux == nil {
		return nil, fmt.Errorf("微信公众号渠道: 需要 HTTP 路由（mux）")
	}
	if cfg.PathPrefix == "" {
		cfg.PathPrefix = "/channels/wechat"
	}
	if cfg.APIBase == "" {
		cfg.APIBase = "https://api.weixin.qq.com"
	}
	return &WeChatChannel{
		cfg:    cfg,
		mux:    mux,
		client: &http.Client{Timeout: 15 * time.Second},
		status: StatusStopped,
	}, nil
}

// Name 返回规范渠道 ID。
func (c *WeChatChannel) Name() string { return wechatChannelName }

// Start 注册接收路由（GET 校验 + POST 消息）。
func (c *WeChatChannel) Start(_ context.Context, in chan<- InboundMessage) error {
	c.mu.Lock()
	c.inbound = in
	c.status = StatusConnected
	c.mu.Unlock()
	c.mux.HandleFunc(c.cfg.PathPrefix, c.handleInbound)
	return nil
}

// Stop 停止接收。
func (c *WeChatChannel) Stop() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status = StatusStopped
	c.inbound = nil
	return nil
}

// Status 返回运行状态。
func (c *WeChatChannel) Status() ChannelStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// wechatSignature 公众平台签名：sha1(字典序排序后的 token/timestamp/nonce 拼接)。
func wechatSignature(token, timestamp, nonce string) string {
	parts := []string{token, timestamp, nonce}
	sort.Strings(parts)
	h := sha1.New()
	h.Write([]byte(strings.Join(parts, "")))
	return fmt.Sprintf("%x", h.Sum(nil))
}

// handleInbound 处理公众平台回调：GET 为接入校验，POST 为消息推送。
func (c *WeChatChannel) handleInbound(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	signature := q.Get("signature")
	timestamp := q.Get("timestamp")
	nonce := q.Get("nonce")
	if !ctEqual(wechatSignature(c.cfg.Token, timestamp, nonce), signature) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	// 接入校验：原样返回 echostr
	if r.Method == http.MethodGet {
		_, _ = w.Write([]byte(q.Get("echostr")))
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 加密模式无法处理（需 AES 解密），明确跳过并提示配置明文模式
	if q.Get("encrypt_type") == "aes" {
		_, _ = w.Write([]byte("success"))
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxResponseBody))
	if err != nil {
		http.Error(w, "read body failed", http.StatusBadRequest)
		return
	}
	var msg wechatXMLMessage
	if err := xml.Unmarshal(body, &msg); err != nil {
		http.Error(w, "invalid xml", http.StatusBadRequest)
		return
	}
	// 立即返回 success（公众平台要求 5 秒内响应，否则重推）
	_, _ = w.Write([]byte("success"))

	if msg.MsgType != "text" || strings.TrimSpace(msg.Content) == "" {
		return
	}

	c.mu.Lock()
	in := c.inbound
	c.mu.Unlock()
	if in == nil {
		return
	}
	select {
	case in <- InboundMessage{
		Channel:   wechatChannelName,
		MessageID: fmt.Sprintf("%d", msg.MsgID),
		UserID:    msg.FromUserName,
		ChatID:    msg.FromUserName, // 公众号会话均为用户一对一
		ChatType:  ChatTypeDM,
		Text:      strings.TrimSpace(msg.Content),
		Timestamp: time.Now(),
	}:
	default:
	}
}

// Send 经客服消息接口异步回复（被动回复 5 秒窗口无法容纳 Agent 执行时长）。
func (c *WeChatChannel) Send(ctx context.Context, msg OutboundMessage) error {
	token, err := c.accessToken(ctx)
	if err != nil {
		return err
	}
	payload := map[string]any{
		"touser":  msg.ChatID,
		"msgtype": "text",
		"text":    map[string]string{"content": msg.Text},
	}
	body, status, err := httpPostJSON(ctx, c.client,
		c.cfg.APIBase+"/cgi-bin/message/custom/send?access_token="+token, nil, payload)
	if err != nil {
		return err
	}
	if status >= 300 {
		return apiError("微信", status, body)
	}
	var resp struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if json.Unmarshal(body, &resp) == nil && resp.ErrCode != 0 {
		return fmt.Errorf("微信 API 调用失败 (errcode %d): %s", resp.ErrCode, resp.ErrMsg)
	}
	return nil
}

// accessToken 获取并缓存公众号 access_token。
func (c *WeChatChannel) accessToken(ctx context.Context) (string, error) {
	return c.tokens.get(func() (string, time.Duration, error) {
		url := fmt.Sprintf("%s/cgi-bin/token?grant_type=client_credential&appid=%s&secret=%s",
			c.cfg.APIBase, c.cfg.AppID, c.cfg.AppSecret)
		body, status, err := httpGetJSON(ctx, c.client, url, nil)
		if err != nil {
			return "", 0, err
		}
		if status >= 300 {
			return "", 0, apiError("微信", status, body)
		}
		var resp struct {
			AccessToken string `json:"access_token"`
			ExpiresIn   int    `json:"expires_in"`
			ErrCode     int    `json:"errcode"`
			ErrMsg      string `json:"errmsg"`
		}
		if err := json.Unmarshal(body, &resp); err != nil || resp.AccessToken == "" {
			return "", 0, fmt.Errorf("微信渠道: 获取 access_token 失败: %s", string(body))
		}
		ttl := time.Duration(resp.ExpiresIn) * time.Second
		if ttl <= 0 {
			ttl = 30 * time.Minute
		}
		return resp.AccessToken, ttl, nil
	})
}
