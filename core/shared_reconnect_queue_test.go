package core

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestCUJ_C7_RecoveredResultWaitsForNewCLITurnBeforeDrainingQueue(t *testing.T) {
	env, a := newSharedCompatEnv(t)
	env.e.agent = &queuedExternalAgent{a}
	env.send("local work")
	first := awaitSharedCompatCall(t, a)
	env.send("KEEP THIS QUEUED")
	env.await(env.e.i18n.T(MsgMessageQueued))
	as := first.session
	as.mu.Lock()
	as.turn = "new-cli-turn"
	as.mu.Unlock()
	as.emit(Event{Type: EventResult, TurnID: first.turn, Content: "RECOVERED LOCAL RESULT", Done: true, Metadata: map[string]any{"result_authoritative": true}})
	env.await("RECOVERED LOCAL RESULT")
	select {
	case c := <-a.calls:
		t.Fatalf("queued input sent into active CLI turn: %+v", c)
	case <-time.After(120 * time.Millisecond):
	}
	as.mu.Lock()
	as.turn = ""
	as.mu.Unlock()
	as.emit(Event{Type: EventResult, TurnID: "new-cli-turn", Content: "CLI FINAL", Done: true})
	queued := awaitSharedCompatCall(t, a)
	if queued.prompt != "KEEP THIS QUEUED" {
		t.Fatalf("lost queued input: %q", queued.prompt)
	}
	queued.finish("QUEUED INPUT EXECUTED")
	env.await("QUEUED INPUT EXECUTED")
	env.send("/history 10")
	env.await("KEEP THIS QUEUED")
}

