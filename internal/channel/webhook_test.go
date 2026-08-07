package channel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// postJSON 向指定 URL 发送带（或不带）Bearer token 的 JSON POST。
func postJSON(t *testing.T, url, token, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// TestWebhook_TokenRequired 验证未配置 token 时拒绝创建（不允许无认证端点）。
func TestWebhook_TokenRequired(t *testing.T) {
	if _, err := NewWebhookChannel(WebhookConfig{}, http.NewServeMux()); err == nil {
		t.Fatal("未配置 token 应拒绝创建 webhook 渠道")
	}
}

// TestWebhook_Inbound_AuthAndDispatch 验证入站鉴权与消息转换：
// 无/错 token → 401；合法请求 → 202 且消息进入队列（字段完整）；空文本 → 400。
func TestWebhook_Inbound_AuthAndDispatch(t *testing.T) {
	mux := http.NewServeMux()
	ch, err := NewWebhookChannel(WebhookConfig{Token: "tok"}, mux)
	if err != nil {
		t.Fatalf("NewWebhookChannel 失败: %v", err)
	}
	in := make(chan InboundMessage, 4)
	if err := ch.Start(context.Background(), in); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// 无 token → 401
	resp := postJSON(t, srv.URL+"/channels/webhook", "", `{"text":"hi"}`)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("无 token 应返回 401，得到 %d", resp.StatusCode)
	}
	// 错误 token → 401
	resp = postJSON(t, srv.URL+"/channels/webhook", "wrong", `{"text":"hi"}`)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("错误 token 应返回 401，得到 %d", resp.StatusCode)
	}
	// 空文本 → 400
	resp = postJSON(t, srv.URL+"/channels/webhook", "tok", `{"user_id":"a"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("空文本应返回 400，得到 %d", resp.StatusCode)
	}

	// 合法请求 → 202，消息入队且字段完整
	resp = postJSON(t, srv.URL+"/channels/webhook", "tok",
		`{"message_id":"m1","user_id":"alice","chat_id":"g1","chat_type":"group","thread_id":"t1","text":"hello","callback_url":"http://cb/x"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("合法请求应返回 202，得到 %d", resp.StatusCode)
	}

	select {
	case msg := <-in:
		if msg.Channel != "webhook" || msg.MessageID != "m1" || msg.UserID != "alice" ||
			msg.ChatID != "g1" || msg.ChatType != ChatTypeGroup || msg.ThreadID != "t1" ||
			msg.Text != "hello" || msg.ReplyTo != "http://cb/x" {
			t.Errorf("入站消息字段转换错误: %+v", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("入站消息未进入队列")
	}
}

// TestWebhook_Send_Callback 验证出站投递：ReplyTo 优先、缺省回退 CallbackURL、
// 均无时报错；回调携带 Bearer token。
func TestWebhook_Send_Callback(t *testing.T) {
	var mu sync.Mutex
	var got webhookCallback
	var gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		_ = json.Unmarshal(body, &got)
		gotAuth = r.Header.Get("Authorization")
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	ch, err := NewWebhookChannel(WebhookConfig{Token: "tok"}, http.NewServeMux())
	if err != nil {
		t.Fatal(err)
	}

	// ReplyTo 优先
	if err := ch.Send(context.Background(), OutboundMessage{ChatID: "g1", Text: "reply", ReplyTo: ts.URL}); err != nil {
		t.Fatalf("Send 失败: %v", err)
	}
	mu.Lock()
	if got.Text != "reply" || got.ChatID != "g1" || gotAuth != "Bearer tok" {
		t.Errorf("回调内容或鉴权错误: %+v auth=%q", got, gotAuth)
	}
	mu.Unlock()

	// 无任何投递地址 → 报错
	if err := ch.Send(context.Background(), OutboundMessage{Text: "x"}); err == nil {
		t.Fatal("无投递地址应报错")
	}

	// 缺省回退 CallbackURL
	ch2, err := NewWebhookChannel(WebhookConfig{Token: "tok", CallbackURL: ts.URL}, http.NewServeMux())
	if err != nil {
		t.Fatal(err)
	}
	if err := ch2.Send(context.Background(), OutboundMessage{ChatID: "c2", Text: "fallback"}); err != nil {
		t.Fatalf("回退默认投递地址失败: %v", err)
	}
	mu.Lock()
	if got.Text != "fallback" {
		t.Errorf("应回退默认投递地址，得到 %+v", got)
	}
	mu.Unlock()
}

// TestWebhook_EndToEnd 端到端：模拟外部系统经 webhook 发消息 → Hub 执行任务
// → 结果异步投递回 callback_url（OpenClaw 类网关反向接入的核心链路）。
func TestWebhook_EndToEnd(t *testing.T) {
	// 回调接收端（模拟调用方系统）
	callbackCh := make(chan webhookCallback, 1)
	cbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var cb webhookCallback
		_ = json.Unmarshal(body, &cb)
		callbackCh <- cb
		w.WriteHeader(http.StatusOK)
	}))
	defer cbSrv.Close()

	mux := http.NewServeMux()
	ch, err := NewWebhookChannel(WebhookConfig{Token: "tok"}, mux)
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{reply: "42"}
	hub := NewHub(func(string) SessionRunner { return runner }, HubConfig{
		QueueSize:      8,
		DedupeWindow:   time.Minute,
		DefaultTimeout: 5 * time.Second,
		SendRetries:    0,
	})
	hub.Register("webhook", ch, ChannelConfig{})
	if err := hub.Start(context.Background()); err != nil {
		t.Fatalf("Hub 启动失败: %v", err)
	}
	defer hub.Stop()

	srv := httptest.NewServer(mux)
	defer srv.Close()

	body := fmt.Sprintf(
		`{"message_id":"e2e-1","user_id":"alice","text":"6 乘 7 等于多少？","callback_url":"%s"}`,
		cbSrv.URL)
	resp := postJSON(t, srv.URL+"/channels/webhook", "tok", body)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("入站应返回 202，得到 %d", resp.StatusCode)
	}

	select {
	case cb := <-callbackCh:
		if cb.Text != "42" {
			t.Errorf("回调内容应为任务答案，得到 %q", cb.Text)
		}
		if cb.ChatID != "alice" {
			t.Errorf("回调 chat_id 应为 alice（缺省用 user_id），得到 %q", cb.ChatID)
		}
		if runner.runCount() != 1 {
			t.Errorf("任务应恰好执行 1 次，实际 %d", runner.runCount())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("等待 callback_url 回复超时")
	}
}
