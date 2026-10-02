package memstore

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/wenruigao/tommy-catty/internal/memory"
)

// asyncOp 后台写操作。
type asyncOp struct {
	kind   opKind
	ctx    context.Context
	entry  memory.MemoryEntry
	userID string
	query  string
	topK   int
	cutoff time.Time
	done   chan error // 可选：调用方需要等待结果时使用
}

type opKind int

const (
	opSaveMemory opKind = iota
	opDeleteMemories
	opSaveProfile
	opPruneBefore
	opSetMeta
	opFlush
)

// AsyncSQLiteConfig 异步 SQLite 存储配置。
type AsyncSQLiteConfig struct {
	// BufferSize 写队列缓冲大小（默认 256）；队列满时写入方阻塞直到有空位。
	BufferSize int
	// FlushInterval 定期刷新间隔（默认 0 = 仅按需刷新）。
	FlushInterval time.Duration
}

// AsyncSQLiteStore 将 SQLiteStore 的写操作移至后台 goroutine 执行，
// 读操作保持同步（WAL 模式允许读写并发）。
//
// 设计参考 OpenClaw 的 SQLite Worker 模式：主线程（ReAct 循环）不再
// 阻塞于磁盘 I/O，写操作经 channel 投递给专用 worker 串行执行。
type AsyncSQLiteStore struct {
	inner *SQLiteStore
	queue chan asyncOp
	wg    sync.WaitGroup
	close chan struct{}
	once  sync.Once
}

// NewAsyncSQLiteStore 包装现有 SQLiteStore，启动后台写 worker。
func NewAsyncSQLiteStore(inner *SQLiteStore, cfg AsyncSQLiteConfig) *AsyncSQLiteStore {
	if cfg.BufferSize <= 0 {
		cfg.BufferSize = 256
	}
	a := &AsyncSQLiteStore{
		inner: inner,
		queue: make(chan asyncOp, cfg.BufferSize),
		close: make(chan struct{}),
	}
	a.wg.Add(1)
	go a.worker(cfg.FlushInterval)
	return a
}

// worker 后台写循环：从队列取操作并执行，直到收到关闭信号后排空队列。
func (a *AsyncSQLiteStore) worker(flushInterval time.Duration) {
	defer a.wg.Done()

	var ticker *time.Ticker
	if flushInterval > 0 {
		ticker = time.NewTicker(flushInterval)
		defer ticker.Stop()
	}

	for {
		select {
		case op := <-a.queue:
			a.execute(op)
		case <-a.close:
			// 排空队列中剩余操作
			for {
				select {
				case op := <-a.queue:
					a.execute(op)
				default:
					return
				}
			}
		default:
			if ticker != nil {
				select {
				case op := <-a.queue:
					a.execute(op)
				case <-ticker.C:
					// 定期刷新：无操作，仅保持 WAL checkpoint 活跃
				case <-a.close:
					for {
						select {
						case op := <-a.queue:
							a.execute(op)
						default:
							return
						}
					}
				}
			} else {
				select {
				case op := <-a.queue:
					a.execute(op)
				case <-a.close:
					for {
						select {
						case op := <-a.queue:
							a.execute(op)
						default:
							return
						}
					}
				}
			}
		}
	}
}

// execute 执行单个写操作。
func (a *AsyncSQLiteStore) execute(op asyncOp) {
	var err error
	switch op.kind {
	case opSaveMemory:
		err = a.inner.SaveMemory(op.ctx, op.entry)
	case opDeleteMemories:
		err = a.inner.DeleteMemories(op.ctx, op.userID)
	case opSaveProfile:
		err = a.inner.SaveProfile(op.ctx, op.userID, op.entry.Content)
	case opPruneBefore:
		err = a.inner.PruneBefore(op.ctx, op.userID, op.cutoff)
	case opSetMeta:
		err = a.inner.SetMeta(op.ctx, op.userID, op.query, op.entry.Content)
	case opFlush:
		// 无操作，仅作为屏障
	}
	if op.done != nil {
		op.done <- err
	} else if err != nil {
		slog.Warn("memstore(async): 后台写入失败", "error", err)
	}
}

// enqueue 将操作投递到写队列。done 为 nil 时异步执行（fire-and-forget），
// 非 nil 时调用方可等待结果。
func (a *AsyncSQLiteStore) enqueue(op asyncOp) error {
	select {
	case a.queue <- op:
		if op.done != nil {
			return <-op.done
		}
		return nil
	case <-a.close:
		return context.Canceled
	}
}

