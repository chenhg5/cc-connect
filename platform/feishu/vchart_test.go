package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

const testVChartCardJSON = `{
  "schema": "2.0",
  "config": {"width_mode": "fill"},
  "body": {
    "elements": [
      {"tag": "markdown", "content": "Weekly requests"},
      {
        "tag": "chart",
        "chart_spec": {
          "type": "bar",
          "data": [{"id": "requests", "values": [{"day": "Mon", "value": 12}]}],
          "xField": "day",
          "yField": "value"
        },
        "aspect_ratio": "2:1"
      }
    ]
  }
}`

type capturedMessageRequest struct {
	MsgType string `json:"msg_type"`
	Content string `json:"content"`
}

func newVChartSendTestPlatform(t *testing.T, capture func(capturedMessageRequest)) (*Platform, *httptest.Server) {
	t.Helper()
	const appID = "cli_vchart_test"
	const appSecret = "secret"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(t, w, map[string]any{"code": 0, "expire": 7200, "tenant_access_token": "t"})
		case r.URL.Path == "/open-apis/im/v1/messages" && r.Method == http.MethodPost:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read message request: %v", err)
				return
			}
			var req capturedMessageRequest
			if err := json.Unmarshal(body, &req); err != nil {
				t.Errorf("unmarshal message request: %v", err)
				return
			}
			capture(req)
			writeJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"message_id": "om_vchart"}})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			writeJSON(t, w, map[string]any{"code": 1, "msg": "unexpected request"})
		}
	}))

	p := &Platform{
		platformName:       "feishu",
		domain:             srv.URL,
		appID:              appID,
		appSecret:          appSecret,
		useInteractiveCard: true,
		client:             lark.NewClient(appID, appSecret, lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client())),
		replayClient:       lark.NewClient(appID, appSecret, lark.WithEnableTokenCache(false), lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client())),
	}
	return p, srv
}

func assertRawVChartRequest(t *testing.T, req capturedMessageRequest) {
	t.Helper()
	if req.MsgType != larkim.MsgTypeInteractive {
		t.Fatalf("msg_type = %q, want %q", req.MsgType, larkim.MsgTypeInteractive)
	}
	if req.Content != strings.TrimSpace(testVChartCardJSON) {
		t.Fatalf("card content was rewritten\n got: %s\nwant: %s", req.Content, strings.TrimSpace(testVChartCardJSON))
	}
}

func TestRawVChartCard_SendPathsPreserveCardJSON(t *testing.T) {
	tests := []struct {
		name string
		send func(*Platform) error
	}{
		{
			name: "send",
			send: func(p *Platform) error {
				return p.Send(context.Background(), replyContext{chatID: "oc_vchart"}, "\n"+testVChartCardJSON+"\n")
			},
		},
		{
			name: "send with status footer",
			send: func(p *Platform) error {
				return p.SendWithStatusFooter(context.Background(), replyContext{chatID: "oc_vchart"}, testVChartCardJSON, "model · ctx")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var captured capturedMessageRequest
			p, srv := newVChartSendTestPlatform(t, func(req capturedMessageRequest) { captured = req })
			defer srv.Close()

			if err := tt.send(p); err != nil {
				t.Fatalf("send raw VChart card: %v", err)
			}
			assertRawVChartRequest(t, captured)
		})
	}
}

func TestBuildRichCard_PreservesRawVChartCardJSON(t *testing.T) {
	got := buildRichCard(core.CardStatusDone, "", nil, "\n"+testVChartCardJSON+"\n", false, "model · ctx")
	if got != strings.TrimSpace(testVChartCardJSON) {
		t.Fatalf("rich card wrapped raw VChart card\n got: %s\nwant: %s", got, strings.TrimSpace(testVChartCardJSON))
	}
}

