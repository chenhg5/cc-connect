package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// fakeOpencodeServer is a stub of the `opencode serve` HTTP API: it records the
// requests a session makes and lets the test push SSE events.
type fakeOpencodeServer struct {
	t  *testing.T
	ts *httptest.Server

	mu           sync.Mutex
	createdBody  map[string]any
	createdDir   string
	createCalls  int
	messageBodys []map[string]any
	abortCalls   int
	// holdMessages makes POST /session/{id}/message block until releaseTurn, so a
	// test can drive a second Send while a turn is still in flight. Tests that do
	// not opt in get an immediate response — otherwise a failed assertion would
	// leave the handler parked and hang the httptest shutdown instead of failing.
	holdMessages   atomic.Bool
	releaseMessage chan struct{}
	shutdownOnce   sync.Once

	subscribersMu sync.Mutex
	subscribers   []chan string
}

func newFakeOpencodeServer(t *testing.T) *fakeOpencodeServer {
	f := &fakeOpencodeServer{t: t, releaseMessage: make(chan struct{}, 8)}
	mux := http.NewServeMux()
	mux.HandleFunc("/session", f.handleCreate)
	mux.HandleFunc("/session/", f.handleSession)
	mux.HandleFunc("/event", f.handleEvents)
	f.ts = httptest.NewServer(mux)
	t.Cleanup(f.shutdown)
	return f
}

func (f *fakeOpencodeServer) srv() *opencodeServer {
	return &opencodeServer{baseURL: f.ts.URL, password: "test-password"}
}

func (f *fakeOpencodeServer) handleCreate(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	f.createCalls++
	f.createdBody = body
	f.createdDir = r.URL.Query().Get("directory")
	f.mu.Unlock()
	writeJSONResponse(w, map[string]any{"id": "ses_stub"})
}

func (f *fakeOpencodeServer) handleSession(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/session/")
	switch {
	case strings.HasSuffix(path, "/abort"):
		f.mu.Lock()
		f.abortCalls++
		f.mu.Unlock()
		writeJSONResponse(w, map[string]any{})
	case strings.HasSuffix(path, "/message"):
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.messageBodys = append(f.messageBodys, body)
		f.mu.Unlock()
		// Hold the response until the test releases it, mimicking a turn that is
		// still running.
		if f.holdMessages.Load() {
			<-f.releaseMessage
		}
		writeJSONResponse(w, map[string]any{"info": map[string]any{"role": "assistant"}})
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeOpencodeServer) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	ch := make(chan string, 64)
	f.subscribersMu.Lock()
	f.subscribers = append(f.subscribers, ch)
	f.subscribersMu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	for {
		select {
		case payload, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		case <-r.Context().Done():
			f.subscribersMu.Lock()
			for i, sub := range f.subscribers {
				if sub == ch {
					f.subscribers = append(f.subscribers[:i], f.subscribers[i+1:]...)
					break
				}
			}
			f.subscribersMu.Unlock()
			return
		}
	}
}

// dropStreams closes every open SSE stream but keeps serving new ones, which is
// how a server restart looks to the session.
func (f *fakeOpencodeServer) dropStreams() {
	f.subscribersMu.Lock()
	for _, sub := range f.subscribers {
		close(sub)
	}
	f.subscribers = nil
	f.subscribersMu.Unlock()
}

// subscriberCount reports how many live SSE streams exist.
func (f *fakeOpencodeServer) subscriberCount() int {
	f.subscribersMu.Lock()
	defer f.subscribersMu.Unlock()
	return len(f.subscribers)
}

// shutdown releases every blocked handler and stops the stub server, so test
// cleanups can never park on an in-flight request.
func (f *fakeOpencodeServer) shutdown() {
	f.shutdownOnce.Do(func() {
		f.releaseMessage <- struct{}{}
		f.releaseMessage <- struct{}{}
		f.subscribersMu.Lock()
		for _, sub := range f.subscribers {
			close(sub)
		}
		f.subscribers = nil
		f.subscribersMu.Unlock()
		f.ts.Close()
	})
}

