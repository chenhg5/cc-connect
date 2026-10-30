package feishu

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// lookupServer is a fake Open Platform for the display-name lookups that run
// before an inbound message reaches the engine. Either lookup can be made to
// hang: the request is accepted and never answered, which is what a half-dead
// keep-alive connection looks like from the client side.
type lookupServer struct {
	srv       *httptest.Server
	chatCalls atomic.Int32
	userCalls atomic.Int32
}

type lookupBehavior int

const (
	lookupNoName lookupBehavior = iota // success, no name (what Feishu returns for p2p chats)
	lookupNamed                        // success with a name
	lookupError                        // business error, e.g. missing scope
	lookupHang                         // never answers
)

func newLookupServer(t *testing.T, chat, user lookupBehavior) *lookupServer {
	t.Helper()
	ls := &lookupServer{}
	release := make(chan struct{})
	hang := func(r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}
	ls.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success", "expire": 7200, "tenant_access_token": "t-token"})
		case strings.HasPrefix(r.URL.Path, "/open-apis/im/v1/chats/"):
			ls.chatCalls.Add(1)
			switch chat {
			case lookupHang:
				hang(r)
			case lookupNamed:
				writeJSON(t, w, map[string]any{"code": 0, "msg": "success", "data": map[string]any{"name": "Team Room", "chat_mode": "group"}})
			case lookupError:
				writeJSON(t, w, map[string]any{"code": 99991672, "msg": "Access denied"})
			default:
				writeJSON(t, w, map[string]any{"code": 0, "msg": "success", "data": map[string]any{"chat_mode": "p2p"}})
			}
		case strings.HasPrefix(r.URL.Path, "/open-apis/contact/v3/users/"):
			ls.userCalls.Add(1)
			switch user {
			case lookupHang:
				hang(r)
			case lookupError:
				writeJSON(t, w, map[string]any{"code": 99991672, "msg": "Access denied"})
			default:
				writeJSON(t, w, map[string]any{"code": 0, "msg": "success", "data": map[string]any{"user": map[string]any{"name": "Alice"}}})
			}
		default:
			// Reactions, replies, etc. are irrelevant here; answer success so
			// nothing else blocks.
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success"})
		}
	}))
	// Cleanups run LIFO: unblock hung handlers first, then close the server
	// (Close waits for in-flight handlers).
	t.Cleanup(ls.srv.Close)
	t.Cleanup(func() { close(release) })
	return ls
}

func (ls *lookupServer) platform(handler core.MessageHandler) *Platform {
	const appID, appSecret = "cli_lookup", "secret-lookup"
	return &Platform{
		platformName: "feishu",
		domain:       ls.srv.URL,
		appID:        appID,
		appSecret:    appSecret,
		botOpenID:    "ou_bot",
		dedup:        &core.MessageDedup{},
		client: lark.NewClient(appID, appSecret,
			lark.WithOpenBaseUrl(ls.srv.URL),
			lark.WithHttpClient(ls.srv.Client()),
		),
		handler: handler,
	}
}

func setNameLookupTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := nameLookupTimeout
	nameLookupTimeout = d
	t.Cleanup(func() { nameLookupTimeout = old })
}

func p2pTextEvent(messageID, chatID, userID, text string) *larkim.P2MessageReceiveV1 {
	chatType, msgType, senderType := "p2p", "text", "user"
	content := `{"text":` + strconv.Quote(text) + `}`
	createTime := strconv.FormatInt(time.Now().UnixMilli(), 10)
	return &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderId:   &larkim.UserId{OpenId: strPtr(userID)},
				SenderType: &senderType,
			},
			Message: &larkim.EventMessage{
				MessageId:   strPtr(messageID),
				ChatId:      strPtr(chatID),
				ChatType:    &chatType,
				MessageType: &msgType,
				Content:     &content,
				CreateTime:  &createTime,
			},
		},
	}
}

// A p2p message must still reach the engine when the chat-name lookup never
// answers. Before the fix the lookup used context.Background() on an SDK
// client with no request timeout, so the dispatch goroutine blocked forever
// and the message was dropped without any log line.
func TestOnMessage_HangingChatLookupDoesNotSwallowMessage(t *testing.T) {
	setNameLookupTimeout(t, 200*time.Millisecond)
	ls := newLookupServer(t, lookupHang, lookupNamed)
	got := make(chan *core.Message, 4)
	p := ls.platform(func(_ core.Platform, msg *core.Message) { got <- msg })

	if err := p.onMessage(context.Background(), p2pTextEvent("om_new", "oc_dm", "ou_alice", "/new")); err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}
	select {
	case msg := <-got:
		if msg.Content != "/new" {
			t.Fatalf("Content = %q, want /new", msg.Content)
		}
		if msg.ChatName != "oc_dm" {
			t.Fatalf("ChatName = %q, want chat ID fallback when the lookup times out", msg.ChatName)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("message never reached the engine handler: dispatch is blocked on the chat-name lookup (chat lookups=%d)", ls.chatCalls.Load())
	}
}

