package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
)

type receiptRequest struct{ method, path, emoji string }

func receiptTestPlatform(t *testing.T, opts map[string]any, serve func(http.ResponseWriter, *http.Request)) *Platform {
	t.Helper()
	opts["app_id"] = "receipt-" + t.Name()
	opts["app_secret"] = "test-secret"
	opts["enable_feishu_card"] = false
	opts["allow_from"] = "*"
	platform, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	p := platform.(*Platform)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/auth/") {
			if _, err := fmt.Fprint(w, `{"code":0,"expire":7200,"tenant_access_token":"test-token"}`); err != nil {
				t.Errorf("write fixture response: %v", err)
			}
			return
		}
		serve(w, r)
	}))
	t.Cleanup(srv.Close)
	p.client = lark.NewClient(opts["app_id"].(string), "test-secret", lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client()))
	return p
}

func receiptMessage() *core.Message {
	return &core.Message{MessageID: "om_receipt", Content: "hello", UserID: "ou_alice", SessionKey: "feishu:oc_main:ou_alice", ReplyCtx: replyContext{messageID: "om_receipt", chatID: "oc_main"}}
}

func awaitReceiptRequest(t *testing.T, requests <-chan receiptRequest) receiptRequest {
	t.Helper()
	select {
	case req := <-requests:
		return req
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for reaction request")
		return receiptRequest{}
	}
}

