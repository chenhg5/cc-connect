package feishu

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
)

func TestDispatchInteractiveFetchesAndPreservesRawCard(t *testing.T) {
	const card = `{"schema":"2.0","body":{"elements":[{"tag":"div","text":{"tag":"plain_text","content":"告警正文"}},{"tag":"button","text":{"tag":"plain_text","content":"查看详情"},"behaviors":[{"type":"open_url","default_url":"https://example.test/alert"}],"value":{"alert_id":"alert-1"}}]}}`
	got := make(chan *core.Message, 1)
	var rawCardQuery bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/open-apis/im/v1/messages/om_interactive" {
			rawCardQuery = r.URL.Query().Get("card_msg_content_type") == "raw_card_content"
			writeInboundCardTestResponse(t, w, "om_interactive", card)
			return
		}
		writeInboundCardTestResponse(t, w, "", "")
	}))
	defer srv.Close()

	p := &Platform{
		platformName:    "feishu",
		domain:          srv.URL,
		appID:           "cli_card_test",
		appSecret:       "secret-card-test",
		inboundCardMode: "both",
		client:          lark.NewClient("cli_card_test", "secret-card-test", lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client())),
		handler:         func(_ core.Platform, msg *core.Message) { got <- msg },
		userNameCache:   sync.Map{},
		chatNameCache:   sync.Map{},
		recalledMsgIDs:  map[string]time.Time{},
		imageBatch:      map[string]*imageBatchEntry{},
	}
	p.dispatchMessage(
		context.Background(), "interactive", `{"title":"lossy event summary"}`, nil,
		"om_interactive", "feishu:oc_chat:ou_user", "ou_user", "oc_chat",
		replyContext{messageID: "om_interactive", chatID: "oc_chat", sessionKey: "feishu:oc_chat:ou_user"}, "", time.Now().UnixMilli(),
	)

	select {
	case msg := <-got:
		if len(msg.Cards) != 1 || !strings.Contains(string(msg.Cards[0].Raw), `"alert_id":"alert-1"`) {
			t.Fatalf("cards = %#v, want raw alert payload", msg.Cards)
		}
		if !strings.Contains(msg.Content, "告警正文") {
			t.Fatalf("summary = %q, want card text", msg.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for interactive card")
	}
	if !rawCardQuery {
		t.Fatal("interactive fetch did not request raw_card_content")
	}
}

func TestDispatchInteractiveSummarySkipsFetchWhenSummaryIsPresent(t *testing.T) {
	var fetches int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/open-apis/im/v1/messages/om_interactive" {
			fetches++
		}
		writeInboundCardTestResponse(t, w, "", "")
	}))
	defer srv.Close()

	got := make(chan *core.Message, 1)
	p := &Platform{
		platformName: "feishu", domain: srv.URL, appID: "cli_card_test", appSecret: "secret-card-test",
		inboundCardMode: "summary", client: lark.NewClient("cli_card_test", "secret-card-test", lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client())),
		handler:       func(_ core.Platform, msg *core.Message) { got <- msg },
		userNameCache: sync.Map{}, chatNameCache: sync.Map{}, recalledMsgIDs: map[string]time.Time{}, imageBatch: map[string]*imageBatchEntry{},
	}
	content := `{"schema":"2.0","body":{"elements":[{"tag":"div","text":{"tag":"plain_text","content":"告警摘要"}}]}}`
	p.dispatchMessage(context.Background(), "interactive", content, nil,
		"om_interactive", "feishu:oc_chat:ou_user", "ou_user", "oc_chat",
		replyContext{messageID: "om_interactive", chatID: "oc_chat", sessionKey: "feishu:oc_chat:ou_user"}, "", time.Now().UnixMilli())

	select {
	case msg := <-got:
		if !strings.Contains(msg.Content, "告警摘要") {
			t.Fatalf("summary = %q, want card text", msg.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for interactive card")
	}
	if fetches != 0 {
		t.Fatalf("summary mode made %d fetches, want 0", fetches)
	}
}

func TestParseMergeForwardPreservesInteractiveChildren(t *testing.T) {
	const card = `{"schema":"2.0","body":{"elements":[{"tag":"div","text":{"tag":"plain_text","content":"告警正文"}}],"action":{"value":{"alert_id":"alert-1"}}}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/open-apis/im/v1/messages/om_forward" {
			writeInboundCardTestResponse(t, w, "", "")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"items": []map[string]any{
				{"message_id": "om_forward", "msg_type": "merge_forward", "sender": map[string]any{"id": "ou_sender", "sender_type": "app"}},
				{"message_id": "om_child", "upper_message_id": "om_forward", "msg_type": "interactive", "sender": map[string]any{"id": "ou_sender", "sender_type": "app"}, "body": map[string]any{"content": `{"json_card":` + quoteInboundJSON(card) + `}`}},
			}},
		})
	}))
	defer srv.Close()

	p := &Platform{
		platformName: "feishu", domain: srv.URL, appID: "cli_card_test", appSecret: "secret-card-test",
		inboundCardMode: "both", client: lark.NewClient("cli_card_test", "secret-card-test", lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client())),
		userNameCache: sync.Map{}, chatNameCache: sync.Map{}, recalledMsgIDs: map[string]time.Time{}, imageBatch: map[string]*imageBatchEntry{},
	}
	_, _, _, cards := p.parseMergeForward(context.Background(), "om_forward")
	if len(cards) != 1 || !strings.Contains(string(cards[0].Raw), `"alert_id":"alert-1"`) {
		t.Fatalf("cards = %#v, want forwarded raw alert card", cards)
	}
}

func writeInboundCardTestResponse(t *testing.T, w http.ResponseWriter, messageID, card string) {
	t.Helper()
	items := []map[string]any{}
	if messageID != "" {
		items = append(items, map[string]any{
			"message_id": messageID,
			"msg_type":   "interactive",
			"sender":     map[string]any{"id": "ou_sender", "sender_type": "app"},
			"body":       map[string]any{"content": `{"json_card":` + quoteInboundJSON(card) + `}`},
		})
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "success", "data": map[string]any{"items": items}}); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}
