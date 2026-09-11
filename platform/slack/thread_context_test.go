package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/slack-go/slack"
)

// newTestPlatformSlackContext assembles a Platform wired against a stub HTTP
// server that pretends to be Slack. Tests use the returned Platform to drive
// fetchThreadContext / maybeFetchThreadContext / formatThreadContext.
//
// apiURL is the httptest.Server URL the Slack client should target. We
// override the default API URL via OptionAPIURL so every Slack API call goes
// to our handler instead of slack.com. The bot token is a placeholder — the
// handler ignores the value.
//
// The slack SDK appends method names to the configured endpoint (no path
// joining), so we normalise the URL to end with a trailing slash + "/api/"
// to match the production layout (https://slack.com/api/<method>).
func newTestPlatformSlackContext(apiURL string, opts ...func(*Platform)) *Platform {
	endpoint := strings.TrimRight(apiURL, "/") + "/api/"
	p := &Platform{
		client: slack.New("xoxb-test-token",
			slack.OptionAPIURL(endpoint),
		),
		threadContextEnabled:     true,
		threadContextMaxMessages: 20,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// TestFormatThreadContext covers the pure formatter: empty input,
// single-message shape (backward-compat), multi-message shape, bot-message
// filtering, blank-text filtering, and the maxMessages cap (most recent N
// messages).
func TestFormatThreadContext(t *testing.T) {
	cases := []struct {
		name        string
		messages    []slack.Message
		maxMessages int
		wantEmpty   bool
		wantSubstr  []string
	}{
		{
			name:        "empty",
			messages:    nil,
			maxMessages: 5,
			wantEmpty:   true,
		},
		{
			name: "single human message uses legacy shape",
			messages: []slack.Message{
				{Msg: slack.Msg{User: "U1", Text: "hello world"}},
			},
			maxMessages: 5,
			wantSubstr:  []string{"[Thread reply from U1]:", "hello world"},
		},
		{
			name: "bot message dropped",
			messages: []slack.Message{
				{Msg: slack.Msg{User: "U1", Text: "human says hi"}},
				{Msg: slack.Msg{User: "B1", Text: "bot noise", BotID: "B1"}},
				{Msg: slack.Msg{User: "U2", Text: "another human"}},
			},
			maxMessages: 10,
			wantSubstr:  []string{"human says hi", "another human"},
		},
		{
			name: "subtype bot_message also dropped",
			messages: []slack.Message{
				{Msg: slack.Msg{User: "U1", Text: "human", SubType: ""}},
				{Msg: slack.Msg{User: "B2", Text: "app integration", SubType: "bot_message"}},
			},
			maxMessages: 10,
			wantSubstr:  []string{"human"},
		},
		{
			name: "blank text dropped",
			messages: []slack.Message{
				{Msg: slack.Msg{User: "U1", Text: ""}},
				{Msg: slack.Msg{User: "U2", Text: "   "}},
				{Msg: slack.Msg{User: "U3", Text: "real"}},
			},
			maxMessages: 10,
			wantSubstr:  []string{"real"},
		},
		{
			name: "all bots returns empty",
			messages: []slack.Message{
				{Msg: slack.Msg{User: "B1", Text: "x", BotID: "B1"}},
			},
			maxMessages: 5,
			wantEmpty:   true,
		},
		{
			name: "multi-message uses numbered chain",
			messages: []slack.Message{
				{Msg: slack.Msg{User: "U1", Text: "first"}},
				{Msg: slack.Msg{User: "U2", Text: "second"}},
				{Msg: slack.Msg{User: "U1", Text: "third"}},
			},
			maxMessages: 10,
			wantSubstr:  []string{"--- Slack thread (3 messages) ---", "[1] U1:", "[2] U2:", "[3] U1:", "first", "second", "third"},
		},
		{
			name: "maxMessages caps to most recent N",
			messages: []slack.Message{
				{Msg: slack.Msg{User: "U1", Text: "oldest"}},
				{Msg: slack.Msg{User: "U2", Text: "middle"}},
				{Msg: slack.Msg{User: "U1", Text: "newest"}},
			},
			maxMessages: 2,
			wantSubstr:  []string{"--- Slack thread (2 messages) ---", "middle", "newest"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatThreadContext(tc.messages, nil, tc.maxMessages)
			if tc.wantEmpty {
				if got != "" {
					t.Fatalf("expected empty, got %q", got)
				}
				return
			}
			if got == "" {
				t.Fatalf("expected non-empty, got empty")
			}
			for _, sub := range tc.wantSubstr {
				if !strings.Contains(got, sub) {
					t.Fatalf("missing %q in:\n%s", sub, got)
				}
			}
		})
	}
}

// TestFetchThreadContext_Success drives the helper through a fake Slack
// server, asserting it returns a formatted transcript when the API succeeds.
func TestFetchThreadContext_Success(t *testing.T) {
	messages := []slack.Message{
		{Msg: slack.Msg{User: "U1", Text: "hello", Timestamp: "1000.0001"}},
		{Msg: slack.Msg{User: "U2", Text: "world", Timestamp: "1000.0002"}},
		{Msg: slack.Msg{User: "B1", Text: "bot reply", Timestamp: "1000.0003", BotID: "B1"}},
	}
	var repliesHits, usersHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// slack-go's auto name resolution will trigger one users.info per
		// human user. We track those separately so we can assert both:
		// 1) conversations.replies was called exactly once (no extra
		//    pagination round-trips on the main fetch), and
		// 2) the SDK was free to do as many users.info calls as it likes.
		switch r.URL.Path {
		case "/api/conversations.replies":
			atomic.AddInt32(&repliesHits, 1)
			_ = r.ParseForm()
			if got := r.FormValue("channel"); got != "C123" {
				t.Errorf("channel = %q, want C123", got)
			}
			if got := r.FormValue("ts"); got != "1000.0000" {
				t.Errorf("ts = %q, want 1000.0000 (thread root)", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":                true,
				"messages":          messages,
				"has_more":          false,
				"response_metadata": map[string]any{"next_cursor": ""},
			})
		case "/api/users.info":
			atomic.AddInt32(&usersHits, 1)
			// Minimal users.info stub — slack-go reads .RealName / .Profile.DisplayName.
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":      true,
				"user":    map[string]any{"id": "U?", "name": "stub", "real_name": "stub"},
			})
		default:
			http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	p := newTestPlatformSlackContext(srv.URL)
	got, err := p.fetchThreadContext(context.Background(), "C123", "1000.0000", 20)
	if err != nil {
		t.Fatalf("fetchThreadContext: %v", err)
	}
	if got == "" {
		t.Fatal("expected non-empty transcript")
	}
	// Bot message must be excluded.
	if strings.Contains(got, "bot reply") {
		t.Errorf("bot message leaked into thread context: %s", got)
	}
	if !strings.Contains(got, "hello") || !strings.Contains(got, "world") {
		t.Errorf("missing human messages in:\n%s", got)
	}
	if got := atomic.LoadInt32(&repliesHits); got != 1 {
		t.Errorf("expected exactly 1 conversations.replies call, got %d", got)
	}
	if got := atomic.LoadInt32(&usersHits); got < 1 {
		t.Errorf("expected at least 1 users.info call for name resolution, got %d", got)
	}
}

// TestFetchThreadContext_APIFailure ensures a Slack API error returns
// without panicking. fetchThreadContext itself does NOT increment the
// failure metric — that is owned by maybeFetchThreadContext's
// logThreadContextFailure wrapper — so direct callers can decide how to
// observe failures. The metric must therefore stay flat for this test.
func TestFetchThreadContext_APIFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Slack returns 200 OK with {ok:false, error:"..."} for most
		// failures. Use that shape so the SDK doesn't retry.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":    false,
			"error": "missing_scope",
		})
	}))
	defer srv.Close()

	p := newTestPlatformSlackContext(srv.URL)
	failBefore := p.metricThreadContextFetchFailedTotal.Load()

	_, err := p.fetchThreadContext(context.Background(), "C123", "1000.0000", 20)
	if err == nil {
		t.Fatal("expected error on missing_scope, got nil")
	}
	if !strings.Contains(err.Error(), "missing_scope") && !strings.Contains(err.Error(), "conversations.replies") {
		t.Errorf("error missing context: %v", err)
	}
	failAfter := p.metricThreadContextFetchFailedTotal.Load()
	if failAfter != failBefore {
		t.Errorf("failure metric should not change on direct fetchThreadContext; before=%d after=%d",
			failBefore, failAfter)
	}
}