// This test also runs unchanged against pre-feature main: the prior history
// callback runs, but no receipt request is sent, so the test fails.
func TestReceiptAcknowledgement_AcceptedMessagePersistsThroughTypingCleanup(t *testing.T) {
	for _, processingEmoji := range []string{"OnIt", "Get", "none"} {
		t.Run(processingEmoji, func(t *testing.T) {
			requests := make(chan receiptRequest, 10)
			var mu sync.Mutex
			active := map[string]string{}
			p := receiptTestPlatform(t, map[string]any{"ack_emoji": "Get", "reaction_emoji": processingEmoji}, func(w http.ResponseWriter, r *http.Request) {
				req := receiptRequest{method: r.Method, path: r.URL.Path}
				mu.Lock()
				switch r.Method {
				case http.MethodPost:
					var body struct {
						ReactionType struct {
							EmojiType string `json:"emoji_type"`
						} `json:"reaction_type"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					req.emoji = body.ReactionType.EmojiType
					active[req.emoji] = "reaction-" + req.emoji
					if _, err := fmt.Fprintf(w, `{"code":0,"data":{"reaction_id":%q}}`, "reaction-"+req.emoji); err != nil {
						t.Errorf("write fixture response: %v", err)
					}
				case http.MethodDelete:
					for emoji, id := range active {
						if strings.HasSuffix(r.URL.Path, "/"+id) {
							delete(active, emoji)
						}
					}
					if _, err := fmt.Fprint(w, `{"code":0}`); err != nil {
						t.Errorf("write fixture response: %v", err)
					}
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				mu.Unlock()
				requests <- req
			})
			var got *core.Message
			p.handler = func(_ core.Platform, m *core.Message) { got = m }
			msg := receiptMessage()
			var historyConsumed atomic.Int32
			msg.OnAccepted = func() { historyConsumed.Add(1) }
			p.dispatchCoreMessage(msg)
			select {
			case req := <-requests:
				t.Fatalf("ack before engine acceptance: %+v", req)
			default:
			}
			got.OnAccepted()
			req := awaitReceiptRequest(t, requests)
			if req.method != http.MethodPost || req.emoji != "Get" || req.path != "/open-apis/im/v1/messages/om_receipt/reactions" {
				t.Fatalf("receipt request = %+v", req)
			}
			got.OnAccepted() // callback composition must be idempotent
			if historyConsumed.Load() != 1 {
				t.Fatalf("history callback count = %d", historyConsumed.Load())
			}
			stop := p.StartTyping(context.Background(), got.ReplyCtx)
			if processingEmoji == "OnIt" {
				req = awaitReceiptRequest(t, requests)
				if req.emoji != "OnIt" {
					t.Fatalf("processing request = %+v", req)
				}
			}
			stop()
			if processingEmoji == "OnIt" {
				req = awaitReceiptRequest(t, requests)
				if req.method != http.MethodDelete || !strings.HasSuffix(req.path, "/reaction-OnIt") {
					t.Fatalf("cleanup = %+v", req)
				}
			}
			mu.Lock()
			if len(active) != 1 || active["Get"] == "" {
				t.Errorf("receipt removed or processing reaction leaked: %v", active)
			}
			mu.Unlock()
			select {
			case req := <-requests:
				t.Errorf("unexpected duplicate reaction: %+v", req)
			default:
			}
		})
	}
}

func TestReceiptAcknowledgement_DisabledAndSyntheticMessages(t *testing.T) {
	for _, tc := range []struct {
		name      string
		opts      map[string]any
		synthetic bool
	}{
		{"omitted", map[string]any{}, false}, {"empty", map[string]any{"ack_emoji": ""}, false},
		{"none", map[string]any{"ack_emoji": "none"}, false}, {"case and space", map[string]any{"ack_emoji": " NONE "}, false},
		{"synthetic", map[string]any{"ack_emoji": "Get"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := receiptTestPlatform(t, tc.opts, func(w http.ResponseWriter, r *http.Request) { t.Errorf("unexpected API call: %s", r.URL.Path) })
			p.handler = func(_ core.Platform, m *core.Message) {}
			msg := receiptMessage()
			if tc.synthetic {
				msg.MessageID = ""
			}
			p.dispatchCoreMessage(msg)
			if msg.OnAccepted != nil {
				t.Fatal("receipt callback attached to disabled/synthetic message")
			}
		})
	}
}

func TestReceiptAcknowledgement_SlowOrFailedAPIIsBestEffort(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			started := make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			p := receiptTestPlatform(t, map[string]any{"ack_emoji": "Get"}, func(w http.ResponseWriter, r *http.Request) {
				close(started)
				<-release
				if fail {
					if _, err := fmt.Fprint(w, `{"code":99991672,"msg":"permission denied"}`); err != nil {
						t.Errorf("write fixture response: %v", err)
					}
				} else {
					if _, err := fmt.Fprint(w, `{"code":0,"data":{"reaction_id":"receipt"}}`); err != nil {
						t.Errorf("write fixture response: %v", err)
					}
				}
			})
			var accepted atomic.Int32
			p.handler = func(_ core.Platform, m *core.Message) { m.OnAccepted(); accepted.Add(1) }
			done := make(chan struct{})
			go func() { p.dispatchCoreMessage(receiptMessage()); close(done) }()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("receipt request not started")
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("receipt HTTP request blocked engine admission")
			}
			if accepted.Load() != 1 {
				t.Fatal("normal message processing did not continue")
			}
			unblock()
		})
	}
}

func TestReceiptAcknowledgement_OnlyAdmittedInboundEvents(t *testing.T) {
	requests := make(chan receiptRequest, 10)
	p := receiptTestPlatform(t, map[string]any{"ack_emoji": "Get"}, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/reactions") {
			requests <- receiptRequest{method: r.Method, path: r.URL.Path}
			if _, err := fmt.Fprint(w, `{"code":0,"data":{"reaction_id":"receipt"}}`); err != nil {
				t.Errorf("write fixture response: %v", err)
			}
		} else if strings.HasSuffix(r.URL.Path, "/reply") {
			if _, err := fmt.Fprint(w, `{"code":0,"data":{"message_id":"denied-reply"}}`); err != nil {
				t.Errorf("write fixture response: %v", err)
			}
		} else {
			t.Errorf("unexpected request: %s", r.URL.Path)
		}
	})
	p.botOpenID = "ou_bot"
	p.allowChat = "oc_main"
	p.allowFrom = "ou_alice"
	p.userNameCache.Store("ou_alice", "Alice")
	p.chatNameCache.Store("oc_main", "Main group")
	var delivered atomic.Int32
	p.handler = func(_ core.Platform, m *core.Message) { delivered.Add(1); m.OnAccepted() }
	send := func(id, user, chat string, mentioned bool) {
		t.Helper()
		event := makeGroupHistoryEvent(t, id, user, "text", `{"text":"hello"}`, chat, "", mentioned)
		if err := p.onMessage(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	send("valid", "ou_alice", "oc_main", true)
	if req := awaitReceiptRequest(t, requests); req.path != "/open-apis/im/v1/messages/valid/reactions" {
		t.Fatalf("acknowledged wrong message: %+v", req)
	}
	send("valid", "ou_alice", "oc_main", true) // duplicate delivery
	send("no-mention", "ou_alice", "oc_main", false)
	send("forbidden-chat", "ou_alice", "oc_other", true)
	send("forbidden-user", "ou_other", "oc_main", true)
	p.markMessageRecalled("recalled")
	send("recalled", "ou_alice", "oc_main", true)
	// All these rejection gates run synchronously in onMessage, before dispatch.
	if delivered.Load() != 1 {
		t.Fatalf("delivered %d events, want only the accepted event", delivered.Load())
	}
	select {
	case req := <-requests:
		t.Fatalf("rejected message acknowledged: %+v", req)
	default:
	}
}

func TestReceiptAcknowledgement_ImageBatchUsesAcceptedCanonicalMessage(t *testing.T) {
	requests := make(chan receiptRequest, 10)
	p := receiptTestPlatform(t, map[string]any{"ack_emoji": "Get", "reaction_emoji": "Get"}, func(w http.ResponseWriter, r *http.Request) {
		requests <- receiptRequest{method: r.Method, path: r.URL.Path}
		if _, err := fmt.Fprint(w, `{"code":0,"data":{"reaction_id":"reaction-Get"}}`); err != nil {
			t.Errorf("write fixture response: %v", err)
		}
	})
	var msg *core.Message
	p.handler = func(_ core.Platform, m *core.Message) { msg = m; m.OnAccepted() }
	// The batching layer keeps the first reply context but admits the last ID.
	// Recalling the first member must not hide receipt of the accepted batch.
	p.markMessageRecalled("om_first")
	p.dispatchImageBatchEntry(&imageBatchEntry{
		sessionKey: "feishu:oc_main:ou_alice", userID: "ou_alice", messageIDs: []string{"om_first", "om_last"},
		images: []core.ImageAttachment{{Data: []byte("first")}, {Data: []byte("last")}},
		rctx:   replyContext{messageID: "om_first", chatID: "oc_main"},
	})
	if req := awaitReceiptRequest(t, requests); req.path != "/open-apis/im/v1/messages/om_last/reactions" {
		t.Fatalf("receipt target=%+v", req)
	}
	stop := p.StartTyping(context.Background(), msg.ReplyCtx)
	if req := awaitReceiptRequest(t, requests); req.path != "/open-apis/im/v1/messages/om_first/reactions" {
		t.Fatalf("typing target=%+v", req)
	}
	stop()
	if req := awaitReceiptRequest(t, requests); req.method != http.MethodDelete || req.path != "/open-apis/im/v1/messages/om_first/reactions/reaction-Get" {
		t.Fatalf("cleanup must not delete canonical receipt: %+v", req)
	}
}

func TestReceiptAcknowledgement_TypingStopsBeforeReceiptCompletes(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var requests atomic.Int32
	p := receiptTestPlatform(t, map[string]any{"ack_emoji": "Get", "reaction_emoji": "Get"}, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodPost {
			t.Errorf("receipt deleted: %s", r.Method)
			return
		}
		close(started)
		<-release
		if _, err := fmt.Fprint(w, `{"code":0,"data":{"reaction_id":"receipt"}}`); err != nil {
			t.Errorf("write fixture response: %v", err)
		}
	})
	p.handler = func(_ core.Platform, m *core.Message) { m.OnAccepted() }
	msg := receiptMessage()
	p.dispatchCoreMessage(msg)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("receipt not started")
	}
	// Both operations must complete even though the identical-emoji receipt
	// request is still in flight. There must be no second create/delete request.
	stopped := make(chan struct{})
	go func() { stop := p.StartTyping(context.Background(), msg.ReplyCtx); stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("typing waited for pending receipt")
	}
	if requests.Load() != 1 {
		t.Fatalf("%d reaction requests, want just the receipt", requests.Load())
	}
	unblock()
}

type receiptDeadlineTransport struct{ observed chan time.Duration }

func (rt *receiptDeadlineTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.Contains(r.URL.Path, "/auth/") {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"code":0,"expire":7200,"tenant_access_token":"test-token"}`))}, nil
	}
	deadline, ok := r.Context().Deadline()
	if !ok {
		rt.observed <- 0
	} else {
		rt.observed <- time.Until(deadline)
	}
	return nil, errors.New("simulated receipt network failure")
}

func TestReceiptAcknowledgement_HTTPRequestHasBoundedDeadline(t *testing.T) {
	p := receiptTestPlatform(t, map[string]any{"ack_emoji": "Get"}, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected real HTTP request") })
	observed := make(chan time.Duration, 1)
	p.client = lark.NewClient("deadline-test", "test-secret", lark.WithHttpClient(&http.Client{Transport: &receiptDeadlineTransport{observed: observed}}))
	p.handler = func(_ core.Platform, m *core.Message) { m.OnAccepted() }
	p.dispatchCoreMessage(receiptMessage())
	select {
	case remaining := <-observed:
		if remaining <= 0 || remaining > 5*time.Second {
			t.Fatalf("receipt request deadline remaining=%v, want (0,5s]", remaining)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("receipt request did not reach transport")
	}
}
