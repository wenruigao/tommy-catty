package memstore

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tommy-cat/agent/internal/memory"
)

// seedEntries 返回两条旧记忆（10 天前）+ 两条新记忆。
func seedEntries(userID string) []memory.MemoryEntry {
	now := time.Now()
	return []memory.MemoryEntry{
		{ID: "old1", UserID: userID, Content: "旧记忆一", Tags: []string{"user"}, Timestamp: now.Add(-10 * 24 * time.Hour)},
		{ID: "old2", UserID: userID, Content: "旧记忆二", Tags: []string{"assistant"}, Timestamp: now.Add(-10 * 24 * time.Hour)},
		{ID: "new1", UserID: userID, Content: "新记忆一", Tags: []string{"user"}, Timestamp: now.Add(-1 * time.Hour)},
		{ID: "new2", UserID: userID, Content: "新记忆二", Tags: []string{"assistant"}, Timestamp: now},
	}
}

// TestTieredStoreNoRemoteRetention 未配置远端：sqlite 全量 + file 按窗口修剪。
func TestTieredStoreNoRemoteRetention(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	store, err := OpenTiered(TieredConfig{
		SQLitePath:      dir + "/memory.db",
		FileDir:         dir + "/memories",
		ProfilesDir:     dir + "/users",
		SQLiteRetention: 0,              // 全量
		FileRetention:   72 * time.Hour, // 3 天
	})
	if err != nil {
		t.Fatalf("OpenTiered: %v", err)
	}
	defer store.Close()

	for _, e := range seedEntries("u1") {
		if err := store.SaveMemory(ctx, e); err != nil {
			t.Fatalf("SaveMemory(%s): %v", e.ID, err)
		}
	}

	// sqlite 层全量：4 条
	fromSQLite, err := store.sqlite.RecentMemories(ctx, "u1", 100)
	if err != nil || len(fromSQLite) != 4 {
		t.Fatalf("sqlite 层应保留全量 4 条，实际 %d err=%v", len(fromSQLite), err)
	}
	// file 层仅窗口内：2 条新记忆
	fromFile, err := store.file.RecentMemories(ctx, "u1", 100)
	if err != nil || len(fromFile) != 2 {
		t.Fatalf("file 层应只保留窗口内 2 条，实际 %d err=%v", len(fromFile), err)
	}
	// 读取走 sqlite（全量）
	got, err := store.RecentMemories(ctx, "u1", 100)
	if err != nil || len(got) != 4 {
		t.Fatalf("RecentMemories 应返回 4 条，实际 %d err=%v", len(got), err)
	}
}

// TestTieredStoreWithRemoteBackfill 配置远端：存量回迁 + 打标 + 按窗口修剪 + 读取走远端。
func TestTieredStoreWithRemoteBackfill(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath, memDir, profDir := dir+"/memory.db", dir+"/memories", dir+"/users"

	// 阶段 A：无远端运行，积累存量（含旧记忆）与画像
	first, err := OpenTiered(TieredConfig{SQLitePath: dbPath, FileDir: memDir, ProfilesDir: profDir})
	if err != nil {
		t.Fatalf("OpenTiered(A): %v", err)
	}
	for _, e := range seedEntries("u1") {
		if err := first.SaveMemory(ctx, e); err != nil {
			t.Fatalf("SaveMemory(A %s): %v", e.ID, err)
		}
	}
	if err := first.SaveProfile(ctx, "u1", "# 画像\n用户喜欢咖啡"); err != nil {
		t.Fatalf("SaveProfile(A): %v", err)
	}
	first.Close()

	// 启动远端记忆服务（内存 file 后端）
	backend := NewFileStore(t.TempDir(), t.TempDir(), 500)
	defer backend.Close()
	srv := httptest.NewServer(NewServer(backend, "test-token").Handler())
	defer srv.Close()

	// 阶段 B：新配置远端，启动应回迁 + 修剪
	second, err := OpenTiered(TieredConfig{
		RemoteURL: srv.URL, RemoteToken: "test-token", Timeout: 2 * time.Second,
		SQLitePath: dbPath, FileDir: memDir, ProfilesDir: profDir,
		SQLiteRetention: 168 * time.Hour, FileRetention: 72 * time.Hour,
	})
	if err != nil {
		t.Fatalf("OpenTiered(B): %v", err)
	}
	defer second.Close()

	// 远端拿到全量 4 条 + 画像
	remoteEntries, err := backend.RecentMemories(ctx, "u1", 100)
	if err != nil || len(remoteEntries) != 4 {
		t.Fatalf("远端应有全量 4 条，实际 %d err=%v", len(remoteEntries), err)
	}
	if p, _ := backend.LoadProfile(ctx, "u1"); p == "" {
		t.Fatal("画像应回迁到远端")
	}
	// 已打同步标记
	if !second.userSynced(ctx, "u1") {
		t.Fatal("回迁成功后应打 remote_synced 标记")
	}
	// 本地按窗口修剪：sqlite/file 各剩 2 条新记忆
	if n, _ := second.sqlite.RecentMemories(ctx, "u1", 100); len(n) != 2 {
		t.Fatalf("sqlite 层回迁后应修剪为 2 条，实际 %d", len(n))
	}
	if n, _ := second.file.RecentMemories(ctx, "u1", 100); len(n) != 2 {
		t.Fatalf("file 层回迁后应修剪为 2 条，实际 %d", len(n))
	}
	// 读取走远端（全量）
	got, err := second.RecentMemories(ctx, "u1", 100)
	if err != nil || len(got) != 4 {
		t.Fatalf("读取应走远端返回全量 4 条，实际 %d err=%v", len(got), err)
	}
	// 新写入三层同步
	if err := second.SaveMemory(ctx, memory.MemoryEntry{ID: "n3", UserID: "u1", Content: "回迁后的新记忆", Tags: []string{"user"}, Timestamp: time.Now()}); err != nil {
		t.Fatalf("SaveMemory(B): %v", err)
	}
	if n, _ := backend.RecentMemories(ctx, "u1", 100); len(n) != 5 {
		t.Fatalf("新记忆应写入远端，实际 %d", len(n))
	}
}

