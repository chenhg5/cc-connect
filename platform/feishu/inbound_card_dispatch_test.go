package feishu

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
)

const testCard20 = `{"schema":"2.0","body":{"elements":[{"tag":"div","text":{"tag":"plain_text","content":"告警正文"}},{"tag":"button","text":{"tag":"plain_text","content":"查看详情"},"behaviors":[{"type":"open_url","default_url":"https://example.test/alert"}],"value":{"alert_id":"alert-1"}}]}}`

func TestDispatchInteractivePreservesRawCard(t *testing.T) {
	got := make(chan *core.Message, 1)
	var rawQuerySeen bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/open-apis/im/v1/messages/om_interactive" {
			rawQuerySeen = r.URL.Query().Get("card_msg_content_type") == "raw_card_content"
			writeInboundCardResponse(t, w, "om_interactive", testCard20)
			return
		}
		writeInboundCardResponse(t, w, "", "")
	}))
	defer srv.Close()

	p := newInboundCardTestPlatform(srv, "both", func(msg *core.Message) { got <- msg })
	p.dispatchMessage(
		context.Background(), "interactive", `{"title":"lossy event summary"}`, nil,
		"om_interactive", "feishu:oc_chat:ou_user", "ou_user", "oc_chat",
		replyContext{messageID: "om_interactive", chatID: "oc_chat", sessionKey: "feishu:oc_chat:ou_user"}, "", time.Now().UnixMilli(),
	)

	select {
	case msg := <-got:
		if len(msg.Cards) != 1 {
			t.Fatalf("Cards = %d, want 1", len(msg.Cards))
		}
		if !strings.Contains(string(msg.Cards[0].Raw), `"alert_id":"alert-1"`) {
			t.Fatalf("raw card lost action value: %s", msg.Cards[0].Raw)
		}
		if !strings.Contains(msg.Content, "告警正文") || !strings.Contains(msg.Content, "https://example.test/alert") {
			t.Fatalf("summary = %q", msg.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for interactive card")
	}
	if !rawQuerySeen {
		t.Fatal("interactive fetch did not request raw_card_content")
	}
}

func TestDispatchMergeForwardPreservesInteractiveChildren(t *testing.T) {
	got := make(chan *core.Message, 1)
	var rawQueries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/open-apis/im/v1/messages/om_forward" {
			rawQueries = append(rawQueries, r.URL.Query().Get("card_msg_content_type"))
			writeMergeForwardResponse(t, w)
			return
		}
		if r.URL.Path == "/open-apis/im/v1/messages/om_child_card" {
			rawQueries = append(rawQueries, r.URL.Query().Get("card_msg_content_type"))
			writeInboundCardResponse(t, w, "om_child_card", testCard20)
			return
		}
		writeInboundCardResponse(t, w, "", "")
	}))
	defer srv.Close()

	p := newInboundCardTestPlatform(srv, "both", func(msg *core.Message) { got <- msg })
	p.dispatchMessage(
		context.Background(), "merge_forward", "", nil,
		"om_forward", "feishu:oc_chat:ou_user", "ou_user", "oc_chat",
		replyContext{messageID: "om_forward", chatID: "oc_chat", sessionKey: "feishu:oc_chat:ou_user"}, "", time.Now().UnixMilli(),
	)

	select {
	case msg := <-got:
		if len(msg.Cards) != 1 {
			t.Fatalf("Cards = %d, want 1", len(msg.Cards))
		}
		if !strings.Contains(string(msg.Cards[0].Raw), `"alert_id":"alert-1"`) {
			t.Fatalf("merge-forward raw card lost action value: %s", msg.Cards[0].Raw)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for merge-forward card")
	}
	for _, value := range rawQueries {
		if value != "raw_card_content" {
			t.Fatalf("merge-forward fetch query = %q, want raw_card_content", value)
		}
	}
	if len(rawQueries) < 1 {
		t.Fatalf("raw queries = %d, want the merge-forward root fetch", len(rawQueries))
	}
}

func TestDispatchEngagedThreadStillPreservesQuotedCards(t *testing.T) {
	got := make(chan *core.Message, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/open-apis/im/v1/messages/om_root_card" {
			writeInboundCardResponse(t, w, "om_root_card", testCard20)
			return
		}
		writeInboundCardResponse(t, w, "", "")
	}))
	defer srv.Close()

	const sessionKey = "feishu:oc_chat:root:om_root_card"
	p := newInboundCardTestPlatform(srv, "both", func(msg *core.Message) { got <- msg })
	p.threadIsolation = true
	p.activeThreadSessions.Store(sessionKey, time.Now())
	p.dispatchMessage(
		context.Background(), "text", `{"text":"你拿到卡片了吗"}`, nil,
		"om_followup", sessionKey, "ou_user", "oc_chat",
		replyContext{messageID: "om_followup", chatID: "oc_chat", sessionKey: sessionKey}, "om_root_card", time.Now().UnixMilli(),
	)

	select {
	case msg := <-got:
		if len(msg.Cards) != 1 {
			t.Fatalf("Cards = %d, want 1 for an engaged thread quote", len(msg.Cards))
		}
		if !strings.Contains(string(msg.Cards[0].Raw), `"alert_id":"alert-1"`) {
			t.Fatalf("quoted card raw payload lost: %s", msg.Cards[0].Raw)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for engaged-thread quoted card")
	}
}

func newInboundCardTestPlatform(srv *httptest.Server, mode string, handler func(*core.Message)) *Platform {
	return &Platform{
		platformName:        "feishu",
		domain:              srv.URL,
		appID:               "cli_card_test",
		appSecret:           "secret-card-test",
		inboundCardMode:     mode,
		inboundCardMaxBytes: 0,
		client: lark.NewClient("cli_card_test", "secret-card-test",
			lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client())),
		handler: func(_ core.Platform, msg *core.Message) { handler(msg) },
	}
}

func writeInboundCardResponse(t *testing.T, w http.ResponseWriter, messageID, card string) {
	t.Helper()
	items := []map[string]any{}
	if messageID != "" {
		items = append(items, map[string]any{
			"message_id": messageID,
			"msg_type":   "interactive",
			"sender":     map[string]any{"id": "ou_sender", "sender_type": "app"},
			"body":       map[string]any{"content": `{"json_card":` + quoteJSON(card) + `}`},
		})
	}
	writeCardTestJSON(t, w, map[string]any{"code": 0, "msg": "success", "data": map[string]any{"items": items}})
}

func writeMergeForwardResponse(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	writeCardTestJSON(t, w, map[string]any{
		"code": 0,
		"msg":  "success",
		"data": map[string]any{"items": []map[string]any{
			{"message_id": "om_forward", "msg_type": "merge_forward", "sender": map[string]any{"id": "ou_sender", "sender_type": "app"}},
			{"message_id": "om_child_card", "upper_message_id": "om_forward", "msg_type": "interactive", "sender": map[string]any{"id": "ou_sender", "sender_type": "app"}, "body": map[string]any{"content": `{"json_card":` + quoteJSON(testCard20) + `}`}},
		}},
	})
}

func quoteJSON(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func writeCardTestJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}
