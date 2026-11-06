package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/chenhg5/cc-connect/core"
)

var (
	_ core.RawCardSender  = (*Platform)(nil)
	_ core.RawCardUpdater = (*Platform)(nil)
)

// rawCardFeishu records what is sent and patched; every send gets a fresh id.
type rawCardFeishu struct {
	mu      sync.Mutex
	sent    []map[string]string // msg_type, content, id, uuid, path
	patched map[string]string   // message id -> card
}

func newRawCardFeishuPlatform(t *testing.T) (*Platform, *rawCardFeishu) {
	t.Helper()
	fake := &rawCardFeishu{patched: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		var req struct {
			MsgType string `json:"msg_type"`
			Content string `json:"content"`
			UUID    string `json:"uuid"`
		}
		_ = json.Unmarshal(body, &req)
		fake.mu.Lock()
		defer fake.mu.Unlock()
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(t, w, map[string]any{"code": 0, "expire": 7200, "tenant_access_token": "t"})
		case r.Method == http.MethodPost && (r.URL.Path == "/open-apis/im/v1/messages" ||
			(strings.HasPrefix(r.URL.Path, "/open-apis/im/v1/messages/") && strings.HasSuffix(r.URL.Path, "/reply"))):
			id := fmt.Sprintf("om_sent_%d", len(fake.sent))
			fake.sent = append(fake.sent, map[string]string{"msg_type": req.MsgType, "content": req.Content, "id": id, "uuid": req.UUID, "path": r.URL.Path})
			writeJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"message_id": id}})
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/open-apis/im/v1/messages/"):
			fake.patched[strings.TrimPrefix(r.URL.Path, "/open-apis/im/v1/messages/")] = req.Content
			writeJSON(t, w, map[string]any{"code": 0})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	p := &Platform{
		platformName:       "feishu",
		domain:             srv.URL,
		appID:              "cli_test",
		appSecret:          "secret",
		useInteractiveCard: true,
		client:             lark.NewClient("cli_test", "secret", lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client())),
		replayClient:       lark.NewClient("cli_test", "secret", lark.WithEnableTokenCache(false), lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client())),
	}
	return p, fake
}

// A raw card goes out as an interactive message, unchanged, its message id
// comes back, and it can be patched in place by that id.
func TestRawCards_SendAndUpdate(t *testing.T) {
	p, fake := newRawCardFeishuPlatform(t)
	ctx := context.Background()
	card := json.RawMessage(`{"schema":"2.0","header":{"title":{"tag":"plain_text","content":"Receipt"}},"body":{"elements":[]}}`)
	id, err := p.SendRawCard(ctx, replyContext{chatID: "oc_p2p", sessionKey: "feishu:oc_p2p:ou_owner"}, card, "")
	if err != nil {
		t.Fatal(err)
	}
	if id == "" || len(fake.sent) != 1 || fake.sent[0]["id"] != id {
		t.Fatalf("id=%q sent=%#v", id, fake.sent)
	}
	if fake.sent[0]["msg_type"] != larkim.MsgTypeInteractive || fake.sent[0]["content"] != string(card) || fake.sent[0]["uuid"] != "" {
		t.Fatalf("sent = %#v", fake.sent[0])
	}
	update := json.RawMessage(`{"schema":"2.0","body":{"elements":[{"tag":"markdown","content":"Updated"}]}}`)
	if err := p.UpdateRawCard(ctx, id, update); err != nil {
		t.Fatal(err)
	}
	if fake.patched[id] != string(update) {
		t.Fatalf("patched = %#v", fake.patched)
	}
	if _, err := p.SendRawCard(ctx, "not a reply context", card, ""); err == nil {
		t.Fatal("a foreign reply context must fail")
	}
}

// A raw card's uuid reaches Feishu with the message -- a new message and a
// reply alike -- so Feishu delivers one message per uuid.
func TestRawCards_UUIDReachesFeishu(t *testing.T) {
	p, fake := newRawCardFeishuPlatform(t)
	ctx := context.Background()
	card := json.RawMessage(`{"schema":"2.0","body":{"elements":[]}}`)
	uuid := "job-0123456789abcdef0123456789abcdef0123456"
	if _, err := p.SendRawCard(ctx, replyContext{chatID: "oc_p2p", sessionKey: "feishu:oc_p2p:ou_owner"}, card, uuid); err != nil {
		t.Fatal(err)
	}
	if _, err := p.SendRawCard(ctx, replyContext{chatID: "oc_chat", messageID: "om_root", sessionKey: "feishu:oc_chat:root:om_root"}, card, uuid); err != nil {
		t.Fatal(err)
	}
	if len(fake.sent) != 2 {
		t.Fatalf("sent = %#v", fake.sent)
	}
	if fake.sent[0]["path"] != "/open-apis/im/v1/messages" || fake.sent[0]["uuid"] != uuid {
		t.Fatalf("new message = %#v", fake.sent[0])
	}
	if !strings.HasSuffix(fake.sent[1]["path"], "/reply") || fake.sent[1]["uuid"] != uuid {
		t.Fatalf("reply = %#v", fake.sent[1])
	}
}

// When Feishu refuses a card (230099 "Failed to create card content",
// 230025 content too long), SendRawCard's error carries the Feishu code as
// "code=<n>" on both send paths -- a new message and a reply in a thread --
// so callers can tell a refused card (not worth retrying unchanged) from a
// transient failure.
func TestRawCards_RefusalCarriesTheFeishuCode(t *testing.T) {
	for _, code := range []int{230099, 230025} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal" {
				writeJSON(t, w, map[string]any{"code": 0, "expire": 7200, "tenant_access_token": "t"})
				return
			}
			writeJSON(t, w, map[string]any{
				"code": code,
				"msg":  "Failed to create card content, ext=ErrCode: 11310; ErrMsg: element exceeds the limit;",
			})
		}))
		p := &Platform{
			platformName:       "feishu",
			domain:             srv.URL,
			appID:              "cli_test",
			appSecret:          "secret",
			useInteractiveCard: true,
			client:             lark.NewClient("cli_test", "secret", lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client())),
			replayClient:       lark.NewClient("cli_test", "secret", lark.WithEnableTokenCache(false), lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client())),
		}
		card := json.RawMessage(`{"schema":"2.0","body":{"elements":[]}}`)
		for _, rc := range []replyContext{
			{chatID: "oc_p2p", sessionKey: "feishu:oc_p2p:ou_owner"},
			{chatID: "oc_chat", messageID: "om_root", sessionKey: "feishu:oc_chat:root:om_root"},
		} {
			_, err := p.SendRawCard(context.Background(), rc, card, "job-x")
			if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("code=%d", code)) {
				t.Fatalf("code %d via %+v: err = %v, want it to carry code=%d", code, rc, err, code)
			}
		}
		srv.Close()
	}
}
