package max

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// MAX delivers callbacks from private dialogs with recipient.chat_id = 0.
// Sending the reply to chat_id=0 is rejected with
// 403 {"code":"chat.denied","message":"Invalid chatId: 0"}, so the platform
// must fall back to POST /messages?user_id=<id>.
func TestPostMessageFallsBackToUserID(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		if r.URL.Query().Get("chat_id") == "0" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"code":"chat.denied","message":"Invalid chatId: 0"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]any{}})
	}))
	defer server.Close()

	p := newTestPlatform(t, server.URL)

	if err := p.sendText(context.Background(),
		replyContext{chatID: "0", userID: "999001"}, "hi", nil); err != nil {
		t.Fatalf("sendText with chat_id=0: %v", err)
	}
	if gotQuery != "user_id=999001" {
		t.Fatalf("expected user_id fallback, got query %q", gotQuery)
	}

	// A real chat id must still win over the fallback.
	if err := p.sendText(context.Background(),
		replyContext{chatID: "555000", userID: "999001"}, "hi", nil); err != nil {
		t.Fatalf("sendText with real chat: %v", err)
	}
	if gotQuery != "chat_id=555000" {
		t.Fatalf("expected chat_id to win, got query %q", gotQuery)
	}

	// Neither target available is a caller error, not a silent 403.
	err := p.sendText(context.Background(), replyContext{}, "hi", nil)
	if err == nil {
		t.Fatal("expected an error when the reply context carries no target")
	}
}

// A button press must land in the same session as the conversation. MAX omits
// recipient.chat_id in the callback, so the platform reuses the chat_id learned
// from the user's earlier messages instead of opening "max:0:<user>".
func TestCallbackReusesLearnedChatID(t *testing.T) {
	p := newTestPlatform(t, "http://127.0.0.1:0")

	var mu sync.Mutex
	var keys []string
	p.handler = func(_ core.Platform, msg *core.Message) {
		mu.Lock()
		keys = append(keys, msg.SessionKey)
		mu.Unlock()
	}

	const (
		userID = int64(999001)
		chatID = int64(555000)
	)

	p.handleMessage(context.Background(), &maxMessage{
		Sender:    maxUser{UserID: userID, Name: "Tester"},
		Recipient: maxRecipient{ChatID: chatID},
		Timestamp: time.Now().UnixMilli(), // otherwise IsOldMessage drops it
		Body:      maxBody{Mid: "mid.1", Text: "hello"},
	})

	p.handleCallback(context.Background(), &maxCallback{
		CallbackID: "cb.1",
		Payload:    "perm:allow_all",
		User:       maxUser{UserID: userID, Name: "Tester"},
		Message:    maxMessage{Body: maxBody{Mid: "mid.2"}}, // recipient.chat_id = 0
	})

	want := "max:" + strconv.FormatInt(chatID, 10) + ":" + strconv.FormatInt(userID, 10)
	mu.Lock()
	defer mu.Unlock()
	if len(keys) != 2 {
		t.Fatalf("expected 2 routed messages, got %d: %v", len(keys), keys)
	}
	if keys[1] != want {
		t.Fatalf("callback landed in %q, want %q (same session as the dialog)", keys[1], want)
	}
}

// Restoring a reply context from a "max:0:<user>" session key must stay usable:
// the chat is gone, but the user is still reachable.
func TestReconstructReplyCtxKeepsUserID(t *testing.T) {
	p := newTestPlatform(t, "http://127.0.0.1:0")

	got, err := p.ReconstructReplyCtx("max:0:999001")
	if err != nil {
		t.Fatalf("ReconstructReplyCtx: %v", err)
	}
	rctx, ok := got.(replyContext)
	if !ok {
		t.Fatalf("unexpected type %T", got)
	}
	if rctx.userID != "999001" || rctx.hasChat() {
		t.Fatalf("got %+v, want userID=999001 and no usable chat", rctx)
	}
}

// Permission buttons must reach the engine as the words it matches on.
// Raw "perm:allow_all" matched nothing, so "Allow all" silently did nothing
// while "Allow" worked by accident (the engine tokenises and finds "allow").
func TestCallbackTranslatesPermissionPayloads(t *testing.T) {
	// Payloads come from the constants (single source of truth with the sender
	// side), but the expected words are spelled out on purpose: taking them from
	// permPayloads would make the test agree with itself instead of pinning the
	// exact wording the engine matches on.
	cases := []struct {
		payload string
		want    string
		perm    bool
	}{
		{payloadAllow, "allow", true},
		{payloadAllowAll, "allow all", true},
		{payloadDeny, "deny", true},
		{"cb_play", "cb_play", false}, // ordinary buttons pass through untouched
	}

	for _, tc := range cases {
		p := newTestPlatform(t, "http://127.0.0.1:0")
		var got *core.Message
		p.handler = func(_ core.Platform, m *core.Message) { got = m }

		p.handleCallback(context.Background(), &maxCallback{
			CallbackID: "cb." + tc.payload,
			Payload:    tc.payload,
			User:       maxUser{UserID: 999001, Name: "Tester"},
			Message:    maxMessage{Body: maxBody{Mid: ""}}, // no mid → no card edit attempt
		})

		if got == nil {
			t.Fatalf("%s: message was not routed", tc.payload)
		}
		if got.Content != tc.want {
			t.Errorf("%s: content = %q, want %q", tc.payload, got.Content, tc.want)
		}
		if got.IsPermissionResponse != tc.perm {
			t.Errorf("%s: IsPermissionResponse = %v, want %v", tc.payload, got.IsPermissionResponse, tc.perm)
		}
	}
}
