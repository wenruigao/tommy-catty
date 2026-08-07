// hub.go 实现 ChannelHub 消息总线：渠道注册与生命周期管理，
// 以及入站管道（去重 → 访问控制 → 群门控 → 会话路由 → 异步执行 → 出站投递）。
package channel

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/tommy-cat/agent/internal/engine"
	"github.com/tommy-cat/agent/internal/session"
)

// defaultMaxChunk 出站消息默认分片上限（rune 计数，渠道实现 Chunker 可覆盖）。
const defaultMaxChunk = 4000

// SessionRunner 会话执行入口的最小接口（*session.UserSession 天然满足）。
// 用最小接口而非具体类型，便于测试注入 fake。
type SessionRunner interface {
	Run(ctx context.Context, goal string) (*engine.ExecutionTrace, error)
}

// HubConfig Hub 配置。
type HubConfig struct {
	// QueueSize 每渠道入站消息队列容量（默认 256，满则丢弃并告警）。
	QueueSize int
	// DedupeWindow 去重窗口（默认 10 分钟，按 渠道名:MessageID）。
	DedupeWindow time.Duration
	// DefaultTimeout 消息执行默认超时（渠道未配置 request_timeout 时使用，默认 120s）。
	DefaultTimeout time.Duration
	// SendRetries 出站投递失败重试次数（指数退避，默认 3）。
	SendRetries int
}

// DefaultHubConfig 返回 Hub 默认配置。
func DefaultHubConfig() HubConfig {
	return HubConfig{
		QueueSize:      256,
		DedupeWindow:   10 * time.Minute,
		DefaultTimeout: 120 * time.Second,
		SendRetries:    3,
	}
}

// ChannelConfig 单渠道的路由与安全策略配置（对应 config.ChannelEntry）。
type ChannelConfig struct {
	// AllowUsers 允许使用的平台用户白名单（空或含 "*" 表示不限制）。
	AllowUsers []string
	// GroupMode 群消息模式：always | mention_only | never
	//（mention 判断由 adapter 侧完成；Hub 仅强制 never 时丢弃群消息）。
	GroupMode string
	// AckMessage 受理提示（非空时执行前先回复一条，长任务体验关键）。
	AckMessage string
	// RequestTimeout 单条消息执行超时（<=0 时使用 HubConfig.DefaultTimeout）。
	RequestTimeout time.Duration
}

// Hub 渠道消息总线：统一注册、启停所有渠道，并驱动入站管道。
type Hub struct {
	cfg        HubConfig
	mu         sync.RWMutex
	channels   map[string]Channel
	configs    map[string]ChannelConfig
	getSession func(userID string) SessionRunner

	stopOnce sync.Once
	stopCh   chan struct{}
	wg       sync.WaitGroup

	dedupMu sync.Mutex
	dedup   map[string]time.Time
}

// NewHub 创建渠道消息总线。getSession 用于按会话键获取（或惰性创建）会话，
// 通常传入 session.SessionManager.GetOrCreate 的包装。
func NewHub(getSession func(userID string) SessionRunner, cfg HubConfig) *Hub {
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 256
	}
	if cfg.DedupeWindow <= 0 {
		cfg.DedupeWindow = 10 * time.Minute
	}
	if cfg.DefaultTimeout <= 0 {
		cfg.DefaultTimeout = 120 * time.Second
	}
	if cfg.SendRetries < 0 {
		cfg.SendRetries = 0
	}
	return &Hub{
		cfg:        cfg,
		channels:   make(map[string]Channel),
		configs:    make(map[string]ChannelConfig),
		getSession: getSession,
		stopCh:     make(chan struct{}),
		dedup:      make(map[string]time.Time),
	}
}

// Register 注册渠道及其策略配置（Start 之前调用）。
func (h *Hub) Register(name string, ch Channel, cc ChannelConfig) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.channels[name] = ch
	h.configs[name] = cc
}

// Start 启动所有已注册渠道：每渠道一个有界入站队列 + 一个消费 goroutine。
// 任一渠道启动失败仅记日志降级，不拖垮其他渠道；全部失败时返回错误。
func (h *Hub) Start(ctx context.Context) error {
	h.mu.RLock()
	defer h.mu.RUnlock()

	started := 0
	for name, ch := range h.channels {
		in := make(chan InboundMessage, h.cfg.QueueSize)
		if err := ch.Start(ctx, in); err != nil {
			log.Printf("警告: 渠道 %q 启动失败，已降级跳过: %v", name, err)
			continue
		}
		h.wg.Add(1)
		go h.consume(name, in)
		started++
		log.Printf("渠道 %q 已启动", name)
	}
	if started == 0 {
		return errors.New("channel: 没有任何渠道成功启动")
	}
	return nil
}

// Stop 优雅停止所有渠道并等待消费 goroutine 退出。
func (h *Hub) Stop() {
	h.stopOnce.Do(func() { close(h.stopCh) })

	h.mu.RLock()
	for name, ch := range h.channels {
		if err := ch.Stop(); err != nil {
			log.Printf("警告: 渠道 %q 停止出错: %v", name, err)
		}
	}
	h.mu.RUnlock()

	h.wg.Wait()
}

// Statuses 返回所有渠道的运行状态快照（供健康检查端点）。
func (h *Hub) Statuses() map[string]ChannelStatus {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make(map[string]ChannelStatus, len(h.channels))
	for name, ch := range h.channels {
		out[name] = ch.Status()
	}
	return out
}

// consume 消费单个渠道的入站队列，直到渠道停止。
func (h *Hub) consume(name string, in <-chan InboundMessage) {
	defer h.wg.Done()
	for {
		select {
		case <-h.stopCh:
			return
		case msg, ok := <-in:
			if !ok {
				return
			}
			h.Dispatch(name, msg)
		}
	}
}

