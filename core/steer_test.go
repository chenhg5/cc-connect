package core

import (
	"path/filepath"
	"strings"
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

// When the platform already acknowledged the message, steering must not add a
// second "已插话" bubble.
func TestSteerBusySession_NoExtraAckWhenPlatformAcked(t *testing.T) {
	e, p := steerTestEngine(t)
	state := &interactiveState{agentSession: newResultAgentSession("ok"), platform: p, replyCtx: "ctx", turnActive: true}
	e.interactiveStates["k"] = state
	sessions := e.sessions
	session := sessions.GetOrCreateActive("k")

	msg := &Message{SessionKey: "k", Platform: "test", UserID: "u", Content: "快点", ReplyCtx: "ctx", Acked: true}
	if !e.steerBusySession(p, msg, "k", session, sessions) {
		t.Fatal("steerBusySession() = false, want true")
	}
	for _, sent := range p.sent {
		if strings.Contains(sent, "插话") {
			t.Fatalf("extra steer ack sent despite platform ack: %q", sent)
		}
	}
}
