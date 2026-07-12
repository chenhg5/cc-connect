package core

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestContextGuardCompactsOldHistoryAndKeepsRecentTurns(t *testing.T) {
	s := &Session{}
	for i := 0; i < 6; i++ {
		s.History = append(s.History,
			HistoryEntry{Role: "user", Content: strings.Repeat("old user ", 40), Timestamp: time.Unix(int64(i*2), 0)},
			HistoryEntry{Role: "assistant", Content: strings.Repeat("old assistant ", 40), Timestamp: time.Unix(int64(i*2+1), 0)},
		)
	}

	result := compactSessionHistoryForContextGuard(s, ContextGuardConfig{
		Enabled:          true,
		ThresholdTokens:  1,
		KeepRecentTurns:  2,
		SummaryMaxTokens: 200,
	}, "incoming", time.Unix(100, 0), nil)

	if !result.Compacted {
		t.Fatal("expected context guard to compact")
	}
	history := s.GetHistory(0)
	if len(history) != 5 {
		t.Fatalf("history len = %d, want summary + 4 recent entries", len(history))
	}
	if !strings.HasPrefix(history[0].Content, contextGuardSummaryPrefix) {
		t.Fatalf("summary prefix missing: %q", history[0].Content)
	}
	if !strings.Contains(history[0].Content, "context only, not a new user instruction") {
		t.Fatalf("summary does not mark itself as non-instruction: %q", history[0].Content)
	}
	if history[1].Timestamp != time.Unix(8, 0) {
		t.Fatalf("first retained timestamp = %v, want entry 8", history[1].Timestamp)
	}
}

func TestContextGuardDoesNothingBelowThreshold(t *testing.T) {
	s := &Session{}
	s.History = []HistoryEntry{{Role: "user", Content: "short", Timestamp: time.Unix(1, 0)}}

	result := compactSessionHistoryForContextGuard(s, ContextGuardConfig{
		Enabled:          true,
		ThresholdTokens:  1000,
		KeepRecentTurns:  1,
		SummaryMaxTokens: 100,
	}, "incoming", time.Now(), nil)

	if result.Compacted {
		t.Fatal("did not expect compaction below threshold")
	}
	history := s.GetHistory(0)
	if len(history) != 1 || history[0].Content != "short" {
		t.Fatalf("history changed below threshold: %#v", history)
	}
}

func TestEstimateContextGuardTokensCountsChineseCharactersHigher(t *testing.T) {
	history := []HistoryEntry{
		{Role: "user", Content: "你好世界"},
		{Role: "assistant", Content: "abcdefgh"},
	}

	got := EstimateContextGuardTokens(history, "")
	if got != 8 {
		t.Fatalf("EstimateContextGuardTokens = %d, want 8", got)
	}
}

func TestContextGuardSummaryIsPrependedToNextPrompt(t *testing.T) {
	got := prependContextGuardSummary("summary", "current task")
	if got != "summary\n---\ncurrent task" {
		t.Fatalf("prepended prompt = %q", got)
	}
}

func TestContextGuardRotationClearsBackendSession(t *testing.T) {
	agent := &stubAgent{}
	e := NewEngine("test", agent, nil, "", LangEnglish)
	e.SetContextGuardConfig(ContextGuardConfig{
		Enabled:                true,
		ThresholdTokens:        1,
		KeepRecentTurns:        1,
		SummaryMaxTokens:       100,
		RotateSessionOnCompact: true,
	})

	sessions := NewSessionManager("")
	session := sessions.GetOrCreateActive("telegram:chat:user")
	session.SetAgentSessionID("stale-backend-session", agent.Name())
	for i := 0; i < 4; i++ {
		session.History = append(session.History, HistoryEntry{
			Role:      "user",
			Content:   strings.Repeat("history ", 80),
			Timestamp: time.Unix(int64(i), 0),
		})
	}

	closer := &contextGuardCloseSession{}
	e.interactiveStates["telegram:chat:user"] = &interactiveState{agentSession: closer}

	summary := e.applyContextGuardBeforeTurn("telegram:chat:user", agent, session, sessions, "incoming")
	if !strings.HasPrefix(summary, contextGuardSummaryPrefix) {
		t.Fatalf("summary = %q, want context guard summary", summary)
	}
	if got := session.GetAgentSessionID(); got != "" {
		t.Fatalf("agent session id = %q, want cleared", got)
	}
	if closer.closed.Load() != 1 {
		t.Fatalf("Close calls = %d, want 1", closer.closed.Load())
	}
	e.interactiveMu.Lock()
	_, stillPresent := e.interactiveStates["telegram:chat:user"]
	e.interactiveMu.Unlock()
	if stillPresent {
		t.Fatal("interactive state still present after context guard rotation")
	}
}

