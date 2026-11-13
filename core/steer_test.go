package core

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func steerTestEngine(t *testing.T) (*Engine, *stubPlatformEngine) {
	t.Helper()
	p := &stubPlatformEngine{n: "test"}
	sess := newResultAgentSession("ok")
	e := NewEngine("test", &resultAgent{session: sess}, []Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), LangChinese)
	t.Cleanup(e.cancel)
	return e, p
}

func TestSteerBusySession_ForwardsIntoRunningTurn(t *testing.T) {
	e, p := steerTestEngine(t)
	state := &interactiveState{agentSession: newResultAgentSession("ok"), platform: p, replyCtx: "ctx", turnActive: true}
	e.interactiveStates["k"] = state
	sessions := e.sessions
	session := sessions.GetOrCreateActive("k")

	msg := &Message{SessionKey: "k", Platform: "test", UserID: "u1", UserName: "amy", Content: "快点", ReplyCtx: "ctx"}
	if !e.steerBusySession(p, msg, "k", session, sessions) {
		t.Fatal("steerBusySession() = false, want true for a running claude-style session")
	}
	if len(state.pendingMessages) != 0 {
		t.Fatalf("pendingMessages = %d, want 0 (steered messages must not queue)", len(state.pendingMessages))
	}
	if len(p.sent) == 0 || !strings.Contains(p.sent[len(p.sent)-1], "插话") {
		t.Fatalf("replies = %v, want a steer ack", p.sent)
	}
}

func TestSteerBusySession_QueueModeAndIdleFallBack(t *testing.T) {
	e, p := steerTestEngine(t)
	state := &interactiveState{agentSession: newResultAgentSession("ok"), platform: p, replyCtx: "ctx", turnActive: true}
	e.interactiveStates["k"] = state
	sessions := e.sessions
	session := sessions.GetOrCreateActive("k")
	msg := &Message{SessionKey: "k", Content: "hi", ReplyCtx: "ctx"}

	e.SetBusyMode("queue")
	if e.steerBusySession(p, msg, "k", session, sessions) {
		t.Fatal("steerBusySession() = true in queue mode, want false")
	}

	e.SetBusyMode("steer")
	state.mu.Lock()
	state.turnActive = false
	state.mu.Unlock()
	if e.steerBusySession(p, msg, "k", session, sessions) {
		t.Fatal("steerBusySession() = true while idle, want false")
	}
}

// Without a deferred-ack handle the engine falls back to a plain reply.
func TestSteerBusySession_FallsBackToPlainReply(t *testing.T) {
	e, p := steerTestEngine(t)
	state := &interactiveState{agentSession: newResultAgentSession("ok"), platform: p, replyCtx: "ctx", turnActive: true}
	e.interactiveStates["k"] = state
	sessions := e.sessions
	session := sessions.GetOrCreateActive("k")

	msg := &Message{SessionKey: "k", Platform: "test", UserID: "u", Content: "快点", ReplyCtx: "ctx"}
	if !e.steerBusySession(p, msg, "k", session, sessions) {
		t.Fatal("steerBusySession() = false, want true")
	}
	found := false
	for _, sent := range p.sent {
		if strings.Contains(sent, "插话") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no steer ack sent: %v", p.sent)
	}
}

type ackStubPlatform struct {
	stubPlatformEngine
	mu      sync.Mutex
	updated []string
}

func (p *ackStubPlatform) SendAck(_ context.Context, _ any, text string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.updated = append(p.updated, text)
	return nil
}

// With a deferred ack the single bubble carries the steer text.
func TestSteerRewritesExistingAck(t *testing.T) {
	p := &ackStubPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	e := NewEngine("test", &resultAgent{session: newResultAgentSession("ok")}, []Platform{p}, "", LangChinese)
	t.Cleanup(e.cancel)

	state := &interactiveState{agentSession: newResultAgentSession("ok"), platform: p, replyCtx: "ctx", turnActive: true}
	e.interactiveStates["k"] = state
	sessions := e.sessions
	session := sessions.GetOrCreateActive("k")

	msg := &Message{SessionKey: "k", Platform: "test", UserID: "u", Content: "快点", ReplyCtx: "ctx", AckHandle: "ack-1"}
	if !e.steerBusySession(p, msg, "k", session, sessions) {
		t.Fatal("steerBusySession() = false")
	}

	p.mu.Lock()
	updated := append([]string(nil), p.updated...)
	sent := append([]string(nil), p.sent...)
	p.mu.Unlock()

	if len(updated) != 1 || !strings.Contains(updated[0], "插话") {
		t.Fatalf("acks = %v, want exactly one steer text", updated)
	}
	for _, s := range sent {
		if strings.Contains(s, "插话") {
			t.Fatalf("separate steer reply sent: %q", s)
		}
	}
}
