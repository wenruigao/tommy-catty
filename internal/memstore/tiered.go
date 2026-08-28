package memstore

import (
	"context"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/wenruigao/tommy-catty/internal/memory"
)

// metaRemoteSynced 是 sqlite _meta 表中标记"本地存量已回迁远端"的键。
const metaRemoteSynced = "remote_synced"

// TieredConfig 分层记忆存储配置。
// remote/sqlite/file 三层同时写入；读取按 remote（全量）→ sqlite → file 回退；
// 本地两层按保留窗口修剪（0 表示保留全量，不修剪）。
type TieredConfig struct {
	RemoteURL   string // 远端记忆服务地址，为空则不挂远端层
	RemoteToken string
	Timeout     time.Duration // 远端请求超时

	SQLitePath        string // sqlite 层数据库路径，默认 data/memory.db
	FileDir           string // file 层 JSONL 目录，默认 data/memories
	ProfilesDir       string // 画像本地兜底目录（沿用 data/users）
	MaxEntriesPerUser int    // 每用户容量上限（透传各驱动）

	SQLiteRetention time.Duration // sqlite 层保留窗口，0 = 全量
	FileRetention   time.Duration // file 层保留窗口，0 = 全量
}

// TieredStore 分层记忆存储：
//   - 配置了远端：remote 全量 + sqlite 最近窗口 + file 最近窗口；
//   - 未配置远端：sqlite 全量 + file 最近窗口。
//
// 首次配置远端时，先把本地存量全量回迁远端（按 ID 幂等），
// 成功打标 remote_synced 后才按窗口修剪本地层。
type TieredStore struct {
	remote Store // 可为 nil
	sqlite *SQLiteStore
	file   *FileStore

	sqliteRetention time.Duration
	fileRetention   time.Duration
}

// OpenTiered 构建分层存储并执行启动维护（远端回迁 + 本地修剪）。
func OpenTiered(cfg TieredConfig) (*TieredStore, error) {
	sqlitePath := cfg.SQLitePath
	if sqlitePath == "" {
		sqlitePath = "data/memory.db"
	}
	fileDir := cfg.FileDir
	if fileDir == "" {
		fileDir = "data/memories"
	}
	sq, err := NewSQLiteStore(sqlitePath, cfg.MaxEntriesPerUser)
	if err != nil {
		return nil, err
	}
	fs := NewFileStore(fileDir, cfg.ProfilesDir, cfg.MaxEntriesPerUser)

	t := &TieredStore{
		sqlite:          sq,
		file:            fs,
		sqliteRetention: cfg.SQLiteRetention,
		fileRetention:   cfg.FileRetention,
	}
	if cfg.RemoteURL != "" {
		t.remote = NewRemoteStore(cfg.RemoteURL, cfg.RemoteToken, cfg.Timeout)
	}
	t.startup(context.Background())
	return t, nil
}

// Mode 返回分层模式描述，用于启动日志。
func (t *TieredStore) Mode() string {
	if t.remote != nil {
		return fmt.Sprintf("remote(全量) + sqlite(%s) + file(%s)",
			retentionLabel(t.sqliteRetention), retentionLabel(t.fileRetention))
	}
	return fmt.Sprintf("sqlite(%s) + file(%s)",
		retentionLabel(t.sqliteRetention), retentionLabel(t.fileRetention))
}

func retentionLabel(d time.Duration) string {
	if d <= 0 {
		return "全量"
	}
	if d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%d天", int(d/(24*time.Hour)))
	}
	return d.String()
}

// SaveMemory 分层写入：远端尽力（失败仅警告），sqlite/file 依次写入并按窗口修剪。
func (t *TieredStore) SaveMemory(ctx context.Context, entry memory.MemoryEntry) error {
	if t.remote != nil {
		if err := t.remote.SaveMemory(ctx, entry); err != nil {
			log.Printf("  ⚠️  memstore: 远端层写入失败（本地层不受影响）: %v", err)
		}
	}
	if err := t.sqlite.SaveMemory(ctx, entry); err != nil {
		return err
	}
	if err := t.file.SaveMemory(ctx, entry); err != nil {
		return err
	}
	t.pruneUser(ctx, entry.UserID)
	return nil
}

