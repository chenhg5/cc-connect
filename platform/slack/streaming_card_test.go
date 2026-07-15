package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/slack-go/slack"
)

// TestStreamingCard_FinalizeFallsBackToFreshPostOnOversize covers the
// `msg_too_long` regression observed during v1.4.0-beta.1 QA: when an agent
// reply grew past Slack's chat.update text limit (~4000 bytes), Finalize
// failed with msg_too_long and the engine had to send a duplicate fallback
// message. After the fix, Finalize detects the overflow up-front and posts
// the full reply as a fresh message via chat.postMessage (which has a much
// larger 40k limit), so the engine sees success and no duplicate is sent.
func TestStreamingCard_FinalizeFallsBackToFreshPostOnOversize(t *testing.T) {
	var (
		postCalls   int32
		updateCalls int32
		lastPosted  string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat.postMessage":
			atomic.AddInt32(&postCalls, 1)
			if err := r.ParseForm(); err != nil {
				t.Fatalf("ParseForm: %v", err)
			}
			lastPosted = r.FormValue("text")
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":      true,
				"channel": "C1",
				"ts":      "1700000000.0001",
			})
		case "/chat.update":
			atomic.AddInt32(&updateCalls, 1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":      true,
				"channel": "C1",
				"ts":      "1700000000.0001",
				"text":    "edited",
			})
		default:
			t.Fatalf("unexpected slack API path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	client := slack.New("xoxb-test", slack.OptionAPIURL(srv.URL+"/"))
	card := &slackStreamingCard{client: client, channel: "C1"}

	// First (short) update posts the card via chat.postMessage.
	if err := card.Update(context.Background(), "hello"); err != nil {
		t.Fatalf("first Update: %v", err)
	}
	if got := atomic.LoadInt32(&postCalls); got != 1 {
		t.Fatalf("postMessage calls after first update = %d, want 1", got)
	}
	if card.ts == "" {
		t.Fatal("card.ts not set after first post")
	}

	// Oversized reply: bigger than slackUpdateMaxText. Finalize must NOT
	// hit chat.update (would 413 with msg_too_long); it should fall back
	// to a fresh postMessage with the full content.
	huge := strings.Repeat("a", slackUpdateMaxText+1024)
	if err := card.Finalize(context.Background(), huge); err != nil {
		t.Fatalf("Finalize (oversized): %v", err)
	}
	if got := atomic.LoadInt32(&updateCalls); got != 0 {
		t.Fatalf("chat.update calls = %d, want 0 (oversized payload should NOT touch chat.update)", got)
	}
	if got := atomic.LoadInt32(&postCalls); got != 2 {
		t.Fatalf("postMessage calls after oversized finalize = %d, want 2 (initial + fallback)", got)
	}
	if !strings.HasPrefix(lastPosted, "aaaa") || len(lastPosted) < len(huge) {
		t.Fatalf("oversized fallback postMessage text len=%d, want >= %d and prefixed with payload", len(lastPosted), len(huge))
	}
	if card.failed {
		t.Fatal("card marked failed despite successful oversized fallback")
	}
}

// TestStreamingCard_UpdateSkipsOversizedPayload ensures that once the rendered
// content crosses the chat.update size cap, subsequent Update() ticks become
// silent no-ops (the engine sees success, the partial card stays on the prior
// snapshot, and Finalize handles the full payload via postFresh).
func TestStreamingCard_UpdateSkipsOversizedPayload(t *testing.T) {
	var (
		postCalls   int32
		updateCalls int32
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat.postMessage":
			atomic.AddInt32(&postCalls, 1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "channel": "C1", "ts": "1700000000.0001"})
		case "/chat.update":
			atomic.AddInt32(&updateCalls, 1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "channel": "C1", "ts": "1700000000.0001", "text": "edited"})
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	client := slack.New("xoxb-test", slack.OptionAPIURL(srv.URL+"/"))
	card := &slackStreamingCard{client: client, channel: "C1"}

	// First post — sets card.ts and bypasses the throttle.
	if err := card.Update(context.Background(), "hi"); err != nil {
		t.Fatalf("first Update: %v", err)
	}
	if got := atomic.LoadInt32(&postCalls); got != 1 {
		t.Fatalf("postMessage calls = %d, want 1", got)
	}

	// Drop the throttle so the next Update() is admissible, then feed it an
	// oversized payload — Update must silently skip without touching
	// chat.update.
	card.lastUpdate = card.lastUpdate.Add(-2 * cardUpdateMinInterval)
	huge := strings.Repeat("b", slackUpdateMaxText+100)
	if err := card.Update(context.Background(), huge); err != nil {
		t.Fatalf("oversized Update: %v", err)
	}
	if got := atomic.LoadInt32(&updateCalls); got != 0 {
		t.Fatalf("chat.update calls after oversized Update = %d, want 0 (silent skip)", got)
	}
}
