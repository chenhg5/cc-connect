package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
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

	// missingSessionOnce makes the next message request fail the way OpenCode
	// does when the stored session id no longer exists.
	missingSessionOnce atomic.Bool

	// patchedSessions/patchedBodys record PATCH /session/{id} calls (permission
	// ruleset updates); permissionReplies records POST /permission/{id}/reply.
	patchedSessions   []string
	patchedBodys      []map[string]any
	permissionReplies []fakePermissionReply
	// resyncMessages is what GET /session/{id}/message returns; the transport
	// asks for it to rebuild its role map on every stream (re)connect.
	resyncMessages []map[string]any
}

// fakePermissionReply is one recorded answer to a permission request.
type fakePermissionReply struct {
	requestID string
	reply     string
}

func newFakeOpencodeServer(t *testing.T) *fakeOpencodeServer {
	f := &fakeOpencodeServer{t: t, releaseMessage: make(chan struct{}, 8)}
	mux := http.NewServeMux()
	mux.HandleFunc("/session", f.handleCreate)
	mux.HandleFunc("/session/", f.handleSession)
	mux.HandleFunc("/permission/", f.handlePermissionReply)
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
	if r.Method == http.MethodGet && strings.HasSuffix(path, "/message") {
		f.mu.Lock()
		msgs := append([]map[string]any(nil), f.resyncMessages...)
		f.mu.Unlock()
		if msgs == nil {
			msgs = []map[string]any{}
		}
		writeJSONResponse(w, msgs)
		return
	}
	if r.Method == http.MethodPatch {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.patchedSessions = append(f.patchedSessions, path)
		f.patchedBodys = append(f.patchedBodys, body)
		f.mu.Unlock()
		writeJSONResponse(w, map[string]any{"id": path})
		return
	}
	switch {
	case strings.HasSuffix(path, "/abort"):
		f.mu.Lock()
		f.abortCalls++
		f.mu.Unlock()
		writeJSONResponse(w, map[string]any{})
	case strings.HasSuffix(path, "/message"):
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if f.missingSessionOnce.CompareAndSwap(true, false) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"name":"NotFoundError","data":{"message":"Session not found: ses_gone"}}`))
			return
		}
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

// handlePermissionReply records answers to pending permission requests.
func (f *fakeOpencodeServer) handlePermissionReply(w http.ResponseWriter, r *http.Request) {
	if !strings.HasSuffix(r.URL.Path, "/reply") {
		http.NotFound(w, r)
		return
	}
	requestID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/permission/"), "/reply")
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	reply, _ := body["reply"].(string)
	f.mu.Lock()
	f.permissionReplies = append(f.permissionReplies, fakePermissionReply{requestID: requestID, reply: reply})
	f.mu.Unlock()
	writeJSONResponse(w, true)
}

func (f *fakeOpencodeServer) replies() []fakePermissionReply {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakePermissionReply(nil), f.permissionReplies...)
}

func (f *fakeOpencodeServer) patches() ([]string, []map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.patchedSessions...), append([]map[string]any(nil), f.patchedBodys...)
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
			_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
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

func messageUpdatedWithRole(sessionID, messageID, role string) map[string]any {
	return map[string]any{
		"type": "message.updated",
		"properties": map[string]any{
			"sessionID": sessionID,
			"info":      map[string]any{"id": messageID, "role": role, "sessionID": sessionID},
		},
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
	f.holdMessages.Store(true) // keep the turn open so events are not raced by the turn-end fallback
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

	// The user's own prompt arrives as a text part of a *user* message (announced
	// first, as OpenCode does): it must not be replayed as reply text.
	f.emit(messageUpdatedWithRole("ses_stub", "msg_user", "user"))
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

	// Read until the turn closes: text may arrive as its own event or inside the
	// closing EventResult, depending on whether the agent buffers step text.
	var got []core.Event
	eventsDeadline := time.After(5 * time.Second)
	for toolUses, toolResults := 0, 0; toolUses < 1 || toolResults < 1; {
		select {
		case evt, ok := <-s.Events():
			if !ok {
				t.Fatalf("event channel closed early: %+v", got)
			}
			got = append(got, evt)
			switch evt.Type {
			case core.EventToolUse:
				toolUses++
			case core.EventToolResult:
				toolResults++
			}
		case <-eventsDeadline:
			t.Fatalf("timed out waiting for the tool events, got %+v", got)
		}
	}
	var toolUses, toolResults, results int
	delivered := make([]string, 0, 2)
	for _, evt := range got {
		switch evt.Type {
		case core.EventText:
			delivered = append(delivered, evt.Content)
		case core.EventToolUse:
			toolUses++
		case core.EventToolResult:
			toolResults++
		case core.EventResult:
			results++
			delivered = append(delivered, evt.Content)
		}
	}
	if toolUses != 1 || toolResults != 1 {
		t.Fatalf("toolUses=%d toolResults=%d, want 1/1 (got %+v)", toolUses, toolResults, got)
	}
	_ = results // turn end is covered by the idle / fallback tests
	// The answer may travel as a text event or inside the closing EventResult,
	// depending on whether the agent buffers step text; either way the reply must
	// be the assistant's text, never an echo of the user's own prompt.
	joined := strings.Join(delivered, "\n")
	if strings.Contains(joined, "run a tool") {
		t.Fatalf("delivered reply %q echoes the user prompt", joined)
	}
	// Text is delivered as a text event on agents that forward step text, and only
	// through the closing EventResult on agents that buffer it — the reply text
	// itself is asserted by the mapping tests of each transport.
	for _, evt := range got {
		if evt.Type == core.EventText && !strings.Contains(evt.Content, "done: hi") {
			t.Fatalf("assistant text event = %q, want the assistant's own text", evt.Content)
		}
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

// existingCmd returns a command that exists on any machine, so tests about
// option parsing do not depend on the opencode CLI being installed (CI does not
// have it) — New only uses it for an exec.LookPath check.
func existingCmd(t *testing.T) string {
	t.Helper()
	for _, candidate := range []string{"sh", "true", "env"} {
		if path, err := exec.LookPath(candidate); err == nil {
			return path
		}
	}
	t.Skip("no shell available to stand in for the opencode CLI")
	return ""
}

// The transport is opt-in: default stays "run" so existing deployments are
// unaffected, and an unknown value fails loudly instead of silently falling back.
func TestNew_TransportOption(t *testing.T) {
	base := map[string]any{"work_dir": t.TempDir(), "cmd": existingCmd(t)}

	agent, err := New(base)
	if err != nil {
		t.Fatalf("New(default): %v", err)
	}
	if got := agent.(*Agent).transport; got != opencodeTransportRun {
		t.Fatalf("default transport = %q, want %q", got, opencodeTransportRun)
	}

	withServer := map[string]any{"work_dir": t.TempDir(), "cmd": existingCmd(t), "opencode_transport": "server"}
	agent, err = New(withServer)
	if err != nil {
		t.Fatalf("New(server): %v", err)
	}
	if got := agent.(*Agent).transport; got != opencodeTransportServer {
		t.Fatalf("transport = %q, want %q", got, opencodeTransportServer)
	}

	if _, err := New(map[string]any{"work_dir": t.TempDir(), "cmd": existingCmd(t), "opencode_transport": "nonsense"}); err == nil {
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
		"cmd":                existingCmd(t),
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

	// Events after the reconnect still reach the engine. A tool part is used here
	// because text may be buffered for the final result depending on the agent.
	f.emit(partUpdated("ses_stub", map[string]any{
		"id": "prt_after", "type": "tool", "tool": "bash", "callID": "call_after",
		"messageID": "msg_after", "sessionID": "ses_stub",
		"state": map[string]any{"status": "running", "input": map[string]any{"command": "echo after"}},
	}))
	got := collectEvents(t, s.Events(), 1, 5*time.Second)
	if got[0].Type != core.EventToolUse || got[0].ToolName != "bash" || got[0].SessionID != "ses_stub" {
		t.Fatalf("event after reconnect = %+v, want the tool_use event", got[0])
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

// A stored agent session can disappear underneath cc-connect (e.g. someone runs
// `opencode session delete`). The run transport recovers by clearing the id; the
// server transport must do the same, otherwise every later message in that
// conversation fails with 404.
func TestServerSession_RecoversFromMissingStoredSession(t *testing.T) {
	f := newFakeOpencodeServer(t)
	s := newTestServerSession(t, f, "ses_gone")
	f.missingSessionOnce.Store(true)

	if err := s.Send("hello", "m1", nil, nil); err != nil {
		t.Fatalf("Send after a deleted session = %v, want a transparent recovery", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		create, _, msgs := f.counts()
		// The session was pre-bound, so the single create is the one the recovery
		// performed after the 404; the single message is the successful retry.
		if create >= 1 && msgs == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("create=%d messages=%d, want a fresh session and a retried message", create, msgs)
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.releaseTurn()
}

// The agent buffers per-step text and only hands it over when a step finishes,
// so the transport must flush it at the real turn end — otherwise the engine
// receives an empty reply ((空响应)) even though the agent answered.
func TestServerSession_FlushesBufferedAnswerAtTurnEnd(t *testing.T) {
	f := newFakeOpencodeServer(t)
	f.holdMessages.Store(true)
	s := newTestServerSession(t, f, "ses_stub")
	waitForSubscriber(t, f)

	f.emit(assistantMessageUpdated("ses_stub", "msg_a"))
	f.emit(partUpdated("ses_stub", map[string]any{
		"id": "prt_text", "type": "text", "text": "the answer",
		"messageID": "msg_a", "sessionID": "ses_stub",
	}))
	f.emit(partUpdated("ses_stub", map[string]any{
		"id": "prt_step", "type": "step-finish", "reason": "stop",
		"messageID": "msg_a", "sessionID": "ses_stub",
	}))
	// No turn end yet: a reason="stop" step also appears for compaction, so the
	// turn must stay open until the session reports idle.
	select {
	case evt := <-s.Events():
		if evt.Type == core.EventResult {
			t.Fatalf("step-finish ended the turn: %+v", evt)
		}
	case <-time.After(700 * time.Millisecond):
	}

	f.emit(map[string]any{"type": "session.idle", "properties": map[string]any{"sessionID": "ses_stub"}})
	var result *core.Event
	deadline := time.After(5 * time.Second)
	for result == nil {
		select {
		case evt := <-s.Events():
			if evt.Type == core.EventResult {
				e := evt
				result = &e
			}
		case <-deadline:
			t.Fatalf("no EventResult after session.idle")
		}
	}
	// Agents that buffer step text deliver the answer inside the result; agents
	// that forward each step deliver it as a text event. Either way the result
	// must arrive, and when it carries content it must be the agent's answer.
	if result.Content != "" && !strings.Contains(result.Content, "the answer") {
		t.Fatalf("result content = %q, want the buffered answer", result.Content)
	}
	f.releaseTurn()
}

// permissionAsked builds a permission.asked SSE event, the shape OpenCode uses
// when a tool call needs approval.
func permissionAsked(sessionID, requestID, permission string, patterns ...string) map[string]any {
	raw := make([]any, 0, len(patterns))
	for _, p := range patterns {
		raw = append(raw, p)
	}
	return map[string]any{
		"type": "permission.asked",
		"properties": map[string]any{
			"id":         requestID,
			"sessionID":  sessionID,
			"permission": permission,
			"patterns":   raw,
		},
	}
}

// hasRule reports whether a ruleset body carries the given rule.
func hasRule(body map[string]any, permission, pattern, action string) bool {
	rules, ok := body["permission"].([]any)
	if !ok {
		return false
	}
	for _, raw := range rules {
		rule, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if rule["permission"] == permission && rule["pattern"] == pattern && rule["action"] == action {
			return true
		}
	}
	return false
}

// A resumed conversation never received the create-time ruleset, so yolo mode
// must push it onto the existing session — otherwise any tool call outside the
// workspace parks the turn on an approval no bridge user can answer (the
// session stays busy and every later message queues behind it).
func TestServerSession_ResumedSessionGetsYoloPermissionRuleset(t *testing.T) {
	f := newFakeOpencodeServer(t)
	s := newTestServerSessionWithMode(t, f, "ses_existing", "yolo")
	defer func() { _ = s.Close() }()

	f.holdMessages.Store(true) // keep the turn in flight while the transport attaches
	if err := s.Send("hello", "", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		sessions, bodys := f.patches()
		if len(sessions) > 0 {
			if sessions[0] != "ses_existing" {
				t.Fatalf("patched session = %q, want the resumed one", sessions[0])
			}
			if !hasRule(bodys[0], "external_directory", "*", "allow") {
				t.Fatalf("ruleset does not allow external_directory: %v", bodys[0])
			}
			if !hasRule(bodys[0], "bash", "*", "allow") {
				t.Fatalf("ruleset does not allow bash: %v", bodys[0])
			}
			// The interactive tools stay denied: nobody can answer them here.
			if !hasRule(bodys[0], "question", "*", "deny") {
				t.Fatalf("ruleset does not deny the question tool: %v", bodys[0])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no permission ruleset was applied to the resumed session")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// A second turn must not repeat the request.
	f.releaseTurn()
	if err := s.Send("again", "", nil, nil); err != nil {
		t.Fatalf("second Send: %v", err)
	}
	f.releaseTurn()
	if sessions, _ := f.patches(); len(sessions) != 1 {
		t.Fatalf("permission ruleset applied %d times, want once", len(sessions))
	}
}

// A brand-new session gets the ruleset from POST /session, so no PATCH is
// needed and, in default mode, no ruleset is sent at all.
func TestServerSession_FreshSessionRulesetOnlyInYoloMode(t *testing.T) {
	f := newFakeOpencodeServer(t)
	s := newTestServerSessionWithMode(t, f, "", "yolo")
	defer func() { _ = s.Close() }()
	f.holdMessages.Store(true) // keep the turn in flight while the transport attaches
	if err := s.Send("hello", "", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if create, _, _ := f.counts(); create != 1 {
		t.Fatalf("create calls = %d, want 1", create)
	}
	f.mu.Lock()
	body := f.createdBody
	f.mu.Unlock()
	if !hasRule(body, "external_directory", "*", "allow") {
		t.Fatalf("create body lacks the yolo ruleset: %v", body)
	}
	if sessions, _ := f.patches(); len(sessions) != 0 {
		t.Fatalf("fresh session was patched %d times, want 0", len(sessions))
	}

	f2 := newFakeOpencodeServer(t)
	s2 := newTestServerSessionWithMode(t, f2, "ses_default", "default")
	defer func() { _ = s2.Close() }()
	f2.holdMessages.Store(true)
	if err := s2.Send("hello", "", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if sessions, _ := f2.patches(); len(sessions) != 0 {
		t.Fatalf("default mode must not push a ruleset (got %d patches)", len(sessions))
	}
}

// In yolo mode an approval request is answered immediately, exactly as the run
// transport's --dangerously-skip-permissions does; leaving it unanswered is what
// hung the session.
func TestServerSession_PermissionAskedAutoApprovedInYoloMode(t *testing.T) {
	f := newFakeOpencodeServer(t)
	s := newTestServerSessionWithMode(t, f, "ses_perm", "yolo")
	defer func() { _ = s.Close() }()
	waitForSubscriber(t, f)

	f.emit(permissionAsked("ses_perm", "per_abc", "external_directory", "/usr/local/data/docs/*"))

	deadline := time.Now().Add(2 * time.Second)
	for {
		replies := f.replies()
		if len(replies) > 0 {
			if replies[0].requestID != "per_abc" {
				t.Fatalf("replied to %q, want per_abc", replies[0].requestID)
			}
			if replies[0].reply != "always" {
				t.Fatalf("reply = %q, want always", replies[0].reply)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("permission request was never answered")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Outside yolo mode the request goes to the engine (Allow/Deny prompt) and the
// user's answer is forwarded to OpenCode.
func TestServerSession_PermissionAskedSurfacedInDefaultMode(t *testing.T) {
	f := newFakeOpencodeServer(t)
	s := newTestServerSessionWithMode(t, f, "ses_perm", "default")
	defer func() { _ = s.Close() }()
	waitForSubscriber(t, f)

	f.emit(permissionAsked("ses_perm", "per_xyz", "external_directory", "/srv/data/*"))

	evt := collectEvents(t, s.Events(), 1, 2*time.Second)[0]
	if evt.Type != core.EventPermissionRequest {
		t.Fatalf("event type = %v, want a permission request", evt.Type)
	}
	if evt.RequestID != "per_xyz" || evt.ToolName != "external_directory" {
		t.Fatalf("permission event = %+v, want per_xyz/external_directory", evt)
	}
	if !strings.Contains(evt.ToolInput, "/srv/data/*") {
		t.Fatalf("permission event input = %q, want the requested pattern", evt.ToolInput)
	}

	if err := s.RespondPermission(evt.RequestID, core.PermissionResult{Behavior: "allow"}); err != nil {
		t.Fatalf("RespondPermission(allow): %v", err)
	}
	if err := s.RespondPermission("per_other", core.PermissionResult{Behavior: "deny"}); err != nil {
		t.Fatalf("RespondPermission(deny): %v", err)
	}

	replies := f.replies()
	if len(replies) != 2 {
		t.Fatalf("replies = %+v, want two", replies)
	}
	if replies[0].reply != "once" {
		t.Fatalf("allow reply = %q, want once", replies[0].reply)
	}
	if replies[1].reply != "reject" {
		t.Fatalf("deny reply = %q, want reject", replies[1].reply)
	}
}

// messageWithRole builds one entry of the GET /session/{id}/message response.
func messageWithRole(id, role string) map[string]any {
	return map[string]any{"info": map[string]any{"id": id, "role": role}}
}

func textPartOf(messageID, text string) map[string]any {
	return map[string]any{"type": "text", "messageID": messageID, "text": text}
}

// stepFinishPart closes a step so the buffered step text is delivered; reason
// "tool-calls" marks it as an intermediate step (the answer path uses "stop").
func stepFinishPart() map[string]any {
	return map[string]any{"type": "step-finish", "reason": "tool-calls"}
}

// A stream that opens mid-turn (or reconnects) never announces the messages
// that started earlier, so their parts arrive with an unknown message id. They
// must still reach the engine: dropping them loses the answer, while the run
// transport never filtered by role at all.
func TestServerSession_KeepsPartsFromUnannouncedMessage(t *testing.T) {
	f := newFakeOpencodeServer(t)
	s := newTestServerSessionWithMode(t, f, "ses_gap", "yolo")
	defer func() { _ = s.Close() }()
	waitForSubscriber(t, f)

	f.emit(partUpdated("ses_gap", textPartOf("msg_never_announced", "the answer")))
	f.emit(partUpdated("ses_gap", stepFinishPart()))

	evt := collectEvents(t, s.Events(), 1, 2*time.Second)[0]
	if evt.Type != core.EventText || !strings.Contains(evt.Content, "the answer") {
		t.Fatalf("event = %+v, want the text of the unannounced message", evt)
	}
}

// The role map is rebuilt from the session's messages when the stream opens, so
// the echo of the user's own prompt is still suppressed even though this
// connection never saw its message.updated event.
func TestServerSession_ResyncsRolesOnAttach(t *testing.T) {
	f := newFakeOpencodeServer(t)
	f.mu.Lock()
	f.resyncMessages = []map[string]any{
		messageWithRole("msg_user_old", "user"),
		messageWithRole("msg_assistant_old", "assistant"),
	}
	f.mu.Unlock()

	s := newTestServerSessionWithMode(t, f, "ses_resync", "yolo")
	defer func() { _ = s.Close() }()
	waitForSubscriber(t, f)

	// The resync is asynchronous; wait until the role map knows the assistant
	// message (its parts are then accepted without any message.updated).
	deadline := time.Now().Add(2 * time.Second)
	for {
		s.msgMu.Lock()
		_, known := s.assistantMsgs["msg_assistant_old"]
		s.msgMu.Unlock()
		if known {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("role map was never resynced from the session")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// User echo: suppressed. Assistant text: delivered.
	f.emit(partUpdated("ses_resync", textPartOf("msg_user_old", "my own prompt")))
	f.emit(partUpdated("ses_resync", textPartOf("msg_assistant_old", "the answer")))
	f.emit(partUpdated("ses_resync", stepFinishPart()))

	evt := collectEvents(t, s.Events(), 1, 2*time.Second)[0]
	if evt.Type != core.EventText || !strings.Contains(evt.Content, "the answer") {
		t.Fatalf("first event = %+v, want the assistant text (the user echo must be dropped)", evt)
	}
	if strings.Contains(evt.Content, "my own prompt") {
		t.Fatalf("user echo leaked into the reply: %+v", evt)
	}
}

// The engine asks the running session for real token usage (reply footer and
// auto-compress decisions). The server transport must report it too — the
// inner session accumulates it from OpenCode's step-finish parts.
func TestServerSession_ReportsContextUsage(t *testing.T) {
	f := newFakeOpencodeServer(t)
	s := newTestServerSessionWithMode(t, f, "ses_usage", "yolo")
	defer func() { _ = s.Close() }()
	waitForSubscriber(t, f)
	if _, ok := any(s.inner).(core.ContextUsageReporter); !ok {
		t.Skip("this base's run session does not report context usage")
	}

	if usage := s.GetContextUsage(); usage != nil {
		t.Fatalf("usage before any step = %+v, want nil", usage)
	}

	f.emit(partUpdated("ses_usage", map[string]any{
		"type":   "step-finish",
		"reason": "tool-calls",
		"tokens": map[string]any{
			"input": 1234, "output": 56, "reasoning": 7, "cache": map[string]any{"read": 89, "write": 10},
		},
	}))

	deadline := time.Now().Add(2 * time.Second)
	for {
		if usage := s.GetContextUsage(); usage != nil {
			if usage.InputTokens != 1234 || usage.OutputTokens != 56 {
				t.Fatalf("usage = %+v, want input 1234 / output 56", usage)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no context usage was reported after a step-finish with tokens")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A server instance is shared by every conversation in a workspace, so a
// session that does not know its own id yet must not swallow other sessions'
// events into this session's reply.
func TestServerSession_IgnoresForeignEventsBeforeOwnID(t *testing.T) {
	f := newFakeOpencodeServer(t)
	s := newTestServerSessionWithMode(t, f, "", "yolo")
	defer func() { _ = s.Close() }()
	waitForSubscriber(t, f)

	f.emit(partUpdated("ses_somebody_else", textPartOf("msg_other", "another user's answer")))
	f.emit(partUpdated("ses_somebody_else", stepFinishPart()))

	select {
	case evt := <-s.Events():
		t.Fatalf("event from another session leaked into this one: %+v", evt)
	case <-time.After(300 * time.Millisecond):
	}
}

// A turn whose stream goes quiet must be aborted with a visible notice instead
// of hanging until the engine's idle timeout (two hours by default) — the
// session would stay busy the whole time and queue every later message behind
// it. The run transport kills a silent process after the same delay.
func TestServerSession_AbortsStalledTurn(t *testing.T) {
	f := newFakeOpencodeServer(t)
	f.holdMessages.Store(true) // the turn never finishes on its own
	s := newTestServerSessionWithMode(t, f, "ses_stall", "yolo")
	defer func() { _ = s.Close() }()
	waitForSubscriber(t, f)

	if err := s.Send("slow task", "m1", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	// One event, then silence.
	f.emit(partUpdated("ses_stall", textPartOf("msg_a", "partial answer")))
	f.emit(partUpdated("ses_stall", stepFinishPart()))

	s.stallTimeout = 80 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.stallWatchdog(ctx, 10*time.Millisecond)

	deadline := time.After(3 * time.Second)
	for {
		select {
		case evt := <-s.Events():
			if evt.Type != core.EventResult {
				continue
			}
			if !strings.Contains(evt.Content, "已自动终止") {
				t.Fatalf("stall result = %q, want the timeout notice", evt.Content)
			}
			_, aborts, _ := f.counts()
			if aborts == 0 {
				t.Fatalf("stalled turn was ended without aborting the session")
			}
			return
		case <-deadline:
			t.Fatalf("stalled turn was never ended")
		}
	}
}

// Events keep a turn alive: the watchdog must only fire after real silence.
func TestServerSession_StallWatchdogKeepsLiveTurn(t *testing.T) {
	f := newFakeOpencodeServer(t)
	f.holdMessages.Store(true)
	s := newTestServerSessionWithMode(t, f, "ses_alive", "yolo")
	defer func() { _ = s.Close() }()
	waitForSubscriber(t, f)

	if err := s.Send("busy task", "m1", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	s.stallTimeout = 400 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.stallWatchdog(ctx, 20*time.Millisecond)

	for i := 0; i < 12; i++ {
		f.emit(partUpdated("ses_alive", textPartOf("msg_a", "working")))
		time.Sleep(50 * time.Millisecond)
		select {
		case evt := <-s.Events():
			if evt.Type == core.EventResult {
				t.Fatalf("a live turn was ended by the stall watchdog: %+v", evt)
			}
		default:
		}
	}
	if _, aborts, _ := f.counts(); aborts != 0 {
		t.Fatalf("live turn was aborted %d times, want 0", aborts)
	}
}

// One OpenCode server per workspace is not enough: the provider credentials
// reach it through the process environment (the config's {env:...} placeholder
// is resolved when the process starts), so two chats in one workspace on
// different providers would share whichever key was injected first — observed
// live as "APIError: Invalid token" for a chat on aiapi whose workspace server
// had been started for deepseek. The provider scope is part of the server key.
func TestServerKey_ScopedByProviderCredentials(t *testing.T) {
	base := opencodeServeConfig{cmd: "opencode", workDir: "/tmp/ws"}

	deepseek := base
	deepseek.providerScope = "deepseek\x00sk-deepseek\x00https://api.deepseek.com/v1"
	aiapi := base
	aiapi.providerScope = "aiapi\x00sk-aiapi\x00https://aiapi.uu.cc/v1"

	if serverKey(deepseek) == serverKey(aiapi) {
		t.Fatal("servers on different providers must not share a key")
	}
	// Same provider, same workspace: still shared.
	key := serverKey(deepseek)
	if key != serverKey(deepseek) {
		t.Fatal("the same provider scope must keep one shared server")
	}
	// A rotated key for the same provider must not reuse the old server.
	rotated := base
	rotated.providerScope = "deepseek\x00sk-rotated\x00https://api.deepseek.com/v1"
	if serverKey(rotated) == serverKey(deepseek) {
		t.Fatal("a rotated credential must start a fresh server")
	}
	// Work directories stay isolated as before.
	other := deepseek
	other.workDir = "/tmp/other"
	if serverKey(other) == serverKey(deepseek) {
		t.Fatal("different workspaces must not share a server")
	}
}

// When we stop a stalled turn ourselves, OpenCode reports the abort back on the
// stream (and the POST fails with MessageAbortedError). That echo must not reach
// the chat: the user already got the notice explaining the stop. Observed live
// as a raw "❌ MessageAbortedError: Aborted" right after the timeout notice.
func TestServerSession_AbortEchoIsNotRelayed(t *testing.T) {
	f := newFakeOpencodeServer(t)
	f.holdMessages.Store(true)
	s := newTestServerSessionWithMode(t, f, "ses_echo", "yolo")
	defer func() { _ = s.Close() }()
	waitForSubscriber(t, f)

	if err := s.Send("long task", "m1", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	s.stallTimeout = 40 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.stallWatchdog(ctx, 10*time.Millisecond)

	var got []core.Event
	deadline := time.After(3 * time.Second)
	for {
		select {
		case evt := <-s.Events():
			got = append(got, evt)
			if evt.Type == core.EventResult {
				goto done
			}
			if evt.Type == core.EventError {
				t.Fatalf("abort echo reached the engine: %+v", evt)
			}
		case <-deadline:
			t.Fatalf("stalled turn was never ended; got %+v", got)
		}
	}
done:
	// The stream reports the abort afterwards; it must stay silent.
	f.emit(map[string]any{
		"type":       "session.error",
		"properties": map[string]any{"sessionID": "ses_echo", "error": map[string]any{"name": "MessageAbortedError", "data": map[string]any{"message": "Aborted"}}},
	})
	select {
	case evt := <-s.Events():
		t.Fatalf("abort echo relayed after the turn: %+v", evt)
	case <-time.After(300 * time.Millisecond):
	}
}

// The watchdog threshold is configurable per project: long silent tool calls are
// normal for some workloads.
func TestParseStallTimeout(t *testing.T) {
	cases := []struct {
		raw  any
		want time.Duration
		bad  bool
	}{
		{raw: "", want: 0},
		{raw: "10m", want: 10 * time.Minute},
		{raw: "90s", want: 90 * time.Second},
		{raw: "OFF", want: 0},
		{raw: "0", want: 0},
		{raw: "nonsense", bad: true},
	}
	for _, c := range cases {
		got, err := parseStallTimeout(c.raw)
		if c.bad {
			if err == nil {
				t.Errorf("parseStallTimeout(%v) = %v, want an error", c.raw, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseStallTimeout(%v): %v", c.raw, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseStallTimeout(%v) = %v, want %v", c.raw, got, c.want)
		}
	}
	if stallTimeoutOrDefault(0) != serverStallTimeout {
		t.Error("an unset timeout must fall back to the default")
	}
	if stallTimeoutOrDefault(7*time.Minute) != 7*time.Minute {
		t.Error("a configured timeout must be used")
	}
}