// TestFetchThreadContext_PaginationCapsAtMax ensures the helper stops asking
// for more pages once it has enough candidates to fill the cap. We feed
// exactly the threshold and a cursor so the helper would otherwise loop.
func TestFetchThreadContext_PaginationCapsAtMax(t *testing.T) {
	var pageHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&pageHits, 1)
		w.Header().Set("Content-Type", "application/json")
		// Always advertise a next_cursor so the helper must actively stop.
		// The candidate messages stay under the cap (maxMessages*2) so the
		// loop breaks on length, not on empty cursor.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":       true,
			"messages": []slack.Message{{Msg: slack.Msg{User: "U1", Text: "m"}}},
			"has_more": true,
			"response_metadata": map[string]any{
				"next_cursor": "next",
			},
		})
	}))
	defer srv.Close()

	p := newTestPlatformSlackContext(srv.URL, func(p *Platform) {
		p.threadContextMaxMessages = 5
	})
	if _, err := p.fetchThreadContext(context.Background(), "C123", "1000.0000", 5); err != nil {
		t.Fatalf("fetchThreadContext: %v", err)
	}
	// pageSize is 50, threshold is maxMessages*2=10, so first page (1 msg)
	// is below the threshold and helper asks again. Second page also 1 msg
	// (total 2 < 10), still asks again. Third page would push it to 3 but
	// the threshold is checked AFTER appending — so it loops until either
	// len(collected) >= 10 OR next_cursor is empty. We always advertise
	// cursor, so the helper hits the length threshold eventually.
	if got := atomic.LoadInt32(&pageHits); got < 2 || got > 20 {
		t.Errorf("unexpected page count %d (want 2-20)", got)
	}
}

