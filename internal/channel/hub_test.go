package channel

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wenruigao/tommy-catty/internal/engine"
	"github.com/wenruigao/tommy-catty/internal/session"
)

// fakeChannel 记录 Send 调用的测试渠道。
type fakeChannel struct {
	name      string
	mu        sync.Mutex
	sent      []OutboundMessage
	sendCh    chan OutboundMessage
	failTimes int
}

func (f *fakeChannel) Name() string                                           { return f.name }
func (f *fakeChannel) Start(_ context.Context, _ chan<- InboundMessage) error { return nil }
func (f *fakeChannel) Stop() error                                            { return nil }
func (f *fakeChannel) Status() ChannelStatus                                  { return StatusConnected }

func (f *fakeChannel) Send(_ context.Context, msg OutboundMessage) error {
	f.mu.Lock()
	if f.failTimes > 0 {
		f.failTimes--
		f.mu.Unlock()
		return errors.New("模拟投递失败")
	}
	f.sent = append(f.sent, msg)
	f.mu.Unlock()
	if f.sendCh != nil {
		f.sendCh <- msg
	}
	return nil
}

func (f *fakeChannel) sentCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

// fakeRunner 记录 Run 调用并返回预设结果的测试会话。
type fakeRunner struct {
	mu    sync.Mutex
	goals []string
	reply string
	err   error
}

func (f *fakeRunner) Run(_ context.Context, goal string) (*engine.ExecutionTrace, error) {
	f.mu.Lock()
	f.goals = append(f.goals, goal)
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return &engine.ExecutionTrace{Steps: []engine.StepResult{{FinalAnswer: f.reply}}}, nil
}

func (f *fakeRunner) runCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.goals)
}

// newTestHub 构建带单个 fakeChannel 的 Hub（SendRetries=0 保证测试确定性）。
func newTestHub(runner SessionRunner) (*Hub, *fakeChannel) {
	fc := &fakeChannel{name: "test", sendCh: make(chan OutboundMessage, 16)}
	hub := NewHub(func(string) SessionRunner { return runner }, HubConfig{
		QueueSize:      16,
		DedupeWindow:   time.Minute,
		DefaultTimeout: 5 * time.Second,
		SendRetries:    0,
	})
	hub.Register("test", fc, ChannelConfig{})
	return hub, fc
}

func waitMsg(t *testing.T, ch chan OutboundMessage) OutboundMessage {
	t.Helper()
	select {
	case m := <-ch:
		return m
	case <-time.After(2 * time.Second):
		t.Fatal("等待出站消息超时")
		return OutboundMessage{}
	}
}

// TestSessionKey_Routing 验证会话键路由规则：
// 群聊按群 ID、单聊按用户 ID，且带渠道前缀隔离。
func TestSessionKey_Routing(t *testing.T) {
	dm := SessionKey("webhook", InboundMessage{UserID: "alice", ChatType: ChatTypeDM})
	if dm != "webhook:alice" {
		t.Errorf("单聊会话键 = %q, want webhook:alice", dm)
	}
	group := SessionKey("feishu", InboundMessage{UserID: "alice", ChatID: "group-1", ChatType: ChatTypeGroup})
	if group != "feishu:group-1" {
		t.Errorf("群聊会话键 = %q, want feishu:group-1", group)
	}
}

// TestHub_DedupAndACL 验证去重（重复 MessageID 只执行一次）
// 与访问控制（白名单外用户直接拒绝）。
func TestHub_DedupAndACL(t *testing.T) {
	runner := &fakeRunner{reply: "ok"}
	hub, fc := newTestHub(runner)
	hub.Register("test", fc, ChannelConfig{AllowUsers: []string{"alice"}})

	msg := InboundMessage{
		Channel: "test", MessageID: "m1", UserID: "alice",
		ChatID: "alice", ChatType: ChatTypeDM, Text: "hello",
	}
	hub.Dispatch("test", msg)
	waitMsg(t, fc.sendCh)

	// 重复 MessageID：被去重丢弃
	hub.Dispatch("test", msg)
	// 白名单外用户：被访问控制拒绝
	unauthorized := msg
	unauthorized.MessageID = "m2"
	unauthorized.UserID = "mallory"
	hub.Dispatch("test", unauthorized)

	time.Sleep(200 * time.Millisecond) // 等待异步执行（若有）完成
	if runner.runCount() != 1 {
		t.Errorf("任务应恰好执行 1 次，实际 %d 次（去重/ACL 失效）", runner.runCount())
	}
	if fc.sentCount() != 1 {
		t.Errorf("出站消息应恰好 1 条，实际 %d 条", fc.sentCount())
	}
}

