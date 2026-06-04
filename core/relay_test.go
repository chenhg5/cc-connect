package core

import (
	"context"
	"errors"
	"fmt"
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

type relayVisibilityPlatform struct {
	stubPlatformEngine
	reconstructed []string
}

func (p *relayVisibilityPlatform) ReconstructReplyCtx(sessionKey string) (any, error) {
	p.mu.Lock()
	p.reconstructed = append(p.reconstructed, sessionKey)
	p.mu.Unlock()
	return sessionKey, nil
}

func runRelayVisibilityScenario(t *testing.T, visibility string) (resp string, sourceSent []string, targetSent []string) {
	t.Helper()

	sourcePlatform := &relayVisibilityPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	targetPlatform := &relayVisibilityPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	sourceEngine := NewEngine("source", &stubAgent{}, []Platform{sourcePlatform}, "", LangEnglish)
	targetSession := newControllableSession("target-session")
	targetEngine := NewEngine("target", &controllableAgent{nextSession: targetSession}, []Platform{targetPlatform}, "", LangEnglish)

	rm := NewRelayManager("")
	rm.Bind("feishu", "chat-1", map[string]string{
		"source": "source-bot",
		"target": "target-bot",
	})
	rm.RegisterEngine("source", sourceEngine)
	rm.RegisterEngine("target", targetEngine)
	if visibility != "" {
		rm.SetVisibility(visibility)
	}

	type relayResult struct {
		resp string
		err  error
	}
	done := make(chan relayResult, 1)
	go func() {
		result, err := rm.Send(context.Background(), RelayRequest{
			From:       "source",
			To:         "target",
			SessionKey: "feishu:chat-1:user-1",
			Message:    "please ask target",
		})
		if result != nil {
			done <- relayResult{resp: result.Response, err: err}
			return
		}
		done <- relayResult{err: err}
	}()

	targetSession.events <- Event{Type: EventResult, Content: "target says long answer"}

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("RelayManager.Send() error = %v", got.err)
		}
		resp = got.resp
	case <-time.After(2 * time.Second):
		t.Fatal("RelayManager.Send() did not return")
	}

	return resp, sourcePlatform.getSent(), targetPlatform.getSent()
}

func TestRelayManager_DefaultVisibilityEchoesFullMessages(t *testing.T) {
	resp, sourceSent, targetSent := runRelayVisibilityScenario(t, "")

	if resp != "target says long answer" {
		t.Fatalf("response = %q, want target response", resp)
	}
	if len(sourceSent) != 1 || sourceSent[0] != "[source-bot → target-bot] please ask target" {
		t.Fatalf("source sent = %#v, want full relay request", sourceSent)
	}
	if len(targetSent) != 1 || targetSent[0] != "[target-bot] target says long answer" {
		t.Fatalf("target sent = %#v, want full relay response", targetSent)
	}
}

func TestRelayManager_VisibilitySummarySuppressesBodies(t *testing.T) {
	resp, sourceSent, targetSent := runRelayVisibilityScenario(t, RelayVisibilitySummary)

	if resp != "target says long answer" {
		t.Fatalf("response = %q, want target response", resp)
	}
	if len(sourceSent) != 1 || sourceSent[0] != "[source-bot → target-bot] relay request sent" {
		t.Fatalf("source sent = %#v, want summary relay request", sourceSent)
	}
	if len(targetSent) != 1 || targetSent[0] != "[target-bot] relay response ready (23 chars)" {
		t.Fatalf("target sent = %#v, want summary relay response", targetSent)
	}
}