// RecentMemories 读取回退链：远端（已同步时，全量）→ sqlite → file。
func (t *TieredStore) RecentMemories(ctx context.Context, userID string, limit int) ([]memory.MemoryEntry, error) {
	if t.remote != nil && t.userSynced(ctx, userID) {
		if entries, err := t.remote.RecentMemories(ctx, userID, limit); err == nil {
			return entries, nil
		} else {
			log.Printf("  ⚠️  memstore: 远端读取失败，回退本地层: %v", err)
		}
	}
	if entries, err := t.sqlite.RecentMemories(ctx, userID, limit); err == nil {
		return entries, nil
	}
	return t.file.RecentMemories(ctx, userID, limit)
}

// SearchMemories 读取回退链同 RecentMemories。
func (t *TieredStore) SearchMemories(ctx context.Context, userID, query string, topK int) ([]memory.MemoryEntry, error) {
	if t.remote != nil && t.userSynced(ctx, userID) {
		if entries, err := t.remote.SearchMemories(ctx, userID, query, topK); err == nil {
			return entries, nil
		} else {
			log.Printf("  ⚠️  memstore: 远端检索失败，回退本地层: %v", err)
		}
	}
	if entries, err := t.sqlite.SearchMemories(ctx, userID, query, topK); err == nil {
		return entries, nil
	}
	return t.file.SearchMemories(ctx, userID, query, topK)
}

// DeleteMemories 三层全部清空。
func (t *TieredStore) DeleteMemories(ctx context.Context, userID string) error {
	var firstErr error
	if t.remote != nil {
		if err := t.remote.DeleteMemories(ctx, userID); err != nil {
			log.Printf("  ⚠️  memstore: 远端层清空失败: %v", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if err := t.sqlite.DeleteMemories(ctx, userID); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := t.file.DeleteMemories(ctx, userID); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// SaveProfile 分层写入：sqlite 为权威层（失败返回错误），远端与 file 尽力而为。
func (t *TieredStore) SaveProfile(ctx context.Context, userID, content string) error {
	if t.remote != nil {
		if err := t.remote.SaveProfile(ctx, userID, content); err != nil {
			log.Printf("  ⚠️  memstore: 远端层画像写入失败: %v", err)
		}
	}
	if err := t.sqlite.SaveProfile(ctx, userID, content); err != nil {
		return err
	}
	if err := t.file.SaveProfile(ctx, userID, content); err != nil {
		log.Printf("  ⚠️  memstore: 本地文件层画像写入失败: %v", err)
	}
	return nil
}

// LoadProfile 读取回退链：远端（已同步时）→ sqlite → file（含旧路径兜底）。
func (t *TieredStore) LoadProfile(ctx context.Context, userID string) (string, error) {
	if t.remote != nil && t.userSynced(ctx, userID) {
		if content, err := t.remote.LoadProfile(ctx, userID); err == nil && content != "" {
			return content, nil
		}
	}
	if content, err := t.sqlite.LoadProfile(ctx, userID); err == nil && content != "" {
		return content, nil
	}
	return t.file.LoadProfile(ctx, userID)
}

// Close 关闭各层。
func (t *TieredStore) Close() error {
	if t.remote != nil {
		_ = t.remote.Close()
	}
	return t.sqlite.Close()
}

// userSynced 判断该用户的本地存量是否已完成远端回迁。
func (t *TieredStore) userSynced(ctx context.Context, userID string) bool {
	v, err := t.sqlite.GetMeta(ctx, userID, metaRemoteSynced)
	return err == nil && v == "1"
}

// startup 启动维护：配置远端时逐用户回迁，随后按窗口修剪本地层。
func (t *TieredStore) startup(ctx context.Context) {
	users := t.localUsers(ctx)
	if t.remote != nil {
		for _, uid := range users {
			if err := t.backfillUser(ctx, uid); err != nil {
				log.Printf("  ⚠️  memstore: 用户 %s 远端回迁失败（下次启动重试，保留本地全量）: %v", uid, err)
			}
		}
	}
	t.pruneLocal(ctx)
}

// backfillUser 将单个用户的本地存量（sqlite ∪ file）全量写入远端，
// 全部成功后打 remote_synced 标记；任一失败则不标记，等待下次重试。
func (t *TieredStore) backfillUser(ctx context.Context, userID string) error {
	if t.userSynced(ctx, userID) {
		return nil
	}
	entries, err := t.localUnion(ctx, userID)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := t.remote.SaveMemory(ctx, e); err != nil {
			return fmt.Errorf("记忆 %s 写入远端失败: %w", e.ID, err)
		}
	}
	// 画像一并回迁（sqlite 权威层优先，其次 file 旧路径）
	content, err := t.sqlite.LoadProfile(ctx, userID)
	if err != nil {
		return err
	}
	if content == "" {
		content, _ = t.file.LoadProfile(ctx, userID)
	}
	if content != "" {
		if err := t.remote.SaveProfile(ctx, userID, content); err != nil {
			return fmt.Errorf("画像写入远端失败: %w", err)
		}
	}
	if err := t.sqlite.SetMeta(ctx, userID, metaRemoteSynced, "1"); err != nil {
		return err
	}
	log.Printf("  ✅ memstore: 用户 %s 已回迁 %d 条记忆到远端", userID, len(entries))
	return nil
}

// localUnion 合并本地两层的记忆（按 ID 去重），按创建时间升序返回。
func (t *TieredStore) localUnion(ctx context.Context, userID string) ([]memory.MemoryEntry, error) {
	byID := make(map[string]memory.MemoryEntry)
	fromSQLite, err := t.sqlite.RecentMemories(ctx, userID, 1<<20)
	if err != nil {
		return nil, err
	}
	for _, e := range fromSQLite {
		byID[e.ID] = e
	}
	fromFile, err := t.file.RecentMemories(ctx, userID, 1<<20)
	if err != nil {
		return nil, err
	}
	for _, e := range fromFile {
		if _, ok := byID[e.ID]; !ok {
			byID[e.ID] = e
		}
	}
	entries := make([]memory.MemoryEntry, 0, len(byID))
	for _, e := range byID {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Timestamp.Before(entries[j].Timestamp)
	})
	return entries, nil
}

// pruneLocal 启动时对所有已知用户按保留窗口修剪本地两层。
func (t *TieredStore) pruneLocal(ctx context.Context) {
	if t.sqliteRetention <= 0 && t.fileRetention <= 0 {
		return // 均为全量层，无需修剪
	}
	for _, uid := range t.localUsers(ctx) {
		t.pruneUser(ctx, uid)
	}
}

// localUsers 返回本地两层中已知的所有用户（sqlite 表 ∪ file 目录，
// 覆盖旧版单后端遗留的 file-only 用户）。
func (t *TieredStore) localUsers(ctx context.Context) []string {
	seen := make(map[string]bool)
	var users []string
	if ids, err := t.sqlite.ListUserIDs(ctx); err == nil {
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				users = append(users, id)
			}
		}
	} else {
		log.Printf("  ⚠️  memstore: 枚举本地用户失败: %v", err)
	}
	for _, id := range t.fileUsers() {
		if !seen[id] {
			seen[id] = true
			users = append(users, id)
		}
	}
	return users
}

