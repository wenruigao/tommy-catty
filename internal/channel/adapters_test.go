// adapters_test.go 覆盖各平台渠道 adapter 的核心链路：
// 入站验签/消息映射/异步入队，出站 API 调用格式。
package channel

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ─────────────────────────── 钉钉 ───────────────────────────

func TestDingTalk_InboundSignAndDispatch(t *testing.T) {
	mux := http.NewServeMux()
	ch, err := NewDingTalkChannel(DingTalkConfig{ClientID: "appkey", ClientSecret: "sec"}, mux)
	if err != nil {
		t.Fatalf("创建渠道失败: %v", err)
	}
	in := make(chan InboundMessage, 4)
	if err := ch.Start(context.Background(), in); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()

	payload := `{"conversationId":"cid1","conversationType":"2","msgId":"m1","msgtype":"text",
		"text":{"content":"你好"},"senderStaffId":"staff1","sessionWebhook":"http://example.com/wh"}`

	// 缺签名 → 401
	resp, _ := http.Post(srv.URL+"/channels/dingtalk", "application/json", strings.NewReader(payload))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("无签名请求期望 401，实际 %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 过期时间戳 → 401
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/channels/dingtalk", strings.NewReader(payload))
	req.Header.Set("timestamp", strconv.FormatInt(time.Now().Add(-2*time.Hour).UnixMilli(), 10))
	req.Header.Set("sign", dingtalkSign(strconv.FormatInt(time.Now().UnixMilli(), 10), "sec"))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("过期时间戳期望 401，实际 %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 合法请求 → 200 + 入队
	ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/channels/dingtalk", strings.NewReader(payload))
	req.Header.Set("timestamp", ts)
	req.Header.Set("sign", dingtalkSign(ts, "sec"))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("合法请求期望 200，实际 %d", resp.StatusCode)
	}
	resp.Body.Close()

	select {
	case msg := <-in:
		if msg.ChatID != "cid1" || msg.UserID != "staff1" || msg.Text != "你好" || msg.ChatType != ChatTypeGroup {
			t.Fatalf("消息映射错误: %+v", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("未收到入站消息")
	}

	// 出站：群聊走 sessionWebhook
	var gotBody []byte
	whSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Write([]byte(`{"errcode":0}`))
	}))
	defer whSrv.Close()
	ch.mu.Lock()
	ch.webhooks["cid1"] = whSrv.URL
	ch.mu.Unlock()
	if err := ch.Send(context.Background(), OutboundMessage{ChatID: "cid1", Text: "回复"}); err != nil {
		t.Fatalf("Send 失败: %v", err)
	}
	if !strings.Contains(string(gotBody), `"content":"回复"`) {
		t.Fatalf("sessionWebhook 投递内容错误: %s", gotBody)
	}
}

// ─────────────────────────── 飞书 ───────────────────────────