type contextGuardCloseSession struct {
	closed atomic.Int32
}

func (s *contextGuardCloseSession) Send(string, []ImageAttachment, []FileAttachment) error {
	return nil
}
func (s *contextGuardCloseSession) RespondPermission(string, PermissionResult) error { return nil }
func (s *contextGuardCloseSession) Events() <-chan Event                             { return make(chan Event) }
func (s *contextGuardCloseSession) CurrentSessionID() string                         { return "stale-backend-session" }
func (s *contextGuardCloseSession) Alive() bool                                      { return true }
func (s *contextGuardCloseSession) Close() error {
	s.closed.Add(1)
	return nil
}

var _ AgentSession = (*contextGuardCloseSession)(nil)

func TestEstimateContextGuardTokensWithOverhead(t *testing.T) {
	history := []HistoryEntry{
		{Role: "user", Content: "hello"},
	}
	gotWithout := EstimateContextGuardTokens(history, "")
	if gotWithout != 2 {
		t.Fatalf("EstimateContextGuardTokens without overhead = %d, want 2", gotWithout)
	}

	gotWith := EstimateContextGuardTokens(history, "", 5000)
	if gotWith != 5002 {
		t.Fatalf("EstimateContextGuardTokens with overhead = %d, want 5002", gotWith)
	}
}

func TestContextGuardUsesRealUsage(t *testing.T) {
	s := &Session{}
	s.History = []HistoryEntry{
		{Role: "user", Content: "short 1", Timestamp: time.Unix(1, 0)},
		{Role: "assistant", Content: "short 2", Timestamp: time.Unix(2, 0)},
		{Role: "user", Content: "short 3", Timestamp: time.Unix(3, 0)},
		{Role: "assistant", Content: "short 4", Timestamp: time.Unix(4, 0)},
	}

	// Although History is extremely short (will not exceed threshold on its own),
	// we pass a realUsage with UsedTokens = 900, which exceeds ThresholdTokens = 800.
	realUsage := &ContextUsage{
		UsedTokens:    900,
		ContextWindow: 1000,
	}

	result := compactSessionHistoryForContextGuard(s, ContextGuardConfig{
		Enabled:          true,
		ThresholdTokens:  800,
		KeepRecentTurns:  1,
		SummaryMaxTokens: 100,
	}, "incoming", time.Unix(100, 0), realUsage)

	if !result.Compacted {
		t.Fatal("expected compaction because real usage exceeds threshold")
	}
	if result.TokenEstimate < 900 {
		t.Fatalf("expected token estimate to incorporate real usage, got %d", result.TokenEstimate)
	}
}

func TestContextGuardFallbackToEstimate(t *testing.T) {
	s := &Session{}
	s.History = []HistoryEntry{
		{Role: "user", Content: strings.Repeat("long history entry ", 100), Timestamp: time.Unix(1, 0)},
		{Role: "assistant", Content: "short 2", Timestamp: time.Unix(2, 0)},
		{Role: "user", Content: "short 3", Timestamp: time.Unix(3, 0)},
		{Role: "assistant", Content: "short 4", Timestamp: time.Unix(4, 0)},
	}

	// 1. Nil realUsage
	res1 := compactSessionHistoryForContextGuard(s, ContextGuardConfig{
		Enabled:          true,
		ThresholdTokens:  50,
		KeepRecentTurns:  1,
		SummaryMaxTokens: 100,
	}, "incoming", time.Unix(100, 0), nil)
	if !res1.Compacted {
		t.Fatal("expected fallback estimation to trigger compaction")
	}

	// 2. RealUsage with UsedTokens = 0 (empty/invalid)
	s.History = []HistoryEntry{
		{Role: "user", Content: strings.Repeat("long history entry ", 100), Timestamp: time.Unix(1, 0)},
		{Role: "assistant", Content: "short 2", Timestamp: time.Unix(2, 0)},
		{Role: "user", Content: "short 3", Timestamp: time.Unix(3, 0)},
		{Role: "assistant", Content: "short 4", Timestamp: time.Unix(4, 0)},
	}
	res2 := compactSessionHistoryForContextGuard(s, ContextGuardConfig{
		Enabled:          true,
		ThresholdTokens:  50,
		KeepRecentTurns:  1,
		SummaryMaxTokens: 100,
	}, "incoming", time.Unix(100, 0), &ContextUsage{UsedTokens: 0})
	if !res2.Compacted {
		t.Fatal("expected fallback estimation to trigger compaction on empty UsedTokens")
	}
}