// TestHub_AckAndReply 验证受理提示先于最终回复投递，
// 且 ReplyTo（渠道回复地址）全链路透传。
func TestHub_AckAndReply(t *testing.T) {
	runner := &fakeRunner{reply: "最终答案"}
	hub, fc := newTestHub(runner)
	hub.Register("test", fc, ChannelConfig{AckMessage: "收到，处理中…"})

	hub.Dispatch("test", InboundMessage{
		Channel: "test", MessageID: "m1", UserID: "alice",
		ChatID: "alice", ChatType: ChatTypeDM, Text: "hello",
		ReplyTo: "http://callback/x",
	})

	ack := waitMsg(t, fc.sendCh)
	if ack.Text != "收到，处理中…" {
		t.Errorf("第一条出站应为受理提示，得到 %q", ack.Text)
	}
	answer := waitMsg(t, fc.sendCh)
	if answer.Text != "最终答案" {
		t.Errorf("第二条出站应为最终答案，得到 %q", answer.Text)
	}
	if answer.ReplyTo != "http://callback/x" {
		t.Errorf("ReplyTo 应透传到出站消息，得到 %q", answer.ReplyTo)
	}
}

// TestHub_RateLimitedReply 验证限流错误转换为友好提示（不透出内部错误）。
func TestHub_RateLimitedReply(t *testing.T) {
	runner := &fakeRunner{err: session.ErrRateLimited}
	hub, fc := newTestHub(runner)

	hub.Dispatch("test", InboundMessage{
		Channel: "test", MessageID: "m1", UserID: "alice",
		ChatID: "alice", ChatType: ChatTypeDM, Text: "hello",
	})

	reply := waitMsg(t, fc.sendCh)
	if !strings.Contains(reply.Text, "频繁") {
		t.Errorf("限流时应回复友好提示，得到 %q", reply.Text)
	}
}

// TestHub_GroupNeverSkipped 验证 group_mode=never 时群消息被直接丢弃。
func TestHub_GroupNeverSkipped(t *testing.T) {
	runner := &fakeRunner{reply: "ok"}
	hub, fc := newTestHub(runner)
	hub.Register("test", fc, ChannelConfig{GroupMode: GroupModeNever})

	hub.Dispatch("test", InboundMessage{
		Channel: "test", MessageID: "m1", UserID: "alice",
		ChatID: "group-1", ChatType: ChatTypeGroup, Text: "hello",
	})

	time.Sleep(200 * time.Millisecond)
	if runner.runCount() != 0 || fc.sentCount() != 0 {
		t.Errorf("group_mode=never 时群消息应被丢弃（runs=%d, sends=%d）",
			runner.runCount(), fc.sentCount())
	}
}

// TestHub_SendRetry 验证出站投递失败后重试成功。
func TestHub_SendRetry(t *testing.T) {
	runner := &fakeRunner{reply: "ok"}
	fc := &fakeChannel{name: "test", failTimes: 1} // 第一次失败
	hub := NewHub(func(string) SessionRunner { return runner }, HubConfig{
		QueueSize:      16,
		DedupeWindow:   time.Minute,
		DefaultTimeout: 5 * time.Second,
		SendRetries:    2,
	})
	hub.Register("test", fc, ChannelConfig{})

	hub.deliver("test", OutboundMessage{ChatID: "alice", Text: "hi"})

	deadline := time.Now().Add(5 * time.Second)
	for fc.sentCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if fc.sentCount() != 1 {
		t.Errorf("重试后应投递成功 1 条，实际 %d 条", fc.sentCount())
	}
}

// TestSplitChunks 验证长文本按 rune 分片（不截断多字节字符）。
func TestSplitChunks(t *testing.T) {
	parts := splitChunks("abcde", 2)
	if len(parts) != 3 || parts[0] != "ab" || parts[1] != "cd" || parts[2] != "e" {
		t.Errorf("splitChunks(abcde,2) = %v", parts)
	}
	parts = splitChunks("你好世界呀", 2)
	if len(parts) != 3 || parts[0] != "你好" || parts[2] != "呀" {
		t.Errorf("中文分片错误: %v", parts)
	}
	if parts := splitChunks("short", 100); len(parts) != 1 {
		t.Errorf("短文本不应分片: %v", parts)
	}
}

// TestUserAllowed 验证访问控制白名单规则。
func TestUserAllowed(t *testing.T) {
	if !userAllowed(nil, "anyone") || !userAllowed([]string{"*"}, "anyone") {
		t.Error("空白名单或通配符应放行所有用户")
	}
	if !userAllowed([]string{"alice"}, "alice") {
		t.Error("名单内用户应放行")
	}
	if userAllowed([]string{"alice"}, "bob") {
		t.Error("名单外用户应拒绝")
	}
}
