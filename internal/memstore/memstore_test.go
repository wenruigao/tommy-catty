package memstore

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tommy-cat/agent/internal/memory"
)

// runStoreContract 对任意 Store 实现执行统一契约测试：
// 保存/最近/检索/画像/用户隔离/幂等更新/清空。
func runStoreContract(t *testing.T, store Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()

	e1 := memory.MemoryEntry{ID: "m1", UserID: "u1", Content: "hello world 测试", Tags: []string{"user"}, Timestamp: now.Add(-time.Minute)}
	e2 := memory.MemoryEntry{ID: "m2", UserID: "u1", Content: "second memory entry", Tags: []string{"assistant"}, Timestamp: now}

	if err := store.SaveMemory(ctx, e1); err != nil {
		t.Fatalf("SaveMemory(e1): %v", err)
	}
	if err := store.SaveMemory(ctx, e2); err != nil {
		t.Fatalf("SaveMemory(e2): %v", err)
	}

	// 最近记忆：从新到旧
	recent, err := store.RecentMemories(ctx, "u1", 10)
	if err != nil {
		t.Fatalf("RecentMemories: %v", err)
	}
	if len(recent) != 2 || recent[0].ID != "m2" || recent[1].ID != "m1" {
		t.Fatalf("RecentMemories 顺序/数量不符: %+v", recent)
	}

	// 关键词检索
	hits, err := store.SearchMemories(ctx, "u1", "second", 5)
	if err != nil {
		t.Fatalf("SearchMemories: %v", err)
	}
	if len(hits) != 1 || hits[0].ID != "m2" {
		t.Fatalf("SearchMemories 未命中预期条目: %+v", hits)
	}

	// 用户隔离：u2 的记忆不影响 u1
	if err := store.SaveMemory(ctx, memory.MemoryEntry{ID: "x1", UserID: "u2", Content: "another user", Timestamp: now}); err != nil {
		t.Fatalf("SaveMemory(u2): %v", err)
	}
	recent, _ = store.RecentMemories(ctx, "u1", 10)
	if len(recent) != 2 {
		t.Fatalf("用户隔离失败，u1 记忆数=%d", len(recent))
	}

	// 幂等更新：同 ID 覆盖内容不新增条目
	e2b := e2
	e2b.Content = "second memory updated"
	if err := store.SaveMemory(ctx, e2b); err != nil {
		t.Fatalf("SaveMemory(upsert): %v", err)
	}
	recent, _ = store.RecentMemories(ctx, "u1", 10)
	if len(recent) != 2 {
		t.Fatalf("upsert 后条目数应不变，实际=%d", len(recent))
	}
	if recent[0].Content != "second memory updated" {
		t.Fatalf("upsert 内容未更新: %q", recent[0].Content)
	}

	// 画像读写
	if err := store.SaveProfile(ctx, "u1", "# 用户画像\n喜欢咖啡"); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	profile, err := store.LoadProfile(ctx, "u1")
	if err != nil || !strings.Contains(profile, "喜欢咖啡") {
		t.Fatalf("LoadProfile 不符: %q err=%v", profile, err)
	}
	// 未写入画像的用户返回空
	profile, err = store.LoadProfile(ctx, "ghost")
	if err != nil || profile != "" {
		t.Fatalf("缺失画像应返回空串: %q err=%v", profile, err)
	}

	// 清空：仅清 u1
	if err := store.DeleteMemories(ctx, "u1"); err != nil {
		t.Fatalf("DeleteMemories: %v", err)
	}
	recent, _ = store.RecentMemories(ctx, "u1", 10)
	if len(recent) != 0 {
		t.Fatalf("清空后 u1 仍有 %d 条记忆", len(recent))
	}
	recent, _ = store.RecentMemories(ctx, "u2", 10)
	if len(recent) != 1 {
		t.Fatalf("清空 u1 不应影响 u2，实际=%d", len(recent))
	}
}

func TestFileStoreContract(t *testing.T) {
	store := NewFileStore(t.TempDir(), t.TempDir(), 500)
	defer store.Close()
	runStoreContract(t, store)
}

func TestSQLiteStoreContract(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "memory.db"), 500)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()
	runStoreContract(t, store)
}