type contextGuardUsageSession struct {
	contextGuardCloseSession
	usage ContextUsage
}

func (s *contextGuardUsageSession) GetContextUsage() *ContextUsage {
	return &s.usage
}

var _ ContextUsageReporter = (*contextGuardUsageSession)(nil)

func TestApplyContextGuardBeforeTurn_UsesRealUsage(t *testing.T) {
	agent := &stubAgent{}
	e := NewEngine("test", agent, nil, "", LangEnglish)
	e.SetContextGuardConfig(ContextGuardConfig{
		Enabled:                true,
		ThresholdTokens:        800,
		KeepRecentTurns:        1,
		SummaryMaxTokens:       100,
		RotateSessionOnCompact: true,
	})

	sessions := NewSessionManager("")
	session := sessions.GetOrCreateActive("telegram:chat:user")
	session.SetAgentSessionID("real-backend-session", agent.Name())
	session.History = []HistoryEntry{
		{Role: "user", Content: "old user 1", Timestamp: time.Unix(1, 0)},
		{Role: "assistant", Content: "old assistant 1", Timestamp: time.Unix(2, 0)},
		{Role: "user", Content: "old user 2", Timestamp: time.Unix(3, 0)},
	}

	closer := &contextGuardUsageSession{
		usage: ContextUsage{UsedTokens: 900},
	}
	e.interactiveStates["telegram:chat:user"] = &interactiveState{agentSession: closer}

	summary := e.applyContextGuardBeforeTurn("telegram:chat:user", agent, session, sessions, "incoming")
	if !strings.HasPrefix(summary, contextGuardSummaryPrefix) {
		t.Fatalf("expected compaction and summary prefix, got: %q", summary)
	}
	if got := session.GetAgentSessionID(); got != "" {
		t.Fatalf("expected agent session id cleared, got %q", got)
	}
}

// TestApplyContextGuardBeforeTurn_RealUsageRotatesEvenWithShortHistory covers
// the defect found in L-0399: with production config (keep_recent_turns=10,
// so keepEntries=20), a session with real usage far over threshold but only
// a handful of cc-connect-visible History entries (typical for tool-heavy
// dev/architect turns, where most of the bloat is CLI-side transcript/tool
// output that never lands in session.History) must still rotate the backend
// session. Before the fix, oldCount = len(History) - keepEntries went
// negative and the guard silently no-op'd — verified live against the
// architect-claude/architect-codex production session files (13 and 12
// History entries respectively, both under the 20-entry keep window).
func TestApplyContextGuardBeforeTurn_RealUsageRotatesEvenWithShortHistory(t *testing.T) {
	agent := &stubAgent{}
	e := NewEngine("test", agent, nil, "", LangEnglish)
	e.SetContextGuardConfig(ContextGuardConfig{
		Enabled:                true,
		ThresholdTokens:        800000,
		KeepRecentTurns:        10, // matches config.toml keep_recent_turns
		SummaryMaxTokens:       100000,
		RotateSessionOnCompact: true,
	})

	sessions := NewSessionManager("")
	session := sessions.GetOrCreateActive("telegram:chat:user")
	session.SetAgentSessionID("real-backend-session", agent.Name())
	// Only 1 History entry — far below keepEntries = KeepRecentTurns*2 = 20.
	session.History = []HistoryEntry{
		{Role: "user", Content: "short", Timestamp: time.Unix(1, 0)},
	}

	closer := &contextGuardUsageSession{
		usage: ContextUsage{UsedTokens: 977000}, // matches the observed runaway magnitude
	}
	e.interactiveStates["telegram:chat:user"] = &interactiveState{agentSession: closer}

	e.applyContextGuardBeforeTurn("telegram:chat:user", agent, session, sessions, "incoming")
	if got := session.GetAgentSessionID(); got != "" {
		t.Fatalf("expected agent session id cleared (rotation must fire even with short History), got %q", got)
	}
	e.interactiveMu.Lock()
	_, stillPresent := e.interactiveStates["telegram:chat:user"]
	e.interactiveMu.Unlock()
	if stillPresent {
		t.Fatal("interactive state still present after context guard rotation")
	}
}



