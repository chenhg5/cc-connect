package core

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type retryCronSession struct {
	*resultAgentSession
	closeOnSend bool
	alive       atomic.Bool
}

func (s *retryCronSession) Send(_ string, _ string, _ []ImageAttachment, _ []FileAttachment) error {
	if s.closeOnSend {
		s.alive.Store(false)
		close(s.events)
		return nil
	}
	s.events <- Event{Type: EventResult, Content: "recovered", Done: true}
	return nil
}

func (s *retryCronSession) Alive() bool { return s.alive.Load() }

type retryCronAgent struct {
	resultAgent
	attempts    atomic.Int32
	alwaysEmpty bool
}

func (a *retryCronAgent) StartSession(_ context.Context, _ string) (AgentSession, error) {
	first := a.attempts.Add(1) == 1
	session := &retryCronSession{resultAgentSession: newResultAgentSession("recovered"), closeOnSend: first || a.alwaysEmpty}
	session.alive.Store(true)
	return session, nil
}

func TestCronSchedulerRetriesEmptyResponseOnce(t *testing.T) {
	store, err := NewCronStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	platform := &stubCronReplyTargetPlatform{stubPlatformEngine: stubPlatformEngine{n: "discord"}}
	agent := &retryCronAgent{}
	engine := NewEngine("test", agent, []Platform{platform}, "", LangEnglish)
	defer engine.cancel()
	scheduler := NewCronScheduler(store)
	scheduler.emptyRetryDelay = time.Millisecond
	engine.cronScheduler = scheduler
	scheduler.RegisterEngine("test", engine)
	job := &CronJob{
		ID:         "transient-empty",
		Project:    "test",
		SessionKey: "discord:channel:user",
		CronExpr:   "* * * * *",
		Prompt:     "do work",
		Enabled:    true,
		CreatedAt:  time.Now(),
	}
	if err := store.Add(job); err != nil {
		t.Fatal(err)
	}

	scheduler.runJob(job, false)
	if got := agent.attempts.Load(); got != 2 {
		t.Fatalf("agent attempts = %d, want one retry after empty response", got)
	}
	stored := store.Get(job.ID)
	if stored == nil || stored.LastError != "" {
		t.Fatalf("recovered cron job recorded a failure: %+v", stored)
	}
	starts := 0
	for _, message := range platform.getSent() {
		if message == "⏰ do work" {
			starts++
		}
	}
	if starts != 1 {
		t.Fatalf("start notifications = %d, want one across both attempts", starts)
	}
}

func TestCronSchedulerStopsAfterOneEmptyRetry(t *testing.T) {
	store, err := NewCronStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	platform := &stubCronReplyTargetPlatform{stubPlatformEngine: stubPlatformEngine{n: "discord"}}
	agent := &retryCronAgent{alwaysEmpty: true}
	engine := NewEngine("test", agent, []Platform{platform}, "", LangEnglish)
	defer engine.cancel()
	scheduler := NewCronScheduler(store)
	scheduler.emptyRetryDelay = time.Millisecond
	engine.cronScheduler = scheduler
	scheduler.RegisterEngine("test", engine)
	job := &CronJob{ID: "persistent-empty", Project: "test", SessionKey: "discord:channel:user", CronExpr: "* * * * *", Prompt: "do work", Enabled: true, CreatedAt: time.Now()}
	if err := store.Add(job); err != nil {
		t.Fatal(err)
	}

	scheduler.runJob(job, false)
	if got := agent.attempts.Load(); got != 2 {
		t.Fatalf("agent attempts = %d, want exactly two", got)
	}
	stored := store.Get(job.ID)
	if stored == nil || !strings.Contains(stored.LastError, "produced an empty response") {
		t.Fatalf("final empty response was not recorded: %+v", stored)
	}
}

func TestCronSchedulerDoesNotRetryOtherFailures(t *testing.T) {
	store, err := NewCronStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	platform := &stubCronReplyTargetPlatform{stubPlatformEngine: stubPlatformEngine{n: "discord"}}
	agent := &retryCronAgent{}
	engine := NewEngine("test", agent, []Platform{platform}, "", LangEnglish)
	defer engine.cancel()
	scheduler := NewCronScheduler(store)
	scheduler.emptyRetryDelay = time.Millisecond
	scheduler.RegisterEngine("test", engine)
	job := &CronJob{ID: "invalid-target", Project: "test", SessionKey: "unknown:channel:user", CronExpr: "* * * * *", Prompt: "do work", Enabled: true, CreatedAt: time.Now()}
	if err := store.Add(job); err != nil {
		t.Fatal(err)
	}

	scheduler.runJob(job, false)
	if got := agent.attempts.Load(); got != 0 {
		t.Fatalf("non-empty failure started %d agent sessions", got)
	}
	stored := store.Get(job.ID)
	if stored == nil || !strings.Contains(stored.LastError, "platform") {
		t.Fatalf("platform failure was not recorded: %+v", stored)
	}
}