// pruneUser 按保留窗口修剪单个用户的本地两层（窗口为 0 的层跳过）。
// 配置了远端但该用户尚未完成回迁时，保留本地全量等待重试。
func (t *TieredStore) pruneUser(ctx context.Context, userID string) {
	if t.remote != nil && !t.userSynced(ctx, userID) {
		return
	}
	now := time.Now()
	if t.sqliteRetention > 0 {
		if err := t.sqlite.PruneBefore(ctx, userID, now.Add(-t.sqliteRetention)); err != nil {
			log.Printf("  ⚠️  memstore: sqlite 层修剪失败: %v", err)
		}
	}
	if t.fileRetention > 0 {
		if err := t.file.PruneBefore(ctx, userID, now.Add(-t.fileRetention)); err != nil {
			log.Printf("  ⚠️  memstore: file 层修剪失败: %v", err)
		}
	}
}

// fileUsers 扫描 file 层 JSONL 目录，返回存在记忆文件的用户 ID。
func (t *TieredStore) fileUsers() []string {
	dirEntries, err := os.ReadDir(t.file.dir)
	if err != nil {
		return nil
	}
	var ids []string
	for _, de := range dirEntries {
		if de.IsDir() {
			continue
		}
		name := de.Name()
		if strings.HasSuffix(name, ".jsonl") {
			ids = append(ids, strings.TrimSuffix(name, ".jsonl"))
		}
	}
	return ids
}
