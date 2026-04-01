package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRelayManager_DefaultTimeout(t *testing.T) {
	rm := NewRelayManager("")

	if rm.timeout != relayTimeout {
		t.Fatalf("rm.timeout = %v, want %v", rm.timeout, relayTimeout)
	}
}

func TestRelayManager_RelayContextHonorsConfiguredTimeout(t *testing.T) {
	rm := NewRelayManager("")
	rm.SetTimeout(3 * time.Second)

	ctx, cancel := rm.relayContext(context.Background())
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected relay context deadline")
	}
	if remaining := time.Until(deadline); remaining <= 0 || remaining > 3*time.Second {
		t.Fatalf("time until deadline = %v, want within (0, 3s]", remaining)
	}
}

func TestRelayManager_RelayContextDisablesTimeoutAtZero(t *testing.T) {
	rm := NewRelayManager("")
	rm.SetTimeout(0)

	baseCtx := context.Background()
	ctx, cancel := rm.relayContext(baseCtx)
	defer cancel()

	if ctx != baseCtx {
		t.Fatal("expected relayContext to return the original context when timeout is disabled")
	}
	if _, ok := ctx.Deadline(); ok {
		t.Fatal("expected no deadline when timeout is disabled")
	}
}

func TestHandleRelay_ReturnsPartialOnTimeout(t *testing.T) {
	e := newTestEngine()
	session := newControllableSession("relay-session")
	e.agent = &controllableAgent{nextSession: session}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	type relayResult struct {
		resp string
		err  error
	}
	done := make(chan relayResult, 1)
	go func() {
		resp, err := e.HandleRelay(ctx, "source", "test:chat-1:user", "hello")
		done <- relayResult{resp: resp, err: err}
	}()

	session.events <- Event{Type: EventText, Content: "partial response", SessionID: "relay-session"}
	time.Sleep(40 * time.Millisecond)
	session.events <- Event{Type: EventThinking, Content: "still working"}

	got := <-done
	if got.err != nil {
		t.Fatalf("HandleRelay() error = %v, want nil", got.err)
	}
	if got.resp != "partial response" {
		t.Fatalf("HandleRelay() response = %q, want %q", got.resp, "partial response")
	}
}

func TestHandleRelay_TimeoutWithoutTextReturnsContextError(t *testing.T) {
	e := newTestEngine()
	session := newControllableSession("relay-session")
	e.agent = &controllableAgent{nextSession: session}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	type relayResult struct {
		resp string
		err  error
	}
	done := make(chan relayResult, 1)
	go func() {
		resp, err := e.HandleRelay(ctx, "source", "test:chat-1:user", "hello")
		done <- relayResult{resp: resp, err: err}
	}()

	time.Sleep(40 * time.Millisecond)
	session.events <- Event{Type: EventThinking, Content: "still working"}

	got := <-done
	if got.resp != "" {
		t.Fatalf("HandleRelay() response = %q, want empty", got.resp)
	}
	if !errors.Is(got.err, context.DeadlineExceeded) {
		t.Fatalf("HandleRelay() error = %v, want context deadline exceeded", got.err)
	}
}

func TestHandleRelay_SingleWorkspaceUsesGlobalAgentAndSourceSessionKey(t *testing.T) {
	e := newTestEngine()
	agent := &sessionEnvRecordingAgent{session: newResultAgentSession("global")}
	e.agent = agent

	sourceSessionKey := "discord:C1:U1"
	resp, err := e.HandleRelay(context.Background(), "source", sourceSessionKey, "hello")
	if err != nil {
		t.Fatalf("HandleRelay() error = %v", err)
	}
	if resp != "global" {
		t.Fatalf("HandleRelay() response = %q, want %q", resp, "global")
	}
	if got := agent.EnvValue("CC_SESSION_KEY"); got != sourceSessionKey {
		t.Fatalf("CC_SESSION_KEY = %q, want %q", got, sourceSessionKey)
	}
	if got := e.sessions.ActiveSessionID("relay:source:discord:C1"); got == "" {
		t.Fatal("expected relay session to be stored under platform-qualified relay key")
	}
}

func TestHandleRelay_MultiWorkspaceRoutesBySourceSessionKey(t *testing.T) {
	baseDir := t.TempDir()
	channelID := "C42"
	wsDir := filepath.Join(baseDir, "relay-ws")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	p := &mockChannelResolver{name: "mock", names: map[string]string{channelID: "relay-ws"}}
	globalAgent := &sessionEnvRecordingAgent{session: newResultAgentSession("global")}
	e := NewEngine("test", globalAgent, []Platform{p}, "", LangEnglish)
	e.SetMultiWorkspace(baseDir, filepath.Join(t.TempDir(), "bindings.json"))

	normalizedWsDir := normalizeWorkspacePath(wsDir)
	workspaceAgent := &sessionEnvRecordingAgent{session: newResultAgentSession("workspace")}
	ws := e.workspacePool.GetOrCreate(normalizedWsDir)
	ws.agent = workspaceAgent
	ws.sessions = NewSessionManager("")

	sourceSessionKey := "mock:" + channelID + ":U1"
	resp, err := e.HandleRelay(context.Background(), "source", sourceSessionKey, "hello")
	if err != nil {
		t.Fatalf("HandleRelay() error = %v", err)
	}
	if resp != "workspace" {
		t.Fatalf("HandleRelay() response = %q, want %q", resp, "workspace")
	}
	if got := workspaceAgent.EnvValue("CC_SESSION_KEY"); got != sourceSessionKey {
		t.Fatalf("workspace CC_SESSION_KEY = %q, want %q", got, sourceSessionKey)
	}
	if got := globalAgent.EnvValue("CC_SESSION_KEY"); got != "" {
		t.Fatalf("global agent should not receive relay env, got %q", got)
	}
	if got := e.sessions.ActiveSessionID("relay:source:mock:" + channelID); got != "" {
		t.Fatalf("expected no global relay session, got %q", got)
	}
	if got := ws.sessions.ActiveSessionID("relay:source:mock:" + channelID); got == "" {
		t.Fatal("expected relay session in workspace session manager")
	}
	if b := e.workspaceBindings.Lookup("project:test", workspaceChannelKey("mock", channelID)); b == nil || b.Workspace != normalizedWsDir {
		t.Fatalf("expected convention binding to be created for %q", normalizedWsDir)
	}
}

func TestHandleRelay_MultiWorkspaceRequiresWorkspaceBinding(t *testing.T) {
	baseDir := t.TempDir()
	globalAgent := &sessionEnvRecordingAgent{session: newResultAgentSession("global")}
	e := NewEngine("test", globalAgent, nil, "", LangEnglish)
	e.SetMultiWorkspace(baseDir, filepath.Join(t.TempDir(), "bindings.json"))

	resp, err := e.HandleRelay(context.Background(), "source", "mock:C404:U1", "hello")
	if err == nil {
		t.Fatal("expected error for unbound relay workspace")
	}
	if resp != "" {
		t.Fatalf("HandleRelay() response = %q, want empty", resp)
	}
	if !strings.Contains(err.Error(), "no workspace binding") {
		t.Fatalf("HandleRelay() error = %v, want missing workspace binding", err)
	}
	if got := e.sessions.ActiveSessionID("relay:source:mock:C404"); got != "" {
		t.Fatalf("expected no global relay session, got %q", got)
	}
}
