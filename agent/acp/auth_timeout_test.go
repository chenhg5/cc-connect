package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// mockACPServer simulates an ACP agent subprocess over pipes.
// It reads JSON-RPC requests and writes responses. The authenticate
// handler can be configured to delay or block.
type mockACPServer struct {
	t            *testing.T
	stdin        io.WriteCloser  // server writes responses here (→ transport reads)
	stdout       io.ReadCloser   // server reads requests here (← transport writes)
	authDelay    time.Duration   // how long to delay the authenticate response
	authErr      bool            // if true, return an error for authenticate
	initResponse json.RawMessage // custom initialize response
}

func newMockACPServer(t *testing.T) (
	serverStdin io.ReadCloser, // transport reads from this
	serverStdout io.WriteCloser, // transport writes to this
	srv *mockACPServer,
) {
	t.Helper()
	// Pipe 1: transport writes → server reads
	trOutR, trOutW := io.Pipe()
	// Pipe 2: server writes → transport reads
	srvOutR, srvOutW := io.Pipe()

	srv = &mockACPServer{
		t:      t,
		stdin:  srvOutW,
		stdout: trOutR,
		initResponse: json.RawMessage(`{
			"protocolVersion": 1,
			"agentCapabilities": {"loadSession": false}
		}`),
	}

	return srvOutR, trOutW, srv
}

func (s *mockACPServer) serve(ctx context.Context) {
	sc := bufio.NewScanner(s.stdout)
	for sc.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}

		var req struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			continue
		}

		switch req.Method {
		case "initialize":
			s.respond(req.ID, s.initResponse, nil)

		case "authenticate":
			if s.authDelay > 0 {
				select {
				case <-time.After(s.authDelay):
				case <-ctx.Done():
					return
				}
			}
			if s.authErr {
				s.respondErr(req.ID, -32000, "auth failed")
			} else {
				s.respond(req.ID, json.RawMessage(`{}`), nil)
			}

		case "session/new":
			s.respond(req.ID, json.RawMessage(`{"sessionId":"test-session-1"}`), nil)

		default:
			s.respond(req.ID, json.RawMessage(`{}`), nil)
		}
	}
}

func (s *mockACPServer) respond(id json.RawMessage, result json.RawMessage, rpcErr *rpcErrPayload) {
	msg := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
	}
	if rpcErr != nil {
		msg["error"] = rpcErr
	} else {
		msg["result"] = result
	}
	data, _ := json.Marshal(msg)
	data = append(data, '\n')
	if _, err := s.stdin.Write(data); err != nil {
		s.t.Logf("mock server write error: %v", err)
	}
}

func (s *mockACPServer) respondErr(id json.RawMessage, code int, message string) {
	s.respond(id, nil, &rpcErrPayload{Code: code, Message: message})
}

// ─── Tests ─────────────────────────────────────────────────────────────────

func TestHandshake_AuthenticateSucceedsBeforeTimeout(t *testing.T) {
	srvIn, trOut, srv := newMockACPServer(t)
	srv.authDelay = 100 * time.Millisecond // fast auth

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tr := newTransport(srvIn, trOut, nil, nil)
	go tr.readLoop(ctx)
	go srv.serve(ctx)

	s := &acpSession{
		ctx:         ctx,
		cancel:      cancel,
		tr:          tr,
		authTimeout: 5 * time.Second,
		events:      make(chan core.Event, 128),
		permByID:    make(map[string]permState),
	}

	err := s.handshake("", "cursor_login")
	if err != nil {
		t.Fatalf("handshake() error = %v, want nil", err)
	}
	if s.currentACPSessionID() != "test-session-1" {
		t.Fatalf("session ID = %q, want test-session-1", s.currentACPSessionID())
	}
}

func TestHandshake_AuthenticateTimesOut(t *testing.T) {
	srvIn, trOut, srv := newMockACPServer(t)
	srv.authDelay = 10 * time.Second // will exceed timeout

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tr := newTransport(srvIn, trOut, nil, nil)
	go tr.readLoop(ctx)
	go srv.serve(ctx)

	s := &acpSession{
		ctx:         ctx,
		cancel:      cancel,
		tr:          tr,
		authTimeout: 200 * time.Millisecond, // short timeout
		events:      make(chan core.Event, 128),
		permByID:    make(map[string]permState),
	}

	start := time.Now()
	err := s.handshake("", "cursor_login")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("handshake() error = nil, want timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error = %q, want 'timed out' message", err.Error())
	}
	if !strings.Contains(err.Error(), "cursor_login") {
		t.Fatalf("error = %q, want auth method in message", err.Error())
	}
	// Should fail fast (within ~timeout), not block for 10s
	if elapsed > 2*time.Second {
		t.Fatalf("elapsed = %v, handshake should have timed out in ~200ms", elapsed)
	}
}