// TestFetchThreadContext_OnlyBotsReturnsEmpty ensures the helper returns ""
// (not an error) when the thread exists but contains only bot messages.
// Core should never see an "empty transcript" error.
func TestFetchThreadContext_OnlyBotsReturnsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":       true,
			"messages": []slack.Message{{Msg: slack.Msg{User: "B1", Text: "x", BotID: "B1"}}},
			"has_more": false,
			"response_metadata": map[string]any{"next_cursor": ""},
		})
	}))
	defer srv.Close()

	p := newTestPlatformSlackContext(srv.URL)
	got, err := p.fetchThreadContext(context.Background(), "C123", "1000.0000", 20)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty transcript, got %q", got)
	}
}

// TestFetchThreadContext_NilClient ensures fetchThreadContext does not
// nil-deref when the SDK client was never constructed (defensive path for
// synthetic fixtures / misconfigured tests).
func TestFetchThreadContext_NilClient(t *testing.T) {
	p := &Platform{threadContextMaxMessages: 20}
	_, err := p.fetchThreadContext(context.Background(), "C", "1000.0000", 20)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "client not initialised") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestFetchThreadContext_EmptyInputsRejected ensures the helper validates
// channelID and threadTS before hitting the network.
func TestFetchThreadContext_EmptyInputsRejected(t *testing.T) {
	p := newTestPlatformSlackContext("http://unused")
	if _, err := p.fetchThreadContext(context.Background(), "", "1000.0000", 20); err == nil {
		t.Error("expected error for empty channel")
	}
	if _, err := p.fetchThreadContext(context.Background(), "C", "", 20); err == nil {
		t.Error("expected error for empty thread_ts")
	}
}

// TestMaybeFetchThreadContext_Disabled ensures the soft-degrading wrapper
// returns "" when the feature is toggled off, without touching the network.
func TestMaybeFetchThreadContext_Disabled(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.Error(w, "should not be called", http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := newTestPlatformSlackContext(srv.URL, func(p *Platform) {
		p.threadContextEnabled = false
	})
	if got := p.maybeFetchThreadContext("C", "1000.0000"); got != "" {
		t.Fatalf("expected empty when disabled, got %q", got)
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Fatalf("Slack API was called %d times; should be 0 when disabled", hits)
	}
}

// TestMaybeFetchThreadContext_NoThread ensures top-level messages skip the
// fetch entirely — Slack threads only exist when ThreadTimeStamp is set.
func TestMaybeFetchThreadContext_NoThread(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
	}))
	defer srv.Close()

	p := newTestPlatformSlackContext(srv.URL)
	if got := p.maybeFetchThreadContext("C", ""); got != "" {
		t.Fatalf("expected empty for top-level, got %q", got)
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Fatalf("Slack API was called %d times; should be 0 for top-level message", hits)
	}
}

// TestMaybeFetchThreadContext_APIFailureReturnsEmpty ensures the wrapper
// swallows errors and returns "" so the main dispatch flow is not blocked.
func TestMaybeFetchThreadContext_APIFailureReturnsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Slack-style error response with HTTP 200 OK. Returning a 5xx would
		// trigger the SDK's internal retry loop, which we don't want to
		// fight in this test.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":    false,
			"error": "internal_error",
		})
	}))
	defer srv.Close()

	p := newTestPlatformSlackContext(srv.URL)
	failBefore := p.metricThreadContextFetchFailedTotal.Load()
	if got := p.maybeFetchThreadContext("C", "1000.0000"); got != "" {
		t.Fatalf("expected empty on failure, got %q", got)
	}
	if p.metricThreadContextFetchFailedTotal.Load() != failBefore+1 {
		t.Fatal("failure metric not incremented")
	}
}

