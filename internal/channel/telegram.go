// telegram.go 实现 Telegram Bot 渠道（长轮询模式，无需公网回调地址）：
// 经 getUpdates 拉取消息，sendMessage 投递回复。群聊默认仅响应
// 含 mention/@ 的消息（mention 门控在 adapter 侧完成）。
package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// telegramChannelName Telegram 渠道的规范渠道 ID。
const telegramChannelName = "telegram"

// TelegramConfig Telegram Bot 配置。
type TelegramConfig struct {
	// Token BotFather 颁发的 Bot Token（必填）。
	Token string
	// APIBase Bot API 基址（默认 https://api.telegram.org，可指向代理）。
	APIBase string
}

// tgUpdate getUpdates 返回的单个更新。
type tgUpdate struct {
	UpdateID int        `json:"update_id"`
	Message  *tgMessage `json:"message"`
}

// tgMessage Telegram 消息（仅列用到的字段）。
type tgMessage struct {
	MessageID int64 `json:"message_id"`
	From      struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
	} `json:"from"`
	Chat struct {
		ID   int64  `json:"id"`
		Type string `json:"type"` // private | group | supergroup | channel
	} `json:"chat"`
	Text     string `json:"text"`
	Entities []struct {
		Type string `json:"type"`
	} `json:"entities"`
	Date int64 `json:"date"`
}

// TelegramChannel Telegram 渠道（实现 Channel 契约）。
type TelegramChannel struct {
	cfg    TelegramConfig
	client *http.Client

	mu      sync.Mutex
	inbound chan<- InboundMessage
	status  ChannelStatus
	cancel  context.CancelFunc
	done    chan struct{}
}

// NewTelegramChannel 创建 Telegram 渠道（token 缺失时拒绝创建）。
func NewTelegramChannel(cfg TelegramConfig) (*TelegramChannel, error) {
	if cfg.Token == "" {
		return nil, fmt.Errorf("telegram 渠道: 必须配置 token")
	}
	if cfg.APIBase == "" {
		cfg.APIBase = "https://api.telegram.org"
	}
	return &TelegramChannel{
		cfg:    cfg,
		client: &http.Client{Timeout: 60 * time.Second}, // 长轮询 timeout=30s + 余量
		status: StatusStopped,
	}, nil
}

// Name 返回规范渠道 ID。
func (c *TelegramChannel) Name() string { return telegramChannelName }

// Start 启动长轮询 goroutine。
func (c *TelegramChannel) Start(ctx context.Context, in chan<- InboundMessage) error {
	pollCtx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.inbound = in
	c.status = StatusConnected
	c.cancel = cancel
	c.done = make(chan struct{})
	c.mu.Unlock()
	go c.pollLoop(pollCtx, in)
	return nil
}

// Stop 停止长轮询并等待退出。
func (c *TelegramChannel) Stop() error {
	c.mu.Lock()
	if c.cancel != nil {
		c.cancel()
	}
	done := c.done
	c.status = StatusStopped
	c.inbound = nil
	c.mu.Unlock()
	if done != nil {
		<-done
	}
	return nil
}

// Status 返回运行状态。
func (c *TelegramChannel) Status() ChannelStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// pollLoop 长轮询主循环：拉取更新 → 转换入队 → 断线退避重连。
func (c *TelegramChannel) pollLoop(ctx context.Context, in chan<- InboundMessage) {
	defer close(c.done)
	offset := 0
	backoff := time.Second

	for ctx.Err() == nil {
		updates, err := c.getUpdates(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			c.mu.Lock()
			c.status = StatusDegraded
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		c.mu.Lock()
		c.status = StatusConnected
		c.mu.Unlock()

		for _, u := range updates {
			offset = u.UpdateID + 1
			if msg := mapTelegramMessage(u.Message); msg != nil {
				select {
				case in <- *msg:
				default:
				}
			}
		}
	}
}

// getUpdates 拉取一次更新（long polling，timeout=30s）。
func (c *TelegramChannel) getUpdates(ctx context.Context, offset int) ([]tgUpdate, error) {
	url := fmt.Sprintf("%s/bot%s/getUpdates?offset=%d&timeout=30&allowed_updates=%s",
		c.cfg.APIBase, c.cfg.Token, offset, `["message"]`)
	body, status, err := httpGetJSON(ctx, c.client, url, nil)
	if err != nil {
		return nil, err
	}
	if status >= 300 {
		return nil, apiError("telegram", status, body)
	}
	var resp struct {
		OK     bool       `json:"ok"`
		Result []tgUpdate `json:"result"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || !resp.OK {
		return nil, fmt.Errorf("telegram getUpdates 响应非法: %s", string(body))
	}
	return resp.Result, nil
}

// mapTelegramMessage 将 Telegram 消息转换为统一入站消息（nil 表示忽略）：
// 仅处理文本；群聊要求含 mention/bot_command 实体（mention 门控）。
func mapTelegramMessage(m *tgMessage) *InboundMessage {
	if m == nil || strings.TrimSpace(m.Text) == "" {
		return nil
	}
	chatType := ChatTypeDM
	if m.Chat.Type != "private" {
		chatType = ChatTypeGroup
		hasMention := false
		for _, e := range m.Entities {
			if e.Type == "mention" || e.Type == "bot_command" {
				hasMention = true
				break
			}
		}
		if !hasMention {
			return nil // 群聊未 @机器人：不响应
		}
	}
	return &InboundMessage{
		Channel:   telegramChannelName,
		MessageID: strconv.FormatInt(m.MessageID, 10),
		UserID:    strconv.FormatInt(m.From.ID, 10),
		ChatID:    strconv.FormatInt(m.Chat.ID, 10),
		ChatType:  chatType,
		Text:      strings.TrimSpace(m.Text),
		Timestamp: time.Unix(m.Date, 0),
	}
}

// Send 经 sendMessage 投递回复。
func (c *TelegramChannel) Send(ctx context.Context, msg OutboundMessage) error {
	url := fmt.Sprintf("%s/bot%s/sendMessage", c.cfg.APIBase, c.cfg.Token)
	payload := map[string]any{
		"chat_id": msg.ChatID,
		"text":    msg.Text,
	}
	body, status, err := httpPostJSON(ctx, c.client, url, nil, payload)
	if err != nil {
		return err
	}
	if status >= 300 {
		return apiError("telegram", status, body)
	}
	var resp struct {
		OK bool `json:"ok"`
	}
	if json.Unmarshal(body, &resp) == nil && !resp.OK {
		return apiError("telegram", status, body)
	}
	return nil
}

// MaxChunk 实现 Chunker：Telegram 单条消息上限 4096 字符。
func (c *TelegramChannel) MaxChunk() int { return 4096 }
