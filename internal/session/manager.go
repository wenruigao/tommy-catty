package session

import (
	"context"
	"sync"
	"time"

	"github.com/wenruigao/tommy-catty/internal/metrics"
)

// ManagerConfig SessionManager 的配置。
type ManagerConfig struct {
	// MaxSessions 最大活跃会话数（默认 1000）
	MaxSessions int
	// SessionTTL 空闲超时，超过则淘汰（默认 30min）
	SessionTTL time.Duration
	// CleanupInterval 过期扫描间隔（默认 5min）
	CleanupInterval time.Duration
}

// DefaultManagerConfig 返回默认的 SessionManager 配置。
func DefaultManagerConfig() ManagerConfig {
	return ManagerConfig{
		MaxSessions:     1000,
		SessionTTL:      30 * time.Minute,
		CleanupInterval: 5 * time.Minute,
	}
}

// SessionManager 管理所有用户会话的生命周期：创建、获取、续期、淘汰。
type SessionManager struct {
	sessions map[string]*UserSession
	mu       sync.RWMutex
	cfg      ManagerConfig
	deps     SessionDeps

	cancel context.CancelFunc // 停止 cleanup goroutine
}

// NewSessionManager 创建会话管理器并启动后台清理 goroutine。
func NewSessionManager(cfg ManagerConfig, deps SessionDeps) *SessionManager {
	if cfg.MaxSessions <= 0 {
		cfg.MaxSessions = 1000
	}
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = 30 * time.Minute
	}
	if cfg.CleanupInterval <= 0 {
		cfg.CleanupInterval = 5 * time.Minute
	}

	ctx, cancel := context.WithCancel(context.Background())
	sm := &SessionManager{
		sessions: make(map[string]*UserSession),
		cfg:      cfg,
		deps:     deps,
		cancel:   cancel,
	}

	go sm.cleanupLoop(ctx)
	return sm
}

// GetOrCreate 获取用户会话，不存在则 lazy 创建。
func (sm *SessionManager) GetOrCreate(userID string) *UserSession {
	// 快速路径：读锁检查
	sm.mu.RLock()
	if s, ok := sm.sessions[userID]; ok {
		sm.mu.RUnlock()
		s.Touch()
		return s
	}
	sm.mu.RUnlock()

	// 慢路径：写锁创建
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// double-check
	if s, ok := sm.sessions[userID]; ok {
		s.Touch()
		return s
	}

	// 容量保护：达到上限时淘汰最久未活跃的
	if len(sm.sessions) >= sm.cfg.MaxSessions {
		sm.evictOldest()
	}

	s := NewUserSession(userID, sm.deps)
	sm.sessions[userID] = s
	metrics.SessionCreated().Add(1)
	return s
}

// Get 获取已存在的会话（不创建）。
func (sm *SessionManager) Get(userID string) (*UserSession, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	s, ok := sm.sessions[userID]
	if ok {
		s.Touch()
	}
	return s, ok
}

// Remove 主动销毁会话（用户登出/数据清除）。
func (sm *SessionManager) Remove(userID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	delete(sm.sessions, userID)
}

// ActiveCount 返回当前活跃会话数。
func (sm *SessionManager) ActiveCount() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return len(sm.sessions)
}

// Shutdown 停止后台清理 goroutine。
func (sm *SessionManager) Shutdown() {
	sm.cancel()
}

// evictOldest 淘汰最久未活跃的会话（调用时需持有写锁）。
func (sm *SessionManager) evictOldest() {
	var oldestID string
	var oldestTime time.Time
	first := true

	for id, s := range sm.sessions {
		if first || s.LastActive.Before(oldestTime) {
			oldestID = id
			oldestTime = s.LastActive
			first = false
		}
	}

	if oldestID != "" {
		delete(sm.sessions, oldestID)
	}
}

// cleanupLoop 定期扫描并淘汰过期会话。
func (sm *SessionManager) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(sm.cfg.CleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sm.cleanup()
		}
	}
}

// cleanup 淘汰所有超过 TTL 的会话。
func (sm *SessionManager) cleanup() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	now := time.Now()
	for id, s := range sm.sessions {
		if now.Sub(s.LastActive) > sm.cfg.SessionTTL {
			delete(sm.sessions, id)
		}
	}
}
