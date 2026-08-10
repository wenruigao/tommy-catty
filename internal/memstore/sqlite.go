package memstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tommy-cat/agent/internal/memory"

	_ "modernc.org/sqlite" // 注册 "sqlite" 驱动（纯 Go 实现，无 CGO）
)

// SQLiteStore SQLite 后端：结构化存储长期记忆与用户画像，
// 支持按用户清理、关键词检索、容量淘汰与冲突标记（superseded）。
type SQLiteStore struct {
	db         *sql.DB
	maxPerUser int
}

const sqliteSchema = `
CREATE TABLE IF NOT EXISTS memories (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL,
    content    TEXT NOT NULL,
    tags       TEXT NOT NULL DEFAULT '',
    superseded INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_memories_user_time ON memories(user_id, created_at);
CREATE TABLE IF NOT EXISTS profiles (
    user_id    TEXT PRIMARY KEY,
    content    TEXT NOT NULL,
    updated_at DATETIME NOT NULL
);
CREATE TABLE IF NOT EXISTS _meta (
    user_id TEXT NOT NULL,
    key     TEXT NOT NULL,
    value   TEXT NOT NULL,
    PRIMARY KEY (user_id, key)
);
`

// NewSQLiteStore 打开（或创建）SQLite 数据库并初始化表结构。
// maxPerUser <= 0 时取默认 500。
func NewSQLiteStore(path string, maxPerUser int) (*SQLiteStore, error) {
	if maxPerUser <= 0 {
		maxPerUser = 500
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("memstore: 创建数据库目录失败: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("memstore: 打开 SQLite 失败: %w", err)
	}
	db.SetMaxOpenConns(1) // 单写者，避免 modernc.org/sqlite 并发写锁冲突
	if _, err := db.Exec(sqliteSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("memstore: 初始化表结构失败: %w", err)
	}
	return &SQLiteStore{db: db, maxPerUser: maxPerUser}, nil
}

// SaveMemory 幂等保存一条记忆，随后做冲突标记与容量淘汰。
func (s *SQLiteStore) SaveMemory(ctx context.Context, entry memory.MemoryEntry) error {
	dto := toDTO(entry)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO memories(id, user_id, content, tags, superseded, created_at)
		 VALUES(?, ?, ?, ?, 0, ?)
		 ON CONFLICT(id) DO UPDATE SET content=excluded.content, tags=excluded.tags,
		 superseded=0, created_at=excluded.created_at`,
		dto.ID, dto.UserID, dto.Content, strings.Join(dto.Tags, ","), dto.CreatedAt)
	if err != nil {
		return err
	}
	if err := s.markConflicts(ctx, entry); err != nil {
		return err
	}
	return s.evict(ctx, dto.UserID)
}

// markConflicts 冲突消解接线：新记忆与最近条目语义矛盾时（conflict.IsSemanticConflict），
// 用 conflict.ResolveConflict 判定并把被取代的旧条目标记 superseded。
func (s *SQLiteStore) markConflicts(ctx context.Context, entry memory.MemoryEntry) error {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, content, tags, created_at FROM memories
		 WHERE user_id=? AND superseded=0 AND id != ?
		 ORDER BY created_at DESC LIMIT 10`, entry.UserID, entry.ID)
	if err != nil {
		return err
	}
	defer rows.Close()

	var conflicting []memory.MemoryEntry
	for rows.Next() {
		var id, content, tags string
		var createdAt time.Time
		if err := rows.Scan(&id, &content, &tags, &createdAt); err != nil {
			return err
		}
		old := memory.MemoryEntry{ID: id, Content: content, Timestamp: createdAt}
		if tags != "" {
			old.Tags = strings.Split(tags, ",")
		}
		if memory.IsSemanticConflict(entry, old) && !entry.Timestamp.Before(old.Timestamp) {
			conflicting = append(conflicting, old)
		}
	}
	if len(conflicting) == 0 {
		return rows.Err()
	}

	_, superseded := memory.ResolveConflict(append(conflicting, entry))
	for _, id := range superseded {
		if id == entry.ID {
			continue // 新条目自身永不标记
		}
		if _, err := s.db.ExecContext(ctx,
			`UPDATE memories SET superseded=1 WHERE id=? AND user_id=?`, id, entry.UserID); err != nil {
			return err
		}
	}
	return rows.Err()
}