// Same as above for the user-name lookup, which runs first in dispatch and is
// uncached on the first message from each user (and on every message when the
// app lacks the contact scope).
func TestOnMessage_HangingUserLookupDoesNotSwallowMessage(t *testing.T) {
	setNameLookupTimeout(t, 200*time.Millisecond)
	ls := newLookupServer(t, lookupNoName, lookupHang)
	got := make(chan *core.Message, 4)
	p := ls.platform(func(_ core.Platform, msg *core.Message) { got <- msg })

	if err := p.onMessage(context.Background(), p2pTextEvent("om_hi", "oc_dm", "ou_alice", "hi")); err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}
	select {
	case msg := <-got:
		if msg.UserName != "ou_alice" {
			t.Fatalf("UserName = %q, want open_id fallback when the lookup times out", msg.UserName)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("message never reached the engine handler: dispatch is blocked on the user-name lookup (user lookups=%d)", ls.userCalls.Load())
	}
}

// Feishu returns no name for p2p chats. That answer is final, so it must be
// cached; otherwise every DM pays a network round-trip on the dispatch path
// (and inside the card-callback window) for the lifetime of the process.
// Errors stay uncached so a transient failure can still be retried.
func TestResolveChatName_Caching(t *testing.T) {
	cases := []struct {
		name      string
		behavior  lookupBehavior
		want      string
		wantCalls int32
	}{
		{"p2p chat without name is cached", lookupNoName, "oc_x", 1},
		{"named group is cached", lookupNamed, "Team Room", 1},
		{"error is not cached", lookupError, "oc_x", 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ls := newLookupServer(t, tc.behavior, lookupNamed)
			p := ls.platform(nil)
			for i := 0; i < 3; i++ {
				if got := p.resolveChatName("oc_x"); got != tc.want {
					t.Fatalf("resolveChatName() call %d = %q, want %q", i+1, got, tc.want)
				}
			}
			if n := ls.chatCalls.Load(); n != tc.wantCalls {
				t.Fatalf("chat lookups = %d after 3 resolves, want %d", n, tc.wantCalls)
			}
		})
	}
}

// Feishu expects a card callback answer within ~3s. Button actions that are
// forwarded to the engine (perm:, askq:, cmd:) used to resolve user and chat
// names while building the arguments of a `go` statement — which Go evaluates
// synchronously — so a slow lookup held the callback open. The callback must
// answer immediately and the forwarded message must still arrive once the
// lookup gives up.
func TestOnCardAction_NameLookupDoesNotBlockCallback(t *testing.T) {
	setNameLookupTimeout(t, time.Second)
	for _, action := range []string{"perm:allow", "askq:0", "cmd:/help"} {
		t.Run(action, func(t *testing.T) {
			ls := newLookupServer(t, lookupHang, lookupHang)
			got := make(chan *core.Message, 4)
			p := ls.platform(func(_ core.Platform, msg *core.Message) { got <- msg })

			returned := make(chan error, 1)
			go func() {
				_, err := p.onCardAction(cardActionEvent("ou_alice", action))
				returned <- err
			}()
			select {
			case err := <-returned:
				if err != nil {
					t.Fatalf("onCardAction() error = %v", err)
				}
			case <-time.After(500 * time.Millisecond):
				t.Fatalf("card callback still blocked after 500ms, waiting on name lookups (user lookups=%d, chat lookups=%d)",
					ls.userCalls.Load(), ls.chatCalls.Load())
			}
			select {
			case msg := <-got:
				if msg.UserName != "ou_alice" || msg.ChatName != "oc_test_chat" {
					t.Fatalf("UserName/ChatName = %q/%q, want ID fallbacks", msg.UserName, msg.ChatName)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("card action never reached the engine handler")
			}
		})
	}
}

// A lookup request that fails (dead connection, timeout) is logged at Warn, so
// a degraded Open API connection is visible at the default log level. Business
// errors such as a missing contact scope stay at Debug: they are not cached and
// would otherwise log on every message.
func TestNameLookup_FailureLogLevels(t *testing.T) {
	cases := []struct {
		name       string
		chat, user lookupBehavior
		wantWarn   []string // substrings that must appear on one WARN line
		quiet      []string // messages that must not appear at Info level or above
	}{
		{"hung chat lookup warns", lookupHang, lookupNamed, []string{"resolve chat name failed", "chat_id=oc_x"}, nil},
		{"hung user lookup warns", lookupNoName, lookupHang, []string{"resolve user name failed", "open_id=ou_alice"}, nil},
		{"business errors stay at debug", lookupError, lookupError, nil, []string{"resolve chat name", "resolve user name"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setNameLookupTimeout(t, 100*time.Millisecond)
			var buf bytes.Buffer
			orig := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
			t.Cleanup(func() { slog.SetDefault(orig) })

			p := newLookupServer(t, tc.chat, tc.user).platform(nil)
			p.resolveUserName("ou_alice")
			p.resolveChatName("oc_x")

			logs := buf.String()
			if len(tc.wantWarn) > 0 {
				found := false
				for _, line := range strings.Split(logs, "\n") {
					ok := strings.Contains(line, "level=WARN")
					for _, s := range tc.wantWarn {
						ok = ok && strings.Contains(line, s)
					}
					found = found || ok
				}
				if !found {
					t.Fatalf("no WARN line containing %q; logs:\n%s", tc.wantWarn, logs)
				}
			}
			for _, s := range tc.quiet {
				if strings.Contains(logs, s) {
					t.Fatalf("%q logged at Info level or above; logs:\n%s", s, logs)
				}
			}
		})
	}
}
