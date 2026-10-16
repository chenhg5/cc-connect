package core

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// capacityScriptSession answers each Send with the next scripted outcome:
// "cap" → the codex at-capacity turn error, anything else → a result.
type capacityScriptSession struct {
	mu     sync.Mutex
	events chan Event
	script []string
	sent   []string
}

func (s *capacityScriptSession) Send(prompt string, _ string, _ []ImageAttachment, _ []FileAttachment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, prompt)
	next := "ok"
	if len(s.script) > 0 {
		next, s.script = s.script[0], s.script[1:]
	}
	if next == "cap" {
		s.events <- Event{Type: EventError, Error: errors.New("Selected model is at capacity. Please try a different model.")}
	} else {
		s.events <- Event{Type: EventResult, Content: "reply:" + next, Done: true}
	}
	return nil
}
func (s *capacityScriptSession) sends() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sent)
}
func (s *capacityScriptSession) RespondPermission(_ string, _ PermissionResult) error { return nil }
func (s *capacityScriptSession) Events() <-chan Event                                 { return s.events }
func (s *capacityScriptSession) CurrentSessionID() string                             { return "cap-session" }
func (s *capacityScriptSession) Alive() bool                                          { return true }
func (s *capacityScriptSession) Close() error                                         { return nil }

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func joined(p *stubPlatformEngine) string { return strings.Join(p.getSent(), "\n") }

func TestCapacityRetry_RetriesWithinTurnThenSucceeds(t *testing.T) {
	sess := &capacityScriptSession{events: make(chan Event, 8), script: []string{"cap", "cap", "third"}}
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &resultAgent{session: sess}, []Platform{p}, "", LangEnglish)

	e.handleMessage(p, &Message{SessionKey: "test:c1", Platform: "test", UserID: "u", UserName: "u", Content: "hello", ReplyCtx: "ctx"})
	waitFor(t, "third attempt reply", func() bool { return strings.Contains(joined(p), "reply:third") })

	if n := sess.sends(); n != 3 {
		t.Fatalf("sends = %d, want 3", n)
	}
	if out := joined(p); strings.Contains(out, "at capacity. Please") || strings.Contains(out, "冷却") {
		t.Fatalf("capacity errors leaked to the user or cooled down needlessly:\n%s", out)
	}
	if e.capacityWait() != 0 {
		t.Fatal("cooldown started although the third attempt succeeded")
	}
}

func TestCapacityRetry_CooldownThenRedeliverAndDouble(t *testing.T) {
	old := capacityCooldownBase
	capacityCooldownBase = 150 * time.Millisecond
	t.Cleanup(func() { capacityCooldownBase = old })

	// 3 fails → cooldown; re-delivery 3 fails → doubled cooldown; then success.
	sess := &capacityScriptSession{events: make(chan Event, 8), script: []string{"cap", "cap", "cap", "cap", "cap", "cap", "finally"}}
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &resultAgent{session: sess}, []Platform{p}, "", LangEnglish)

	start := time.Now()
	e.handleMessage(p, &Message{SessionKey: "test:c1", Platform: "test", UserID: "u", UserName: "u", Content: "hello", ReplyCtx: "ctx"})
	waitFor(t, "first cooldown", func() bool { return e.capacityWait() > 0 })
	if n := sess.sends(); n != 3 {
		t.Fatalf("sends before first cooldown = %d, want 3", n)
	}
	waitFor(t, "re-delivered message to succeed", func() bool { return strings.Contains(joined(p), "reply:finally") })
	elapsed := time.Since(start)

	if n := sess.sends(); n != 7 {
		t.Fatalf("sends = %d, want 7 (3 + 3 after first cooldown + 1 after second)", n)
	}
	// Cooldowns were 150ms then 300ms.
	if elapsed < 450*time.Millisecond {
		t.Fatalf("finished after %v, want ≥ 450ms (cooldown must double)", elapsed)
	}
	for _, s := range sess.sent {
		if !strings.Contains(s, "hello") {
			t.Fatalf("a retry sent a different message: %q", s)
		}
	}
	if c := strings.Count(joined(p), "冷却"); c != 2 {
		t.Fatalf("cooldown notices = %d, want 2:\n%s", c, joined(p))
	}
	e.capMu.Lock()
	round := e.capRound
	e.capMu.Unlock()
	if round != 0 {
		t.Fatalf("capRound = %d after success, want reset to 0", round)
	}
}

func TestCapacityRetry_MessageDuringCooldownWaits(t *testing.T) {
	old := capacityCooldownBase
	capacityCooldownBase = 200 * time.Millisecond
	t.Cleanup(func() { capacityCooldownBase = old })

	sess := &capacityScriptSession{events: make(chan Event, 8), script: []string{"cap", "cap", "cap", "first", "second"}}
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &resultAgent{session: sess}, []Platform{p}, "", LangEnglish)

	e.handleMessage(p, &Message{SessionKey: "test:c1", Platform: "test", UserID: "u", UserName: "u", Content: "one", ReplyCtx: "ctx"})
	waitFor(t, "cooldown", func() bool { return e.capacityWait() > 0 })
	waitFor(t, "first turn to release the session", func() bool { return !e.sessions.GetOrCreateActive("test:c1").Busy() })

	e.handleMessage(p, &Message{SessionKey: "test:c1", Platform: "test", UserID: "u", UserName: "u", Content: "two", ReplyCtx: "ctx"})
	waitFor(t, "cooling notice", func() bool { return strings.Contains(joined(p), "冷却中") })
	if n := sess.sends(); n != 3 {
		t.Fatalf("a message sent during cooldown reached the model: sends = %d, want 3", n)
	}
	waitFor(t, "both messages answered", func() bool {
		out := joined(p)
		return strings.Contains(out, "reply:first") && strings.Contains(out, "reply:second")
	})
}