// TestSlackOptionsForThreadContext ensures New() parses the new config
// knobs correctly: defaults, overrides, and clamping at the hard cap.
func TestSlackOptionsForThreadContext(t *testing.T) {
	cases := []struct {
		name            string
		opts            map[string]any
		wantEnabled     bool
		wantMaxMessages int
	}{
		{
			name:            "all defaults",
			opts:            map[string]any{"bot_token": "xoxb", "app_token": "xapp"},
			wantEnabled:     true,
			wantMaxMessages: defaultSlackThreadContextMaxMessages,
		},
		{
			name: "explicit disable",
			opts: map[string]any{
				"bot_token": "xoxb", "app_token": "xapp",
				"thread_context_enabled": false,
			},
			wantEnabled:     false,
			wantMaxMessages: defaultSlackThreadContextMaxMessages,
		},
		{
			name: "smaller cap",
			opts: map[string]any{
				"bot_token": "xoxb", "app_token": "xapp",
				"thread_context_max_messages": 5,
			},
			wantEnabled:     true,
			wantMaxMessages: 5,
		},
		{
			name: "over-cap clamped to hard cap",
			opts: map[string]any{
				"bot_token": "xoxb", "app_token": "xapp",
				"thread_context_max_messages": 99999,
			},
			wantEnabled:     true,
			wantMaxMessages: slackThreadContextMaxMessagesHardCap,
		},
		{
			name: "zero clamped to 1",
			opts: map[string]any{
				"bot_token": "xoxb", "app_token": "xapp",
				"thread_context_max_messages": 0,
			},
			wantEnabled:     true,
			wantMaxMessages: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := New(tc.opts)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			sp, ok := p.(*Platform)
			if !ok {
				t.Fatalf("unexpected platform type %T", p)
			}
			if sp.threadContextEnabled != tc.wantEnabled {
				t.Errorf("threadContextEnabled = %v, want %v",
					sp.threadContextEnabled, tc.wantEnabled)
			}
			if sp.threadContextMaxMessages != tc.wantMaxMessages {
				t.Errorf("threadContextMaxMessages = %d, want %d",
					sp.threadContextMaxMessages, tc.wantMaxMessages)
			}
		})
	}
}

// TestFormatThreadContext_UsesDisplayName covers the helper that resolves a
// user ID to a cached display name. Without a populated userNameCache the
// formatter falls back to the user ID (still useful).
func TestFormatThreadContext_UsesDisplayName(t *testing.T) {
	p := newTestPlatformSlackContext("http://unused")
	// Pre-populate the cache so displayNameFor resolves "U1" -> "Alice".
	p.userNameCache.Store("U1", "Alice")

	got := formatThreadContext(
		[]slack.Message{{Msg: slack.Msg{User: "U1", Text: "hi"}}},
		p, 5,
	)
	if !strings.Contains(got, "[Thread reply from Alice]:") {
		t.Fatalf("expected display name Alice in output, got:\n%s", got)
	}
	// Unknown user falls back to ID.
	got2 := formatThreadContext(
		[]slack.Message{{Msg: slack.Msg{User: "U_unknown", Text: "hi"}}},
		p, 5,
	)
	if !strings.Contains(got2, "[Thread reply from U_unknown]:") {
		t.Fatalf("expected fallback to user ID, got:\n%s", got2)
	}
}

// TestFormatThreadContext_RespectsMostRecent verifies the cap picks the
// newest N messages in chronological order, not the oldest N.
func TestFormatThreadContext_RespectsMostRecent(t *testing.T) {
	msgs := make([]slack.Message, 10)
	for i := range msgs {
		msgs[i] = slack.Message{Msg: slack.Msg{
			User:      fmt.Sprintf("U%d", i),
			Text:      fmt.Sprintf("msg-%d", i),
			Timestamp: fmt.Sprintf("1000.%04d", i),
		}}
	}
	got := formatThreadContext(msgs, nil, 3)
	// Newest 3 are msg-7, msg-8, msg-9.
	for _, want := range []string{"msg-7", "msg-8", "msg-9"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in cap-to-newest output:\n%s", want, got)
		}
	}
	for _, banned := range []string{"msg-0", "msg-1", "msg-6"} {
		if strings.Contains(got, banned) {
			t.Errorf("cap should have dropped %q:\n%s", banned, got)
		}
	}
}

// Note: We rely on TestFetchThreadContext_Success et al. to verify the
// slack-go client actually routes to our test server — those tests pass
// real Slack API methods (conversations.replies, users.info) and observe
// the requests on the stub, which is more thorough than a synthetic
// auth.test sanity check.