func TestRelayManager_VisibilityNoneSuppressesGroupEcho(t *testing.T) {
	resp, sourceSent, targetSent := runRelayVisibilityScenario(t, RelayVisibilityNone)

	if resp != "target says long answer" {
		t.Fatalf("response = %q, want target response", resp)
	}
	if len(sourceSent) != 0 {
		t.Fatalf("source sent = %#v, want no relay request echo", sourceSent)
	}
	if len(targetSent) != 0 {
		t.Fatalf("target sent = %#v, want no relay response echo", targetSent)
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
		resp, err := e.HandleRelay(ctx, "source", "chat-1", "hello")
		done <- relayResult{resp: resp, err: err}
	}()

	session.events <- Event{Type: EventText, Content: "partial response", SessionID: "relay-session"}
	time.Sleep(40 * time.Millisecond)
	// After timeout, HandleRelay consumes the next event from the channel to
	// unblock the for-range loop, then checks ctx.Err() and spawns the drain
	// goroutine. We need two events: one to unblock HandleRelay, and one
	// EventResult for the drain goroutine to close the session cleanly.
	session.events <- Event{Type: EventThinking, Content: "still working"}
	session.events <- Event{Type: EventResult, Content: "done"}

	got := <-done
	if got.err != nil {
		t.Fatalf("HandleRelay() error = %v, want nil", got.err)
	}
	if got.resp != "partial response" {
		t.Fatalf("HandleRelay() response = %q, want %q", got.resp, "partial response")
	}

	// Wait for the background drain goroutine to close the session.
	select {
	case <-session.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("background drain goroutine did not close the session")
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
		resp, err := e.HandleRelay(ctx, "source", "chat-1", "hello")
		done <- relayResult{resp: resp, err: err}
	}()

	time.Sleep(40 * time.Millisecond)
	// One event to unblock HandleRelay's for-range, one for the drain goroutine.
	session.events <- Event{Type: EventThinking, Content: "still working"}
	session.events <- Event{Type: EventResult, Content: "done"}

	got := <-done
	if got.resp != "" {
		t.Fatalf("HandleRelay() response = %q, want empty", got.resp)
	}
	if !errors.Is(got.err, context.DeadlineExceeded) {
		t.Fatalf("HandleRelay() error = %v, want context deadline exceeded", got.err)
	}

	select {
	case <-session.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("background drain goroutine did not close the session")
	}
}

// relayFallbackAgent fails the first StartSession call (simulating a corrupt
// resume) and returns freshSession on the second call (fresh start).
type relayFallbackAgent struct {
	callCount    int
	freshSession AgentSession
}

func (a *relayFallbackAgent) Name() string { return "fallback" }
func (a *relayFallbackAgent) StartSession(_ context.Context, sessionID string) (AgentSession, error) {
	a.callCount++
	if a.callCount == 1 && sessionID != "" {
		return nil, fmt.Errorf("simulated resume failure")
	}
	return a.freshSession, nil
}
func (a *relayFallbackAgent) ListSessions(_ context.Context) ([]AgentSessionInfo, error) {
	return nil, nil
}
func (a *relayFallbackAgent) Stop() error { return nil }

func TestHandleRelay_ResumeFailureFallsBackToFreshSession(t *testing.T) {
	e := newTestEngine()
	freshSession := newControllableSession("fresh-session")

	e.agent = &relayFallbackAgent{freshSession: freshSession}

	// Pre-set a stale session ID so that the first StartSession tries to resume.
	relaySessionKey := "relay:source:chat-1"
	sess := e.sessions.GetOrCreateActive(relaySessionKey)
	sess.SetAgentSessionID("stale-id", "fallback")
	e.sessions.Save()

	ctx := context.Background()
	done := make(chan string, 1)
	go func() {
		resp, err := e.HandleRelay(ctx, "source", "chat-1", "hello")
		if err != nil {
			done <- "error: " + err.Error()
			return
		}
		done <- resp
	}()

	// The fresh session should receive the message and respond.
	freshSession.events <- Event{Type: EventResult, Content: "recovered", SessionID: "fresh-session"}

	got := <-done
	if got != "recovered" {
		t.Fatalf("HandleRelay() = %q, want %q", got, "recovered")
	}

	// Session should be closed after EventResult.
	select {
	case <-freshSession.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("session was not closed after EventResult")
	}
}
