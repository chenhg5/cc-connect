package feishu

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	lark "github.com/larksuite/oapi-sdk-go/v3"
)

func TestQuotedInteractiveCardPreservesLinks(t *testing.T) {
	const link = "[QSim](https://example.com/issues/42?run=1&view=full)"
	cases := []struct{ name, card string }{
		{"card1_lark_md", `{"header":{"title":{"content":"Issue 42"}},"elements":[{"tag":"div","text":{"tag":"lark_md","content":"` + link + `"}}]}`},
		{"card2_markdown", `{"schema":"2.0","body":{"elements":[{"tag":"markdown","content":"` + link + `"}]}}`},
		{"raw_button_actions", `{"body":{"property":{"elements":[{"tag":"button","property":{"text":{"tag":"plain_text","property":{"content":"QSim"}},"actions":[{"type":"open_url","action":{"url":"https://example.com/issues/42?run=1&view=full"}}]}}]}}}`},
		{"raw_link", `{"body":{"property":{"elements":[{"tag":"link","property":{"content":"QSim","url":{"url":"https://example.com/issues/42?run=1&view=full"}}}]}}}`},
		{"raw_markdown_link", `{"body":{"property":{"elements":[{"tag":"markdown","property":{"elements":[{"tag":"link","property":{"content":"QSim","url":{"url":"https://example.com/issues/42?run=1&view=full"}}}]}}]}}}`},
		{"href", `{"body":{"elements":[{"tag":"link","content":"QSim","href":"https://example.com/issues/42?run=1&view=full"}]}}`},
		{"button_default_url", `{"body":{"property":{"elements":[{"tag":"button","property":{"text":{"tag":"plain_text","property":{"content":"QSim"}},"behaviors":[{"type":"openUrl","default_url":"https://example.com/issues/42?run=1&view=full"}]}}]}}}`},
		{"flat_button_default_url", `{"schema":"2.0","body":{"elements":[{"tag":"button","text":{"tag":"plain_text","content":"QSim"},"behaviors":[{"type":"open_url","default_url":"https://example.com/issues/42?run=1&view=full"}]}]}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := `{"json_card":` + strconv.Quote(tc.card) + `}`
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/open-apis/auth/v3/tenant_access_token/internal":
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "expire": 7200, "tenant_access_token": "quoted-card-token"})
				case "/open-apis/im/v1/messages/om_quoted_card":
					if r.URL.Query().Get("card_msg_content_type") != "raw_card_content" {
						t.Error("quote must request raw_card_content")
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"items": []any{map[string]any{"msg_type": "interactive", "body": map[string]any{"content": raw}}}}})
				default:
					t.Errorf("unexpected request: %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			p := &Platform{client: lark.NewClient("cli_quoted_card", "test-secret", lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client()))}
			msg := p.fetchSingleMessage(context.Background(), "om_quoted_card")
			if msg == nil || !strings.Contains(msg.text, link) {
				t.Fatalf("quoted card = %+v, want %q", msg, link)
			}
			if got := extractInteractiveCardText(raw); !strings.Contains(got, link) {
				t.Errorf("fallback text = %q, want %q", got, link)
			}
		})
	}
}
