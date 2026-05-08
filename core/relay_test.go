package core

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestParseSessionKeyParts(t *testing.T) {
	tests := []struct {
		name             string
		sessionKey       string
		wantPlatform     string
		wantChatID       string
		wantFullChatID   string
		wantErr          bool
	}{
		{
			name:           "feishu thread isolation",
			sessionKey:     "feishu:oc_abc:root:om_xyz",
			wantPlatform:   "feishu",
			wantChatID:     "oc_abc",
			wantFullChatID: "oc_abc:root:om_xyz",
		},
		{
			name:           "feishu share_session_in_channel",
			sessionKey:     "feishu:oc_abc",
			wantPlatform:   "feishu",
			wantChatID:     "oc_abc",
			wantFullChatID: "oc_abc",
		},
		{
			name:           "telegram shared channel with topic",
			sessionKey:     "telegram:123:456",
			wantPlatform:   "telegram",
			wantChatID:     "123",
			wantFullChatID: "123:456",
		},
		{
			name:           "telegram per-user with topic",
			sessionKey:     "telegram:123:456:789",
			wantPlatform:   "telegram",
			wantChatID:     "123",
			wantFullChatID: "123:456:789",
		},
		{
			name:           "telegram shared channel no topic",
			sessionKey:     "telegram:123",
			wantPlatform:   "telegram",
			wantChatID:     "123",
			wantFullChatID: "123",
		},
		{
			name:           "discord per-user",
			sessionKey:     "discord:CHAN:USER",
			wantPlatform:   "discord",
			wantChatID:     "CHAN",
			wantFullChatID: "CHAN:USER",
		},
		{
			name:           "discord shared channel",
			sessionKey:     "discord:CHAN",
			wantPlatform:   "discord",
			wantChatID:     "CHAN",
			wantFullChatID: "CHAN",
		},
		{
			name:           "relay session key",
			sessionKey:     "relay:source:chatID",
			wantPlatform:   "relay",
			wantChatID:     "chatID",
			wantFullChatID: "chatID",
		},
		{
			name:        "invalid format",
			sessionKey:  "invalid",
			wantErr:     true,
		},
		{
			name:        "empty string",
			sessionKey:  "",
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			platform, chatID, fullChatID, err := parseSessionKeyParts(tt.sessionKey)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if platform != tt.wantPlatform {
				t.Fatalf("platform = %q, want %q", platform, tt.wantPlatform)
			}
			if chatID != tt.wantChatID {
				t.Fatalf("chatID = %q, want %q", chatID, tt.wantChatID)
			}
			if fullChatID != tt.wantFullChatID {
				t.Fatalf("fullChatID = %q, want %q", fullChatID, tt.wantFullChatID)
			}
		})
	}
}

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
