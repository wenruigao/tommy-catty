package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/tommy-cat/agent/internal/llm"
	"github.com/tommy-cat/agent/internal/memstore"
)

// profilerHistoryLimit 送给 LLM 总结的最近会话消息条数上限。
const profilerHistoryLimit = 20

// ProfilerChatFunc 是 UserProfiler 使用的简化 LLM 对话函数，
// 由入口从 engine.LLMClient / llm.Gateway 适配注入。
type ProfilerChatFunc func(ctx context.Context, messages []llm.Message) (string, error)

// UserProfiler 周期性总结用户的指令偏好与对话风格，
// 写入 data/users/{userID}/user.md 作为用户画像，供系统提示词引用。
// 所有失败均静默处理，不阻断正常任务执行。
type UserProfiler struct {
	dir      string           // 用户画像根目录（如 data/users）
	interval int              // 每完成多少次 Run 更新一次画像
	chatFn   ProfilerChatFunc // 可为 nil（nil 时禁用画像生成）
	store    memstore.Store   // 记忆存储后端（非 nil 时画像经后端读写，可为 nil）

	mu     sync.Mutex     // 防并发写同一画像文件
	counts map[string]int // 每用户已完成的 Run 计数
}

// NewUserProfiler 创建用户画像生成器。
// interval 为更新间隔（每完成 N 次 Run 更新一次），<= 0 时取默认 3。
// chatFn 为 nil 时画像生成被禁用。
func NewUserProfiler(dir string, interval int, chatFn ProfilerChatFunc) *UserProfiler {
	if interval <= 0 {
		interval = 3
	}
	return &UserProfiler{
		dir:      dir,
		interval: interval,
		chatFn:   chatFn,
		counts:   make(map[string]int),
	}
}

// SetStore 注入记忆存储后端；注入后画像读写优先经后端（file 后端与本地文件路径一致，
// sqlite/remote 后端实现多实例共享），后端失败时回退本地文件。
func (p *UserProfiler) SetStore(store memstore.Store) {
	if p != nil {
		p.store = store
	}
}

// OnRunComplete 在一次 Run 完成后调用；累计达到更新间隔时，
// 用 LLM 总结该用户最近会话并更新 user.md。任何失败都静默忽略。
func (p *UserProfiler) OnRunComplete(ctx context.Context, userID string, history []llm.Message) {
	if p == nil || p.chatFn == nil || (p.dir == "" && p.store == nil) {
		return
	}

	p.mu.Lock()
	p.counts[userID]++
	n := p.counts[userID]
	p.mu.Unlock()

	if n%p.interval != 0 {
		return
	}
	p.update(ctx, userID, history)
}

// update 调用 LLM 生成并写入用户画像（持有写锁，失败静默）。
func (p *UserProfiler) update(ctx context.Context, userID string, history []llm.Message) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// 截取最近若干条消息，避免 prompt 过长
	if len(history) > profilerHistoryLimit {
		history = history[len(history)-profilerHistoryLimit:]
	}
	var convo strings.Builder
	for _, m := range history {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		content := m.Content
		if len([]rune(content)) > 500 {
			content = string([]rune(content)[:500]) + "…"
		}
		fmt.Fprintf(&convo, "%s: %s\n", m.Role, content)
	}
	if convo.Len() == 0 {
		return
	}

	// 已存在画像时一并提供，要求 LLM 保留仍有效的旧偏好（后端优先，回退本地文件）
	oldProfile := loadUserProfileVia(p.store, p.dir, userID)

	userPrompt := "以下是该用户最近的会话记录：\n" + convo.String()
	if oldProfile != "" {
		userPrompt += "\n以下是该用户已有的画像内容：\n" + oldProfile +
			"\n请在新画像中保留仍然有效的旧偏好，并合并本次会话中新增的偏好。"
	}
	userPrompt += "\n请输出更新后的用户画像正文（Markdown，不要输出多余解释）。"

	messages := []llm.Message{
		{Role: "system", Content: `你是用户画像分析助手。请根据用户最近的会话记录，总结该用户的指令偏好与对话风格，
包括：常用的任务类型、表达习惯、对回答详略与格式的偏好、常用的工具或技术栈等。
如果用户在对话中明确说了"记住…"、"以后都…"之类的偏好，必须吸收进画像。
只描述用户偏好，不要包含任务本身的具体内容，不要包含任何敏感信息（密码、密钥等）。`},
		{Role: "user", Content: userPrompt},
	}

	profile, err := p.chatFn(ctx, messages)
	if err != nil || strings.TrimSpace(profile) == "" {
		return // 失败静默，保留旧画像
	}

	// 画像落盘：注入存储后端时经后端写入（失败回退本地文件），否则直接写本地
	if p.store != nil {
		if err := p.store.SaveProfile(ctx, userID, profile); err == nil {
			return
		}
	}
	if p.dir == "" {
		return
	}
	path := userProfilePath(p.dir, userID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(path, []byte(profile), 0o644)
}