func TestFeishu_ChallengeSignatureAndDispatch(t *testing.T) {
	mux := http.NewServeMux()
	ch, err := NewFeishuChannel(FeishuConfig{
		AppID: "a", AppSecret: "s", EncryptKey: "ek", VerificationToken: "vt",
	}, mux)
	if err != nil {
		t.Fatalf("创建渠道失败: %v", err)
	}
	in := make(chan InboundMessage, 4)
	if err := ch.Start(context.Background(), in); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()

	post := func(body string, ts, nonce, sig string) *http.Response {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/channels/feishu", strings.NewReader(body))
		req.Header.Set("X-Lark-Request-Timestamp", ts)
		req.Header.Set("X-Lark-Request-Nonce", nonce)
		req.Header.Set("X-Lark-Signature", sig)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	// 签名错误 → 401
	resp := post(`{"type":"url_verification","challenge":"abc","token":"vt"}`, "123", "n1", "wrong")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("签名错误期望 401，实际 %d", resp.StatusCode)
	}
	resp.Body.Close()

	// url_verification 挑战 → 原样返回 challenge
	challenge := `{"type":"url_verification","challenge":"abc","token":"vt"}`
	resp = post(challenge, "123", "n1", feishuSignature("123", "n1", "ek", []byte(challenge)))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("challenge 期望 200，实际 %d", resp.StatusCode)
	}
	var got map[string]string
	json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if got["challenge"] != "abc" {
		t.Fatalf("challenge 应答错误: %v", got)
	}

	// 消息事件 → 入队（mention 占位符被剥离）
	content, _ := json.Marshal(map[string]string{"text": "@_user_1 帮我查天气"})
	event, _ := json.Marshal(map[string]any{
		"schema": "2.0",
		"header": map[string]string{
			"event_id":   "e1",
			"event_type": "im.message.receive_v1",
			"token":      "vt",
		},
		"event": map[string]any{
			"sender": map[string]any{
				"sender_id":   map[string]string{"open_id": "ou1"},
				"sender_type": "user",
			},
			"message": map[string]any{
				"message_id":   "fm1",
				"chat_id":      "oc1",
				"chat_type":    "group",
				"message_type": "text",
				"content":      string(content),
				"mentions":     []map[string]string{{"key": "@_user_1", "name": "bot"}},
			},
		},
	})
	resp = post(string(event), "456", "n2", feishuSignature("456", "n2", "ek", event))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("事件期望 200，实际 %d", resp.StatusCode)
	}
	resp.Body.Close()
	select {
	case msg := <-in:
		if msg.Text != "帮我查天气" || msg.ChatID != "oc1" || msg.UserID != "ou1" || msg.ChatType != ChatTypeGroup {
			t.Fatalf("消息映射错误: %+v", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("未收到入站消息")
	}
}

// ─────────────────────────── 微信公众号 ───────────────────────────

func TestWeChat_EchostrAndXMLDispatch(t *testing.T) {
	mux := http.NewServeMux()
	ch, err := NewWeChatChannel(WeChatConfig{AppID: "a", AppSecret: "s", Token: "tok"}, mux)
	if err != nil {
		t.Fatalf("创建渠道失败: %v", err)
	}
	in := make(chan InboundMessage, 4)
	if err := ch.Start(context.Background(), in); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()

	sig := wechatSignature("tok", "111", "n1")

	// GET 接入校验：合法签名回显 echostr，非法签名 401
	resp, _ := http.Get(srv.URL + "/channels/wechat?signature=bad&timestamp=111&nonce=n1&echostr=hello")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("非法签名期望 401，实际 %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp, _ = http.Get(srv.URL + "/channels/wechat?signature=" + sig + "&timestamp=111&nonce=n1&echostr=hello")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "hello" {
		t.Fatalf("echostr 回显错误: %q", string(body))
	}

	// POST XML 文本消息 → 入队
	xmlMsg := `<xml><ToUserName>gh_1</ToUserName><FromUserName>openid1</FromUserName>
		<CreateTime>1700000000</CreateTime><MsgType>text</MsgType>
		<Content>帮我写周报</Content><MsgId>42</MsgId></xml>`
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/channels/wechat?signature="+sig+"&timestamp=111&nonce=n1", strings.NewReader(xmlMsg))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST 期望 200，实际 %d", resp.StatusCode)
	}
	resp.Body.Close()
	select {
	case msg := <-in:
		if msg.Text != "帮我写周报" || msg.ChatID != "openid1" || msg.ChatType != ChatTypeDM || msg.MessageID != "42" {
			t.Fatalf("消息映射错误: %+v", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("未收到入站消息")
	}
}

// ─────────────────────────── 企业微信 ───────────────────────────

// wecomEncrypt 按企业微信算法构造加密回调密文（测试侧镜像实现）。
func wecomEncrypt(t *testing.T, encodingAESKey, msg, receiveID string) string {
	t.Helper()
	key, err := base64.StdEncoding.DecodeString(encodingAESKey + "=")
	if err != nil {
		t.Fatal(err)
	}
	plain := make([]byte, 16+len(msg)+4+len(receiveID))
	copy(plain[16:20], func() []byte {
		b := make([]byte, 4)
		binary.BigEndian.PutUint32(b, uint32(len(msg)))
		return b
	}())
	copy(plain[20:], msg)
	copy(plain[20+len(msg):], receiveID)
	if pad := 32 - len(plain)%32; pad > 0 {
		plain = append(plain, bytes.Repeat([]byte{byte(pad)}, pad)...)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, key[:16]).CryptBlocks(out, plain)
	return base64.StdEncoding.EncodeToString(out)
}

func TestWeCom_PlainAndEncryptedDispatch(t *testing.T) {
	mux := http.NewServeMux()
	aesKey := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))[:43]
	ch, err := NewWeComChannel(WeComConfig{
		CorpID: "corp1", AgentID: "100", AgentSecret: "s", Token: "tok", EncodingAESKey: aesKey,
	}, mux)
	if err != nil {
		t.Fatalf("创建渠道失败: %v", err)
	}
	in := make(chan InboundMessage, 4)
	if err := ch.Start(context.Background(), in); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()

	xmlMsg := `<xml><ToUserName>corp1</ToUserName><FromUserName>wxuser1</FromUserName>
		<CreateTime>1700000000</CreateTime><MsgType>text</MsgType>
		<Content>查询库存</Content><MsgId>7</MsgId><AgentID>100</AgentID></xml>`

	// 明文模式：签名错误 → 401
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/channels/wecom?msg_signature=bad&timestamp=1&nonce=n", strings.NewReader(xmlMsg))
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("明文非法签名期望 401，实际 %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 明文模式：合法签名 → 入队
	req, _ = http.NewRequest(http.MethodPost,
		srv.URL+"/channels/wecom?msg_signature="+wecomSignature("tok", "1", "n")+"&timestamp=1&nonce=n",
		strings.NewReader(xmlMsg))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("明文合法请求期望 200，实际 %d", resp.StatusCode)
	}
	resp.Body.Close()
	select {
	case msg := <-in:
		if msg.Text != "查询库存" || msg.UserID != "wxuser1" || msg.ChatType != ChatTypeDM {
			t.Fatalf("明文消息映射错误: %+v", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("未收到明文入站消息")
	}

	// 加密模式：AES 解密 + receiveid 校验 → 入队
	encrypted := wecomEncrypt(t, aesKey, xmlMsg, "corp1")
	encBody := fmt.Sprintf(`<xml><ToUserName>corp1</ToUserName><Encrypt>%s</Encrypt></xml>`, encrypted)
	sig := wecomSignature("tok", "2", "n2", encrypted)
	req, _ = http.NewRequest(http.MethodPost,
		srv.URL+"/channels/wecom?encrypt_type=aes&msg_signature="+sig+"&timestamp=2&nonce=n2",
		strings.NewReader(encBody))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("加密合法请求期望 200，实际 %d", resp.StatusCode)
	}
	resp.Body.Close()
	select {
	case msg := <-in:
		if msg.Text != "查询库存" || msg.MessageID != "7" {
			t.Fatalf("加密消息映射错误: %+v", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("未收到加密入站消息")
	}

	// 加密模式：receiveid 不匹配 → 400
	wrongEnc := wecomEncrypt(t, aesKey, xmlMsg, "otherCorp")
	wrongBody := fmt.Sprintf(`<xml><ToUserName>corp1</ToUserName><Encrypt>%s</Encrypt></xml>`, wrongEnc)
	sig = wecomSignature("tok", "3", "n3", wrongEnc)
	req, _ = http.NewRequest(http.MethodPost,
		srv.URL+"/channels/wecom?encrypt_type=aes&msg_signature="+sig+"&timestamp=3&nonce=n3",
		strings.NewReader(wrongBody))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("receiveid 不匹配期望 400，实际 %d", resp.StatusCode)
	}
	resp.Body.Close()
}

// ─────────────────────────── Telegram ───────────────────────────

func TestTelegram_MapMessage(t *testing.T) {
	dm := &tgMessage{MessageID: 1, Text: "hi", Date: 1700000000}
	dm.From.ID = 101
	dm.Chat.ID = 101
	dm.Chat.Type = "private"
	if msg := mapTelegramMessage(dm); msg == nil || msg.Text != "hi" || msg.ChatType != ChatTypeDM {
		t.Fatalf("私聊映射错误: %+v", msg)
	}

	group := &tgMessage{MessageID: 2, Text: "随便聊聊", Date: 1700000000}
	group.From.ID = 101
	group.Chat.ID = -200
	group.Chat.Type = "group"
	if msg := mapTelegramMessage(group); msg != nil {
		t.Fatalf("群聊未 @ 应忽略，实际: %+v", msg)
	}

	at := &tgMessage{MessageID: 3, Text: "@bot 查一下", Date: 1700000000}
	at.From.ID = 101
	at.Chat.ID = -200
	at.Chat.Type = "supergroup"
	at.Entities = []struct {
		Type string `json:"type"`
	}{{Type: "mention"}}
	if msg := mapTelegramMessage(at); msg == nil || msg.ChatType != ChatTypeGroup {
		t.Fatalf("群聊 @ 映射错误: %+v", msg)
	}
}

func TestTelegram_PollAndSend(t *testing.T) {
	var sendPath string
	var sendBody []byte
	updates := `[{"update_id":1,"message":{"message_id":10,"text":"你好",
		"from":{"id":101},"chat":{"id":101,"type":"private"},"date":1700000000}}]`
	once := true
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			if once {
				once = false
				fmt.Fprintf(w, `{"ok":true,"result":%s}`, updates)
				return
			}
			w.Write([]byte(`{"ok":true,"result":[]}`))
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			sendPath = r.URL.Path
			sendBody, _ = io.ReadAll(r.Body)
			w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	ch, err := NewTelegramChannel(TelegramConfig{Token: "tok", APIBase: api.URL})
	if err != nil {
		t.Fatalf("创建渠道失败: %v", err)
	}
	in := make(chan InboundMessage, 8)
	if err := ch.Start(context.Background(), in); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	defer ch.Stop()

	select {
	case msg := <-in:
		if msg.Text != "你好" || msg.ChatID != "101" || msg.Channel != "telegram" {
			t.Fatalf("消息映射错误: %+v", msg)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("长轮询未收到消息")
	}

	if err := ch.Send(context.Background(), OutboundMessage{ChatID: "101", Text: "回复"}); err != nil {
		t.Fatalf("Send 失败: %v", err)
	}
	if sendPath != "/bottok/sendMessage" || !strings.Contains(string(sendBody), `"text":"回复"`) {
		t.Fatalf("sendMessage 调用错误: path=%s body=%s", sendPath, sendBody)
	}
}

// ─────────────────────────── WhatsApp ───────────────────────────

func TestWhatsApp_InboundAuthAndSend(t *testing.T) {
	mux := http.NewServeMux()
	ch, err := NewWhatsAppChannel(WhatsAppConfig{Token: "wt", PhoneNumberID: "PNID"}, mux)
	if err != nil {
		t.Fatalf("创建渠道失败: %v", err)
	}
	in := make(chan InboundMessage, 4)
	if err := ch.Start(context.Background(), in); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()

	payload := `{"object":"whatsapp_business_account","entry":[{"changes":[{"value":{
		"metadata":{"phone_number_id":"PNID"},
		"messages":[{"id":"wamid1","from":"8613800000000","type":"text","text":{"body":"hello"}}]}}]}]}`

	// 无 token → 401
	resp, _ := http.Post(srv.URL+"/channels/whatsapp", "application/json", strings.NewReader(payload))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("无 token 期望 401，实际 %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 合法 token → 200 + 入队
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/channels/whatsapp", strings.NewReader(payload))
	req.Header.Set("Authorization", "Bearer wt")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("合法请求期望 200，实际 %d", resp.StatusCode)
	}
	resp.Body.Close()
	select {
	case msg := <-in:
		if msg.Text != "hello" || msg.ChatID != "8613800000000" || msg.ChatType != ChatTypeDM {
			t.Fatalf("消息映射错误: %+v", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("未收到入站消息")
	}

	// 出站：Cloud API 路径与消息格式
	var sendPath string
	var sendAuth string
	var sendBody []byte
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sendPath = r.URL.Path
		sendAuth = r.Header.Get("Authorization")
		sendBody, _ = io.ReadAll(r.Body)
		w.Write([]byte(`{"messages":[{"id":"wamid2"}]}`))
	}))
	defer cloud.Close()
	ch.cfg.APIBase = cloud.URL
	if err := ch.Send(context.Background(), OutboundMessage{ChatID: "8613800000000", Text: "回复"}); err != nil {
		t.Fatalf("Send 失败: %v", err)
	}
	if sendPath != "/PNID/messages" || sendAuth != "Bearer wt" || !strings.Contains(string(sendBody), `"body":"回复"`) {
		t.Fatalf("Cloud API 调用错误: path=%s auth=%s body=%s", sendPath, sendAuth, sendBody)
	}
}

// ─────────────────────────── QQ ───────────────────────────

func TestQQ_ValidationAndDispatch(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef"
	mux := http.NewServeMux()
	ch, err := NewQQChannel(QQConfig{AppID: "1001", AppSecret: secret}, mux)
	if err != nil {
		t.Fatalf("创建渠道失败: %v", err)
	}
	in := make(chan InboundMessage, 4)
	if err := ch.Start(context.Background(), in); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// op 13 回调验证：返回可被派生公钥验证的 ed25519 签名
	valBody := `{"d":{"plain_token":"ptok","event_ts":"1725442341"},"op":13}`
	resp, _ := http.Post(srv.URL+"/channels/qq", "application/json", strings.NewReader(valBody))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("op13 期望 200，实际 %d", resp.StatusCode)
	}
	var valResp map[string]string
	json.NewDecoder(resp.Body).Decode(&valResp)
	resp.Body.Close()
	sig, err := hex.DecodeString(valResp["signature"])
	if err != nil || valResp["plain_token"] != "ptok" {
		t.Fatalf("op13 应答错误: %v", valResp)
	}
	pub := qqDerivedKey(secret).Public().(ed25519.PublicKey)
	if !ed25519.Verify(pub, []byte("1725442341ptok"), sig) {
		t.Fatal("op13 签名无法用派生公钥验证")
	}

	// 伪造签名头 → 401
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/channels/qq", strings.NewReader(valBody))
	req.Header.Set("x-signature-ed25519", strings.Repeat("ab", 64))
	req.Header.Set("x-signature-timestamp", "1")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("伪造签名期望 401，实际 %d", resp.StatusCode)
	}
	resp.Body.Close()

	// op 0 群 @ 消息（带合法签名）→ ACK op12 + 入队
	groupEvent := `{"op":0,"id":"GROUP_AT_MESSAGE_CREATE:1","s":42,"t":"GROUP_AT_MESSAGE_CREATE",
		"d":{"id":"msg1","content":"@bot 帮我查","timestamp":"2026-01-01T00:00:00+08:00",
		"author":{"member_openid":"m1"},"group_openid":"g1"}}`
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/channels/qq", strings.NewReader(groupEvent))
	priv := qqDerivedKey(secret)
	req.Header.Set("x-signature-ed25519", hex.EncodeToString(ed25519.Sign(priv, append([]byte("1725442341"), []byte(groupEvent)...))))
	req.Header.Set("x-signature-timestamp", "1725442341")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("合法事件期望 200，实际 %d", resp.StatusCode)
	}
	var ack map[string]any
	json.NewDecoder(resp.Body).Decode(&ack)
	resp.Body.Close()
	if ack["op"] != float64(12) || ack["id"] != "GROUP_AT_MESSAGE_CREATE:1" {
		t.Fatalf("ACK 回包错误: %v", ack)
	}
	select {
	case msg := <-in:
		if msg.Text != "@bot 帮我查" || msg.ChatID != "g1" || msg.UserID != "m1" || msg.ChatType != ChatTypeGroup {
			t.Fatalf("消息映射错误: %+v", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("未收到入站消息")
	}

	// 出站：群聊路径 + msg_id 被动回复 + QQBot 鉴权
	var sendPath, sendAuth string
	var sendBody []byte
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/getAppAccessToken") {
			w.Write([]byte(`{"access_token":"at1","expires_in":"7200"}`))
			return
		}
		sendPath = r.URL.Path
		sendAuth = r.Header.Get("Authorization")
		sendBody, _ = io.ReadAll(r.Body)
		w.Write([]byte(`{"id":"ROBOT1.0_x"}`))
	}))
	defer api.Close()
	ch.cfg.APIBase = api.URL
	ch.cfg.TokenURL = api.URL + "/getAppAccessToken"
	if err := ch.Send(context.Background(), OutboundMessage{ChatID: "g1", Text: "结果"}); err != nil {
		t.Fatalf("Send 失败: %v", err)
	}
	if sendPath != "/v2/groups/g1/messages" || sendAuth != "QQBot at1" {
		t.Fatalf("出站调用错误: path=%s auth=%s", sendPath, sendAuth)
	}
	if !strings.Contains(string(sendBody), `"msg_id":"msg1"`) || !strings.Contains(string(sendBody), `"content":"结果"`) {
		t.Fatalf("出站消息体错误: %s", sendBody)
	}
}

// ─────────────────────────── 共享 helper ───────────────────────────

func TestWeComSignature_SortOrder(t *testing.T) {
	// 签名与手工排序结果一致
	parts := []string{"tok", "2", "n", "enc"}
	sort.Strings(parts)
	h := sha1.New()
	h.Write([]byte(strings.Join(parts, "")))
	want := fmt.Sprintf("%x", h.Sum(nil))
	if got := wecomSignature("tok", "2", "n", "enc"); got != want {
		t.Fatalf("wecomSignature = %s, want %s", got, want)
	}
}