// SaveMemory 异步保存记忆（fire-and-forget，错误由 worker 日志记录）。
func (a *AsyncSQLiteStore) SaveMemory(ctx context.Context, entry memory.MemoryEntry) error {
	return a.enqueue(asyncOp{
		kind:  opSaveMemory,
		ctx:   context.WithoutCancel(ctx),
		entry: entry,
	})
}

// SaveMemorySync 同步保存记忆（等待写入完成），用于启动回迁等需要确认的场景。
func (a *AsyncSQLiteStore) SaveMemorySync(ctx context.Context, entry memory.MemoryEntry) error {
	done := make(chan error, 1)
	return a.enqueue(asyncOp{
		kind:  opSaveMemory,
		ctx:   context.WithoutCancel(ctx),
		entry: entry,
		done:  done,
	})
}

// SearchMemories 同步读取（WAL 模式下可与后台写并发）。
func (a *AsyncSQLiteStore) SearchMemories(ctx context.Context, userID, query string, topK int) ([]memory.MemoryEntry, error) {
	return a.inner.SearchMemories(ctx, userID, query, topK)
}

// RecentMemories 同步读取。
func (a *AsyncSQLiteStore) RecentMemories(ctx context.Context, userID string, limit int) ([]memory.MemoryEntry, error) {
	return a.inner.RecentMemories(ctx, userID, limit)
}

// DeleteMemories 异步清空用户记忆。
func (a *AsyncSQLiteStore) DeleteMemories(ctx context.Context, userID string) error {
	return a.enqueue(asyncOp{
		kind:   opDeleteMemories,
		ctx:    context.WithoutCancel(ctx),
		userID: userID,
	})
}

// SaveProfile 异步保存用户画像。
func (a *AsyncSQLiteStore) SaveProfile(ctx context.Context, userID, content string) error {
	return a.enqueue(asyncOp{
		kind:   opSaveProfile,
		ctx:    context.WithoutCancel(ctx),
		userID: userID,
		entry:  memory.MemoryEntry{Content: content},
	})
}

// LoadProfile 同步读取用户画像。
func (a *AsyncSQLiteStore) LoadProfile(ctx context.Context, userID string) (string, error) {
	return a.inner.LoadProfile(ctx, userID)
}

// PruneBefore 异步修剪过期记忆。
func (a *AsyncSQLiteStore) PruneBefore(ctx context.Context, userID string, cutoff time.Time) error {
	return a.enqueue(asyncOp{
		kind:   opPruneBefore,
		ctx:    context.WithoutCancel(ctx),
		userID: userID,
		cutoff: cutoff,
	})
}

// GetMeta 同步读取元数据。
func (a *AsyncSQLiteStore) GetMeta(ctx context.Context, userID, key string) (string, error) {
	return a.inner.GetMeta(ctx, userID, key)
}

// SetMeta 异步写入元数据。
func (a *AsyncSQLiteStore) SetMeta(ctx context.Context, userID, key, value string) error {
	return a.enqueue(asyncOp{
		kind:   opSetMeta,
		ctx:    context.WithoutCancel(ctx),
		userID: userID,
		query:  key,
		entry:  memory.MemoryEntry{Content: value},
	})
}

// SetMetaSync 同步写入元数据（用于回迁标记等需要立即确认的场景）。
func (a *AsyncSQLiteStore) SetMetaSync(ctx context.Context, userID, key, value string) error {
	done := make(chan error, 1)
	return a.enqueue(asyncOp{
		kind:   opSetMeta,
		ctx:    context.WithoutCancel(ctx),
		userID: userID,
		query:  key,
		entry:  memory.MemoryEntry{Content: value},
		done:   done,
	})
}

// ListUserIDs 同步读取用户列表。
func (a *AsyncSQLiteStore) ListUserIDs(ctx context.Context) ([]string, error) {
	return a.inner.ListUserIDs(ctx)
}

// Flush 等待队列中所有待处理写操作完成（屏障语义）。
func (a *AsyncSQLiteStore) Flush() error {
	done := make(chan error, 1)
	return a.enqueue(asyncOp{kind: opFlush, ctx: context.Background(), done: done})
}

// Close 关闭写队列，排空待处理操作后释放底层数据库。
func (a *AsyncSQLiteStore) Close() error {
	a.once.Do(func() { close(a.close) })
	a.wg.Wait()
	return a.inner.Close()
}

// Inner 返回底层同步 SQLiteStore（供 TieredStore 启动维护等需要同步语义的场景使用）。
func (a *AsyncSQLiteStore) Inner() *SQLiteStore {
	return a.inner
}
