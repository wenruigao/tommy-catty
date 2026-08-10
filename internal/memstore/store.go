// Package memstore 实现记忆持久化抽象层：
// 长期记忆（对话条目）与用户画像统一经 Store 接口读写，
// 支持三种可切换后端——本地文件（JSONL）、SQLite、远程记忆服务（REST）。
package memstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tommy-cat/agent/internal/memory"
)

// 后端类型常量（对应 config.yaml memory.storage.type）。
const (
	BackendFile   = "file"   // 本地 JSONL 文件（默认，与旧行为兼容）
	BackendSQLite = "sqlite" // 本地 SQLite 数据库（modernc.org/sqlite，纯 Go）
	BackendRemote = "remote" // 远程记忆服务（cmd/memstore 提供的 REST 端点）
)

// Store 统一的记忆存储接口（多用户感知）：
// 长期记忆按 userID 隔离，用户画像（user.md 内容）与记忆同后端存放。
type Store interface {
	// SaveMemory 保存一条长期记忆（按 ID 幂等 upsert）。
	SaveMemory(ctx context.Context, entry memory.MemoryEntry) error

	// SearchMemories 按关键词/标签检索用户最相关的 topK 条记忆。
	SearchMemories(ctx context.Context, userID, query string, topK int) ([]memory.MemoryEntry, error)

	// RecentMemories 获取用户最近 limit 条记忆（从新到旧）。
	RecentMemories(ctx context.Context, userID string, limit int) ([]memory.MemoryEntry, error)

	// DeleteMemories 清空指定用户的全部长期记忆。
	DeleteMemories(ctx context.Context, userID string) error

	// SaveProfile 保存用户画像（Markdown 文本）。
	SaveProfile(ctx context.Context, userID, content string) error

	// LoadProfile 读取用户画像，不存在时返回空字符串。
	LoadProfile(ctx context.Context, userID string) (string, error)

	// Close 释放底层资源（文件句柄 / 数据库连接等）。
	Close() error
}

// Config 构建 Store 所需的参数（由 config.MemoryConfig 转换而来）。
type Config struct {
	Type              string        // file / sqlite / remote（空按 file 处理）
	FileDir           string        // file 后端 JSONL 根目录（默认 data/memories）
	ProfilesDir       string        // 画像目录（file 后端沿用 data/users/{userID}/user.md）
	SQLitePath        string        // sqlite 后端数据库文件路径（默认 data/memory.db）
	URL               string        // remote 后端服务地址（如 http://mem.internal:9301）
	Token             string        // remote 后端鉴权令牌（Bearer）
	Timeout           time.Duration // remote 后端请求超时（默认 3s）
	MaxEntriesPerUser int           // 每用户长期记忆上限（0 表示按默认 500）
}

// Open 按配置构建对应后端的 Store；type 未知或必填项缺失时报错。
func Open(cfg Config) (Store, error) {
	switch cfg.Type {
	case "", BackendFile:
		dir := cfg.FileDir
		if dir == "" {
			dir = "data/memories"
		}
		return NewFileStore(dir, cfg.ProfilesDir, cfg.MaxEntriesPerUser), nil
	case BackendSQLite:
		path := cfg.SQLitePath
		if path == "" {
			path = "data/memory.db"
		}
		return NewSQLiteStore(path, cfg.MaxEntriesPerUser)
	case BackendRemote:
		if cfg.URL == "" {
			return nil, errors.New("memstore: remote 后端需要配置 url")
		}
		return NewRemoteStore(cfg.URL, cfg.Token, cfg.Timeout), nil
	default:
		return nil, fmt.Errorf("memstore: 未知后端类型 %q（可选 file/sqlite/remote）", cfg.Type)
	}
}

// MemoryDTO 记忆条目的序列化结构（JSONL 行 / REST 载荷 / 测试断言共用）。
type MemoryDTO struct {
	ID         string    `json:"id"`
	UserID     string    `json:"user_id"`
	Content    string    `json:"content"`
	Tags       []string  `json:"tags"`
	Superseded bool      `json:"superseded"`
	CreatedAt  time.Time `json:"created_at"`
}

func toDTO(e memory.MemoryEntry) MemoryDTO {
	tags := e.Tags
	if tags == nil {
		tags = []string{}
	}
	return MemoryDTO{
		ID:        e.ID,
		UserID:    e.UserID,
		Content:   e.Content,
		Tags:      tags,
		CreatedAt: e.Timestamp,
	}
}

func fromDTO(d MemoryDTO) memory.MemoryEntry {
	return memory.MemoryEntry{
		ID:        d.ID,
		UserID:    d.UserID,
		Content:   d.Content,
		Tags:      d.Tags,
		Timestamp: d.CreatedAt,
	}
}

// memoryAdapter 将多用户 Store 适配为单用户的 memory.Memory 接口，
// 供 CombinedMemory 作为长期记忆层接入（userID 在构造时固定）。
type memoryAdapter struct {
	store  Store
	userID string
}

// NewMemoryAdapter 创建 Store → memory.Memory 的适配器；store 为 nil 时返回 nil。
func NewMemoryAdapter(store Store, userID string) memory.Memory {
	if store == nil {
		return nil
	}
	return &memoryAdapter{store: store, userID: userID}
}

func (a *memoryAdapter) Store(ctx context.Context, entry memory.MemoryEntry) error {
	entry.UserID = a.userID
	if entry.ID == "" {
		return errors.New("memstore: 记忆条目缺少 ID")
	}
	return a.store.SaveMemory(ctx, entry)
}

func (a *memoryAdapter) Search(ctx context.Context, query string, topK int) ([]memory.MemoryEntry, error) {
	return a.store.SearchMemories(ctx, a.userID, query, topK)
}

func (a *memoryAdapter) GetRecent(ctx context.Context, limit int) ([]memory.MemoryEntry, error) {
	return a.store.RecentMemories(ctx, a.userID, limit)
}

func (a *memoryAdapter) Clear(ctx context.Context) error {
	return a.store.DeleteMemories(ctx, a.userID)
}
