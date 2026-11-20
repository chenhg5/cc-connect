package core

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
)

func TestEngine_BeginShutdownRejectsInboundWork(t *testing.T) {
	platform := &stubPlatformEngine{n: "test"}
	agent := &stubStartSessionAgent{}
	engine := NewEngine("test", agent, []Platform{platform}, "", LangEnglish)
	t.Cleanup(func() {
		if err := engine.Stop(); err != nil {
			t.Error(err)
		}
	})
	engine.BeginShutdown()
	if err := engine.ctx.Err(); err != nil {
		t.Fatalf("ingress gate canceled the engine before cron could drain: %v", err)
	}

	for _, content := range []string{"hello", "/new"} {
		engine.ReceiveMessage(platform, &Message{
			SessionKey: "test:shutdown:user", Platform: platform.Name(),
			UserID: "user", UserName: "user", Content: content,
		})
	}
	if card := engine.handleCardNav("nav:/help agent", "test:shutdown:user"); card != nil {
		t.Fatal("closed ingress handled card navigation")
	}
	if _, err := engine.HandleRelay(t.Context(), "source", "test:shutdown:user", "hello"); !errors.Is(err, context.Canceled) {
		t.Fatalf("closed ingress accepted relay: %v", err)
	}
	if sessions := engine.GetSessions().ListSessions("test:shutdown:user"); len(sessions) != 0 {
		t.Fatalf("closed ingress created %d sessions", len(sessions))
	}
	if len(agent.calls) != 0 || len(platform.getSent()) != 0 {
		t.Fatal("closed ingress started an agent or sent a reply")
	}
}

type shutdownCronPlatform struct {
	stubPlatformEngine
	entered chan struct{}
	allow   chan struct{}
}

func (platform *shutdownCronPlatform) ReconstructReplyCtx(string) (any, error) {
	close(platform.entered)
	<-platform.allow
	return "reply", nil
}

func TestEngine_LateCronWorkerCannotCreateSessionAfterStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		platform := &shutdownCronPlatform{
			stubPlatformEngine: stubPlatformEngine{n: "test"},
			entered:            make(chan struct{}), allow: make(chan struct{}),
		}
		agent := &stubStartSessionAgent{}
		engine := NewEngine("test", agent, []Platform{platform}, "", LangEnglish)
		job := &CronJob{ID: "late", SessionKey: "test:shutdown", Prompt: "hello", SessionMode: "new_per_run"}
		result := make(chan error, 1)
		go func() { result <- engine.ExecuteCronJob(job) }()
		<-platform.entered
		stopErr := engine.Stop()
		close(platform.allow)
		if err := <-result; !errors.Is(err, errCronShutdown) || stopErr != nil {
			t.Fatalf("unexpected errors after engine stop: cron=%v stop=%v", err, stopErr)
		}
		if sessions := engine.GetSessions().ListSessions(job.SessionKey); len(sessions) != 0 {
			t.Fatalf("late cron created %d side sessions", len(sessions))
		}
		if len(agent.calls) != 0 || len(platform.getSent()) != 0 {
			t.Fatal("late cron started an agent or sent a reply")
		}
	})
}
