package core

import (
	"strings"
	"testing"
	"time"
)

// hangingAgentSession accepts the prompt and never finishes the turn, like
// a long-running cron job.
type hangingAgentSession struct {
	*resultAgentSession
	sent chan struct{}
}

func (s *hangingAgentSession) Send(string, string, []ImageAttachment, []FileAttachment) error {
	select {
	case s.sent <- struct{}{}:
	default:
	}
	return nil
}

// Stop must not wait for a running job to finish (#2313): shutdown would
// otherwise block for the rest of the job and hit the service stop timeout.
func TestCronScheduler_StopDoesNotWaitForRunningJob(t *testing.T) {
	store, err := NewCronStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	platform := &stubCronReplyTargetPlatform{stubPlatformEngine: stubPlatformEngine{n: "discord"}}
	session := &hangingAgentSession{resultAgentSession: newResultAgentSession(""), sent: make(chan struct{}, 1)}
	e := NewEngine("test", &resultAgent{session: session}, []Platform{platform}, "", LangEnglish)
	defer e.cancel()

	cs := NewCronScheduler(store)
	e.cronScheduler = cs
	cs.RegisterEngine("test", e)
	job := &CronJob{ID: "long", Project: "test", SessionKey: "discord:channel-1:user-1",
		CronExpr: "* * * * *", Prompt: "go", Enabled: true, CreatedAt: time.Now()}
	if err := store.Add(job); err != nil {
		t.Fatal(err)
	}
	if err := cs.Start(); err != nil {
		t.Fatal(err)
	}
	cs.mu.Lock()
	cs.entries[job.ID].nextRun = time.Now().Add(-time.Minute)
	cs.mu.Unlock()
	cs.signalWakeUp()

	select {
	case <-session.sent:
	case <-time.After(3 * time.Second):
		t.Fatal("job did not start")
	}

	stopped := make(chan struct{})
	go func() {
		cs.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop blocked on the running job")
	}

	got := store.Get(job.ID)
	if got == nil || !strings.Contains(got.LastError, "shutdown") {
		t.Fatalf("LastError = %q, want the run marked as interrupted by shutdown", got.LastError)
	}
}
