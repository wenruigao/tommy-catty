// Command memstore 是记忆存储服务与迁移工具：
//
//	serve    启动 REST 服务（供 agent 的 memory.storage.type=remote 后端接入）
//	migrate  将存量用户画像（data/users/*/user.md）导入 sqlite/remote 后端
//
// 示例：
//
//	memstore serve -addr :9301 -token $MEMSTORE_TOKEN -backend sqlite -db data/memory.db
//	memstore migrate -from data/users -to sqlite -db data/memory.db
//	memstore migrate -from data/users -to remote -url http://mem.internal:9301 -token $MEMSTORE_TOKEN
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tommy-cat/agent/internal/memstore"
)

func main() {
	cmd := "serve"
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		cmd = os.Args[1]
	}

	switch cmd {
	case "serve":
		runServe(os.Args[2:])
	case "migrate":
		runMigrate(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "用法: memstore serve|migrate [flags]\n")
		os.Exit(2)
	}
}

// runServe 启动记忆存储 REST 服务。
func runServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", ":9301", "监听地址")
	token := fs.String("token", os.Getenv("MEMSTORE_TOKEN"), "Bearer 鉴权令牌（空则不鉴权）")
	backend := fs.String("backend", "sqlite", "落地后端 file|sqlite")
	dir := fs.String("dir", "data/memories", "file 后端 JSONL 目录")
	db := fs.String("db", "data/memory.db", "sqlite 后端数据库路径")
	profiles := fs.String("profiles", "data/users", "file 后端画像目录")
	max := fs.Int("max-entries", 500, "每用户长期记忆上限")
	fs.Parse(args)

	var store memstore.Store
	var err error
	switch *backend {
	case memstore.BackendFile:
		store = memstore.NewFileStore(*dir, *profiles, *max)
	case memstore.BackendSQLite:
		store, err = memstore.NewSQLiteStore(*db, *max)
	default:
		log.Fatalf("serve: 落地后端只支持 file|sqlite，收到 %q", *backend)
	}
	if err != nil {
		log.Fatalf("serve: 初始化后端失败: %v", err)
	}
	defer store.Close()

	srv := &http.Server{
		Addr:              *addr,
		Handler:           memstore.NewServer(store, *token).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	authMode := "关闭"
	if *token != "" {
		authMode = "开启"
	}
	log.Printf("memstore 服务已启动 addr=%s backend=%s 鉴权=%s", *addr, *backend, authMode)
	log.Fatal(srv.ListenAndServe())
}

// runMigrate 将存量画像导入目标后端：扫描 -from 目录下 */user.md，
// 以目录名作为 userID 写入目标后端的画像存储。
func runMigrate(args []string) {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	from := fs.String("from", "data/users", "存量画像目录（含 {userID}/user.md）")
	to := fs.String("to", "sqlite", "目标后端 sqlite|remote")
	db := fs.String("db", "data/memory.db", "sqlite 目标数据库路径")
	url := fs.String("url", "", "remote 目标服务地址")
	token := fs.String("token", os.Getenv("MEMSTORE_TOKEN"), "remote 目标鉴权令牌")
	fs.Parse(args)

	var store memstore.Store
	var err error
	switch *to {
	case memstore.BackendSQLite:
		store, err = memstore.NewSQLiteStore(*db, 0)
	case memstore.BackendRemote:
		if *url == "" {
			log.Fatal("migrate: -to remote 需要 -url")
		}
		store = memstore.NewRemoteStore(*url, *token, 5*time.Second)
	default:
		log.Fatalf("migrate: 目标后端只支持 sqlite|remote，收到 %q", *to)
	}
	if err != nil {
		log.Fatalf("migrate: 打开目标后端失败: %v", err)
	}
	defer store.Close()

	entries, err := os.ReadDir(*from)
	if err != nil {
		log.Fatalf("migrate: 读取 %s 失败: %v", *from, err)
	}

	ctx := context.Background()
	count := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		userID := e.Name()
		data, err := os.ReadFile(filepath.Join(*from, userID, "user.md"))
		if err != nil {
			continue // 无画像文件的目录跳过
		}
		if err := store.SaveProfile(ctx, userID, string(data)); err != nil {
			log.Printf("migrate: 导入用户 %s 失败: %v", userID, err)
			continue
		}
		count++
	}
	fmt.Printf("✅ 迁移完成：%d 个用户画像已从 %s 导入 %s 后端\n", count, *from, *to)
}
