package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/slack-go/slack"
)

type capturedCall struct {
	path   string
	text   string
	blocks []map[string]any
}

func newFooterTestServer(t *testing.T) (*httptest.Server, *[]capturedCall, *sync.Mutex) {
	t.Helper()
	var mu sync.Mutex
	var calls []capturedCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		c := capturedCall{path: r.URL.Path, text: r.FormValue("text")}
		if raw := r.FormValue("blocks"); raw != "" {
			if err := json.Unmarshal([]byte(raw), &c.blocks); err != nil {
				t.Fatalf("blocks JSON: %v", err)
			}
		}
		mu.Lock()
		calls = append(calls, c)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "channel": "C1", "ts": "1700000000.0001"})
	}))
	return srv, &calls, &mu
}

// The footer renders as a context block (small/dim) under a section with the
// body, instead of being inlined into the body as bold text.
func TestStreamingCard_FinalizeWithStatusFooterUsesContextBlock(t *testing.T) {
	srv, calls, mu := newFooterTestServer(t)
	defer srv.Close()
	card := &slackStreamingCard{client: slack.New("xoxb-test", slack.OptionAPIURL(srv.URL+"/")), channel: "C1"}

	if err := card.Update(context.Background(), "partial"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	footer := "[ctx: ~1%] · glm-5.3-flash · ~/plaud/web4"
	if err := card.FinalizeWithStatusFooter(context.Background(), "**ok**", footer); err != nil {
		t.Fatalf("FinalizeWithStatusFooter: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	last := (*calls)[len(*calls)-1]
	if last.path != "/chat.update" {
		t.Fatalf("final call path = %q, want chat.update of the posted card", last.path)
	}
	if len(last.blocks) != 2 || last.blocks[0]["type"] != "section" || last.blocks[1]["type"] != "context" {
		t.Fatalf("blocks = %#v, want [section, context]", last.blocks)
	}
	section := last.blocks[0]["text"].(map[string]any)
	if section["type"] != "mrkdwn" || section["text"] != "*ok*" {
		t.Fatalf("section text = %#v, want converted mrkdwn body", section)
	}
	elem := last.blocks[1]["elements"].([]any)[0].(map[string]any)
	// plain_text: the footer's two "~" must not be parsed as strikethrough.
	if elem["type"] != "plain_text" || elem["text"] != footer {
		t.Fatalf("context element = %#v, want plain_text footer", elem)
	}
	if !strings.Contains(last.text, "ok") || !strings.Contains(last.text, "glm-5.3-flash") {
		t.Fatalf("notification fallback text = %q, want body and footer", last.text)
	}

	// Idempotent: an identical finalize does not hit the API again.
	n := len(*calls)
	mu.Unlock()
	_ = card.FinalizeWithStatusFooter(context.Background(), "**ok**", footer)
	mu.Lock()
	if len(*calls) != n {
		t.Fatalf("duplicate finalize made %d extra calls", len(*calls)-n)
	}
}

// A body too long for a section block (3000 chars) falls back to the plain
// text path with the footer inlined, so long replies still deliver.
func TestStreamingCard_FinalizeWithStatusFooterLongBodyFallsBackInline(t *testing.T) {
	srv, calls, mu := newFooterTestServer(t)
	defer srv.Close()
	card := &slackStreamingCard{client: slack.New("xoxb-test", slack.OptionAPIURL(srv.URL+"/")), channel: "C1"}

	body := strings.Repeat("a", slackSectionMaxText+10)
	if err := card.FinalizeWithStatusFooter(context.Background(), body, "model-x"); err != nil {
		t.Fatalf("FinalizeWithStatusFooter: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	last := (*calls)[len(*calls)-1]
	if len(last.blocks) != 0 {
		t.Fatalf("blocks = %#v, want plain text fallback", last.blocks)
	}
	if !strings.HasPrefix(last.text, body) || !strings.Contains(last.text, "model-x") {
		t.Fatalf("fallback text missing body or footer (len %d)", len(last.text))
	}
}
