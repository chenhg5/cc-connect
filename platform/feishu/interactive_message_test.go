package feishu

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// Forwarded cards must use the full API content and the same dispatch path as
// text messages, preserving quotes, history admission, and message timestamps.
func TestDispatchInteractiveCard_PreservesMessageContext(t *testing.T) {
	card := `{"schema":"2.0","body":{"elements":[{"tag":"markdown","content":"@_user_1 /help"}]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(t, w, map[string]any{"code": 0, "expire": 7200, "tenant_access_token": "test-token"})
		case "/open-apis/im/v1/messages/om_card":
			if r.Method != http.MethodGet || r.URL.Query().Get("card_msg_content_type") != "raw_card_content" {
				t.Errorf("unexpected card request: %s %s", r.Method, r.URL)
			}
			writeJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"items": []any{map[string]any{"body": map[string]any{"content": `{"json_card":` + jsonEscape(card) + `}`}}}}})
		case "/open-apis/im/v1/messages/om_parent":
			writeJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"items": []any{map[string]any{"msg_type": "text", "sender": map[string]any{"sender_type": "user"}, "body": map[string]any{"content": `{"text":"quoted context"}`}}}}})
		default:
			t.Errorf("unexpected request: %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	var got *core.Message
	p := &Platform{platformName: "feishu", botOpenID: "ou_bot", client: lark.NewClient("interactive-context", "secret", lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client())), handler: func(_ core.Platform, msg *core.Message) { got = msg }}
	p.userNameCache.Store("ou_user", "Tester")
	p.chatNameCache.Store("oc_chat", "Group")
	accepted := false
	history := groupHistoryContext{entries: []groupHistoryEntry{{text: "recent context", senderType: "app"}}, onAccepted: func() { accepted = true }}
	rc := replyContext{messageID: "om_card", sessionKey: "feishu:oc_chat:ou_user"}
	p.dispatchMessageWithHistory(context.Background(), "interactive", `{"text":"请升级至最新版本客户端"}`, []*larkim.MentionEvent{postMentionForTest("@_user_1", "ou_bot", "Bot")}, "om_card", rc.sessionKey, "ou_user", "oc_chat", rc, "om_parent", 12345, history)
	if got == nil {
		t.Fatal("interactive card was not dispatched")
	}
	if got.Content != "/help" {
		t.Fatalf("Content = %q, want full card text with bot mention removed", got.Content)
	}
	if !strings.Contains(got.ExtraContent, "quoted context") || !strings.Contains(got.ExtraContent, "recent context") {
		t.Fatalf("missing quote/history: %q", got.ExtraContent)
	}
	if got.UserMessageTimeMs != 12345 || got.MessageID != "om_card" || got.SessionKey != rc.sessionKey || got.UserName != "Tester" || got.ChatName != "Group" {
		t.Fatalf("lost message metadata: %#v", got)
	}
	if accepted || got.OnAccepted == nil {
		t.Fatal("history must be consumed only on admission")
	}
	got.OnAccepted()
	if !accepted {
		t.Fatal("history admission callback was lost")
	}
}

func TestFetchInteractiveCardText_FailedResponses(t *testing.T) {
	for _, body := range []string{`not json`, `{"code":999}`, `{"code":0,"data":{"items":[]}}`, `{"code":0,"data":{"items":[{"body":{"content":""}}]}}`} {
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "tenant_access_token") {
					writeJSON(t, w, map[string]any{"code": 0, "expire": 7200, "tenant_access_token": "test-token"})
					return
				}
				_, _ = w.Write([]byte(body))
			}))
			defer srv.Close()
			p := &Platform{client: lark.NewClient("interactive-errors", "secret", lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client()))}
			if got := p.fetchInteractiveCardText(context.Background(), "om_card"); got != "" {
				t.Fatalf("got %q for failed response", got)
			}
		})
	}
}
