package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// acpScriptedServer answers the first N JSON-RPC requests a client sends,
// one scripted result per request, in order. A nil result produces a
// JSON-RPC error instead. It also records every method it was asked for
// so a test can assert which resume path was taken.
type acpScriptedServer struct {
	mu      sync.Mutex
	methods []string
}

func (s *acpScriptedServer) record(method string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.methods = append(s.methods, method)
}

func (s *acpScriptedServer) called() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.methods...)
}

func (s *acpScriptedServer) calledMethod(method string) bool {
	for _, m := range s.called() {
		if m == method {
			return true
		}
	}
	return false
}

// startACPScriptedServer wires a transport to an in-memory server that
// answers `results[i]` to the i-th client request. Ordering is request
// driven, so the client is never blocked waiting on the server.
func startACPScriptedServer(t *testing.T, results []any) (*transport, *acpScriptedServer, context.CancelFunc) {
	t.Helper()
	clientIn, serverOut := io.Pipe()
	serverIn, clientOut := io.Pipe()
	srv := &acpScriptedServer{}
	tr := newTransport(clientIn, clientOut, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	go tr.readLoop(ctx)

	go func() {
		defer func() { _ = serverOut.Close() }()
		br := bufio.NewReader(serverIn)
		enc := json.NewEncoder(serverOut)
		for _, want := range results {
			line, err := br.ReadBytes('\n')
			if err != nil {
				return
			}
			var req struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if json.Unmarshal(line, &req) != nil || req.Method == "" {
				continue
			}
			if want == nil {
				rpcErr := &rpcErrPayload{Code: -32601, Message: "method not found: " + req.Method}
				srv.record(req.Method)
				_ = enc.Encode(rpcResponseMsg{JSONRPC: "2.0", ID: req.ID, Error: rpcErr})
			} else {
				srv.record(req.Method)
				_ = enc.Encode(rpcResponseMsg{JSONRPC: "2.0", ID: req.ID, Result: want})
			}
		}
	}()
	return tr, srv, cancel
}

// initializeResult builds an `initialize` payload advertising exactly the
// capabilities named in the arguments.
func initializeResult(loadSession, resume, list bool) map[string]any {
	caps := map[string]any{}
	if loadSession {
		caps["loadSession"] = true
	}
	sessionCaps := map[string]any{}
	if list {
		sessionCaps["list"] = map[string]any{}
	}
	if resume {
		sessionCaps["resume"] = map[string]any{}
	}
	caps["sessionCapabilities"] = sessionCaps
	return map[string]any{
		"protocolVersion":   1,
		"agentInfo":         map[string]any{"name": "scripted-acp", "version": "test"},
		"agentCapabilities": caps,
		"authMethods":       []any{},
	}
}

// newHandshakeTestSession builds the minimum acpSession needed to drive
// handshake over a scripted transport.
func newHandshakeTestSession(tr *transport) *acpSession {
	return &acpSession{
		workDir:       "C:\\work",
		events:        make(chan core.Event, 8),
		ctx:           context.Background(),
		permByID:      make(map[string]permState),
		toolInputByID: make(map[string]string),
		tr:            tr,
	}
}

// A server that advertises only `sessionCapabilities.resume` (no
// loadSession) must be resumed via session/resume. Falling through to
// session/new here silently discards the conversation.
func TestHandshake_ResumesViaSessionResumeWhenOnlyResumeAdvertised(t *testing.T) {
	tr, srv, cancel := startACPScriptedServer(t, []any{
		initializeResult(false, true, false),
		map[string]any{"sessionId": "sess-resumed"},
	})
	defer cancel()

	s := newHandshakeTestSession(tr)
	if err := s.handshake("sess-old", ""); err != nil {
		t.Fatalf("handshake: %v", err)
	}

	if !srv.calledMethod("session/resume") {
		t.Fatalf("expected session/resume to be called, got %v", srv.called())
	}
	if srv.calledMethod("session/new") {
		t.Fatalf("session/new must not be called when a resume capability exists, got %v", srv.called())
	}
	if got := s.currentACPSessionID(); got != "sess-resumed" {
		t.Fatalf("agent session id = %q, want %q", got, "sess-resumed")
	}
}

// A server advertising loadSession but not resume keeps the historical
// session/load behaviour.
func TestHandshake_ResumesViaSessionLoadWhenOnlyLoadAdvertised(t *testing.T) {
	tr, srv, cancel := startACPScriptedServer(t, []any{
		initializeResult(true, false, false),
		map[string]any{"sessionId": "sess-loaded"},
	})
	defer cancel()

	s := newHandshakeTestSession(tr)
	if err := s.handshake("sess-old", ""); err != nil {
		t.Fatalf("handshake: %v", err)
	}

	if !srv.calledMethod("session/load") {
		t.Fatalf("expected session/load to be called, got %v", srv.called())
	}
	if srv.calledMethod("session/resume") {
		t.Fatalf("session/resume must not be called when not advertised, got %v", srv.called())
	}
	if got := s.currentACPSessionID(); got != "sess-loaded" {
		t.Fatalf("agent session id = %q, want %q", got, "sess-loaded")
	}
}

// A failed resume must fall back to session/new so the session still
// becomes usable.
func TestHandshake_FallsBackToNewWhenResumeFails(t *testing.T) {
	tr, srv, cancel := startACPScriptedServer(t, []any{
		initializeResult(false, true, false),
		nil, // session/resume -> JSON-RPC error
		map[string]any{"sessionId": "sess-fresh"},
	})
	defer cancel()

	s := newHandshakeTestSession(tr)
	if err := s.handshake("sess-old", ""); err != nil {
		t.Fatalf("handshake: %v", err)
	}

	if !srv.calledMethod("session/resume") {
		t.Fatalf("expected session/resume to be attempted, got %v", srv.called())
	}
	if !srv.calledMethod("session/new") {
		t.Fatalf("expected fallback to session/new, got %v", srv.called())
	}
	if got := s.currentACPSessionID(); got != "sess-fresh" {
		t.Fatalf("agent session id = %q, want %q", got, "sess-fresh")
	}
}

// Without a persisted id there is nothing to resume.
func TestHandshake_NoResumeWhenNoSessionID(t *testing.T) {
	tr, srv, cancel := startACPScriptedServer(t, []any{
		initializeResult(false, true, false),
		map[string]any{"sessionId": "sess-fresh"},
	})
	defer cancel()

	s := newHandshakeTestSession(tr)
	if err := s.handshake("", ""); err != nil {
		t.Fatalf("handshake: %v", err)
	}

	if srv.calledMethod("session/resume") || srv.calledMethod("session/load") {
		t.Fatalf("no resume call expected, got %v", srv.called())
	}
	if !srv.calledMethod("session/new") {
		t.Fatalf("expected session/new, got %v", srv.called())
	}
}

// A server advertising neither capability is started fresh.
func TestHandshake_NewWhenNoCapabilityAdvertised(t *testing.T) {
	tr, srv, cancel := startACPScriptedServer(t, []any{
		initializeResult(false, false, false),
		map[string]any{"sessionId": "sess-fresh"},
	})
	defer cancel()

	s := newHandshakeTestSession(tr)
	if err := s.handshake("sess-old", ""); err != nil {
		t.Fatalf("handshake: %v", err)
	}

	if srv.calledMethod("session/resume") || srv.calledMethod("session/load") {
		t.Fatalf("no resume call expected, got %v", srv.called())
	}
	if !srv.calledMethod("session/new") {
		t.Fatalf("expected session/new, got %v", srv.called())
	}
}

// A server that omits `sessionId` from a successful resume response must
// still be treated as resumed. Servers that return only `configOptions`
// are valid, and falling through to session/new would silently discard
// the conversation.
func TestHandshake_ResumeWithoutSessionIDInResponse(t *testing.T) {
	tr, srv, cancel := startACPScriptedServer(t, []any{
		initializeResult(false, true, false),
		map[string]any{"configOptions": []any{
			map[string]any{"id": "model", "type": "select", "currentValue": "x"},
		}}, // no "sessionId"
	})
	defer cancel()

	s := newHandshakeTestSession(tr)
	if err := s.handshake("sess-old", ""); err != nil {
		t.Fatalf("handshake: %v", err)
	}

	if !srv.calledMethod("session/resume") {
		t.Fatalf("expected session/resume to be called, got %v", srv.called())
	}
	if srv.calledMethod("session/new") {
		t.Fatalf("session/new must not be called when resume succeeded, got %v", srv.called())
	}
	if got := s.currentACPSessionID(); got != "sess-old" {
		t.Fatalf("agent session id = %q, want the requested %q", got, "sess-old")
	}
}

// Guard rail: the scripted server itself must not deadlock the suite.
func TestHandshake_CompletesPromptly(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		tr, _, cancel := startACPScriptedServer(t, []any{
			initializeResult(false, true, false),
			map[string]any{"sessionId": "sess-resumed"},
		})
		defer cancel()
		s := newHandshakeTestSession(tr)
		if err := s.handshake("sess-old", ""); err != nil {
			t.Errorf("handshake: %v", err)
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("handshake did not complete within 10s")
	}
}
