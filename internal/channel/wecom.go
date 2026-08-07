// wecom.go 实现企业微信自建应用渠道：
// 入站支持明文与 AES 加密两种回调模式（msg_signature 签名验证 +
// EncodingAESKey 解密）；出站经应用消息接口 message/send 投递。
package channel

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// wecomChannelName 企业微信渠道的规范渠道 ID。
const wecomChannelName = "wecom"

// WeComConfig 企业微信配置。
type WeComConfig struct {
	// CorpID 企业 ID（解密后校验 receiveid 归属）。
	CorpID string
	// AgentID 自建应用 agentid（出站必填）。
	AgentID string
	// AgentSecret 自建应用 secret（获取 access_token）。
	AgentSecret string
	// Token 回调配置的 Token（签名验证）。
	Token string
	// EncodingAESKey 回调加密密钥（43 位；配置后支持加密模式，未配置仅支持明文）。
	EncodingAESKey string
	// PathPrefix 接收路由前缀（默认 "/channels/wecom"）。
	PathPrefix string
	// APIBase 企业微信 API 基址（默认 https://qyapi.weixin.qq.com，可指向代理）。
	APIBase string
}

// wecomXMLMessage 企业微信回调消息 XML（明文或解密后的内层消息）。
type wecomXMLMessage struct {
	XMLName      xml.Name `xml:"xml"`
	ToUserName   string   `xml:"ToUserName"`
	FromUserName string   `xml:"FromUserName"`
	CreateTime   int64    `xml:"CreateTime"`
	MsgType      string   `xml:"MsgType"`
	Content      string   `xml:"Content"`
	MsgID        int64    `xml:"MsgId"`
	AgentID      string   `xml:"AgentID"`
}

// wecomEncryptedXML 加密模式回调的外层 XML。
type wecomEncryptedXML struct {
	XMLName    xml.Name `xml:"xml"`
	ToUserName string   `xml:"ToUserName"`
	Encrypt    string   `xml:"Encrypt"`
}

// WeComChannel 企业微信渠道（实现 Channel 契约）。
type WeComChannel struct {
	cfg    WeComConfig
	mux    *http.ServeMux
	client *http.Client
	tokens tokenCache

	mu      sync.Mutex
	inbound chan<- InboundMessage
	status  ChannelStatus
}

// NewWeComChannel 创建企业微信渠道（必填项缺失时拒绝创建）。
func NewWeComChannel(cfg WeComConfig, mux *http.ServeMux) (*WeComChannel, error) {
	if cfg.CorpID == "" || cfg.AgentSecret == "" || cfg.Token == "" || cfg.AgentID == "" {
		return nil, fmt.Errorf("企业微信渠道: 必须配置 corp_id、agent_id、agent_secret 与 token")
	}
	if mux == nil {
		return nil, fmt.Errorf("企业微信渠道: 需要 HTTP 路由（mux）")
	}
	if cfg.PathPrefix == "" {
		cfg.PathPrefix = "/channels/wecom"
	}
	if cfg.APIBase == "" {
		cfg.APIBase = "https://qyapi.weixin.qq.com"
	}
	return &WeComChannel{
		cfg:    cfg,
		mux:    mux,
		client: &http.Client{Timeout: 15 * time.Second},
		status: StatusStopped,
	}, nil
}

// Name 返回规范渠道 ID。
func (c *WeComChannel) Name() string { return wecomChannelName }

// Start 注册接收路由（GET 校验 + POST 消息）。
func (c *WeComChannel) Start(_ context.Context, in chan<- InboundMessage) error {
	c.mu.Lock()
	c.inbound = in
	c.status = StatusConnected
	c.mu.Unlock()
	c.mux.HandleFunc(c.cfg.PathPrefix, c.handleInbound)
	return nil
}

// Stop 停止接收。
func (c *WeComChannel) Stop() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status = StatusStopped
	c.inbound = nil
	return nil
}

// Status 返回运行状态。
func (c *WeComChannel) Status() ChannelStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// wecomSignature 企业微信签名：sha1(字典序排序后的 token/timestamp/nonce[/encrypt] 拼接)。
func wecomSignature(parts ...string) string {
	sort.Strings(parts)
	h := sha1.New()
	h.Write([]byte(strings.Join(parts, "")))
	return fmt.Sprintf("%x", h.Sum(nil))
}

