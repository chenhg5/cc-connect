package core

import (
	"strings"
	"testing"
	"time"
)

type silentEmptyPlatform struct{ stubPlatformEngine }

func (p *silentEmptyPlatform) ReconstructReplyCtx(string) (any, error) { return "ctx", nil }

func TestExecuteHeartbeat_SilentEmptyResponse(t *testing.T) {
	for _, silent := range []bool{true, false} {
		p := &silentEmptyPlatform{stubPlatformEngine{n: "test"}}
		e := NewEngine("test", &resultAgent{session: newResultAgentSession("")}, []Platform{p}, "", LangEnglish)
		defer e.cancel()
		key := "test:heartbeat-empty"
		if err := e.ExecuteHeartbeat(key, "check", silent); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(2 * time.Second)
		for e.sessions.GetOrCreateActive(key).HistoryLen() < 2 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if e.sessions.GetOrCreateActive(key).HistoryLen() < 2 {
			t.Fatal("heartbeat result did not complete")
		}
		messages := p.getSent()
		if silent && len(messages) != 0 {
			t.Fatalf("silent heartbeat sent %v", messages)
		}
		if !silent && (len(messages) < 2 || !strings.Contains(strings.ToLower(strings.Join(messages, " ")), "empty response")) {
			t.Fatalf("visible heartbeat omitted empty response: %v", messages)
		}
	}
}

func TestExecuteCronJob_SilentEmptyResponse(t *testing.T) {
	for _, silent := range []bool{true, false} {
		store, err := NewCronStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		p := &stubCronReplyTargetPlatform{stubPlatformEngine: stubPlatformEngine{n: "discord"}}
		e := NewEngine("test", &resultAgent{session: newResultAgentSession("")}, []Platform{p}, "", LangEnglish)
		defer e.cancel()
		e.cronScheduler = NewCronScheduler(store)
		job := &CronJob{ID: "empty", SessionKey: "discord:channel:user", Prompt: "check", Silent: &silent}
		if err := store.Add(job); err != nil {
			t.Fatal(err)
		}
		if err := e.ExecuteCronJob(job); err != nil {
			t.Fatal(err)
		}
		messages := p.getSent()
		if silent && len(messages) != 0 {
			t.Fatalf("silent cron sent %v", messages)
		}
		if !silent && (len(messages) < 2 || !strings.Contains(strings.ToLower(strings.Join(messages, " ")), "empty response")) {
			t.Fatalf("visible cron omitted empty response: %v", messages)
		}
	}
}