func TestRemoteStoreContract(t *testing.T) {
	backend := NewFileStore(t.TempDir(), t.TempDir(), 500)
	defer backend.Close()
	srv := httptest.NewServer(NewServer(backend, "test-token").Handler())
	defer srv.Close()

	store := NewRemoteStore(srv.URL, "test-token", 2*time.Second)
	runStoreContract(t, store)

	// 健康检查
	if err := store.HealthCheck(context.Background()); err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
}

func TestRemoteStoreUnauthorized(t *testing.T) {
	backend := NewFileStore(t.TempDir(), t.TempDir(), 500)
	defer backend.Close()
	srv := httptest.NewServer(NewServer(backend, "right-token").Handler())
	defer srv.Close()

	store := NewRemoteStore(srv.URL, "wrong-token", 2*time.Second)
	err := store.SaveMemory(context.Background(), memory.MemoryEntry{ID: "m1", UserID: "u1", Content: "x", Timestamp: time.Now()})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("错误令牌应返回 401，实际: %v", err)
	}
}

// TestFileStoreEviction 容量治理：超出上限时按时间淘汰最旧条目（file/sqlite 一致）。
func TestFileStoreEviction(t *testing.T) {
	store := NewFileStore(t.TempDir(), "", 3)
	defer store.Close()
	testEviction(t, store)
}

func TestSQLiteStoreEviction(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "memory.db"), 3)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()
	testEviction(t, store)
}

func testEviction(t *testing.T, store Store) {
	t.Helper()
	ctx := context.Background()
	base := time.Now()
	for i := 0; i < 5; i++ {
		e := memory.MemoryEntry{
			ID:        "e" + string(rune('a'+i)),
			UserID:    "u1",
			Content:   "memory content",
			Timestamp: base.Add(time.Duration(i) * time.Second),
		}
		if err := store.SaveMemory(ctx, e); err != nil {
			t.Fatalf("SaveMemory #%d: %v", i, err)
		}
	}
	recent, err := store.RecentMemories(ctx, "u1", 10)
	if err != nil {
		t.Fatalf("RecentMemories: %v", err)
	}
	if len(recent) != 3 {
		t.Fatalf("容量上限 3，实际 %d 条", len(recent))
	}
	if recent[0].ID != "ee" || recent[2].ID != "ec" {
		t.Fatalf("应保留最新 3 条（ee/ed/ec），实际: %v %v", recent[0].ID, recent[len(recent)-1].ID)
	}
}

// TestSQLiteStoreConflictSuperseded 冲突消解接线：新记忆与旧记忆语义矛盾时，
// 旧条目被标记 superseded 且不再出现在检索/最近结果中。
func TestSQLiteStoreConflictSuperseded(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "memory.db"), 500)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Now()
	// 两条高关键词重叠、仅否定词差异的内容（满足 IsSemanticConflict 判定）
	old := memory.MemoryEntry{ID: "old1", UserID: "u1", Content: "deploy config yaml file path", Timestamp: now.Add(-time.Hour)}
	fresh := memory.MemoryEntry{ID: "new1", UserID: "u1", Content: "deploy config yaml not path", Timestamp: now}

	if err := store.SaveMemory(ctx, old); err != nil {
		t.Fatalf("SaveMemory(old): %v", err)
	}
	if err := store.SaveMemory(ctx, fresh); err != nil {
		t.Fatalf("SaveMemory(fresh): %v", err)
	}

	recent, _ := store.RecentMemories(ctx, "u1", 10)
	if len(recent) != 1 || recent[0].ID != "new1" {
		t.Fatalf("冲突旧条目应被排除，实际: %+v", recent)
	}
}

func TestOpenFactory(t *testing.T) {
	dir := t.TempDir()

	if _, err := Open(Config{Type: "unknown"}); err == nil {
		t.Fatal("未知后端类型应报错")
	}
	if _, err := Open(Config{Type: BackendRemote}); err == nil {
		t.Fatal("remote 缺 url 应报错")
	}
	s, err := Open(Config{Type: BackendFile, FileDir: dir})
	if err != nil {
		t.Fatalf("Open(file): %v", err)
	}
	s.Close()
	s, err = Open(Config{Type: BackendSQLite, SQLitePath: filepath.Join(dir, "m.db")})
	if err != nil {
		t.Fatalf("Open(sqlite): %v", err)
	}
	s.Close()
}
