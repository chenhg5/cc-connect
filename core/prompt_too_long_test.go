package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// The provider's error as cc-connect logged it for jingyu on 2026-10-19.
const tooLongErrText = `{"error":{"message":"{\"code\":11115,\"msg\":\"prompt is too long: 1227574 tokens > 1048576 maximum\",\"requestId\":\"x\"}"}}`

// tooLongAgent starts a new session (ID s1, s2, …) on every StartSession and
// answers the sends of all its sessions from one script: "long" → the
// too-long turn error, "hold:<x>" → wait for release, anything else → a result.
type tooLongAgent struct {
	mu       sync.Mutex
	script   []string
	sent     []string // prompts, in order, across sessions
	sentBy   []string // session ID per send
	starts   []string // resume ID per StartSession
	release  chan struct{}
	handoffs map[string]*SessionHandoff
}

func (a *tooLongAgent) Name() string { return "stub" }
func (a *tooLongAgent) StartSession(_ context.Context, resume string) (AgentSession, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.starts = append(a.starts, resume)
	return &tooLongSession{a: a, id: fmt.Sprintf("s%d", len(a.starts)), events: make(chan Event, 8), alive: true}, nil
}
func (a *tooLongAgent) ListSessions(_ context.Context) ([]AgentSessionInfo, error) { return nil, nil }
func (a *tooLongAgent) Stop() error                                                { return nil }
func (a *tooLongAgent) ReadSessionHandoff(id string) (*SessionHandoff, error) {
	if h, ok := a.handoffs[id]; ok {
		return h, nil
	}
	return nil, errNotFound
}
func (a *tooLongAgent) snapshot() (sent, by, starts []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.sent...), append([]string(nil), a.sentBy...), append([]string(nil), a.starts...)
}

type tooLongSession struct {
	a      *tooLongAgent
	id     string
	events chan Event
	mu     sync.Mutex
	alive  bool
}

func (s *tooLongSession) Send(prompt string, _ string, _ []ImageAttachment, _ []FileAttachment) error {
	a := s.a
	a.mu.Lock()
	a.sent = append(a.sent, prompt)
	a.sentBy = append(a.sentBy, s.id)
	next := "ok"
	if len(a.script) > 0 {
		next, a.script = a.script[0], a.script[1:]
	}
	a.mu.Unlock()
	emit := func(next string) {
		if next == "long" {
			s.events <- Event{Type: EventError, Error: errors.New(tooLongErrText)}
		} else {
			s.events <- Event{Type: EventResult, Content: "reply:" + next, Done: true}
		}
	}
	if rest, ok := strings.CutPrefix(next, "hold:"); ok {
		go func() { <-a.release; emit(rest) }()
		return nil
	}
	emit(next)
	return nil
}
func (s *tooLongSession) RespondPermission(_ string, _ PermissionResult) error { return nil }
func (s *tooLongSession) Events() <-chan Event                                 { return s.events }
func (s *tooLongSession) CurrentSessionID() string                             { return s.id }
func (s *tooLongSession) Alive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.alive
}
func (s *tooLongSession) Close() error {
	s.mu.Lock()
	s.alive = false
	s.mu.Unlock()
	return nil
}

func newTooLongEngine(t *testing.T, p Platform, script ...string) (*Engine, *tooLongAgent) {
	a := &tooLongAgent{script: script, release: make(chan struct{}), handoffs: map[string]*SessionHandoff{
		"s1": {Path: "/x/s1.jsonl", Tail: []HistoryEntry{{Role: "user", Content: "旧会话里的话"}}},
	}}
	e := NewEngine("test", a, []Platform{p}, "", LangEnglish)
	t.Cleanup(func() { e.Stop() })
	return e, a
}

func assertNoRawTooLong(t *testing.T, out string) {
	t.Helper()
	if strings.Contains(out, "11115") || strings.Contains(out, "1048576") {
		t.Fatalf("raw provider error reached the channel:\n%s", out)
	}
}

func TestPromptTooLong_FreshSessionRerunsOnce(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e, a := newTooLongEngine(t, p, "long", "second")

	e.handleMessage(p, &Message{SessionKey: "test:c1", Platform: "test", UserID: "u", UserName: "u", Content: "hello", ReplyCtx: "ctx"})
	waitFor(t, "re-run reply", func() bool { return strings.Contains(joined(p), "reply:second") })

	sent, by, starts := a.snapshot()
	if len(sent) != 2 || by[0] != "s1" || by[1] != "s2" {
		t.Fatalf("sends = %q on %q, want hello on s1 then once more on s2", sent, by)
	}
	if !strings.HasSuffix(sent[1], "hello") || !strings.Contains(sent[1], handoffHead+" s1") || !strings.Contains(sent[1], "旧会话里的话") {
		t.Fatalf("re-run prompt lacks the message or the handoff:\n%s", sent[1])
	}
	if len(starts) != 2 || starts[1] != "" {
		t.Fatalf("StartSession resume IDs = %q, want the second to start fresh", starts)
	}
	out := joined(p)
	assertNoRawTooLong(t, out)
	if strings.Count(out, "♻️") != 1 {
		t.Fatalf("want one ♻️ notice:\n%s", out)
	}
	s := e.sessions.GetOrCreateActive("test:c1")
	waitFor(t, "session released", func() bool { return !s.Busy() })
	if id := s.GetAgentSessionID(); id != "s2" {
		t.Fatalf("agent session = %q, want s2", id)
	}
	if n := s.HistoryLen(); n != 2 {
		t.Fatalf("history entries = %d, want 2 (one user, one assistant)", n)
	}
}