func TestUpdateMessageWithStatusFooter_PreservesRawVChartCardJSON(t *testing.T) {
	const appID = "cli_vchart_update"
	const appSecret = "secret"
	var captured string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(t, w, map[string]any{"code": 0, "expire": 7200, "tenant_access_token": "t"})
		case r.URL.Path == "/open-apis/im/v1/messages/om_preview" && r.Method == http.MethodPatch:
			body, _ := io.ReadAll(r.Body)
			var req struct {
				Content string `json:"content"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				t.Errorf("unmarshal patch request: %v", err)
				return
			}
			captured = req.Content
			writeJSON(t, w, map[string]any{"code": 0})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			writeJSON(t, w, map[string]any{"code": 1, "msg": "unexpected request"})
		}
	}))
	defer srv.Close()

	p := &Platform{
		platformName:       "feishu",
		domain:             srv.URL,
		appID:              appID,
		appSecret:          appSecret,
		useInteractiveCard: true,
		client:             lark.NewClient(appID, appSecret, lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client())),
		replayClient:       lark.NewClient(appID, appSecret, lark.WithEnableTokenCache(false), lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client())),
	}

	err := p.UpdateMessageWithStatusFooter(
		context.Background(),
		&feishuPreviewHandle{messageID: "om_preview", chatID: "oc_vchart"},
		testVChartCardJSON,
		"model · ctx",
	)
	if err != nil {
		t.Fatalf("UpdateMessageWithStatusFooter: %v", err)
	}
	if captured != strings.TrimSpace(testVChartCardJSON) {
		t.Fatalf("patched card content was rewritten\n got: %s\nwant: %s", captured, strings.TrimSpace(testVChartCardJSON))
	}
}

func TestIsCardJSON_RequiresCompleteCard2Body(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{name: "VChart card", content: " \n" + testVChartCardJSON + "\n", want: true},
		{name: "ordinary JSON", content: `{"schema":"2.0","message":"body"}`, want: false},
		{name: "legacy schema", content: `{"schema":"1.0","body":{"elements":[]}}`, want: false},
		{name: "missing elements", content: `{"schema":"2.0","body":{}}`, want: false},
		{name: "incomplete JSON", content: `{"schema":"2.0","body":{"elements":[`, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isCardJSON(tt.content); got != tt.want {
				t.Fatalf("isCardJSON() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSendPreviewStart_VChartCardEntityFailureFallsBackInline(t *testing.T) {
	const appID = "cli_vchart_fallback"
	const appSecret = "secret"
	var captured capturedMessageRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(t, w, map[string]any{"code": 0, "expire": 7200, "tenant_access_token": "t"})
		case r.URL.Path == "/open-apis/cardkit/v1/cards" && r.Method == http.MethodPost:
			writeJSON(t, w, map[string]any{"code": 400, "msg": "card entity unavailable"})
		case r.URL.Path == "/open-apis/im/v1/messages" && r.Method == http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(body, &captured); err != nil {
				t.Errorf("unmarshal fallback message request: %v", err)
				return
			}
			writeJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"message_id": "om_inline_vchart"}})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			writeJSON(t, w, map[string]any{"code": 1, "msg": "unexpected request"})
		}
	}))
	defer srv.Close()

	p := &Platform{
		platformName:       "feishu",
		domain:             srv.URL,
		appID:              appID,
		appSecret:          appSecret,
		useInteractiveCard: true,
		client:             lark.NewClient(appID, appSecret, lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client())),
		replayClient:       lark.NewClient(appID, appSecret, lark.WithEnableTokenCache(false), lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client())),
	}

	handleAny, err := p.SendPreviewStart(context.Background(), replyContext{chatID: "oc_vchart"}, testVChartCardJSON)
	if err != nil {
		t.Fatalf("SendPreviewStart: %v", err)
	}
	handle, ok := handleAny.(*feishuPreviewHandle)
	if !ok {
		t.Fatalf("preview handle type = %T, want *feishuPreviewHandle", handleAny)
	}
	if handle.cardID != "" {
		t.Fatalf("fallback handle cardID = %q, want empty", handle.cardID)
	}
	assertRawVChartRequest(t, captured)
}

func TestSendPreviewStart_VChartCardUsesCardEntity(t *testing.T) {
	const appID = "cli_vchart_entity"
	const appSecret = "secret"
	var (
		entityType string
		entityData string
		message    capturedMessageRequest
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(t, w, map[string]any{"code": 0, "expire": 7200, "tenant_access_token": "t"})
		case r.URL.Path == "/open-apis/cardkit/v1/cards" && r.Method == http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			var req struct {
				Type string `json:"type"`
				Data string `json:"data"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				t.Errorf("unmarshal card entity request: %v", err)
				return
			}
			entityType = req.Type
			entityData = req.Data
			writeJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"card_id": "card_vchart"}})
		case r.URL.Path == "/open-apis/im/v1/messages" && r.Method == http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(body, &message); err != nil {
				t.Errorf("unmarshal card message request: %v", err)
				return
			}
			writeJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"message_id": "om_entity_vchart"}})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			writeJSON(t, w, map[string]any{"code": 1, "msg": "unexpected request"})
		}
	}))
	defer srv.Close()

	p := &Platform{
		platformName:       "feishu",
		domain:             srv.URL,
		appID:              appID,
		appSecret:          appSecret,
		useInteractiveCard: true,
		client:             lark.NewClient(appID, appSecret, lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client())),
		replayClient:       lark.NewClient(appID, appSecret, lark.WithEnableTokenCache(false), lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client())),
	}

	handleAny, err := p.SendPreviewStart(context.Background(), replyContext{chatID: "oc_vchart"}, "\n"+testVChartCardJSON+"\n")
	if err != nil {
		t.Fatalf("SendPreviewStart: %v", err)
	}
	handle, ok := handleAny.(*feishuPreviewHandle)
	if !ok {
		t.Fatalf("preview handle type = %T, want *feishuPreviewHandle", handleAny)
	}
	if handle.cardID != "card_vchart" {
		t.Fatalf("preview handle cardID = %q, want card_vchart", handle.cardID)
	}
	if entityType != "card_json" {
		t.Fatalf("card entity type = %q, want card_json", entityType)
	}
	if entityData != strings.TrimSpace(testVChartCardJSON) {
		t.Fatalf("card entity data was rewritten\n got: %s\nwant: %s", entityData, strings.TrimSpace(testVChartCardJSON))
	}
	if message.MsgType != larkim.MsgTypeInteractive {
		t.Fatalf("message msg_type = %q, want interactive", message.MsgType)
	}
	if message.Content != `{"type":"card","data":{"card_id":"card_vchart"}}` {
		t.Fatalf("message content = %s, want card_id reference", message.Content)
	}
}