// handleInbound 处理企业微信回调：GET 接入校验；POST 消息（明文或加密）。
func (c *WeComChannel) handleInbound(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	// GET 接入校验（URL 验证 echostr，此处仅做签名校验后返回成功）
	if r.Method == http.MethodGet {
		if !ctEqual(wecomSignature(c.cfg.Token, q.Get("timestamp"), q.Get("nonce"), q.Get("echostr")), q.Get("msg_signature")) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
		// 完整的 echostr 解密回显仅加密模式需要；明文接入直接回显
		_, _ = w.Write([]byte(q.Get("echostr")))
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxResponseBody))
	if err != nil {
		http.Error(w, "read body failed", http.StatusBadRequest)
		return
	}

	var xmlMsg wecomXMLMessage
	if q.Get("encrypt_type") == "aes" {
		// 加密模式：验签 → 解密 → 解析内层 XML
		var enc wecomEncryptedXML
		if err := xml.Unmarshal(body, &enc); err != nil || enc.Encrypt == "" {
			http.Error(w, "invalid encrypted body", http.StatusBadRequest)
			return
		}
		if !ctEqual(wecomSignature(c.cfg.Token, q.Get("timestamp"), q.Get("nonce"), enc.Encrypt), q.Get("msg_signature")) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
		plain, err := wecomDecrypt(c.cfg.EncodingAESKey, enc.Encrypt, c.cfg.CorpID)
		if err != nil {
			http.Error(w, "decrypt failed", http.StatusBadRequest)
			return
		}
		if err := xml.Unmarshal(plain, &xmlMsg); err != nil {
			http.Error(w, "invalid inner xml", http.StatusBadRequest)
			return
		}
	} else {
		// 明文模式：仅签名校验
		if !ctEqual(wecomSignature(c.cfg.Token, q.Get("timestamp"), q.Get("nonce")), q.Get("msg_signature")) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
		if err := xml.Unmarshal(body, &xmlMsg); err != nil {
			http.Error(w, "invalid xml", http.StatusBadRequest)
			return
		}
	}

	// 立即返回 success（空字符串亦可，企业微信不重推已响应请求）
	_, _ = w.Write([]byte("success"))

	if xmlMsg.MsgType != "text" || strings.TrimSpace(xmlMsg.Content) == "" {
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
		Channel:   wecomChannelName,
		MessageID: fmt.Sprintf("%d", xmlMsg.MsgID),
		UserID:    xmlMsg.FromUserName,
		ChatID:    xmlMsg.FromUserName, // 自建应用会话均为用户一对一
		ChatType:  ChatTypeDM,
		Text:      strings.TrimSpace(xmlMsg.Content),
		Timestamp: time.Now(),
	}:
	default:
	}
}

// wecomDecrypt 解密企业微信消息（AES-256-CBC，块大小 32 的 PKCS#7）：
// random(16) + msgLen(4 大端) + msg + receiveid，校验 receiveid 归属。
func wecomDecrypt(encodingAESKey, encrypted, corpID string) ([]byte, error) {
	if encodingAESKey == "" {
		return nil, fmt.Errorf("未配置 encoding_aes_key，无法处理加密回调")
	}
	key, err := base64.StdEncoding.DecodeString(encodingAESKey + "=")
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("encoding_aes_key 非法")
	}
	data, err := base64.StdEncoding.DecodeString(encrypted)
	if err != nil {
		return nil, fmt.Errorf("密文 base64 解码失败: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(data)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("密文长度非法")
	}
	plain := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, key[:16]).CryptBlocks(plain, data)

	// 去除 PKCS#7 填充（块大小 32）
	if len(plain) == 0 {
		return nil, fmt.Errorf("解密结果为空")
	}
	pad := int(plain[len(plain)-1])
	if pad < 1 || pad > 32 || pad > len(plain) {
		return nil, fmt.Errorf("填充非法")
	}
	plain = plain[:len(plain)-pad]

	// random(16) + msgLen(4) + msg + receiveid
	if len(plain) < 20 {
		return nil, fmt.Errorf("明文长度非法")
	}
	msgLen := int(binary.BigEndian.Uint32(plain[16:20]))
	if 20+msgLen > len(plain) {
		return nil, fmt.Errorf("消息长度字段非法")
	}
	msg := plain[20 : 20+msgLen]
	receiveID := string(plain[20+msgLen:])
	if receiveID != corpID {
		return nil, fmt.Errorf("receiveid 与 corp_id 不匹配")
	}
	return msg, nil
}

// Send 经应用消息接口回复文本。
func (c *WeComChannel) Send(ctx context.Context, msg OutboundMessage) error {
	token, err := c.accessToken(ctx)
	if err != nil {
		return err
	}
	agentID, err := strconv.Atoi(c.cfg.AgentID)
	if err != nil {
		return fmt.Errorf("企业微信渠道: agent_id 非法: %w", err)
	}
	payload := map[string]any{
		"touser":  msg.ChatID,
		"msgtype": "text",
		"agentid": agentID,
		"text":    map[string]string{"content": msg.Text},
	}
	body, status, err := httpPostJSON(ctx, c.client,
		c.cfg.APIBase+"/cgi-bin/message/send?access_token="+token, nil, payload)
	if err != nil {
		return err
	}
	if status >= 300 {
		return apiError("企业微信", status, body)
	}
	var resp struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if json.Unmarshal(body, &resp) == nil && resp.ErrCode != 0 {
		return fmt.Errorf("企业微信 API 调用失败 (errcode %d): %s", resp.ErrCode, resp.ErrMsg)
	}
	return nil
}

// accessToken 获取并缓存企业微信 access_token。
func (c *WeComChannel) accessToken(ctx context.Context) (string, error) {
	return c.tokens.get(func() (string, time.Duration, error) {
		url := fmt.Sprintf("%s/cgi-bin/gettoken?corpid=%s&corpsecret=%s",
			c.cfg.APIBase, c.cfg.CorpID, c.cfg.AgentSecret)
		body, status, err := httpGetJSON(ctx, c.client, url, nil)
		if err != nil {
			return "", 0, err
		}
		if status >= 300 {
			return "", 0, apiError("企业微信", status, body)
		}
		var resp struct {
			AccessToken string `json:"access_token"`
			ExpiresIn   int    `json:"expires_in"`
			ErrCode     int    `json:"errcode"`
		}
		if err := json.Unmarshal(body, &resp); err != nil || resp.AccessToken == "" {
			return "", 0, fmt.Errorf("企业微信渠道: 获取 access_token 失败: %s", string(body))
		}
		ttl := time.Duration(resp.ExpiresIn) * time.Second
		if ttl <= 0 {
			ttl = 30 * time.Minute
		}
		return resp.AccessToken, ttl, nil
	})
}