// Dispatch 执行入站管道（导出以便测试；正常运行由 consume 调用）：
// 空文本过滤 → 去重 → 访问控制 → 群门控 → 受理提示 → 异步执行。
func (h *Hub) Dispatch(name string, msg InboundMessage) {
	h.mu.RLock()
	cc := h.configs[name]
	h.mu.RUnlock()

	if strings.TrimSpace(msg.Text) == "" {
		return
	}
	if h.isDuplicate(name, msg.MessageID) {
		log.Printf("渠道 %q 丢弃重复消息 %q（平台重推/回调重试）", name, msg.MessageID)
		return
	}
	if !userAllowed(cc.AllowUsers, msg.UserID) {
		log.Printf("渠道 %q 拒绝未授权用户 %q", name, msg.UserID)
		return
	}
	if msg.ChatType == ChatTypeGroup && cc.GroupMode == GroupModeNever {
		return
	}

	sessionKey := SessionKey(name, msg)
	if cc.AckMessage != "" {
		h.deliver(name, OutboundMessage{
			ChatID:   msg.ChatID,
			ThreadID: msg.ThreadID,
			Text:     cc.AckMessage,
			ReplyTo:  msg.ReplyTo,
		})
	}

	timeout := cc.RequestTimeout
	if timeout <= 0 {
		timeout = h.cfg.DefaultTimeout
	}
	go h.execute(name, msg, sessionKey, timeout)
}

// execute 异步执行任务并投递结果。渠道消息与 HTTP 消息同等待遇：
// session.Run 内已接线的输入剥离、task_start 检查点、限流与门禁全部生效。
func (h *Hub) execute(name string, msg InboundMessage, sessionKey string, timeout time.Duration) {
	sess := h.getSession(sessionKey)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	result, err := sess.Run(ctx, msg.Text)

	var reply string
	switch {
	case errors.Is(err, session.ErrRateLimited):
		reply = "请求过于频繁，请稍后再试（渠道限流）"
	case err != nil:
		log.Printf("渠道 %q 任务执行失败（会话 %s）: %v", name, sessionKey, err)
		reply = "任务执行失败或被安全策略拦截，请检查输入内容或联系管理员"
	default:
		reply = extractAnswer(result)
		if reply == "" {
			reply = "任务已执行，但没有可回复的文本结果"
		}
	}

	h.deliver(name, OutboundMessage{
		ChatID:   msg.ChatID,
		ThreadID: msg.ThreadID,
		Text:     reply,
		ReplyTo:  msg.ReplyTo,
	})
}

// extractAnswer 从执行追踪中提取最终答案（取最后一个非空 FinalAnswer）。
func extractAnswer(result *engine.ExecutionTrace) string {
	if result == nil {
		return ""
	}
	for i := len(result.Steps) - 1; i >= 0; i-- {
		if result.Steps[i].FinalAnswer != "" {
			return result.Steps[i].FinalAnswer
		}
	}
	return ""
}

// deliver 出站投递：按渠道分片能力切分长文本，逐片带重试发送。
func (h *Hub) deliver(channelName string, msg OutboundMessage) {
	h.mu.RLock()
	ch, ok := h.channels[channelName]
	h.mu.RUnlock()
	if !ok {
		return
	}

	maxChunk := defaultMaxChunk
	if c, ok := ch.(Chunker); ok && c.MaxChunk() > 0 {
		maxChunk = c.MaxChunk()
	}

	for _, part := range splitChunks(msg.Text, maxChunk) {
		m := msg
		m.Text = part
		if err := h.sendWithRetry(ch, m); err != nil {
			log.Printf("警告: 渠道 %q 投递最终失败（会话 %s）: %v", channelName, msg.ChatID, err)
		}
	}
}

// sendWithRetry 指数退避重试投递（500ms 起，每次翻倍）。
func (h *Hub) sendWithRetry(ch Channel, msg OutboundMessage) error {
	var lastErr error
	backoff := 500 * time.Millisecond
	for attempt := 0; attempt <= h.cfg.SendRetries; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		lastErr = ch.Send(ctx, msg)
		cancel()
		if lastErr == nil {
			return nil
		}
		if attempt < h.cfg.SendRetries {
			time.Sleep(backoff)
			backoff *= 2
		}
	}
	return lastErr
}

// isDuplicate 判断消息是否在去重窗口内已处理过（空 MessageID 不去重）。
func (h *Hub) isDuplicate(name, messageID string) bool {
	if messageID == "" {
		return false
	}
	key := name + ":" + messageID
	now := time.Now()

	h.dedupMu.Lock()
	defer h.dedupMu.Unlock()

	// 机会式清理过期条目
	for k, t := range h.dedup {
		if now.Sub(t) > h.cfg.DedupeWindow {
			delete(h.dedup, k)
		}
	}
	if _, ok := h.dedup[key]; ok {
		return true
	}
	h.dedup[key] = now
	return false
}

// userAllowed 访问控制：空白名单或含 "*" 视为不限制。
func userAllowed(allowUsers []string, userID string) bool {
	if len(allowUsers) == 0 {
		return true
	}
	for _, u := range allowUsers {
		if u == "*" || u == userID {
			return true
		}
	}
	return false
}

// splitChunks 按 rune 数切分长文本（避免截断多字节字符）。
func splitChunks(text string, max int) []string {
	runes := []rune(text)
	if max <= 0 || len(runes) <= max {
		return []string{text}
	}
	var parts []string
	for len(runes) > 0 {
		if len(runes) <= max {
			parts = append(parts, string(runes))
			break
		}
		parts = append(parts, string(runes[:max]))
		runes = runes[max:]
	}
	return parts
}