func TestRawVChartCard_StreamingWaitsForCompleteJSON(t *testing.T) {
	partial := `{"schema":"2.0","body":{"elements":[`
	if !isIncompleteJSONObject(partial) {
		t.Fatal("partial Card 2.0 JSON was not recognized as incomplete")
	}

	preview := buildPreviewCardJSON(partial)
	if strings.Contains(preview, partial) {
		t.Fatalf("preview exposed partial Card 2.0 JSON: %s", preview)
	}
	rich := buildRichCard(core.CardStatusWorking, "", nil, partial, true, "")
	if strings.Contains(rich, partial) {
		t.Fatalf("rich preview exposed partial Card 2.0 JSON: %s", rich)
	}

	p := &Platform{platformName: "feishu", useInteractiveCard: true}
	handle := &feishuPreviewHandle{messageID: "om_preview", cardID: "card_vchart"}
	if err := p.StreamRichCardText(context.Background(), handle, partial); err != nil {
		t.Fatalf("partial StreamRichCardText error = %v, want suppressed frame", err)
	}
	if err := p.UpdateMessage(context.Background(), handle, partial); err != nil {
		t.Fatalf("partial UpdateMessage error = %v, want no-op", err)
	}
	if err := p.StreamRichCardText(context.Background(), handle, testVChartCardJSON); !errors.Is(err, core.ErrNotSupported) {
		t.Fatalf("complete card StreamRichCardText error = %v, want ErrNotSupported for full-card update", err)
	}
}

func TestResolveRichCardMarkdown_PreservesRawVChartCardJSON(t *testing.T) {
	p := &Platform{platformName: "feishu"}
	got := p.ResolveRichCardMarkdown(context.Background(), "\n"+testVChartCardJSON+"\n", true)
	if got != strings.TrimSpace(testVChartCardJSON) {
		t.Fatalf("markdown resolver rewrote raw VChart card\n got: %s\nwant: %s", got, strings.TrimSpace(testVChartCardJSON))
	}
}