func TestPromptTooLong_RerunFailsAgainNoLoop(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e, a := newTooLongEngine(t, p, "long", "long", "never")

	e.handleMessage(p, &Message{SessionKey: "test:c1", Platform: "test", UserID: "u", UserName: "u", Content: "hello", ReplyCtx: "ctx"})
	waitFor(t, "error after re-run", func() bool { return strings.Contains(joined(p), "11115") })
	waitFor(t, "session released", func() bool { return !e.sessions.GetOrCreateActive("test:c1").Busy() })

	if sent, _, starts := a.snapshot(); len(sent) != 2 || len(starts) != 2 {
		t.Fatalf("sends = %d, starts = %d; want 2 and 2 (one re-run only)", len(sent), len(starts))
	}
	if out := joined(p); strings.Count(out, "♻️") != 1 || strings.Count(out, "11115") != 1 {
		t.Fatalf("want one ♻️ notice and one error:\n%s", out)
	}
}

// A queued message hits the limit: it is the one re-run, and the message queued
// behind it moves to the new session.
func TestPromptTooLong_QueuedMessageRerun(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e, a := newTooLongEngine(t, p, "hold:a", "long", "b", "c")

	e.handleMessage(p, &Message{SessionKey: "test:c1", Platform: "test", UserID: "u", UserName: "u", Content: "msgA", ReplyCtx: "ctx"})
	waitFor(t, "A sent", func() bool { sent, _, _ := a.snapshot(); return len(sent) == 1 })
	e.handleMessage(p, &Message{SessionKey: "test:c1", Platform: "test", UserID: "u", UserName: "u", Content: "msgB", ReplyCtx: "ctx"})
	e.handleMessage(p, &Message{SessionKey: "test:c1", Platform: "test", UserID: "u", UserName: "u", Content: "msgC", ReplyCtx: "ctx"})
	close(a.release)
	waitFor(t, "C answered", func() bool { return strings.Contains(joined(p), "reply:c") })

	sent, by, _ := a.snapshot()
	if len(sent) != 4 || !strings.HasSuffix(sent[2], "msgB") || by[2] != "s2" || !strings.HasSuffix(sent[3], "msgC") || by[3] != "s2" {
		t.Fatalf("sends = %q on %q, want A, B on s1, then B and C on s2", sent, by)
	}
	assertNoRawTooLong(t, joined(p))
}

// The cron exec path (the 2026-10-19 "produced an empty response" spam) re-runs too.
func TestPromptTooLong_CronJobRerun(t *testing.T) {
	p := &cronHistoryPlatform{historyPlatform{stubPlatformEngine: stubPlatformEngine{n: "discord"}}}
	e, a := newTooLongEngine(t, p, "ok", "long", "cron")
	// A first turn binds the session to s1.
	e.handleMessage(p, &Message{SessionKey: "discord:c1", Platform: "discord", UserID: "u", UserName: "u", Content: "hi", ReplyCtx: "ctx"})
	waitFor(t, "first reply", func() bool { return strings.Contains(strings.Join(p.getSent(), "\n"), "reply:ok") })
	waitFor(t, "session released", func() bool { return !e.sessions.GetOrCreateActive("discord:c1").Busy() })

	if err := e.ExecuteCronJob(&CronJob{ID: "92af21b3", SessionKey: "discord:c1", Prompt: "巡检"}); err != nil {
		t.Fatalf("ExecuteCronJob: %v", err)
	}
	sent, by, _ := a.snapshot()
	if len(sent) != 3 || !strings.HasSuffix(sent[2], "巡检") || by[2] != "s2" {
		t.Fatalf("sends = %q on %q, want the job once more on s2", sent, by)
	}
	out := strings.Join(p.getSent(), "\n")
	assertNoRawTooLong(t, out)
	if !strings.Contains(out, "reply:cron") || strings.Count(out, "♻️") != 1 {
		t.Fatalf("channel:\n%s", out)
	}
}

// A message queued into the old state while the session is being replaced is
// carried to the new session, not lost with the old state.
func TestPromptTooLong_MessageQueuedDuringRecycleKept(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e, a := newTooLongEngine(t, p, "long", "again", "late")
	testHookBeforePromptTooLongRecycle = func() {
		e.handleMessage(p, &Message{SessionKey: "test:c1", Platform: "test", UserID: "u", UserName: "u", Content: "msgLate", ReplyCtx: "ctx"})
	}
	t.Cleanup(func() { testHookBeforePromptTooLongRecycle = nil })

	e.handleMessage(p, &Message{SessionKey: "test:c1", Platform: "test", UserID: "u", UserName: "u", Content: "hello", ReplyCtx: "ctx"})
	waitFor(t, "late message answered", func() bool { return strings.Contains(joined(p), "reply:late") })

	sent, by, _ := a.snapshot()
	if len(sent) != 3 || !strings.HasSuffix(sent[1], "hello") || !strings.HasSuffix(sent[2], "msgLate") || by[2] != "s2" {
		t.Fatalf("sends = %q on %q, want hello on s1, then hello and msgLate on s2", sent, by)
	}
}