// emit pushes one SSE event to every subscriber and waits until the session has
// consumed it (via waitForEvent) — the test owns that sequencing.
func (f *fakeOpencodeServer) emit(payload map[string]any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		f.t.Fatalf("marshal event: %v", err)
	}
	f.subscribersMu.Lock()
	subs := append([]chan string(nil), f.subscribers...)
	f.subscribersMu.Unlock()
	for _, sub := range subs {
		select {
		case sub <- string(raw):
		default:
		}
	}
}

func (f *fakeOpencodeServer) releaseTurn() {
	f.releaseMessage <- struct{}{}
}

func (f *fakeOpencodeServer) counts() (create, abort int, messages int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.createCalls, f.abortCalls, len(f.messageBodys)
}

func (f *fakeOpencodeServer) lastMessage() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.messageBodys) == 0 {
		return nil
	}
	return f.messageBodys[len(f.messageBodys)-1]
}

func writeJSONResponse(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func partUpdated(sessionID string, part map[string]any) map[string]any {
	return map[string]any{
		"type":       "message.part.updated",
		"properties": map[string]any{"sessionID": sessionID, "part": part},
	}
}

func assistantMessageUpdated(sessionID, messageID string) map[string]any {
	return map[string]any{
		"type": "message.updated",
		"properties": map[string]any{
			"sessionID": sessionID,
			"info":      map[string]any{"id": messageID, "role": "assistant", "sessionID": sessionID},
		},
	}
}

// collectEvents drains up to n events, failing the test on timeout.
func collectEvents(t *testing.T, ch <-chan core.Event, n int, timeout time.Duration) []core.Event {
	t.Helper()
	var got []core.Event
	deadline := time.After(timeout)
	for len(got) < n {
		select {
		case evt, ok := <-ch:
			if !ok {
				t.Fatalf("event channel closed after %d events, want %d", len(got), n)
			}
			got = append(got, evt)
		case <-deadline:
			t.Fatalf("timed out waiting for %d events, got %d (%+v)", n, len(got), got)
		}
	}
	return got
}

func newTestServerSession(t *testing.T, f *fakeOpencodeServer, resumeID string) *serverSession {
	return newTestServerSessionWithMode(t, f, resumeID, "yolo")
}

func newTestServerSessionWithMode(t *testing.T, f *fakeOpencodeServer, resumeID, mode string) *serverSession {
	t.Helper()
	cfg := opencodeServeConfig{cmd: "opencode", workDir: "/tmp/ws"}
	s, err := newServerSessionOn(context.Background(), f.srv(), cfg, "openai/gpt-5.6-sol", mode, "", resumeID)
	if err != nil {
		t.Fatalf("newServerSessionOn: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// The engine must not see the user's own prompt echoed back as reply text, and a
// mid-turn Send must not create a second session or abort the running turn.
func TestServerSession_MidTurnSendAppendsToRunningTurn(t *testing.T) {
	f := newFakeOpencodeServer(t)
	f.holdMessages.Store(true)
	s := newTestServerSession(t, f, "")

	if err := s.Send("first prompt", "m1", nil, nil); err != nil {
		t.Fatalf("first Send: %v", err)
	}
	// Wait until the first prompt reached the server, then inject mid-turn.
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, _, msgs := f.counts()
		if msgs == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("first prompt never reached the server")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := s.Send("/ps supplement text", "m2", nil, nil); err != nil {
		t.Fatalf("mid-turn Send: %v", err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for {
		_, _, msgs := f.counts()
		if msgs == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("mid-turn supplement never reached the server")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if create, abort, _ := f.counts(); create != 1 || abort != 0 {
		t.Fatalf("create=%d abort=%d, want create=1 abort=0 (a supplement must not restart or abort the turn)", create, abort)
	}

	parts, _ := f.lastMessage()["parts"].([]any)
	if len(parts) != 1 {
		t.Fatalf("supplement parts = %v, want one text part", f.lastMessage()["parts"])
	}
	if text, _ := parts[0].(map[string]any)["text"].(string); text != "/ps supplement text" {
		t.Fatalf("supplement text = %q", text)
	}

	// Absence checks need both turns released (the POST returns once the turn is
	// over) and the session closed.
	f.releaseTurn()
	f.releaseTurn()
	deadline = time.Now().Add(3 * time.Second)
	for s.turnInFlight.Load() {
		if time.Now().After(deadline) {
			t.Fatalf("turn still marked in flight after both prompts completed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = s.Close()
	if _, abort, _ := f.counts(); abort != 0 {
		t.Fatalf("abort called %d times after a non-interrupting supplement", abort)
	}
}

func TestServerSession_SendCreatesSessionWithWorkspaceAndPermissions(t *testing.T) {
	f := newFakeOpencodeServer(t)
	s := newTestServerSession(t, f, "")

	if err := s.Send("hello", "m1", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		f.mu.Lock()
		created := f.createCalls
		dir := f.createdDir
		body := f.createdBody
		f.mu.Unlock()
		if created == 1 {
			if dir != "/tmp/ws" {
				t.Fatalf("session created with directory=%q, want /tmp/ws", dir)
			}
			perms, _ := body["permission"].([]any)
			if len(perms) == 0 {
				t.Fatalf("session created without a permission ruleset (yolo parity): %v", body)
			}
			// POST /session rejects the message-shaped model ({providerID, modelID})
			// with a bare 400, so the model must travel with each message instead.
			if _, ok := body["model"]; ok {
				t.Fatalf("session create body must not carry a model: %v", body)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("session was never created")
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.releaseTurn()
}

func TestServerSession_MapsAssistantPartsAndSuppressesUserEcho(t *testing.T) {
	f := newFakeOpencodeServer(t)
	s := newTestServerSession(t, f, "ses_stub")

	if err := s.Send("run a tool", "m1", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	// Wait for the SSE subscriber to attach.
	deadline := time.Now().Add(3 * time.Second)
	for {
		f.subscribersMu.Lock()
		n := len(f.subscribers)
		f.subscribersMu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("session never subscribed to the event stream")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The user's own prompt arrives as a text part of a *user* message: it must
	// not be replayed as reply text.
	f.emit(partUpdated("ses_stub", map[string]any{
		"id": "prt_user", "type": "text", "text": "run a tool",
		"messageID": "msg_user", "sessionID": "ses_stub",
	}))
	f.emit(assistantMessageUpdated("ses_stub", "msg_assistant"))
	// Tool parts arrive once per state change; only one use + one result belong
	// in the progress card.
	f.emit(partUpdated("ses_stub", map[string]any{
		"id": "prt_tool", "type": "tool", "tool": "bash", "callID": "call_1",
		"messageID": "msg_assistant", "sessionID": "ses_stub",
		"state": map[string]any{"status": "pending", "input": map[string]any{"command": "echo hi"}},
	}))
	f.emit(partUpdated("ses_stub", map[string]any{
		"id": "prt_tool", "type": "tool", "tool": "bash", "callID": "call_1",
		"messageID": "msg_assistant", "sessionID": "ses_stub",
		"state": map[string]any{"status": "running", "input": map[string]any{"command": "echo hi"}},
	}))
	f.emit(partUpdated("ses_stub", map[string]any{
		"id": "prt_tool", "type": "tool", "tool": "bash", "callID": "call_1",
		"messageID": "msg_assistant", "sessionID": "ses_stub",
		"state": map[string]any{"status": "completed", "input": map[string]any{"command": "echo hi"}, "output": "hi"},
	}))
	f.emit(partUpdated("ses_stub", map[string]any{
		"id": "prt_text", "type": "text", "text": "done: hi",
		"messageID": "msg_assistant", "sessionID": "ses_stub",
	}))
	f.emit(partUpdated("ses_stub", map[string]any{
		"id": "prt_step", "type": "step-finish", "reason": "stop",
		"messageID": "msg_assistant", "sessionID": "ses_stub",
	}))

	got := collectEvents(t, s.Events(), 4, 5*time.Second)
	var texts, toolUses, toolResults, results int
	for _, evt := range got {
		switch evt.Type {
		case core.EventText:
			texts++
			if evt.Content != "done: hi" {
				t.Fatalf("unexpected reply text %q (user prompt must not be echoed)", evt.Content)
			}
		case core.EventToolUse:
			toolUses++
		case core.EventToolResult:
			toolResults++
		case core.EventResult:
			results++
		}
	}
	if texts != 1 || toolUses != 1 || toolResults != 1 || results != 1 {
		t.Fatalf("texts=%d toolUses=%d toolResults=%d results=%d, want 1/1/1/1 (got %+v)",
			texts, toolUses, toolResults, results, got)
	}

	// Every forwarded event must carry the session id: the engine persists
	// agent_session_id from the first event that has one, which is what keeps a
	// mid-turn /stop from losing the conversation.
	for _, evt := range got {
		if evt.SessionID != "ses_stub" {
			t.Fatalf("event %+v carries SessionID=%q, want ses_stub", evt, evt.SessionID)
		}
	}

	f.releaseTurn()
}

func TestServerSession_CloseAbortsRunningTurn(t *testing.T) {
	f := newFakeOpencodeServer(t)
	f.holdMessages.Store(true)
	s := newTestServerSession(t, f, "ses_stub")

	if err := s.Send("long task", "m1", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, _, msgs := f.counts()
		if msgs == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("prompt never reached the server")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, abort, _ := f.counts(); abort != 1 {
		t.Fatalf("abort calls = %d, want 1 (Close must stop the in-flight turn)", abort)
	}
	f.releaseTurn()
}

func TestParseProviderScopedModel(t *testing.T) {
	cases := []struct {
		in       string
		provider string
		model    string
		nilOut   bool
	}{
		{in: "openai/gpt-5.6-sol", provider: "openai", model: "gpt-5.6-sol"},
		{in: "aiapi/glm-5.3", provider: "aiapi", model: "glm-5.3"},
		// Provider-scoped model names may themselves contain a slash; only the
		// first one separates provider from model.
		{in: "chatgpt/openai/gpt-5.6-sol", provider: "chatgpt", model: "openai/gpt-5.6-sol"},
		{in: "bare-model", nilOut: true},
		{in: "", nilOut: true},
	}
	for _, tc := range cases {
		got := parseProviderScopedModel(tc.in)
		if tc.nilOut {
			if got != nil {
				t.Errorf("parseProviderScopedModel(%q) = %v, want nil", tc.in, got)
			}
			continue
		}
		if got == nil {
			t.Fatalf("parseProviderScopedModel(%q) = nil", tc.in)
		}
		if got["providerID"] != tc.provider || got["modelID"] != tc.model {
			t.Errorf("parseProviderScopedModel(%q) = %v, want %s/%s", tc.in, got, tc.provider, tc.model)
		}
	}
}

func TestServerSession_ImagesBecomeFileParts(t *testing.T) {
	f := newFakeOpencodeServer(t)
	f.holdMessages.Store(true)
	s := newTestServerSession(t, f, "ses_stub")

	img := core.ImageAttachment{MimeType: "image/png", Data: []byte{0x89, 'P', 'N', 'G'}, FileName: "shot.png"}
	if err := s.Send("look at this", "m1", []core.ImageAttachment{img}, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, _, msgs := f.counts(); msgs == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("prompt never reached the server")
		}
		time.Sleep(10 * time.Millisecond)
	}

	parts, _ := f.lastMessage()["parts"].([]any)
	if len(parts) != 2 {
		t.Fatalf("parts = %v, want text + file", f.lastMessage()["parts"])
	}
	filePart, _ := parts[1].(map[string]any)
	if filePart["type"] != "file" || filePart["mime"] != "image/png" {
		t.Fatalf("image part = %v", filePart)
	}
	if url, _ := filePart["url"].(string); !strings.HasPrefix(url, "data:image/png;base64,") {
		t.Fatalf("image part url = %q, want a data URL", url)
	}
	f.releaseTurn()
}

// An unreachable server while the session is idle is retried silently — there is
// no turn to report to, and a flapping server must not spam the chat. Mid-turn
// losses are reported (see TestServerSession_StreamLossReportedOnceMidTurn).
func TestServerSession_IdleStreamFailureIsSilent(t *testing.T) {
	f := newFakeOpencodeServer(t)
	f.ts.Close() // server gone before the session subscribes
	s := newTestServerSession(t, f, "ses_stub")

	select {
	case evt := <-s.Events():
		t.Fatalf("idle session emitted %+v, want silence", evt)
	case <-time.After(1500 * time.Millisecond):
	}
}

var _ io.Closer = (*fakeOpencodeServer)(nil)

// Close satisfies io.Closer so the stub can be used where a closer is expected.
func (f *fakeOpencodeServer) Close() error {
	f.ts.Close()
	return nil
}

// The transport is opt-in: default stays "run" so existing deployments are
// unaffected, and an unknown value fails loudly instead of silently falling back.
func TestNew_TransportOption(t *testing.T) {
	base := map[string]any{"work_dir": t.TempDir()}

	agent, err := New(base)
	if err != nil {
		t.Fatalf("New(default): %v", err)
	}
	if got := agent.(*Agent).transport; got != opencodeTransportRun {
		t.Fatalf("default transport = %q, want %q", got, opencodeTransportRun)
	}

	withServer := map[string]any{"work_dir": t.TempDir(), "opencode_transport": "server"}
	agent, err = New(withServer)
	if err != nil {
		t.Fatalf("New(server): %v", err)
	}
	if got := agent.(*Agent).transport; got != opencodeTransportServer {
		t.Fatalf("transport = %q, want %q", got, opencodeTransportServer)
	}

	if _, err := New(map[string]any{"work_dir": t.TempDir(), "opencode_transport": "nonsense"}); err == nil {
		t.Fatal("New(bad transport) = nil error, want rejection")
	}
}

// Multi-workspace projects construct a per-workspace agent from
// WorkspaceAgentOptions, so the transport has to travel with them; otherwise a
// project configured for the server transport silently reverts to `opencode run`
// in every workspace.
func TestWorkspaceAgentOptions_CarriesTransport(t *testing.T) {
	agent, err := New(map[string]any{
		"work_dir":           t.TempDir(),
		"opencode_transport": opencodeTransportServer,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	opts := agent.(*Agent).WorkspaceAgentOptions()
	if got := opts["opencode_transport"]; got != opencodeTransportServer {
		t.Fatalf("WorkspaceAgentOptions()[opencode_transport] = %v, want %q", got, opencodeTransportServer)
	}
}

// The engine persists agent_session_id from text events only, so a turn aborted
// before any text arrives (/stop during a tool call) must still learn the id up
// front — otherwise the next message starts a fresh conversation.
func TestServerSession_AnnouncesNewSessionID(t *testing.T) {
	f := newFakeOpencodeServer(t)
	s := newTestServerSession(t, f, "")

	if err := s.Send("task", "m1", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	evt := collectEvents(t, s.Events(), 1, 5*time.Second)[0]
	if evt.Type != core.EventText || evt.SessionID != "ses_stub" || evt.Content != "" {
		t.Fatalf("first event = %+v, want an empty text event carrying ses_stub", evt)
	}

	f.releaseTurn()
}

// Project modes other than yolo must keep OpenCode's own permission behaviour,
// exactly as the run transport does by omitting --dangerously-skip-permissions.
func TestServerSession_NonYoloKeepsOpenCodePermissions(t *testing.T) {
	f := newFakeOpencodeServer(t)
	s := newTestServerSessionWithMode(t, f, "", "default")

	if err := s.Send("hello", "m1", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		f.mu.Lock()
		created, body := f.createCalls, f.createdBody
		f.mu.Unlock()
		if created == 1 {
			if _, ok := body["permission"]; ok {
				t.Fatalf("non-yolo session must not force an allow-all ruleset: %v", body)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("session was never created")
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.releaseTurn()
}

// A server restart must not end the session: the event stream reconnects and the
// conversation continues.
func TestServerSession_ReconnectsEventStream(t *testing.T) {
	f := newFakeOpencodeServer(t)
	s := newTestServerSession(t, f, "ses_stub")
	waitForSubscriber(t, f)

	f.dropStreams()
	deadline := time.Now().Add(5 * time.Second)
	for f.subscriberCount() == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("session never reconnected to the event stream")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Events after the reconnect still reach the engine.
	f.emit(assistantMessageUpdated("ses_stub", "msg_after"))
	f.emit(partUpdated("ses_stub", map[string]any{
		"id": "prt_after", "type": "text", "text": "after reconnect",
		"messageID": "msg_after", "sessionID": "ses_stub",
	}))
	got := collectEvents(t, s.Events(), 1, 5*time.Second)
	if got[0].Type != core.EventText || got[0].Content != "after reconnect" {
		t.Fatalf("event after reconnect = %+v", got[0])
	}
}

// Losing the stream mid-turn is reported once; a flapping server must not spam
// the chat with repeated errors.
func TestServerSession_StreamLossReportedOnceMidTurn(t *testing.T) {
	f := newFakeOpencodeServer(t)
	f.holdMessages.Store(true)
	s := newTestServerSession(t, f, "ses_stub")
	waitForSubscriber(t, f)

	if err := s.Send("task", "m1", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	f.dropStreams()
	first := collectEvents(t, s.Events(), 1, 5*time.Second)
	if first[0].Type != core.EventError {
		t.Fatalf("first event = %+v, want EventError for the lost stream", first[0])
	}
	// Wait for the reader to re-establish and drop again: no second report.
	deadline := time.Now().Add(5 * time.Second)
	for f.subscriberCount() == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("stream never came back")
		}
		time.Sleep(20 * time.Millisecond)
	}
	f.dropStreams()
	select {
	case evt := <-s.Events():
		if evt.Type == core.EventError {
			t.Fatalf("duplicate stream-loss error: %+v", evt)
		}
	case <-time.After(1500 * time.Millisecond):
	}
	f.releaseTurn()
}

func waitForSubscriber(t *testing.T, f *fakeOpencodeServer) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for f.subscriberCount() == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("session never subscribed to the event stream")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Startup is serialized per workspace so two sessions cannot each spawn a server
// for the same directory; unrelated workspaces keep independent locks.
func TestStartLockIsPerWorkspace(t *testing.T) {
	a1 := startLock("opencode\x00\x00/tmp/ws-a")
	a2 := startLock("opencode\x00\x00/tmp/ws-a")
	b := startLock("opencode\x00\x00/tmp/ws-b")

	if a1 != a2 {
		t.Fatal("same workspace returned different startup locks")
	}
	if a1 == b {
		t.Fatal("different workspaces shared a startup lock")
	}
}

// OpenCode also emits a reason="stop" step-finish when it compacts a
// conversation, so ending the turn on that part would cut a running turn short
// (partial or empty reply). Only session.idle may end the turn.
func TestServerSession_CompactionStepFinishDoesNotEndTurn(t *testing.T) {
	f := newFakeOpencodeServer(t)
	s := newTestServerSession(t, f, "ses_stub")
	waitForSubscriber(t, f)

	f.emit(assistantMessageUpdated("ses_stub", "msg_a"))
	f.emit(partUpdated("ses_stub", map[string]any{
		"id": "prt_step", "type": "step-finish", "reason": "stop",
		"messageID": "msg_a", "sessionID": "ses_stub",
		"tokens": map[string]any{"total": 9436},
	}))
	select {
	case evt := <-s.Events():
		if evt.Type == core.EventResult {
			t.Fatalf("compaction step-finish ended the turn early: %+v", evt)
		}
	case <-time.After(1200 * time.Millisecond):
	}

	// The real end of turn: session.idle.
	f.emit(map[string]any{"type": "session.idle", "properties": map[string]any{"sessionID": "ses_stub"}})
	got := collectEvents(t, s.Events(), 1, 5*time.Second)
	if got[0].Type != core.EventResult {
		t.Fatalf("session.idle produced %+v, want EventResult", got[0])
	}
}

// If the stream never delivers session.idle, the turn must still be closed once
// the message request returns — otherwise the engine waits forever.
func TestServerSession_TurnResultFallbackWhenIdleMissed(t *testing.T) {
	f := newFakeOpencodeServer(t)
	s := newTestServerSession(t, f, "ses_stub")

	if err := s.Send("task", "m1", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	// No session.idle is emitted: release the POST and expect a result anyway.
	// (The session is pre-bound, so no announce event precedes it.)
	f.releaseTurn()
	got := collectEvents(t, s.Events(), 1, 8*time.Second)
	if got[0].Type != core.EventResult || !got[0].Done {
		t.Fatalf("event = %+v, want the turn-closing EventResult", got[0])
	}
}
