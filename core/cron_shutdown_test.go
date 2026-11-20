package core

import (
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

type shutdownCronSession struct {
	stubAgentSession
	sent   chan struct{}
	events chan Event
	result string
	calls  atomic.Int32
}

func (session *shutdownCronSession) Send(string, string, []ImageAttachment, []FileAttachment) error {
	if session.calls.Add(1) == 1 {
		if session.sent != nil {
			close(session.sent)
		}
		if session.result != "" {
			session.events <- Event{Type: EventResult, Content: session.result, Done: true}
		}
	}
	return nil
}

func (session *shutdownCronSession) Events() <-chan Event { return session.events }

func (session *shutdownCronSession) Close() error {
	close(session.events)
	return nil
}

func TestCronScheduler_StopInterruptsRunningJob(t *testing.T) {
	for _, testcase := range []struct {
		name    string
		manual  bool
		timeout int
	}{
		{name: "scheduled with timeout", timeout: 4},
		{name: "manual without timeout", manual: true},
	} {
		t.Run(testcase.name, func(t *testing.T) {
			dir := t.TempDir()
			synctest.Test(t, func(test *testing.T) {
				store, err := NewCronStore(dir)
				if err != nil {
					test.Fatal(err)
				}
				job := &CronJob{
					ID: "shutdown", Project: "test", SessionKey: "test:shutdown",
					CronExpr: "@every 3s", Prompt: "wait", Enabled: true, Mute: true,
					TimeoutMins: &testcase.timeout, SessionMode: "new_per_run",
				}
				if err := store.Add(job); err != nil {
					test.Fatal(err)
				}
				platform := &stubCronReplyTargetPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
				blocked := &shutdownCronSession{sent: make(chan struct{}), events: make(chan Event)}
				engine := NewEngine("test", &resultAgent{session: blocked}, []Platform{platform}, "", LangEnglish)
				stopped := false
				defer func() {
					if !stopped {
						_ = engine.Stop()
					}
				}()
				scheduler := NewCronScheduler(store)
				scheduler.RegisterEngine("test", engine)
				if testcase.manual {
					if err := scheduler.RunJobNow(job.ID); err != nil {
						test.Fatal(err)
					}
				} else if err := scheduler.Start(); err != nil {
					test.Fatal(err)
				}
				select {
				case <-blocked.sent:
				case <-time.After(4 * time.Second):
					test.Fatal("cron did not send the prompt")
				}
				stopDone := make(chan struct{})
				go func() {
					scheduler.Stop()
					close(stopDone)
				}()
				select {
				case <-stopDone:
				case <-time.After(5*time.Second + 100*time.Millisecond):
					test.Fatal("scheduler waited more than five seconds for a blocked cron")
				}
				if err := engine.Stop(); err != nil {
					test.Fatal(err)
				}
				stopped = true
				synctest.Wait()
				if sessions := engine.GetSessions().ListSessions(job.SessionKey); len(sessions) != 1 || sessions[0].Busy() {
					test.Fatalf("late worker did not release its only session: %v", sessions)
				}

				reloaded, err := NewCronStore(dir)
				if err != nil {
					test.Fatal(err)
				}
				interrupted := reloaded.Get(job.ID)
				if interrupted == nil || !interrupted.Enabled || interrupted.LastRun.IsZero() || interrupted.LastError != "interrupted by shutdown" {
					test.Fatalf("interrupted cron did not persist the expected state: %+v", interrupted)
				}
				interruptedAt := interrupted.LastRun
				result := &shutdownCronSession{result: "next tick", events: make(chan Event, 1)}
				nextEngine := NewEngine("test", &resultAgent{session: result}, []Platform{platform}, "", LangEnglish)
				defer func() {
					if err := nextEngine.Stop(); err != nil {
						test.Error(err)
					}
				}()
				nextScheduler := NewCronScheduler(reloaded)
				nextScheduler.RegisterEngine("test", nextEngine)
				if err := nextScheduler.Start(); err != nil {
					test.Fatal(err)
				}
				defer nextScheduler.Stop()
				time.Sleep(time.Second)
				synctest.Wait()
				if calls := result.calls.Load(); calls != 0 {
					test.Fatalf("interrupted run repeated on restart: %d calls", calls)
				}
				time.Sleep(3 * time.Second)
				synctest.Wait()
				if calls := result.calls.Load(); calls != 1 {
					test.Fatalf("expected exactly one next-cycle run, got: %d calls", calls)
				}
				if latest := reloaded.snapshotJob(job.ID); latest.LastError != "" || !latest.LastRun.After(interruptedAt) {
					test.Fatalf("next cycle did not record success: %+v", latest)
				}
			})
		})
	}
}

func TestCronScheduler_StopBoundsBookkeepingWait(t *testing.T) {
	synctest.Test(t, func(test *testing.T) {
		scheduler := NewCronScheduler(nil)
		run := scheduler.run
		run.waiters.Add(1)
		defer run.waiters.Done()
		started := time.Now()
		scheduler.Stop()
		if elapsed := time.Since(started); elapsed != 5*time.Second {
			test.Fatalf("stop took %s with bookkeeping stuck, want 5s", elapsed)
		}
	})
}