// TestTieredBackfillFailureKeepsLocal 回迁失败：不打标、不修剪，读取留在本地。
func TestTieredBackfillFailureKeepsLocal(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath, memDir, profDir := dir+"/memory.db", dir+"/memories", dir+"/users"

	first, err := OpenTiered(TieredConfig{SQLitePath: dbPath, FileDir: memDir, ProfilesDir: profDir})
	if err != nil {
		t.Fatalf("OpenTiered(A): %v", err)
	}
	for _, e := range seedEntries("u1") {
		if err := first.SaveMemory(ctx, e); err != nil {
			t.Fatalf("SaveMemory(A): %v", err)
		}
	}
	first.Close()

	// 远端令牌错误 → 回迁全部失败（401）
	backend := NewFileStore(t.TempDir(), t.TempDir(), 500)
	defer backend.Close()
	srv := httptest.NewServer(NewServer(backend, "right-token").Handler())
	defer srv.Close()

	second, err := OpenTiered(TieredConfig{
		RemoteURL: srv.URL, RemoteToken: "wrong-token", Timeout: 2 * time.Second,
		SQLitePath: dbPath, FileDir: memDir, ProfilesDir: profDir,
		SQLiteRetention: 168 * time.Hour, FileRetention: 72 * time.Hour,
	})
	if err != nil {
		t.Fatalf("OpenTiered(B): %v", err)
	}
	defer second.Close()

	if second.userSynced(ctx, "u1") {
		t.Fatal("回迁失败不应打 remote_synced 标记")
	}
	// 本地全量保留（旧记忆不被修剪）
	got, err := second.RecentMemories(ctx, "u1", 100)
	if err != nil || len(got) != 4 {
		t.Fatalf("回迁失败应保留本地全量 4 条，实际 %d err=%v", len(got), err)
	}
	// 远端无数据
	if n, _ := backend.RecentMemories(ctx, "u1", 100); len(n) != 0 {
		t.Fatalf("回迁失败远端不应有成功写入，实际 %d", len(n))
	}
}

// TestTieredReadFallbackWhenRemoteDown 已同步用户的远端宕机时读取回退本地层。
func TestTieredReadFallbackWhenRemoteDown(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath, memDir, profDir := dir+"/memory.db", dir+"/memories", dir+"/users"

	backend := NewFileStore(t.TempDir(), t.TempDir(), 500)
	srv := httptest.NewServer(NewServer(backend, "test-token").Handler())

	store, err := OpenTiered(TieredConfig{
		RemoteURL: srv.URL, RemoteToken: "test-token", Timeout: 1 * time.Second,
		SQLitePath: dbPath, FileDir: memDir, ProfilesDir: profDir,
	})
	if err != nil {
		t.Fatalf("OpenTiered: %v", err)
	}
	defer store.Close()
	if err := store.SaveMemory(ctx, memory.MemoryEntry{ID: "m1", UserID: "u1", Content: "测试内容", Tags: []string{"user"}, Timestamp: time.Now()}); err != nil {
		t.Fatalf("SaveMemory: %v", err)
	}
	if err := store.sqlite.SetMeta(ctx, "u1", metaRemoteSynced, "1"); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	srv.Close() // 远端宕机

	got, err := store.RecentMemories(ctx, "u1", 10)
	if err != nil || len(got) != 1 {
		t.Fatalf("远端宕机应回退本地层，实际 %d err=%v", len(got), err)
	}
}
