package core

import (
	"strings"
	"testing"
)

// A cron/timer job that fires while its channel's turn is running must wait in the queue and
// run when that turn ends, with its reply sent to the channel (2026-10-19: e10bb278 was dropped
// as "session is busy" and nothing showed in the channel).

func busySessionWithLiveState(t *testing.T, e *Engine, p Platform, key string, as AgentSession) (*Session, *interactiveState) {
	t.Helper()
	session := e.sessions.GetOrCreateActive(key)
	if !session.TryLock() {
		t.Fatal("could not lock session to simulate a running turn")
	}
	state := &interactiveState{agentSession: as, platform: p, replyCtx: "user-turn-ctx"}
	e.interactiveMu.Lock()
	e.interactiveStates[key] = state
	e.interactiveMu.Unlock()
	return session, state
}

func assertQueuedThenDelivered(t *testing.T, e *Engine, p *stubCronReplyTargetPlatform, session *Session, state *interactiveState, key, prompt, result string) {
	t.Helper()
	state.mu.Lock()
	queued := len(state.pendingMessages)
	var content string
	if queued == 1 {
		content = state.pendingMessages[0].content
	}
	state.mu.Unlock()
	if queued != 1 || !strings.Contains(content, prompt) {
		t.Fatalf("pending = %d (content %q), want the job's prompt queued once", queued, content)
	}
	for _, s := range p.getSent() {
		if s == result {
			t.Fatalf("result sent before the running turn ended: %#v", p.getSent())
		}
	}

	// The running turn ends: its drain loop takes the queued job.
	if !e.drainPendingMessages(state, session, e.sessions, key) {
		t.Fatal("drainPendingMessages did not unlock the session")
	}
	found := false
	for _, s := range p.getSent() {
		if s == result {
			found = true
		}
	}
	if !found {
		t.Fatalf("sent = %#v, want the job's result %q in the channel", p.getSent(), result)
	}
	if session.Busy() {
		t.Fatal("session still busy after the queued job ran")
	}
}

func TestExecuteTimerJob_BusySessionQueuesAndDeliversAfterTurn(t *testing.T) {
	p := &stubCronReplyTargetPlatform{stubPlatformEngine: stubPlatformEngine{n: "discord"}}
	as := newResultAgentSession("timer done")
	e := NewEngine("test", &resultAgent{session: as}, []Platform{p}, "", LangEnglish)
	defer e.cancel()

	key := "discord:channel-1:user-1"
	session, state := busySessionWithLiveState(t, e, p, key, as)

	job := &TimerJob{ID: "t1", SessionKey: key, Prompt: "check the build", Description: "build check"}
	if err := e.ExecuteTimerJob(job); err != nil {
		t.Fatalf("ExecuteTimerJob() on a busy session = %v, want nil (queued)", err)
	}
	assertQueuedThenDelivered(t, e, p, session, state, key, "check the build", "timer done")
	if len(as.sentPrompts) != 1 || !strings.Contains(as.sentPrompts[0], "check the build") {
		t.Fatalf("agent prompts = %#v, want the timer prompt", as.sentPrompts)
	}
}

func TestExecuteCronJob_BusySessionQueuesAndDeliversAfterTurn(t *testing.T) {
	p := &stubCronReplyTargetPlatform{stubPlatformEngine: stubPlatformEngine{n: "discord"}}
	as := newResultAgentSession("cron done")
	e := NewEngine("test", &resultAgent{session: as}, []Platform{p}, "", LangEnglish)
	defer e.cancel()

	key := "discord:channel-1:user-1"
	session, state := busySessionWithLiveState(t, e, p, key, as)

	job := &CronJob{ID: "c1", SessionKey: key, Prompt: "daily digest", Description: "digest"}
	if err := e.ExecuteCronJob(job); err != nil {
		t.Fatalf("ExecuteCronJob() on a busy session = %v, want nil (queued, no empty-response error)", err)
	}
	assertQueuedThenDelivered(t, e, p, session, state, key, "daily digest", "cron done")
}

// The race guard: if the running turn finished between the failed TryLock and the queue append,
// the job must still run (drainOrphanedQueue), not sit in the queue.
func TestQueueScheduledTurn_TurnEndedMeanwhile_DrainsOrphan(t *testing.T) {
	p := &stubCronReplyTargetPlatform{stubPlatformEngine: stubPlatformEngine{n: "discord"}}
	as := newResultAgentSession("orphan done")
	e := NewEngine("test", &resultAgent{session: as}, []Platform{p}, "", LangEnglish)
	defer e.cancel()

	key := "discord:channel-1:user-1"
	session, _ := busySessionWithLiveState(t, e, p, key, as)
	session.Unlock() // the running turn has just ended

	msg := &Message{SessionKey: key, Platform: "discord", UserID: "timer", UserName: "timer", Content: "late job", ReplyCtx: "rctx"}
	if !e.queueScheduledTurn(p, msg, session, e.sessions, key, e.agent, "") {
		t.Fatal("queueScheduledTurn() = false, want queued")
	}
	waitFor(t, "orphaned queued job delivered", func() bool {
		for _, s := range p.getSent() {
			if s == "orphan done" {
				return true
			}
		}
		return false
	})
}
