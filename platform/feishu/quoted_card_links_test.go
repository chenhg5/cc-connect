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
	cases := []struct {
		name, card, wantExact string
		wantContains          string
	}{
		{"card1_lark_md", `{"header":{"title":{"content":"Issue 42"}},"elements":[{"tag":"div","text":{"tag":"lark_md","content":"` + link + `"}}]}`, "", link},
		{"parentheses_url_preserved_for_agent", `{"elements":[{"tag":"a","text":"QSim","href":"https://en.wikipedia.org/wiki/Foo_(bar)"}]}`, "[QSim](https://en.wikipedia.org/wiki/Foo_(bar))", "https://en.wikipedia.org/wiki/Foo_(bar)"},
		{"card2_markdown", `{"schema":"2.0","body":{"elements":[{"tag":"markdown","content":"` + link + `"}]}}`, link, link},
		{"raw_button_actions", `{"body":{"property":{"elements":[{"tag":"button","property":{"text":{"tag":"plain_text","property":{"content":"QSim"}},"actions":[{"type":"open_url","action":{"url":"https://example.com/issues/42?run=1&view=full"}}]}}]}}}`, link, link},
		{"raw_link", `{"body":{"property":{"elements":[{"tag":"link","property":{"content":"QSim","url":{"url":"https://example.com/issues/42?run=1&view=full"}}}]}}}`, link, link},
		{"raw_markdown_link", `{"body":{"property":{"elements":[{"tag":"markdown","property":{"elements":[{"tag":"link","property":{"content":"QSim","url":{"url":"https://example.com/issues/42?run=1&view=full"}}}]}}]}}}`, link, link},
		{"href", `{"body":{"elements":[{"tag":"link","content":"QSim","href":"https://example.com/issues/42?run=1&view=full"}]}}`, link, link},
		{"button_default_url", `{"body":{"property":{"elements":[{"tag":"button","property":{"text":{"tag":"plain_text","property":{"content":"QSim"}},"behaviors":[{"type":"openUrl","default_url":"https://example.com/issues/42?run=1&view=full"}]}}]}}}`, link, link},
		{"flat_button_default_url", `{"schema":"2.0","body":{"elements":[{"tag":"button","text":{"tag":"plain_text","content":"QSim"},"behaviors":[{"type":"open_url","default_url":"https://example.com/issues/42?run=1&view=full"}]}]}}`, link, link},
		{"a_lark_md_with_href_no_double_wrap", `{"elements":[{"tag":"a","text":{"tag":"lark_md","content":"` + link + `"},"href":"https://example.com/issues/42?run=1&view=full"}]}`, link, link},
		{"a_multi_url", `{"elements":[{"tag":"a","text":{"tag":"plain_text","content":"QSim"},"multi_url":{"url":"https://example.com/issues/42?run=1&view=full"}}]}`, link, link},
		{"property_text_label", `{"body":{"property":{"elements":[{"tag":"a","property":{"text":{"tag":"plain_text","content":"QSim"},"url":{"url":"https://example.com/issues/42?run=1&view=full"}}}]}}}`, link, link},
		{"property_text_nested_content", `{"body":{"property":{"elements":[{"tag":"a","property":{"text":{"tag":"plain_text","property":{"content":"QSim"}},"url":{"url":"https://example.com/issues/42?run=1&view=full"}}}]}}}`, link, link},
		{"link_tag_property_text", `{"body":{"property":{"elements":[{"tag":"link","property":{"text":{"tag":"plain_text","content":"QSim"},"url":{"url":"https://example.com/issues/42?run=1&view=full"}}}]}}}`, link, link},
		{"no_link_plain_label", `{"schema":"2.0","body":{"elements":[{"tag":"markdown","content":"plain label only"}]}}`, "plain label only", "plain label only"},
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
			if msg == nil || !strings.Contains(msg.text, tc.wantContains) {
				t.Fatalf("quoted card = %+v, want contains %q", msg, tc.wantContains)
			}
			got := extractInteractiveCardText(raw)
			if tc.wantExact != "" && got != tc.wantExact {
				t.Errorf("fallback text = %q, want exact %q", got, tc.wantExact)
			}
			if !strings.Contains(got, tc.wantContains) {
				t.Errorf("fallback text = %q, want contains %q", got, tc.wantContains)
			}
			if tc.name == "no_link_plain_label" && (strings.Contains(got, "[") || strings.Contains(got, "](")) {
				t.Errorf("fallback text = %q, want no markdown link brackets", got)
			}
			if tc.name == "a_lark_md_with_href_no_double_wrap" && strings.Contains(got, "[[") {
				t.Errorf("fallback text = %q, double-wrapped markdown link", got)
			}
		})
	}
}

func TestQuotedInteractiveCardEmptyPropertyTextFallsBack(t *testing.T) {
	for _, tag := range []string{"a", "link"} {
		for _, empty := range []string{`{}`, `null`, `""`} {
			for _, url := range []string{"", "https://example.com/x"} {
				t.Run(tag+"/"+empty+"/"+url, func(t *testing.T) {
					card := `{"elements":[{"tag":` + strconv.Quote(tag) + `,"text":{"tag":"plain_text","content":"QSim"},"property":{"text":` + empty + `},"href":` + strconv.Quote(url) + `}]}`
					want := "QSim"
					if url != "" {
						want = "[QSim](" + url + ")"
					}
					if got := extractInteractiveCardText(card); got != want {
						t.Fatalf("quoted card = %q, want %q", got, want)
					}
				})
			}
		}
	}
}

func TestQuotedInteractiveCardEscapesLabelAndPreservesURL(t *testing.T) {
	cases := []struct {
		label, url, want string
	}{
		{"Q]Sim", "https://example.com/x", `[Q\]Sim](https://example.com/x)`},
		{"Q[Sim", "https://example.com/x", `[Q\[Sim](https://example.com/x)`},
		{`Q\Sim`, "https://example.com/x", `[Q\\Sim](https://example.com/x)`},
		{"QSim", "https://example.com/a)b", `[QSim](https://example.com/a)b)`},
		{"QSim", "https://example.com/a(b", `[QSim](https://example.com/a(b)`},
		{"QSim", "https://en.wikipedia.org/wiki/Foo_(bar)", `[QSim](https://en.wikipedia.org/wiki/Foo_(bar))`},
		{"QSim", "https://example.com/a%28b%29c", `[QSim](https://example.com/a%28b%29c)`},
	}
	for _, tc := range cases {
		t.Run(tc.label+"/"+tc.url, func(t *testing.T) {
			card := `{"elements":[{"tag":"a","text":` + strconv.Quote(tc.label) + `,"href":` + strconv.Quote(tc.url) + `}]}`
			if got := extractInteractiveCardText(card); got != tc.want {
				t.Fatalf("quoted card = %q, want %q", got, tc.want)
			}
		})
	}
}