// A busy retry is held locally while the shared foreground waits for the
// current CLI turn to finish.  /stop must discard that retained retry too;
// otherwise the message can execute after the user has been told it stopped.
func TestCUJ_C7_StopCancelsRetainedBusyRetry(t *testing.T) {
	env, a := newSharedCompatEnv(t)
	env.e.agent = &busyRetryAgent{a}
	env.send("local work")
	first := awaitSharedCompatCall(t, a)
	env.send("RETRY QUEUED INPUT")
	env.await(env.e.i18n.T(MsgMessageQueued))
	first.finish("FIRST FINAL")
	env.await(env.e.i18n.T(MsgSharedReconnected))
	env.send("/stop")
	env.await(env.e.i18n.T(MsgExecutionStopped))
	select {
	case c := <-a.calls:
		c.finish("UNWANTED")
		t.Fatalf("/stop executed retained queued input after reporting stopped: %q", c.prompt)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestCUJ_C7_CompletedReplayDoesNotRestartTimeouts(t *testing.T) {
	env := newSharedTestEnv(t)
	env.e.eventIdleTimeout = 80 * time.Millisecond
	env.e.maxTurnTime = 120 * time.Millisecond
	env.send("/attach first")
	env.await("Attached to session first")
	as := env.a.connection("first")
	env.send("/steer before reconnect")
	env.await("STEER observed: before reconnect")
	as.emit(Event{Type: EventResult, TurnID: "active-first", Content: "OLD FINAL", Done: true})
	env.await("OLD FINAL")
	as.mu.Lock()
	as.turn = "new-cli-turn"
	as.mu.Unlock()
	env.e.interactiveMu.Lock()
	state := env.e.interactiveStates["test:user"]
	env.e.interactiveMu.Unlock()
	state.mu.Lock()
	replay := state.sharedReplayEvents
	state.mu.Unlock()
	replay <- Event{Type: EventTurnStarted, TurnID: "active-first"}
	replay <- Event{Type: EventText, TurnID: "active-first", Content: "STALE REPLAY BODY", Metadata: map[string]any{"phase": "commentary"}}
	time.Sleep(240 * time.Millisecond)
	env.send("/steer after replay")
	env.await("STEER observed: after replay")
	visible := env.visible()
	if strings.Contains(visible, "STALE REPLAY BODY") || strings.Contains(visible, "timed out") || strings.Contains(visible, "maximum time") {
		t.Fatalf("completed replay resurrected presentation: %s", visible)
	}
	env.send("/detach")
	env.await("Detached")
}

func TestSharedPresentationTimeoutCannotCancelReplacementTurn(t *testing.T) {
	for _, idle := range []bool{true, false} {
		t.Run(map[bool]string{true: "idle", false: "maximum"}[idle], func(t *testing.T) {
			env := newSharedTestEnv(t)
			if idle {
				env.e.eventIdleTimeout = 80 * time.Millisecond
			} else {
				env.e.eventIdleTimeout = 0
				env.e.maxTurnTime = 80 * time.Millisecond
			}
			env.send("/attach first")
			env.await("Attached to session first")
			as := env.a.connection("first")
			as.emit(Event{Type: EventToolUse, TurnID: "active-first", ToolName: "Bash", ToolInput: "OLD LIVE"})
			env.await("OLD LIVE")
			as.mu.Lock()
			as.turn = "replacement"
			as.mu.Unlock()
			time.Sleep(180 * time.Millisecond)
			env.send("/steer still alive")
			env.await("STEER observed: still alive")
			if strings.Contains(env.visible(), "timed out") || strings.Contains(env.visible(), "maximum time") {
				t.Fatal(env.visible())
			}
		})
	}
}

type busyRetryAgent struct{ *sharedCompatAgent }

func (a *busyRetryAgent) StartSession(ctx context.Context, id string) (AgentSession, error) {
	as, err := a.sharedCompatAgent.StartSession(ctx, id)
	if err != nil {
		return nil, err
	}
	return &busyRetrySession{sharedCompatSession: as.(*sharedCompatSession)}, nil
}

type busyRetrySession struct {
	*sharedCompatSession
	rejected bool
}

func (s *busyRetrySession) SendTurn(prompt, id string, images []ImageAttachment, files []FileAttachment) (string, error) {
	s.mu.Lock()
	reject := prompt == "RETRY QUEUED INPUT" && !s.rejected
	if reject {
		s.rejected = true
		s.turn = "racing-cli-turn"
	}
	s.mu.Unlock()
	if reject {
		s.emit(Event{Type: EventRuntimeStatus, Content: "connected"})
		return "", ErrAgentTurnBusy
	}
	return s.sharedCompatSession.SendTurn(prompt, id, images, files)
}

func TestCUJ_C7_BusySendRetryPreservesQueueAndHistory(t *testing.T) {
	env, a := newSharedCompatEnv(t)
	env.e.agent = &busyRetryAgent{a}
	env.send("local work")
	first := awaitSharedCompatCall(t, a)
	env.send("RETRY QUEUED INPUT")
	env.await(env.e.i18n.T(MsgMessageQueued))
	first.finish("FIRST FINAL")
	env.await(env.e.i18n.T(MsgSharedReconnected))
	select {
	case c := <-a.calls:
		t.Fatalf("busy input accepted too early: %+v", c)
	case <-time.After(100 * time.Millisecond):
	}
	first.session.mu.Lock()
	first.session.turn = ""
	first.session.mu.Unlock()
	first.session.emit(Event{Type: EventResult, TurnID: "racing-cli-turn", Content: "CLI DONE", Done: true})
	next := awaitSharedCompatCall(t, a)
	if next.prompt != "RETRY QUEUED INPUT" {
		t.Fatalf("lost busy input: %q", next.prompt)
	}
	next.finish("RETRY SUCCEEDED")
	env.await("RETRY SUCCEEDED")
	env.send("/history 10")
	env.await("History (last")
	history := env.e.sessions.GetOrCreateActive("test:user").GetHistory(100)
	count := 0
	for _, item := range history {
		if item.Role == "user" && item.Content == "RETRY QUEUED INPUT" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("retry duplicated user history: %+v", history)
	}
	if strings.Contains(env.visible(), "agent turn is busy") {
		t.Fatal("retryable rejection surfaced as failure")
	}
}
