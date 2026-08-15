package feishu

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/timmyagentic/cc-connect-next/core"
)

func TestMarkThreadSessionActiveReportsFirstActivation(t *testing.T) {
	p := &Platform{threadIsolation: true}
	const key = "feishu:oc_chat:root:om_root"
	if !p.markThreadSessionActive(key) {
		t.Fatal("first activation must request thread bootstrap")
	}
	if p.markThreadSessionActive(key) {
		t.Fatal("concurrent activation must not duplicate an in-flight bootstrap")
	}
	p.finishThreadBootstrap(key, false)
	if !p.markThreadSessionActive(key) {
		t.Fatal("failed bootstrap must become retryable")
	}
	p.finishThreadBootstrap(key, true)
	if p.markThreadSessionActive(key) {
		t.Fatal("successful bootstrap must not repeat")
	}
}

func TestOnMessageThreadBootstrapRetriesAfterRootFetchFailure(t *testing.T) {
	const (
		appID      = "cli_thread_bootstrap_retry"
		appSecret  = "secret-thread-bootstrap-retry"
		botOpenID  = "ou_bot"
		userOpenID = "ou_user"
		chatID     = "oc_chat"
		rootMsgID  = "om_root"
		childMsgID = "om_child"
	)

	rootCalls := 0
	got := make(chan *core.Message, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(t, w, map[string]any{
				"code": 0, "msg": "success", "expire": 7200,
				"tenant_access_token": "tenant-token",
			})
		case r.URL.Path == "/open-apis/im/v1/messages/"+rootMsgID:
			rootCalls++
			if rootCalls == 1 {
				writeJSON(t, w, map[string]any{"code": 19001, "msg": "temporary failure"})
				return
			}
			writeJSON(t, w, map[string]any{
				"code": 0, "msg": "success",
				"data": map[string]any{"items": []map[string]any{{
					"msg_type": "text", "parent_id": "",
					"sender": map[string]any{"id": userOpenID, "sender_type": "user"},
					"body":   map[string]any{"content": `{"text":"可恢复的根消息"}`},
				}}},
			})
		case r.URL.Path == "/open-apis/im/v1/messages/"+childMsgID:
			writeJSON(t, w, map[string]any{
				"code": 0, "msg": "success",
				"data": map[string]any{"items": []map[string]any{{
					"msg_type": "text", "parent_id": rootMsgID,
					"sender": map[string]any{"id": userOpenID, "sender_type": "user"},
					"body":   map[string]any{"content": `{"text":"不完整的子消息"}`},
				}}},
			})
		case strings.HasPrefix(r.URL.Path, "/open-apis/contact/v3/users/"):
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success"})
		case strings.HasPrefix(r.URL.Path, "/open-apis/im/v1/chats/"):
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	p := &Platform{
		platformName:    "feishu",
		domain:          srv.URL,
		appID:           appID,
		appSecret:       appSecret,
		botOpenID:       botOpenID,
		threadIsolation: true,
		dedup:           &core.MessageDedup{},
		client: lark.NewClient(appID, appSecret,
			lark.WithOpenBaseUrl(srv.URL),
			lark.WithHttpClient(srv.Client()),
		),
		handler: func(_ core.Platform, msg *core.Message) { got <- msg },
	}

	makeEvent := func(messageID string) *larkim.P2MessageReceiveV1 {
		chatType := "group"
		senderType := "user"
		msgType := "text"
		content := `{"text":"@_user_1 继续"}`
		createTime := strconv.FormatInt(time.Now().Add(time.Second).UnixMilli(), 10)
		threadID := "omt_thread"
		return &larkim.P2MessageReceiveV1{Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderId: &larkim.UserId{OpenId: strPtr(userOpenID)}, SenderType: &senderType,
			},
			Message: &larkim.EventMessage{
				MessageId: strPtr(messageID), ParentId: strPtr(childMsgID), RootId: strPtr(rootMsgID), ThreadId: &threadID,
				ChatId: strPtr(chatID), ChatType: &chatType, MessageType: &msgType,
				Content: &content, CreateTime: &createTime,
				Mentions: []*larkim.MentionEvent{{
					Key: strPtr("@_user_1"), Id: &larkim.UserId{OpenId: strPtr(botOpenID)}, Name: strPtr("Bot"),
				}},
			},
		}}
	}

	if err := p.onMessage(context.Background(), makeEvent("om_trigger_1")); err != nil {
		t.Fatalf("first onMessage() error = %v", err)
	}
	select {
	case first := <-got:
		if !strings.Contains(first.ExtraContent, "不完整的子消息") || strings.Contains(first.ExtraContent, "可恢复的根消息") {
			t.Fatalf("first bootstrap context = %q, want only the partial child", first.ExtraContent)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first dispatch")
	}

	if err := p.onMessage(context.Background(), makeEvent("om_trigger_2")); err != nil {
		t.Fatalf("second onMessage() error = %v", err)
	}
	select {
	case second := <-got:
		if !strings.Contains(second.ExtraContent, "可恢复的根消息") || !strings.Contains(second.ExtraContent, "不完整的子消息") {
			t.Fatalf("retried bootstrap context = %q", second.ExtraContent)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for retried dispatch")
	}
	if rootCalls != 2 {
		t.Fatalf("root fetch calls = %d, want retry", rootCalls)
	}
}

func TestDispatchMessageCompletesBootstrapOnlyAfterCoreDispatch(t *testing.T) {
	const (
		appID      = "cli_thread_bootstrap_dispatch"
		appSecret  = "secret-thread-bootstrap-dispatch"
		rootMsgID  = "om_root"
		sessionKey = "feishu:oc_chat:root:" + rootMsgID
		fileKey    = "file_current"
		fileMsgID  = "om_file"
		imageKey   = "image_current"
		imageMsgID = "om_image"
	)

	var rootCalls atomic.Int32
	got := make(chan *core.Message, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(t, w, map[string]any{
				"code": 0, "msg": "success", "expire": 7200,
				"tenant_access_token": "tenant-token",
			})
		case "/open-apis/im/v1/messages/" + rootMsgID:
			rootCalls.Add(1)
			writeJSON(t, w, map[string]any{
				"code": 0, "msg": "success",
				"data": map[string]any{"items": []map[string]any{{
					"msg_type": "text", "parent_id": "",
					"sender": map[string]any{"id": "", "sender_type": "user"},
					"body":   map[string]any{"content": `{"text":"必须送达的根上下文"}`},
				}}},
			})
		case "/open-apis/im/v1/messages/" + fileMsgID + "/resources/" + fileKey:
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte("file payload"))
		case "/open-apis/im/v1/messages/" + imageMsgID + "/resources/" + imageKey:
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	p := &Platform{
		platformName:    "feishu",
		appID:           appID,
		appSecret:       appSecret,
		threadIsolation: true,
		client: lark.NewClient(appID, appSecret,
			lark.WithOpenBaseUrl(srv.URL),
			lark.WithHttpClient(srv.Client()),
		),
		handler: func(_ core.Platform, msg *core.Message) { got <- msg },
	}

	if !p.markThreadSessionActive(sessionKey) {
		t.Fatal("first bootstrap reservation was not acquired")
	}
	p.dispatchMessage(context.Background(), "text", `{`, nil, "om_bad", sessionKey, "", "",
		replyContext{sessionKey: sessionKey, bootstrapThread: true}, rootMsgID, 0)
	select {
	case msg := <-got:
		t.Fatalf("malformed trigger unexpectedly dispatched: %q", msg.MessageID)
	default:
	}

	if !p.markThreadSessionActive(sessionKey) {
		t.Fatal("bootstrap was marked complete before the malformed trigger reached Core")
	}
	p.dispatchMessage(context.Background(), "text", `{"text":"继续"}`, nil, "om_valid", sessionKey, "", "",
		replyContext{sessionKey: sessionKey, bootstrapThread: true}, rootMsgID, 0)
	select {
	case msg := <-got:
		if msg.MessageID != "om_valid" || !strings.Contains(msg.ExtraContent, "必须送达的根上下文") {
			t.Fatalf("retried dispatch = (%q, %q)", msg.MessageID, msg.ExtraContent)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for valid retried dispatch")
	}
	if p.markThreadSessionActive(sessionKey) {
		t.Fatal("successful Core dispatch must complete bootstrap")
	}

	fileSessionKey := "feishu:oc_chat:root:om_file_topic"
	if !p.markThreadSessionActive(fileSessionKey) {
		t.Fatal("file-turn bootstrap reservation was not acquired")
	}
	p.dispatchMessage(context.Background(), "file", `{"file_key":"`+fileKey+`","file_name":"input.txt"}`, nil,
		fileMsgID, fileSessionKey, "", "", replyContext{sessionKey: fileSessionKey, bootstrapThread: true}, rootMsgID, 0)
	select {
	case msg := <-got:
		if !strings.Contains(msg.ExtraContent, "必须送达的根上下文") || len(msg.Files) != 1 || msg.Files[0].FileName != "input.txt" {
			t.Fatalf("file bootstrap dispatch = extra %q files %+v", msg.ExtraContent, msg.Files)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for file bootstrap dispatch")
	}
	if rootCalls.Load() != 3 {
		t.Fatalf("root fetch calls = %d, want 3", rootCalls.Load())
	}
	if p.markThreadSessionActive(fileSessionKey) {
		t.Fatal("file turn with injected root context must complete bootstrap")
	}

	imageSessionKey := "feishu:oc_chat:root:om_image_topic"
	bootstrap, queued, wait, done := p.prepareThreadBootstrapDispatch(imageSessionKey)
	if !bootstrap || queued || wait != nil || done == nil {
		t.Fatalf("image bootstrap ticket = (%v, %v, %v, %v)", bootstrap, queued, wait != nil, done != nil)
	}
	p.dispatchMessage(context.Background(), "image", `{"image_key":"`+imageKey+`"}`, nil,
		imageMsgID, imageSessionKey, "", "", replyContext{
			sessionKey: imageSessionKey, bootstrapThread: bootstrap, bootstrapWait: wait, bootstrapDone: done,
		}, "", 0)
	select {
	case msg := <-got:
		if msg.MessageID != imageMsgID || len(msg.Images) != 1 {
			t.Fatalf("root image dispatch = id %q images %d", msg.MessageID, len(msg.Images))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("unparented bootstrap image was buffered instead of dispatched")
	}
	select {
	case <-done:
	default:
		t.Fatal("image bootstrap FIFO ticket was not released")
	}
	if p.markThreadSessionActive(imageSessionKey) {
		t.Fatal("dispatched root image must complete bootstrap")
	}
}

func TestOnMessageThreadBootstrapQueuesConcurrentFollowUpsInOrder(t *testing.T) {
	const (
		appID      = "cli_thread_bootstrap_order"
		appSecret  = "secret-thread-bootstrap-order"
		botOpenID  = "ou_bot"
		userOpenID = "ou_user"
		chatID     = "oc_chat"
		rootMsgID  = "om_root"
	)

	rootStarted := make(chan struct{}, 1)
	allowRoot := make(chan struct{})
	var allowRootOnce sync.Once
	releaseRoot := func() { allowRootOnce.Do(func() { close(allowRoot) }) }
	var rootCalls atomic.Int32
	got := make(chan *core.Message, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(t, w, map[string]any{
				"code": 0, "msg": "success", "expire": 7200,
				"tenant_access_token": "tenant-token",
			})
		case r.URL.Path == "/open-apis/im/v1/messages/"+rootMsgID:
			rootCalls.Add(1)
			rootStarted <- struct{}{}
			<-allowRoot
			writeJSON(t, w, map[string]any{
				"code": 0, "msg": "success",
				"data": map[string]any{"items": []map[string]any{{
					"msg_type": "text", "parent_id": "",
					"sender": map[string]any{"id": userOpenID, "sender_type": "user"},
					"body":   map[string]any{"content": `{"text":"按顺序恢复的根消息"}`},
				}}},
			})
		case strings.HasPrefix(r.URL.Path, "/open-apis/contact/v3/users/"):
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success"})
		case strings.HasPrefix(r.URL.Path, "/open-apis/im/v1/chats/"):
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	defer releaseRoot()

	p := &Platform{
		platformName:    "feishu",
		domain:          srv.URL,
		appID:           appID,
		appSecret:       appSecret,
		botOpenID:       botOpenID,
		threadIsolation: true,
		dedup:           &core.MessageDedup{},
		client: lark.NewClient(appID, appSecret,
			lark.WithOpenBaseUrl(srv.URL),
			lark.WithHttpClient(srv.Client()),
		),
		handler: func(_ core.Platform, msg *core.Message) { got <- msg },
	}

	makeEvent := func(messageID, text string, offset time.Duration) *larkim.P2MessageReceiveV1 {
		chatType := "group"
		senderType := "user"
		msgType := "text"
		content := `{"text":"@_user_1 ` + text + `"}`
		createTime := strconv.FormatInt(time.Now().Add(offset).UnixMilli(), 10)
		threadID := "omt_thread"
		return &larkim.P2MessageReceiveV1{Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderId: &larkim.UserId{OpenId: strPtr(userOpenID)}, SenderType: &senderType,
			},
			Message: &larkim.EventMessage{
				MessageId: strPtr(messageID), RootId: strPtr(rootMsgID), ThreadId: &threadID,
				ChatId: strPtr(chatID), ChatType: &chatType, MessageType: &msgType,
				Content: &content, CreateTime: &createTime,
				Mentions: []*larkim.MentionEvent{{
					Key: strPtr("@_user_1"), Id: &larkim.UserId{OpenId: strPtr(botOpenID)}, Name: strPtr("Bot"),
				}},
			},
		}}
	}

	if err := p.onMessage(context.Background(), makeEvent("om_trigger_1", "第一条", time.Second)); err != nil {
		t.Fatalf("first onMessage() error = %v", err)
	}
	select {
	case <-rootStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for root fetch to start")
	}

	if err := p.onMessage(context.Background(), makeEvent("om_trigger_2", "第二条", 2*time.Second)); err != nil {
		t.Fatalf("second onMessage() error = %v", err)
	}
	select {
	case early := <-got:
		t.Fatalf("message %q overtook the in-flight bootstrap", early.MessageID)
	case <-time.After(150 * time.Millisecond):
	}

	releaseRoot()
	var first, second *core.Message
	select {
	case first = <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first ordered dispatch")
	}
	select {
	case second = <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second ordered dispatch")
	}
	if first.MessageID != "om_trigger_1" || second.MessageID != "om_trigger_2" {
		t.Fatalf("dispatch order = [%q, %q]", first.MessageID, second.MessageID)
	}
	if !strings.Contains(first.ExtraContent, "按顺序恢复的根消息") {
		t.Fatalf("first ExtraContent = %q, want recovered root", first.ExtraContent)
	}
	if second.ExtraContent != "" {
		t.Fatalf("second ExtraContent = %q, root must not be reinjected", second.ExtraContent)
	}
	if rootCalls.Load() != 1 {
		t.Fatalf("root fetch calls = %d, want 1", rootCalls.Load())
	}
}

func TestOnMessageThreadIsolationBootstrapsExistingRoot(t *testing.T) {
	const (
		appID        = "cli_thread_bootstrap"
		appSecret    = "secret-thread-bootstrap"
		botOpenID    = "ou_bot"
		userOpenID   = "ou_user"
		chatID       = "oc_chat"
		rootMsgID    = "om_root"
		triggerMsgID = "om_trigger"
	)

	got := make(chan *core.Message, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(t, w, map[string]any{
				"code": 0, "msg": "success", "expire": 7200,
				"tenant_access_token": "tenant-token",
			})
		case r.URL.Path == "/open-apis/im/v1/messages/"+rootMsgID:
			writeJSON(t, w, map[string]any{
				"code": 0, "msg": "success",
				"data": map[string]any{"items": []map[string]any{{
					"msg_type": "post", "parent_id": "",
					"sender": map[string]any{"id": "ou_root_author", "sender_type": "user"},
					"body":   map[string]any{"content": `{"title":"环境信息","content":[[{"tag":"text","text":"根消息上下文"}]]}`},
				}}},
			})
		case strings.HasPrefix(r.URL.Path, "/open-apis/contact/v3/users/"):
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success"})
		case strings.HasPrefix(r.URL.Path, "/open-apis/im/v1/chats/"):
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	p := &Platform{
		platformName:    "feishu",
		domain:          srv.URL,
		appID:           appID,
		appSecret:       appSecret,
		botOpenID:       botOpenID,
		threadIsolation: true,
		dedup:           &core.MessageDedup{},
		client: lark.NewClient(appID, appSecret,
			lark.WithOpenBaseUrl(srv.URL),
			lark.WithHttpClient(srv.Client()),
		),
		handler: func(_ core.Platform, msg *core.Message) { got <- msg },
	}

	chatType := "group"
	senderType := "user"
	msgType := "text"
	content := `{"text":"@_user_1 看看这个"}`
	createTime := strconv.FormatInt(time.Now().Add(time.Second).UnixMilli(), 10)
	threadID := "omt_thread"
	if err := p.onMessage(context.Background(), &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderId:   &larkim.UserId{OpenId: strPtr(userOpenID)},
				SenderType: &senderType,
			},
			Message: &larkim.EventMessage{
				MessageId: strPtr(triggerMsgID), RootId: strPtr(rootMsgID), ThreadId: &threadID,
				ChatId: strPtr(chatID), ChatType: &chatType, MessageType: &msgType,
				Content: &content, CreateTime: &createTime,
				Mentions: []*larkim.MentionEvent{{
					Key: strPtr("@_user_1"), Id: &larkim.UserId{OpenId: strPtr(botOpenID)}, Name: strPtr("Bot"),
				}},
			},
		},
	}); err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}

	select {
	case msg := <-got:
		if msg.SessionKey != "feishu:"+chatID+":root:"+rootMsgID {
			t.Fatalf("SessionKey = %q, want root-scoped session", msg.SessionKey)
		}
		if msg.Content != "看看这个" {
			t.Fatalf("Content = %q, want trigger text", msg.Content)
		}
		if !strings.Contains(msg.ExtraContent, "根消息上下文") {
			t.Fatalf("ExtraContent = %q, want root context", msg.ExtraContent)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for bootstrapped message")
	}
}
