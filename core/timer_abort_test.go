package core

import (
	"testing"
	"time"
)

// A timer run whose agent never reports a result must not leave the agent
// process behind once the scheduler gives up on it (e.g. after an upstream
// 502 the turn never completed and the CLI lingered after "job timed out").
func TestAbortTimerRun_ClosesHungSession(t *testing.T) {
	store, err := NewTimerStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	platform := &stubCronReplyTargetPlatform{stubPlatformEngine: stubPlatformEngine{n: "discord"}}
	agentSession := newControllableSession("hung")
	e := NewEngine("test", &resultAgent{session: agentSession}, []Platform{platform}, "", LangEnglish)
	defer e.cancel()
	e.timerScheduler = NewTimerScheduler(store)

	job := &TimerJob{
		ID:          "hung1",
		SessionKey:  "discord:channel-1:user-1",
		Prompt:      "never finishes",
		Mute:        true,
		SessionMode: "new_per_run",
	}

	done := make(chan error, 1)
	go func() { done <- e.ExecuteTimerJob(job) }()

	deadline := time.Now().Add(2 * time.Second)
	for !e.AbortTimerRun(job.ID) {
		if time.Now().After(deadline) {
			t.Fatal("timer run never became abortable")
		}
		time.Sleep(10 * time.Millisecond)
	}

	select {
	case <-agentSession.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("agent session was not closed after abort")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ExecuteTimerJob did not return after abort")
	}
}

func TestAbortTimerRun_UnknownRun(t *testing.T) {
	e := NewEngine("test", &resultAgent{session: newControllableSession("x")}, nil, "", LangEnglish)
	defer e.cancel()
	if e.AbortTimerRun("missing") {
		t.Fatal("AbortTimerRun() = true for a run that was never started")
	}
}
