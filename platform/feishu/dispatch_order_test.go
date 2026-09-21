package feishu

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// Reproduce a slow forwarded-message lookup followed immediately by text.
// The engine rejects the forward as stale if the newer text reaches it first.
func TestOnMessage_SlowMergeForwardPreservesDispatchOrder(t *testing.T) {
	lookupStarted := make(chan struct{})
	releaseLookup := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseLookup) }) }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(t, w, map[string]any{"code": 0, "tenant_access_token": "test-token", "expire": 7200})
		case "/open-apis/im/v1/messages/om_forward":
			close(lookupStarted)
			<-releaseLookup
			writeJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"items": []any{
				map[string]any{"message_id": "om_child", "upper_message_id": "om_forward", "msg_type": "text",
					"body": map[string]any{"content": `{"text":"forwarded context"}`}},
			}}})
		default:
			t.Errorf("unexpected API request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	defer release()

	messages := make(chan *core.Message, 8)
	p := &Platform{
		platformName: "feishu", dedup: &core.MessageDedup{},
		client:  lark.NewClient("dispatch-order", "test-secret", lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client())),
		handler: func(_ core.Platform, msg *core.Message) { messages <- msg },
	}
	p.userNameCache.Store("ou_sender", "Sender")
	p.chatNameCache.Store("oc_first", "First chat")
	p.chatNameCache.Store("oc_other", "Other chat")
	baseTime := time.Now().Add(time.Second).UnixMilli()
	send := func(id, chat, kind, content string, timestamp int64) {
		t.Helper()
		if err := p.onMessage(context.Background(), dispatchOrderEvent(id, chat, kind, content, timestamp)); err != nil {
			t.Fatal(err)
		}
	}
	send("om_forward", "oc_first", "merge_forward", "", baseTime)
	select {
	case <-lookupStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("forwarded-message lookup did not start")
	}
	send("om_text", "oc_first", "text", `{"text":"summarize it"}`, baseTime+418)
	// A different session must still be delivered while the lookup is blocked.
	send("om_other", "oc_other", "text", `{"text":"independent task"}`, baseTime+500)
	if msg := awaitGroupHistoryMessage(t, messages); msg.MessageID != "om_other" {
		t.Fatalf("later text overtook the pending forward: got %s before lookup completed", msg.MessageID)
	}
	select {
	case msg := <-messages:
		t.Fatalf("later message %s overtook the pending forward", msg.MessageID)
	case <-time.After(100 * time.Millisecond):
	}
	// Early returns must release the lane, including a recall received while
	// waiting for the forward and malformed/unsupported messages.
	send("om_recalled", "oc_first", "text", `{"text":"withdrawn"}`, baseTime+450)
	p.markMessageRecalled("om_recalled")
	send("om_bad", "oc_first", "text", "invalid JSON", baseTime+460)
	send("om_unsupported", "oc_first", "unknown", "{}", baseTime+470)
	release()
	forward := awaitGroupHistoryMessage(t, messages)
	if forward.MessageID != "om_forward" || !strings.Contains(forward.Content, "forwarded context") {
		t.Fatalf("expected forwarded content first, got %+v", forward)
	}
	text := awaitGroupHistoryMessage(t, messages)
	if text.MessageID != "om_text" || text.Content != "summarize it" {
		t.Fatalf("expected follow-up text second, got %+v", text)
	}
	if forward.UserMessageTimeMs != baseTime || text.UserMessageTimeMs != baseTime+418 {
		t.Fatal("dispatch must preserve original message timestamps")
	}
	// Existing message-ID dedup must still work after ordered dispatch.
	send("om_forward", "oc_first", "merge_forward", "", baseTime)
	send("om_final", "oc_first", "text", `{"text":"thanks"}`, baseTime+600)
	if msg := awaitGroupHistoryMessage(t, messages); msg.MessageID != "om_final" {
		t.Fatalf("unexpected duplicate dispatch: %s", msg.MessageID)
	}
}

func dispatchOrderEvent(id, chat, kind, content string, timestamp int64) *larkim.P2MessageReceiveV1 {
	return &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{SenderId: &larkim.UserId{OpenId: strPtr("ou_sender")}, SenderType: strPtr("user")},
			Message: &larkim.EventMessage{
				MessageId: strPtr(id), ChatId: strPtr(chat), ChatType: strPtr("p2p"),
				MessageType: strPtr(kind), Content: strPtr(content), CreateTime: strPtr(strconv.FormatInt(timestamp, 10)),
			},
		},
	}
}