// PruneBefore 删除指定用户在 cutoff 之前创建的记忆。
func (s *SQLiteStore) PruneBefore(ctx context.Context, userID string, cutoff time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM memories WHERE user_id = ? AND created_at < ?`, userID, cutoff)
	return err
}

// ListUserIDs 返回存在记忆的所有用户 ID（用于启动修剪与远端同步）。
func (s *SQLiteStore) ListUserIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT user_id FROM memories`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// GetMeta 读取每用户元数据（不存在返回空串）。
func (s *SQLiteStore) GetMeta(ctx context.Context, userID, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx,
		`SELECT value FROM _meta WHERE user_id = ? AND key = ?`, userID, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetMeta 写入每用户元数据（upsert）。
func (s *SQLiteStore) SetMeta(ctx context.Context, userID, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO _meta (user_id, key, value) VALUES (?, ?, ?)
		 ON CONFLICT(user_id, key) DO UPDATE SET value = excluded.value`,
		userID, key, value)
	return err
}

// evict 容量治理：超出上限时按时间从旧到新删除。
func (s *SQLiteStore) evict(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM memories WHERE user_id=? AND id NOT IN
		 (SELECT id FROM memories WHERE user_id=? ORDER BY created_at DESC LIMIT ?)`,
		userID, userID, s.maxPerUser)
	return err
}

// SearchMemories 关键词 LIKE 匹配 content/tags，按时间倒序取前 topK。
func (s *SQLiteStore) SearchMemories(ctx context.Context, userID, query string, topK int) ([]memory.MemoryEntry, error) {
	query = strings.TrimSpace(query)
	if query == "" || topK <= 0 {
		return nil, nil
	}
	keywords := strings.Fields(query)

	var conds []string
	var args []any
	for _, kw := range keywords {
		conds = append(conds, "(content LIKE ? OR tags LIKE ?)")
		args = append(args, "%"+kw+"%", "%"+kw+"%")
	}
	stmt := fmt.Sprintf(
		`SELECT id, user_id, content, tags, created_at FROM memories
		 WHERE user_id=? AND superseded=0 AND %s
		 ORDER BY created_at DESC LIMIT ?`, strings.Join(conds, " OR "))
	args = append([]any{userID}, args...)
	args = append(args, topK)
	return s.queryEntries(ctx, stmt, args...)
}

// RecentMemories 返回最近 limit 条记忆（从新到旧）。
func (s *SQLiteStore) RecentMemories(ctx context.Context, userID string, limit int) ([]memory.MemoryEntry, error) {
	if limit <= 0 {
		return nil, nil
	}
	return s.queryEntries(ctx,
		`SELECT id, user_id, content, tags, created_at FROM memories
		 WHERE user_id=? AND superseded=0
		 ORDER BY created_at DESC LIMIT ?`, userID, limit)
}

// DeleteMemories 清空指定用户全部记忆。
func (s *SQLiteStore) DeleteMemories(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM memories WHERE user_id=?`, userID)
	return err
}

// SaveProfile 幂等保存用户画像。
func (s *SQLiteStore) SaveProfile(ctx context.Context, userID, content string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO profiles(user_id, content, updated_at) VALUES(?, ?, ?)
		 ON CONFLICT(user_id) DO UPDATE SET content=excluded.content, updated_at=excluded.updated_at`,
		userID, content, time.Now())
	return err
}

// LoadProfile 读取用户画像，不存在返回空字符串。
func (s *SQLiteStore) LoadProfile(ctx context.Context, userID string) (string, error) {
	var content string
	err := s.db.QueryRowContext(ctx,
		`SELECT content FROM profiles WHERE user_id=?`, userID).Scan(&content)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return content, nil
}

// Close 关闭数据库连接。
func (s *SQLiteStore) Close() error { return s.db.Close() }

func (s *SQLiteStore) queryEntries(ctx context.Context, stmt string, args ...any) ([]memory.MemoryEntry, error) {
	rows, err := s.db.QueryContext(ctx, stmt, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []memory.MemoryEntry
	for rows.Next() {
		var id, uid, content, tags string
		var createdAt time.Time
		if err := rows.Scan(&id, &uid, &content, &tags, &createdAt); err != nil {
			return nil, err
		}
		entry := memory.MemoryEntry{ID: id, UserID: uid, Content: content, Timestamp: createdAt}
		if tags != "" {
			entry.Tags = strings.Split(tags, ",")
		}
		out = append(out, entry)
	}
	return out, rows.Err()
}
