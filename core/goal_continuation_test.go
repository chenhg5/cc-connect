package core

import (
	"testing"
	"time"
)

type goalTestSession struct {
	stubAgentSession
	enabled bool
	status  string
	known   bool
	limit   int
}

func (s *goalTestSession) GoalContinuationEnabled() bool  { return s.enabled }
func (s *goalTestSession) GoalStatus() (string, bool)     { return s.status, s.known }
func (s *goalTestSession) GoalContinuationPrompt() string { return "continue goal" }
func (s *goalTestSession) GoalContinuationLimit() int     { return s.limit }

func TestGoalContinuationSelection(t *testing.T) {
	for _, tc := range []struct {
		name, status            string
		enabled, known, stopped bool
		limit, count            int
		want                    bool
	}{
		{name: "active", status: "active", enabled: true, known: true, want: true},
		{name: "disabled", status: "active", known: true},
		{name: "unknown", status: "active", enabled: true},
		{name: "blocked", status: "blocked", enabled: true, known: true},
		{name: "stalled", status: "stalled", enabled: true, known: true},
		{name: "complete", status: "complete", enabled: true, known: true},
		{name: "paused", status: "paused", enabled: true, known: true},
		{name: "budget limited", status: "budgetLimited", enabled: true, known: true},
		{name: "stopped", status: "active", enabled: true, known: true, stopped: true},
		{name: "cap", status: "active", enabled: true, known: true, limit: 3, count: 3},
		{name: "unlimited", status: "active", enabled: true, known: true, count: 100, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &interactiveState{agentSession: &goalTestSession{enabled: tc.enabled, status: tc.status, known: tc.known, limit: tc.limit}, stopped: tc.stopped, goalContinuations: tc.count}
			s.mu.Lock()
			next, synthetic, ok := nextInteractiveTurnLocked(s)
			s.mu.Unlock()
			if ok != tc.want || synthetic != tc.want {
				t.Fatalf("next=%v synthetic=%v want=%v", ok, synthetic, tc.want)
			}
			if tc.want && (next.content != "continue goal" || next.messageID != "" || len(s.pendingMessages) != 0) {
				t.Fatal("internal turn leaked into queue or message ID")
			}
		})
	}
}
func TestGoalContinuationPrioritizesQueuedUser(t *testing.T) {
	s := &interactiveState{agentSession: &goalTestSession{enabled: true, status: "active", known: true}, pendingMessages: []queuedMessage{{content: "user", messageID: "id"}, {content: "second"}}}
	s.mu.Lock()
	next, synthetic, ok := nextInteractiveTurnLocked(s)
	s.mu.Unlock()
	if !ok || synthetic || next.content != "user" || len(s.pendingMessages) != 1 || s.goalContinuations != 0 {
		t.Fatal("queued user must win without synthetic continuation")
	}
}

type goalLoopSession struct {
	goalTestSession
	events chan Event
	sends  []string
}

func (s *goalLoopSession) Events() <-chan Event { return s.events }
func (s *goalLoopSession) Send(prompt, _ string, _ []ImageAttachment, _ []FileAttachment) error {
	s.sends = append(s.sends, prompt)
	s.events <- Event{Type: EventResult, Content: "goal step", Done: true}
	return nil
}
func TestGoalContinuationDoesNotAddUserHistory(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	as := &goalLoopSession{goalTestSession: goalTestSession{enabled: true, status: "active", known: true, limit: 1}, events: make(chan Event, 8)}
	e := NewEngine("test", &controllableAgent{nextSession: as}, []Platform{p}, "", LangEnglish)
	session := e.sessions.GetOrCreateActive("goal-test")
	session.AddHistory("user", "real user")
	state := &interactiveState{agentSession: as, platform: p, replyCtx: "ctx"}
	as.events <- Event{Type: EventResult, Content: "first step", Done: true}
	e.processInteractiveEvents(state, session, e.sessions, "goal-test", "user-id", time.Now(), nil, nil, "ctx", 0)
	if len(as.sends) != 1 {
		t.Fatalf("sends=%v", as.sends)
	}
	users := 0
	for _, h := range session.GetHistory(100) {
		if h.Role == "user" {
			users++
			if h.Content != "real user" {
				t.Fatal("synthetic prompt leaked into history")
			}
		}
	}
	if users != 1 {
		t.Fatalf("user history entries=%d", users)
	}
	if len(p.getSent()) < 2 {
		t.Fatal("both assistant turns should be visible")
	}
}
