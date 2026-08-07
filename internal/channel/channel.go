// Package channel 实现通用 Channel 接入层（对齐 OpenClaw 的 Channel 机制）：
// 所有外部消息平台（飞书/钉钉/Telegram/webhook 等）实现统一的 Channel 契约，
// 由 Hub 统一负责注册发现、生命周期管理、访问控制、去重与会话路由。
//
// 消息链路：平台消息 → Channel adapter → 统一 InboundMessage → Hub 管道
// → SessionManager(sessionKey) → UserSession.Run → OutboundMessage → Channel.Send → 平台
package channel

import (
	"context"
	"time"
)

// ChatType 消息会话类型。
type ChatType string

const (
	// ChatTypeDM 单聊（会话键按平台用户）。
	ChatTypeDM ChatType = "dm"
	// ChatTypeGroup 群聊（会话键按群 ID，群成员共享上下文）。
	ChatTypeGroup ChatType = "group"
)

// 群聊响应模式（group_mode 配置取值；mention 判断由 adapter 侧完成）。
const (
	// GroupModeAlways 群内所有消息都响应。
	GroupModeAlways = "always"
	// GroupModeMentionOnly 仅响应 @机器人 的消息（默认）。
	GroupModeMentionOnly = "mention_only"
	// GroupModeNever 不响应任何群消息（仅单聊）。
	GroupModeNever = "never"
)

// ChannelStatus 渠道运行状态（供健康检查）。
type ChannelStatus string

const (
	// StatusStopped 已停止。
	StatusStopped ChannelStatus = "stopped"
	// StatusConnected 正常连接。
	StatusConnected ChannelStatus = "connected"
	// StatusDegraded 降级运行（部分功能不可用）。
	StatusDegraded ChannelStatus = "degraded"
)

// InboundMessage 平台无关的统一入站消息（adapter 负责将平台事件转换为本结构）。
type InboundMessage struct {
	// Channel 渠道名（规范渠道 ID，如 "webhook"/"telegram"）。
	Channel string
	// MessageID 平台消息 ID（Hub 按 渠道名:MessageID 去重）。
	MessageID string
	// UserID 平台用户 ID。
	UserID string
	// ChatID 会话 ID：DM 时通常等于 UserID，群聊时为群 ID。
	ChatID string
	// ChatType 会话类型（DM | Group）。
	ChatType ChatType
	// ThreadID 线程/话题 ID（可选，回复时原样带回）。
	ThreadID string
	// Text 纯文本内容（adapter 负责剥离 @机器人 等平台语法）。
	Text string
	// ReplyTo 渠道特定的回复地址（如 webhook 的 callback_url）：
	// 由 adapter 填入，Hub 回复时原样复制到 OutboundMessage。
	ReplyTo string
	// Raw 平台原始事件（adapter 自留，投递时可还原平台特定字段）。
	Raw any
	// Timestamp 消息时间。
	Timestamp time.Time
}

// OutboundMessage 统一出站消息。
type OutboundMessage struct {
	// ChatID 目标会话 ID。
	ChatID string
	// ThreadID 目标线程/话题 ID（可选）。
	ThreadID string
	// Text 回复文本（超长时由 Hub 按渠道分片能力切分）。
	Text string
	// ReplyTo 继承自入站消息的回复地址（可为空，渠道使用默认投递地址）。
	ReplyTo string
}

// Channel 所有平台 adapter 的统一契约（对齐 OpenClaw 的 ChannelPlugin）：
// 契约先行，实现自由——新增一个平台只需新增一个实现 + 一段 YAML 配置。
type Channel interface {
	// Name 规范渠道 ID，如 "telegram" / "webhook"。
	Name() string
	// Start 启动接收：adapter 将平台事件转换为 InboundMessage 写入 in。
	// 入站确认（ack）应在 adapter 内部立即完成，不得阻塞。
	Start(ctx context.Context, in chan<- InboundMessage) error
	// Send 投递回复到平台。
	Send(ctx context.Context, msg OutboundMessage) error
	// Stop 优雅停止渠道。
	Stop() error
	// Status 运行状态（供健康检查）。
	Status() ChannelStatus
}

// Chunker 可选接口：渠道实现后，出站长文本按其单条上限分片投递。
type Chunker interface {
	// MaxChunk 单条消息最大字符数（rune 计数）。
	MaxChunk() int
}

// SessionKey 计算消息对应的会话键（即 SessionManager 的 userID）。
// 规则：群聊 = "{channel}:{chatID}"（群成员共享会话），
// 单聊 = "{channel}:{userID}"。前缀隔离保证渠道身份与 HTTP X-User-ID 身份
// 绝不串会话；per-user 限流与审计天然按会话键生效。
func SessionKey(channelName string, msg InboundMessage) string {
	id := msg.UserID
	if msg.ChatType == ChatTypeGroup && msg.ChatID != "" {
		id = msg.ChatID
	}
	return channelName + ":" + id
}
