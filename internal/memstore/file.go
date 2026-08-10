package memstore

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/tommy-cat/agent/internal/memory"
)

// FileStore 本地文件后端：
// 长期记忆按用户追加写 data/memories/{userID}.jsonl，
// 用户画像沿用 data/users/{userID}/user.md（与 persona 旧路径一致，零迁移）。
type FileStore struct {
	dir        string // JSONL 根目录
	profiles   string // 画像根目录（可为空，空则画像读写返回空/报错）
	maxPerUser int

	mu sync.Mutex // 保护文件读写与容量淘汰
}

// NewFileStore 创建文件后端。dir 为 JSONL 根目录，profiles 为画像目录（可为空），
// maxPerUser <= 0 时取默认 500。
func NewFileStore(dir, profiles string, maxPerUser int) *FileStore {
	if maxPerUser <= 0 {
		maxPerUser = 500
	}
	return &FileStore{dir: dir, profiles: profiles, maxPerUser: maxPerUser}
}

// safeFileName 将 userID 转换为安全的文件名片段（替换路径分隔符）。
func safeFileName(userID string) string {
	return strings.NewReplacer("/", "_", "\\", "_", "..", "_").Replace(userID)
}

func (s *FileStore) memoryPath(userID string) string {
	return filepath.Join(s.dir, safeFileName(userID)+".jsonl")
}

func (s *FileStore) profilePath(userID string) string {
	if s.profiles == "" {
		return ""
	}
	return filepath.Join(s.profiles, safeFileName(userID), "user.md")
}

// SaveMemory 追加写一条记忆（同 ID 重复保存时先移除旧行），超容量时淘汰最旧条目。
func (s *FileStore) SaveMemory(_ context.Context, entry memory.MemoryEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	entries := s.loadLocked(entry.UserID)

	// 幂等 upsert：剔除同 ID 旧条目与容量超限的最旧条目
	filtered := make([]MemoryDTO, 0, len(entries)+1)
	for _, e := range entries {
		if e.ID != entry.ID {
			filtered = append(filtered, e)
		}
	}
	filtered = append(filtered, toDTO(entry))
	sort.SliceStable(filtered, func(i, j int) bool {
		return filtered[i].CreatedAt.Before(filtered[j].CreatedAt)
	})
	if len(filtered) > s.maxPerUser {
		filtered = filtered[len(filtered)-s.maxPerUser:]
	}
	return s.writeAllLocked(entry.UserID, filtered)
}

// SearchMemories 关键词扫描匹配（content/tags 任一命中查询词即算相关），按时间取前 topK。
func (s *FileStore) SearchMemories(_ context.Context, userID, query string, topK int) ([]memory.MemoryEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries := s.loadLocked(userID)
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" || topK <= 0 {
		return nil, nil
	}
	keywords := strings.Fields(query)

	var hits []MemoryDTO
	// 从新到旧扫描，命中即收集
	for i := len(entries) - 1; i >= 0 && len(hits) < topK; i-- {
		e := entries[i]
		if e.Superseded {
			continue
		}
		text := strings.ToLower(e.Content + " " + strings.Join(e.Tags, " "))
		for _, kw := range keywords {
			if strings.Contains(text, kw) {
				hits = append(hits, e)
				break
			}
		}
	}
	return mapDTOs(hits), nil
}

// RecentMemories 返回最近 limit 条记忆（从新到旧，跳过被取代条目）。
func (s *FileStore) RecentMemories(_ context.Context, userID string, limit int) ([]memory.MemoryEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if limit <= 0 {
		return nil, nil
	}
	entries := s.loadLocked(userID)
	var out []MemoryDTO
	for i := len(entries) - 1; i >= 0 && len(out) < limit; i-- {
		if !entries[i].Superseded {
			out = append(out, entries[i])
		}
	}
	return mapDTOs(out), nil
}

// DeleteMemories 删除该用户的记忆文件（不存在视为成功）。
func (s *FileStore) DeleteMemories(_ context.Context, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	err := os.Remove(s.memoryPath(userID))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// SaveProfile 写入画像文件（路径与旧 profiler 一致：{profiles}/{userID}/user.md）。
func (s *FileStore) SaveProfile(_ context.Context, userID, content string) error {
	path := s.profilePath(userID)
	if path == "" {
		return os.ErrNotExist
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// LoadProfile 读取画像文件，不存在返回空字符串。
func (s *FileStore) LoadProfile(_ context.Context, userID string) (string, error) {
	path := s.profilePath(userID)
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

// Close 文件后端无持久句柄，直接返回 nil。
func (s *FileStore) Close() error { return nil }

// loadLocked 读取用户全部记忆行（按写入顺序），损坏行跳过。需持有 mu。
func (s *FileStore) loadLocked(userID string) []MemoryDTO {
	f, err := os.Open(s.memoryPath(userID))
	if err != nil {
		return nil
	}
	defer f.Close()

	var out []MemoryDTO
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var dto MemoryDTO
		if err := json.Unmarshal([]byte(line), &dto); err != nil {
			continue // 跳过损坏行，保持与审计日志一致的容错策略
		}
		out = append(out, dto)
	}
	return out
}

// writeAllLocked 全量重写用户记忆文件（原子性：先写临时文件再 rename）。需持有 mu。
func (s *FileStore) writeAllLocked(userID string, entries []MemoryDTO) error {
	path := s.memoryPath(userID)
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, e := range entries {
		data, err := json.Marshal(e)
		if err != nil {
			f.Close()
			os.Remove(tmp)
			return err
		}
		w.Write(data)
		w.WriteByte('\n')
	}
	if err := w.Flush(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func mapDTOs(dtos []MemoryDTO) []memory.MemoryEntry {
	if len(dtos) == 0 {
		return nil
	}
	out := make([]memory.MemoryEntry, 0, len(dtos))
	for _, d := range dtos {
		out = append(out, fromDTO(d))
	}
	return out
}
