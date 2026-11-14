package qqbot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// ── Issue #2216: the passive-reply quota is silently swallowing messages ────
//
// QQ limits the passive replies of one incoming event (4 within 60 minutes
// for C2C). The adapter sent every chunk of a turn passively, never looked
// at the business code in the response, and stopped at the first error — so
// from the 5th message on, everything the agent said was dropped without a
// word.

// qqFakeAPI is a fake QQ Bot API that records the message requests it got.
type qqFakeAPI struct {
	mu sync.Mutex
	// requests holds the decoded body of every message request.
	requests []map[string]any
	// passiveCode is the business code returned for a request that carries
	// msg_id (0 = success).
	passiveCode int
	// activeCode is the business code returned for a request without msg_id.
	activeCode int
}

func (f *qqFakeAPI) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/getAppAccessToken" {
			_, _ = fmt.Fprint(w, `{"access_token":"test-token","expires_in":"7200"}`)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)

		f.mu.Lock()
		f.requests = append(f.requests, body)
		f.mu.Unlock()

		_, isPassive := body["msg_id"]
		code := f.activeCode
		if isPassive {
			code = f.passiveCode
		}
		if code != 0 {
			// QQ reports business failures with a 2xx status.
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"code":%d,"message":"qq fake failure"}`, code)
			return
		}
		_, _ = fmt.Fprint(w, `{"id":"msg-1","code":0}`)
	}
}

func (f *qqFakeAPI) sent() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.requests...)
}

// newQQTestPlatform wires a Platform to the fake API.
func newQQTestPlatform(t *testing.T, api *qqFakeAPI) *Platform {
	t.Helper()

	server := httptest.NewServer(api.handler(t))
	t.Cleanup(server.Close)

	origClient := core.HTTPClient
	origTokenURL := tokenURL
	origApiBase := apiBaseProduction
	t.Cleanup(func() {
		core.HTTPClient = origClient
		tokenURL = origTokenURL
		apiBaseProduction = origApiBase
	})
	core.HTTPClient = server.Client()
	tokenURL = server.URL + "/app/getAppAccessToken"
	apiBaseProduction = server.URL

	return &Platform{
		sandbox:     false,
		token:       "test-token",
		tokenExpiry: time.Now().Add(time.Hour),
	}
}

// captureLogs redirects the default logger into a buffer for the duration of
// the test.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(orig) })
	return buf
}

// TestSend_PassiveQuotaExceededResendsAsActiveMessage is the regression test
// for issue #2216: when the passive-reply quota of the incoming event is
// used up, the same content must still reach the user — as an active
// message, which does not consume the passive quota.
func TestSend_PassiveQuotaExceededResendsAsActiveMessage(t *testing.T) {
	api := &qqFakeAPI{passiveCode: qqCodePassiveReplyLimit}
	p := newQQTestPlatform(t, api)

	rctx := &replyContext{
		messageType: "c2c",
		userOpenID:  "user-123",
		eventMsgID:  "event-msg-1",
	}

	if err := p.Send(context.Background(), rctx, "final answer"); err != nil {
		t.Fatalf("Send returned error: %v", err)
	}

	requests := api.sent()
	if len(requests) != 2 {
		t.Fatalf("got %d requests, want 2 (passive attempt + active fallback)", len(requests))
	}
	if _, ok := requests[0]["msg_id"]; !ok {
		t.Fatalf("first request must be the passive one, got %v", requests[0])
	}
	if _, ok := requests[1]["msg_id"]; ok {
		t.Fatalf("the fallback request must not carry msg_id, got %v", requests[1])
	}
	if got := requests[1]["content"]; got != "final answer" {
		t.Fatalf("fallback content = %v, want %q", got, "final answer")
	}
}

// TestSend_ExpiredMsgIDResendsAsActiveMessage covers the other passive
// failure: the msg_id of the incoming event is too old to reply to.
func TestSend_ExpiredMsgIDResendsAsActiveMessage(t *testing.T) {
	api := &qqFakeAPI{passiveCode: qqCodeMsgIDExpired}
	p := newQQTestPlatform(t, api)

	rctx := &replyContext{
		messageType: "c2c",
		userOpenID:  "user-123",
		eventMsgID:  "event-msg-1",
	}

	if err := p.Send(context.Background(), rctx, "final answer"); err != nil {
		t.Fatalf("Send returned error: %v", err)
	}

	requests := api.sent()
	if len(requests) != 2 {
		t.Fatalf("got %d requests, want 2", len(requests))
	}
	if _, ok := requests[1]["msg_id"]; ok {
		t.Fatalf("the fallback request must not carry msg_id, got %v", requests[1])
	}
}

// TestSend_SuccessfulPassiveSendUnchanged makes sure the normal path is
// untouched: the message goes out passively, with msg_id and msg_seq, and
// there is no second request.
func TestSend_SuccessfulPassiveSendUnchanged(t *testing.T) {
	api := &qqFakeAPI{}
	p := newQQTestPlatform(t, api)

	rctx := &replyContext{
		messageType: "c2c",
		userOpenID:  "user-123",
		eventMsgID:  "event-msg-1",
	}

	if err := p.Send(context.Background(), rctx, "hello"); err != nil {
		t.Fatalf("Send returned error: %v", err)
	}

	requests := api.sent()
	if len(requests) != 1 {
		t.Fatalf("got %d requests, want exactly 1", len(requests))
	}
	if got := requests[0]["msg_id"]; got != "event-msg-1" {
		t.Fatalf("msg_id = %v, want %q", got, "event-msg-1")
	}
	if _, ok := requests[0]["msg_seq"]; !ok {
		t.Fatalf("the passive request must carry msg_seq, got %v", requests[0])
	}
	if got := requests[0]["content"]; got != "hello" {
		t.Fatalf("content = %v, want %q", got, "hello")
	}
}

// TestSend_BusinessCodeOn200IsNotSuccess makes sure a 2xx response that
// carries a failure code is not treated as a delivered message.
func TestSend_BusinessCodeOn200IsNotSuccess(t *testing.T) {
	api := &qqFakeAPI{passiveCode: qqCodePassiveReplyLimit, activeCode: qqCodePassiveReplyLimit}
	p := newQQTestPlatform(t, api)

	rctx := &replyContext{
		messageType: "c2c",
		userOpenID:  "user-123",
		eventMsgID:  "event-msg-1",
	}

	err := p.Send(context.Background(), rctx, "final answer")
	if err == nil {
		t.Fatal("Send must fail when the API answers with a business error code")
	}
	if !strings.Contains(err.Error(), fmt.Sprint(qqCodePassiveReplyLimit)) {
		t.Fatalf("error %q must mention the QQ error code %d", err, qqCodePassiveReplyLimit)
	}
}

// TestSend_DroppedContentIsLogged makes sure a chunk that could not be
// delivered leaves a trace: the content and the QQ error code are written to
// the error log.
func TestSend_DroppedContentIsLogged(t *testing.T) {
	api := &qqFakeAPI{passiveCode: qqCodePassiveReplyLimit, activeCode: qqCodePassiveReplyLimit}
	p := newQQTestPlatform(t, api)

	logs := captureLogs(t)

	rctx := &replyContext{
		messageType: "c2c",
		userOpenID:  "user-123",
		eventMsgID:  "event-msg-1",
	}

	if err := p.Send(context.Background(), rctx, "the final answer of the turn"); err == nil {
		t.Fatal("Send must fail when the message could not be delivered at all")
	}

	out := logs.String()
	if !strings.Contains(out, "the final answer of the turn") {
		t.Fatalf("the dropped content must be logged, got:\n%s", out)
	}
	if !strings.Contains(out, fmt.Sprint(qqCodePassiveReplyLimit)) {
		t.Fatalf("the QQ error code must be logged, got:\n%s", out)
	}
}

// TestSend_ContinuesAfterChunkFailure makes sure one failed chunk does not
// swallow the rest of the reply: the final answer of the turn must still be
// attempted.
func TestSend_ContinuesAfterChunkFailure(t *testing.T) {
	api := &qqFakeAPI{passiveCode: qqCodePassiveReplyLimit, activeCode: qqCodePassiveReplyLimit}
	p := newQQTestPlatform(t, api)

	logs := captureLogs(t)

	rctx := &replyContext{
		messageType: "c2c",
		userOpenID:  "user-123",
		eventMsgID:  "event-msg-1",
	}

	// Long enough to be split into several chunks.
	content := strings.Repeat("a", messageMaxLen+10) + "TAIL-MARKER"

	if err := p.Send(context.Background(), rctx, content); err == nil {
		t.Fatal("Send must fail when the message could not be delivered at all")
	}

	// Every chunk must have been attempted, not just the first one.
	requests := api.sent()
	if len(requests) < 4 {
		t.Fatalf("got %d requests, want every chunk to be attempted", len(requests))
	}
	if !strings.Contains(logs.String(), "TAIL-MARKER") {
		t.Fatalf("the dropped content of the last chunk must be logged, got:\n%s", logs.String())
	}
}

func TestCheckQQResponseCode(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
		wantMsg string
	}{
		{"success", `{"id":"m1","code":0}`, false, ""},
		{"no code field", `{"id":"m1"}`, false, ""},
		{"quota", `{"code":40034128,"message":"limit"}`, true, "limit"},
		{"expired msg id", `{"code":40034005,"message":"expired"}`, true, "expired"},
		{"not json", `plain text error`, false, ""},
		{"empty", ``, false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := checkQQResponseCode([]byte(tc.body))
			if tc.wantErr && err == nil {
				t.Fatalf("checkQQResponseCode(%q) = nil, want an error", tc.body)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("checkQQResponseCode(%q) = %v, want nil", tc.body, err)
			}
			if tc.wantErr && !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("error %q must mention %q", err, tc.wantMsg)
			}
		})
	}
}

func TestIsPassiveReplyLimit(t *testing.T) {
	if !isPassiveReplyLimit(&qqAPIError{Code: qqCodePassiveReplyLimit}) {
		t.Fatal("the passive reply quota error must be classified as a quota error")
	}
	if !isPassiveReplyLimit(&qqAPIError{Code: qqCodeMsgIDExpired}) {
		t.Fatal("the expired msg_id error must be classified as a quota error")
	}
	if isPassiveReplyLimit(&qqAPIError{Code: 12345}) {
		t.Fatal("an unrelated QQ error must not be classified as a quota error")
	}
	if isPassiveReplyLimit(fmt.Errorf("network down")) {
		t.Fatal("a transport error must not be classified as a quota error")
	}
}

func TestTruncateChunk(t *testing.T) {
	if got := truncateChunk("short"); got != "short" {
		t.Fatalf("truncateChunk(short) = %q, want %q", got, "short")
	}
	long := strings.Repeat("x", maxLoggedChunkLen+50)
	got := truncateChunk(long)
	if len([]rune(got)) > maxLoggedChunkLen+20 {
		t.Fatalf("truncateChunk returned %d runes, want it bounded", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "...(truncated)") {
		t.Fatalf("truncateChunk = %q, want a truncation marker", got)
	}
}
