package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/timmyagentic/cc-connect-next/core"
)

func TestStreamRichCardTextReturnsRateLimitForFallback(t *testing.T) {
	const cardID = "card_rate_limited"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(t, w, map[string]any{
				"code": 0, "msg": "success", "expire": 7200, "tenant_access_token": "tenant-token",
			})
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/elements/"):
			writeJSON(t, w, map[string]any{"code": 230020, "msg": "rate limited"})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	client := lark.NewClient("cli_rate_limit", "secret",
		lark.WithOpenBaseUrl(srv.URL),
		lark.WithHttpClient(srv.Client()),
	)
	p := &Platform{
		platformName: "feishu",
		domain:       srv.URL,
		appID:        "cli_rate_limit",
		appSecret:    "secret",
		client:       client,
		replayClient: client,
	}
	handle := &feishuPreviewHandle{cardID: cardID}
	err := p.StreamRichCardText(context.Background(), handle, "unrendered frame")
	if err == nil || !errors.Is(err, errFeishuCardRateLimited) {
		t.Fatalf("StreamRichCardText() error = %v, want distinguishable rate-limit failure", err)
	}
}

type capturedRichCardRequest struct {
	Method string
	Path   string
	Body   map[string]any
}

func TestRichCardLifecycleUsesQuotedCardKitEntityAndMonotonicUpdates(t *testing.T) {
	const (
		appID     = "cli_rich_card"
		appSecret = "rich-card-secret"
		triggerID = "om_trigger"
		messageID = "om_answer_card"
		cardID    = "card_entity_123"
	)
	var mu sync.Mutex
	var requests []capturedRichCardRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		body := map[string]any{}
		if len(bodyBytes) > 0 {
			if err := json.Unmarshal(bodyBytes, &body); err != nil {
				t.Errorf("decode %s %s body: %v; raw=%s", r.Method, r.URL.Path, err, bodyBytes)
			}
		}
		mu.Lock()
		requests = append(requests, capturedRichCardRequest{Method: r.Method, Path: r.URL.Path, Body: body})
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(t, w, map[string]any{
				"code": 0, "msg": "success", "expire": 7200, "tenant_access_token": "tenant-token",
			})
		case r.Method == http.MethodPost && r.URL.Path == "/open-apis/cardkit/v1/cards":
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success", "data": map[string]any{"card_id": cardID}})
		case r.Method == http.MethodPost && r.URL.Path == "/open-apis/im/v1/messages/"+triggerID+"/reply":
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success", "data": map[string]any{"message_id": messageID}})
		default:
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success", "data": map[string]any{}})
		}
	}))
	defer srv.Close()

	newClient := func() *lark.Client {
		return lark.NewClient(appID, appSecret,
			lark.WithOpenBaseUrl(srv.URL),
			lark.WithHttpClient(srv.Client()),
		)
	}
	p := &Platform{
		platformName:       "feishu",
		domain:             srv.URL,
		appID:              appID,
		appSecret:          appSecret,
		useInteractiveCard: true,
		client:             newClient(),
		replayClient:       newClient(),
	}
	initial := buildRichCard(core.CardStatusThinking, "thinking", nil, "", true, "")
	handleValue, err := p.SendPreviewStart(context.Background(), replyContext{
		messageID:  triggerID,
		chatID:     "oc_chat",
		sessionKey: "feishu:oc_chat:ou_user",
	}, initial)
	if err != nil {
		t.Fatalf("SendPreviewStart() error = %v", err)
	}
	handle, ok := handleValue.(*feishuPreviewHandle)
	if !ok {
		t.Fatalf("preview handle type = %T", handleValue)
	}
	if handle.messageID != messageID || handle.cardID != cardID {
		t.Fatalf("preview handle = %+v, want message=%s card=%s", handle, messageID, cardID)
	}

	if err := p.StreamRichCardText(context.Background(), handle, "streamed answer"); err != nil {
		t.Fatalf("StreamRichCardText() error = %v", err)
	}
	final := buildRichCard(core.CardStatusDone, "done", nil, "final answer", false, "")
	if err := p.UpdateMessage(context.Background(), handle, final); err != nil {
		t.Fatalf("UpdateMessage() error = %v", err)
	}
	if err := p.DeletePreviewMessage(context.Background(), handle); err != nil {
		t.Fatalf("DeletePreviewMessage() error = %v", err)
	}

	mu.Lock()
	captured := append([]capturedRichCardRequest(nil), requests...)
	mu.Unlock()
	find := func(method, path string) *capturedRichCardRequest {
		for i := range captured {
			if captured[i].Method == method && captured[i].Path == path {
				return &captured[i]
			}
		}
		return nil
	}
	create := find(http.MethodPost, "/open-apis/cardkit/v1/cards")
	if create == nil || create.Body["type"] != "card_json" {
		t.Fatalf("missing CardKit entity create request: %+v", captured)
	}
	reply := find(http.MethodPost, "/open-apis/im/v1/messages/"+triggerID+"/reply")
	if reply == nil {
		t.Fatalf("initial card did not quote the trigger message: %+v", captured)
	}
	if content, _ := reply.Body["content"].(string); !strings.Contains(content, cardID) {
		t.Fatalf("reply content does not reference CardKit card_id: %+v", reply.Body)
	}
	stream := find(http.MethodPut, "/open-apis/cardkit/v1/cards/"+cardID+"/elements/"+richCardMainTextElementID+"/content")
	if stream == nil || stream.Body["content"] != "streamed answer" || stream.Body["sequence"] != float64(1) {
		t.Fatalf("unexpected per-element stream request: %+v", stream)
	}
	update := find(http.MethodPut, "/open-apis/cardkit/v1/cards/"+cardID)
	if update == nil || update.Body["sequence"] != float64(2) {
		t.Fatalf("full-card update did not share the monotonic sequence: %+v", update)
	}
	if find(http.MethodDelete, "/open-apis/im/v1/messages/"+messageID) == nil {
		t.Fatalf("silent cleanup did not target the created answer message: %+v", captured)
	}
}
