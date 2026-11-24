package core

import (
	"slices"
	"testing"
	"time"
)

func newModelSwitchTestEngine() (*Engine, *stubPlatformEngine, *stubModelModeAgent) {
	p := &stubPlatformEngine{n: "plain"}
	agent := &stubModelModeAgent{
		model: "gpt-4.1-mini",
		providers: []ProviderConfig{{
			Name:   "openai",
			Model:  "gpt-4.1-mini",
			Models: []ModelOption{{Name: "gpt-4.1", Alias: "gpt"}, {Name: "gpt-4.1-mini", Alias: "mini"}},
		}},
	}
	return NewEngine("test", agent, []Platform{p}, "", LangEnglish), p, agent
}

func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// TestCmdModel_DuringTurnDefersRestartUntilTurnCompletes is a regression test:
// /model sent while a turn is running used to close the agent session at once,
// killing the turn and silently dropping its reply. The switch must instead be
// deferred until the turn's reply has been delivered.
func TestCmdModel_DuringTurnDefersRestartUntilTurnCompletes(t *testing.T) {
	e, p, agent := newModelSwitchTestEngine()
	key := "test:user1"
	session := e.sessions.GetOrCreateActive(key)
	lockGen, ok := session.TryLock()
	if !ok {
		t.Fatal("could not lock session")
	}
	as := newControllableSession("live")
	state := &interactiveState{agentSession: as, busySession: session, platform: p, replyCtx: "ctx"}
	e.interactiveMu.Lock()
	e.interactiveStates[key] = state
	e.interactiveMu.Unlock()

	e.cmdModel(p, &Message{SessionKey: key, ReplyCtx: "ctx"}, []string{"switch", "gpt"})

	if isClosed(as.closed) {
		t.Fatal("/model closed the agent session while its turn was still running")
	}
	if agent.GetModel() != "gpt-4.1" {
		t.Fatalf("agent model = %q, want gpt-4.1", agent.GetModel())
	}
	if want := e.i18n.Tf(MsgModelChangeQueued, "gpt-4.1"); !slices.Contains(p.getSent(), want) {
		t.Fatalf("replies = %q, want queued notice %q", p.getSent(), want)
	}

	// The running turn finishes and its reply must still reach the user.
	as.events <- Event{Type: EventResult, Content: "the reply", Done: true}
	e.processInteractiveEvents(state, session, e.sessions, key, "m1", time.Now(), nil, nil, "ctx", lockGen)
	if !slices.Contains(p.getSent(), "the reply") {
		t.Fatalf("replies = %q, want the in-flight turn's reply delivered", p.getSent())
	}
	if isClosed(as.closed) {
		t.Fatal("agent session closed before the turn's drain step")
	}

	// End of turn: the deferred switch restarts the session.
	if !e.drainPendingMessages(state, session, e.sessions, key, lockGen) {
		t.Fatal("drainPendingMessages did not release the session lock")
	}
	if !isClosed(as.closed) {
		t.Fatal("agent session was not restarted after the turn completed")
	}
	e.interactiveMu.Lock()
	_, stillThere := e.interactiveStates[key]
	e.interactiveMu.Unlock()
	if stillThere {
		t.Fatal("interactive state kept after deferred model switch; next turn would reuse the old model")
	}
	if session.Busy() {
		t.Fatal("session still locked after drain")
	}
}

// TestCmdModel_IdleRestartsSessionImmediately pins the unchanged behaviour
// when no turn is running.
func TestCmdModel_IdleRestartsSessionImmediately(t *testing.T) {
	e, p, _ := newModelSwitchTestEngine()
	key := "test:user1"
	session := e.sessions.GetOrCreateActive(key)
	as := newControllableSession("idle")
	e.interactiveMu.Lock()
	e.interactiveStates[key] = &interactiveState{agentSession: as, busySession: session, platform: p, replyCtx: "ctx"}
	e.interactiveMu.Unlock()

	e.cmdModel(p, &Message{SessionKey: key, ReplyCtx: "ctx"}, []string{"switch", "gpt"})

	if !isClosed(as.closed) {
		t.Fatal("idle /model did not restart the agent session")
	}
	if want := e.i18n.Tf(MsgModelChanged, "gpt-4.1"); !slices.Contains(p.getSent(), want) {
		t.Fatalf("replies = %q, want %q", p.getSent(), want)
	}
}
