package server

import (
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// HotReloadable 可热加载的资源接口。
type HotReloadable interface {
	// Reload 重新加载资源内容。返回 error 表示加载失败（保留旧值）。
	Reload() error
	// Name 资源名称（日志标识）。
	Name() string
}

// HotReloader 监听 SIGHUP 信号，触发所有注册资源的热加载。
// 典型用法：在 main 中创建后注册 policy/agent.md 等资源，调用 Start()。
type HotReloader struct {
	mu        sync.RWMutex
	resources []HotReloadable
	stopCh    chan struct{}
	once      sync.Once
}

// NewHotReloader 创建热加载器。
func NewHotReloader() *HotReloader {
	return &HotReloader{
		stopCh: make(chan struct{}),
	}
}

// Register 注册一个可热加载资源。
func (hr *HotReloader) Register(r HotReloadable) {
	hr.mu.Lock()
	defer hr.mu.Unlock()
	hr.resources = append(hr.resources, r)
}

// Start 启动 SIGHUP 监听（非阻塞，后台 goroutine）。
func (hr *HotReloader) Start() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGHUP)

	go func() {
		for {
			select {
			case <-sigCh:
				hr.reloadAll()
			case <-hr.stopCh:
				signal.Stop(sigCh)
				return
			}
		}
	}()
}

// Stop 停止监听。
func (hr *HotReloader) Stop() {
	hr.once.Do(func() { close(hr.stopCh) })
}

// reloadAll 依次重新加载所有注册资源。
func (hr *HotReloader) reloadAll() {
	hr.mu.RLock()
	defer hr.mu.RUnlock()

	for _, r := range hr.resources {
		if err := r.Reload(); err != nil {
			slog.Error("热加载失败", "resource", r.Name(), "error", err)
		} else {
			slog.Info("热加载成功", "resource", r.Name())
		}
	}
}

// ReloadNow 手动触发一次全量热加载（供测试或 API 调用）。
func (hr *HotReloader) ReloadNow() {
	hr.reloadAll()
}

// FileResource 基于文件的热加载资源：读取文件内容后调用 OnReload 回调。
type FileResource struct {
	FilePath string
	Label    string
	OnReload func(data []byte) error
}

// Reload 重新读取文件并触发回调。
func (f *FileResource) Reload() error {
	data, err := os.ReadFile(f.FilePath)
	if err != nil {
		return err
	}
	if f.OnReload != nil {
		return f.OnReload(data)
	}
	return nil
}

// Name 返回资源标识。
func (f *FileResource) Name() string {
	if f.Label != "" {
		return f.Label
	}
	return f.FilePath
}