func TestHandshake_NoAuthMethodSkipsAuthenticate(t *testing.T) {
	srvIn, trOut, srv := newMockACPServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tr := newTransport(srvIn, trOut, nil, nil)
	go tr.readLoop(ctx)
	go srv.serve(ctx)

	s := &acpSession{
		ctx:         ctx,
		cancel:      cancel,
		tr:          tr,
		authTimeout: 5 * time.Second,
		events:      make(chan core.Event, 128),
		permByID:    make(map[string]permState),
	}

	err := s.handshake("", "")
	if err != nil {
		t.Fatalf("handshake() error = %v, want nil", err)
	}
	if s.currentACPSessionID() != "test-session-1" {
		t.Fatalf("session ID = %q, want test-session-1", s.currentACPSessionID())
	}
}

func TestHandshake_AuthenticateErrorPropagated(t *testing.T) {
	srvIn, trOut, srv := newMockACPServer(t)
	srv.authErr = true

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tr := newTransport(srvIn, trOut, nil, nil)
	go tr.readLoop(ctx)
	go srv.serve(ctx)

	s := &acpSession{
		ctx:         ctx,
		cancel:      cancel,
		tr:          tr,
		authTimeout: 5 * time.Second,
		events:      make(chan core.Event, 128),
		permByID:    make(map[string]permState),
	}

	err := s.handshake("", "cursor_login")
	if err == nil {
		t.Fatal("handshake() error = nil, want auth error")
	}
	if !strings.Contains(err.Error(), "authenticate") {
		t.Fatalf("error = %q, want 'authenticate' in message", err.Error())
	}
	// Should NOT contain "timed out" — this is a regular auth error
	if strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error = %q, should not say timed out for non-timeout errors", err.Error())
	}
}

func TestNew_AuthTimeoutDefault(t *testing.T) {
	a, err := New(map[string]any{
		"command":     "true",
		"auth_method": "cursor_login",
	})
	if err != nil {
		t.Fatal(err)
	}
	agent := a.(*Agent)
	if agent.authTimeout != defaultAuthTimeout {
		t.Fatalf("authTimeout = %v, want %v", agent.authTimeout, defaultAuthTimeout)
	}
}

func TestNew_AuthTimeoutCustom(t *testing.T) {
	a, err := New(map[string]any{
		"command":      "true",
		"auth_method":  "cursor_login",
		"auth_timeout": "5m",
	})
	if err != nil {
		t.Fatal(err)
	}
	agent := a.(*Agent)
	if agent.authTimeout != 5*time.Minute {
		t.Fatalf("authTimeout = %v, want 5m", agent.authTimeout)
	}
}

func TestNew_AuthTimeoutInvalid(t *testing.T) {
	_, err := New(map[string]any{
		"command":      "true",
		"auth_timeout": "not-a-duration",
	})
	if err == nil {
		t.Fatal("expected error for invalid auth_timeout")
	}
	if !strings.Contains(err.Error(), "invalid auth_timeout") {
		t.Fatalf("error = %q, want 'invalid auth_timeout'", err.Error())
	}
}

func TestNew_AuthTimeoutZeroDisablesExtraTimeout(t *testing.T) {
	a, err := New(map[string]any{
		"command":      "true",
		"auth_timeout": "0s",
	})
	if err != nil {
		t.Fatal(err)
	}
	agent := a.(*Agent)
	if agent.authTimeout != 0 {
		t.Fatalf("authTimeout = %v, want 0 (no extra timeout)", agent.authTimeout)
	}
}

func TestNew_AuthTimeoutNegativeRejected(t *testing.T) {
	_, err := New(map[string]any{
		"command":      "true",
		"auth_timeout": "-5m",
	})
	if err == nil {
		t.Fatal("expected error for negative auth_timeout")
	}
	if !strings.Contains(err.Error(), "must not be negative") {
		t.Fatalf("error = %q, want 'must not be negative'", err.Error())
	}
}

func TestHandshake_ZeroAuthTimeoutUsesSessionContext(t *testing.T) {
	// With authTimeout=0, no additional timeout is applied — uses session ctx directly.
	srvIn, trOut, srv := newMockACPServer(t)
	srv.authDelay = 50 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tr := newTransport(srvIn, trOut, nil, nil)
	go tr.readLoop(ctx)
	go srv.serve(ctx)

	s := &acpSession{
		ctx:         ctx,
		cancel:      cancel,
		tr:          tr,
		authTimeout: 0, // no additional timeout
		events:      make(chan core.Event, 128),
		permByID:    make(map[string]permState),
	}

	err := s.handshake("", "cursor_login")
	if err != nil {
		t.Fatalf("handshake() error = %v", err)
	}
}

// Ensure the timeout error message includes actionable guidance.
func TestHandshake_TimeoutErrorMessage(t *testing.T) {
	srvIn, trOut, srv := newMockACPServer(t)
	srv.authDelay = 10 * time.Second

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tr := newTransport(srvIn, trOut, nil, nil)
	go tr.readLoop(ctx)
	go srv.serve(ctx)

	timeout := 100 * time.Millisecond
	s := &acpSession{
		ctx:         ctx,
		cancel:      cancel,
		tr:          tr,
		authTimeout: timeout,
		events:      make(chan core.Event, 128),
		permByID:    make(map[string]permState),
	}

	err := s.handshake("", "my_auth")
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	for _, want := range []string{"my_auth", "timed out", fmt.Sprint(timeout), "authenticate manually"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q: %s", want, msg)
		}
	}
}
