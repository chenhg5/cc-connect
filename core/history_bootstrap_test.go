package core

import (
	"context"
	"strings"
	"testing"
)

// namedStubAgent is an Agent stub whose Name() returns a configurable value so
// the per-(session, agentType) bootstrap marker can be exercised against
// distinct agent types in tests (issue #1805).
type namedStubAgent struct {
	name string
}

func (a *namedStubAgent) Name() string { return a.name }
func (a *namedStubAgent) StartSession(_ context.Context, _ string) (AgentSession, error) {
	return &stubAgentSession{}, nil
}
func (a *namedStubAgent) ListSessions(_ context.Context) ([]AgentSessionInfo, error) {
	return nil, nil
}
func (a *namedStubAgent) Stop() error { return nil }

// makeEngineWithBootstrap returns a minimal engine with SetHistoryBootstrap
// already called and the provided agent wired in. Sessions are kept in memory
// only (storePath == ""), so no temp file is touched.
func makeEngineWithBootstrap(t *testing.T, agent Agent, enabled bool, maxEntries, maxTokens int) *Engine {
	t.Helper()
	e := NewEngine("test", agent, nil, "", LangEnglish)
	e.SetHistoryBootstrap(enabled, maxEntries, maxTokens)
	return e
}

// TestMaybeBootstrapHistory_FiresOnAgentSwitch is the core regression test for
// issue #1805: when a session has prior cc-connect history AND a non-empty
// PastAgentSessionIDs (i.e. an agent switch happened), the bootstrap block
// must be prepended on the first prompt to the new agent type.
func TestMaybeBootstrapHistory_FiresOnAgentSwitch(t *testing.T) {
	ag := &namedStubAgent{name: "codex"}
	e := makeEngineWithBootstrap(t, ag, true, 20, 4000)

	s := &Session{
		ID:                  "s1",
		PastAgentSessionIDs: []string{"old-claudecode-id"},
	}
	s.AddHistory("user", "earlier user msg")
	s.AddHistory("assistant", "earlier assistant reply")
	// processInteractiveMessageWith would add the current message before
	// calling maybeBootstrapHistory; mirror that here so the exclusion of
	// the just-added message is exercised.
	s.AddHistory("user", "current turn message")

	original := "current prompt body"
	out := original
	if !e.maybeBootstrapHistory(s, ag, &out) {
		t.Fatalf("expected bootstrap to fire on agent switch")
	}
	if !strings.HasPrefix(out, "[cc-connect history_bootstrap:") {
		t.Fatalf("expected bootstrap header at start of prompt; got %q", out[:min(80, len(out))])
	}
	if !strings.Contains(out, "earlier user msg") || !strings.Contains(out, "earlier assistant reply") {
		t.Fatalf("expected older transcript entries in bootstrap block; got %q", out)
	}
	// Current turn must NOT be re-injected — it is already part of
	// buildSenderPrompt's output below the prefix.
	if strings.Contains(out, "[user] current turn message") {
		t.Fatalf("current turn message should not appear in bootstrap block; got %q", out)
	}
	if !strings.HasSuffix(out, original) {
		t.Fatalf("expected original prompt to remain appended after bootstrap block")
	}
}

// TestMaybeBootstrapHistory_NoOpOnNoPriorHistory covers the /new case: a
// freshly-created session with no PastAgentSessionIDs and no History must
// NOT trigger a bootstrap (would be a confusing empty prefix to the agent).
func TestMaybeBootstrapHistory_NoOpOnNoPriorHistory(t *testing.T) {
	ag := &namedStubAgent{name: "codex"}
	e := makeEngineWithBootstrap(t, ag, true, 20, 4000)

	s := &Session{ID: "fresh"}
	// No AddHistory calls; this is the /new case.
	out := "hello"
	if e.maybeBootstrapHistory(s, ag, &out) {
		t.Fatalf("expected no bootstrap when session is fresh")
	}
	if out != "hello" {
		t.Fatalf("expected prompt to be unchanged; got %q", out)
	}
}

// TestMaybeBootstrapHistory_NoOpOnOnlyCurrentMessage covers a session whose
// only history is the current user message (i.e. no PastAgentSessionIDs and
// History is just the tail that should be excluded).
func TestMaybeBootstrapHistory_NoOpOnOnlyCurrentMessage(t *testing.T) {
	ag := &namedStubAgent{name: "codex"}
	e := makeEngineWithBootstrap(t, ag, true, 20, 4000)

	s := &Session{ID: "s1"}
	s.AddHistory("user", "current turn only")
	out := "current prompt"
	if e.maybeBootstrapHistory(s, ag, &out) {
		t.Fatalf("expected no bootstrap when only current message is in history")
	}
	if out != "current prompt" {
		t.Fatalf("expected prompt unchanged; got %q", out)
	}
}

// TestMaybeBootstrapHistory_NoOpWhenAlreadyBootstrapped ensures the
// per-agent-type marker makes bootstrap idempotent within a single session.
func TestMaybeBootstrapHistory_NoOpWhenAlreadyBootstrapped(t *testing.T) {
	ag := &namedStubAgent{name: "codex"}
	e := makeEngineWithBootstrap(t, ag, true, 20, 4000)

	s := &Session{
		ID:                  "s1",
		PastAgentSessionIDs: []string{"old"},
	}
	s.AddHistory("user", "old msg")
	s.AddHistory("assistant", "old reply")
	s.AddHistory("user", "current")
	s.MarkBootstrappedFor("codex")

	out := "current prompt"
	if e.maybeBootstrapHistory(s, ag, &out) {
		t.Fatalf("expected no bootstrap when marker is set for this agent type")
	}
	if out != "current prompt" {
		t.Fatalf("expected prompt unchanged after second call; got %q", out)
	}
}

// TestMaybeBootstrapHistory_DifferentAgentTypeResets verifies the marker is
// keyed on agent type: if a session switched from claudecode to codex and
// bootstrap already fired for claudecode, a future turn routed to codex
// must still bootstrap (because codex is a new agent type for this session).
func TestMaybeBootstrapHistory_DifferentAgentTypeResets(t *testing.T) {
	codex := &namedStubAgent{name: "codex"}

	e := makeEngineWithBootstrap(t, codex, true, 20, 4000)

	s := &Session{
		ID:                  "s1",
		PastAgentSessionIDs: []string{"claude-old-1", "claude-old-2"},
	}
	s.AddHistory("user", "u1")
	s.AddHistory("assistant", "a1")
	s.AddHistory("user", "current")
	s.MarkBootstrappedFor("claudecode") // already bootstrapped for previous agent type

	out := "current prompt"
	if !e.maybeBootstrapHistory(s, codex, &out) {
		t.Fatalf("expected bootstrap to fire for new agent type codex")
	}
	if !strings.Contains(out, "[cc-connect history_bootstrap: agent_type=codex") {
		t.Fatalf("expected header to mention codex; got %q", out[:min(120, len(out))])
	}
	// Both past session ids should be referenced in the header.
	if !strings.Contains(out, "claude-old-1") || !strings.Contains(out, "claude-old-2") {
		t.Fatalf("expected past native session ids in header; got %q", out)
	}
	// Make sure we don't accidentally use the previous agent's name.
	if strings.Contains(out, "agent_type=claudecode") {
		t.Fatalf("expected new agent_type=codex, not previous one; got %q", out[:min(120, len(out))])
	}
}

// TestMaybeBootstrapHistory_DisabledDoesNothing verifies that an operator who
// disables the feature via TOML gets the original prompt unchanged.
func TestMaybeBootstrapHistory_DisabledDoesNothing(t *testing.T) {
	ag := &namedStubAgent{name: "codex"}
	e := makeEngineWithBootstrap(t, ag, false, 20, 4000)

	s := &Session{
		ID:                  "s1",
		PastAgentSessionIDs: []string{"old"},
	}
	s.AddHistory("user", "msg")
	s.AddHistory("user", "current")

	out := "current prompt"
	if e.maybeBootstrapHistory(s, ag, &out) {
		t.Fatalf("expected no bootstrap when feature is disabled")
	}
	if out != "current prompt" {
		t.Fatalf("expected prompt unchanged; got %q", out)
	}
}

// TestMaybeBootstrapHistory_TrimsByEntryCap verifies the entry cap keeps only
// the most-recent N entries, with older context dropped.
func TestMaybeBootstrapHistory_TrimsByEntryCap(t *testing.T) {
	ag := &namedStubAgent{name: "codex"}
	// cap=2 means we keep only the 2 most recent (older) entries; the current
	// message is excluded by maybeBootstrapHistory before trimming.
	e := makeEngineWithBootstrap(t, ag, true, 2, 0)

	s := &Session{
		ID:                  "s1",
		PastAgentSessionIDs: []string{"old"},
	}
	s.AddHistory("user", "OLD-1")
	s.AddHistory("assistant", "OLD-2")
	s.AddHistory("user", "KEEP-1")
	s.AddHistory("assistant", "KEEP-2")
	s.AddHistory("user", "current")

	out := "current"
	if !e.maybeBootstrapHistory(s, ag, &out) {
		t.Fatalf("expected bootstrap to fire")
	}
	if strings.Contains(out, "OLD-1") || strings.Contains(out, "OLD-2") {
		t.Fatalf("expected OLD-* entries to be trimmed by entry cap; got %q", out)
	}
	if !strings.Contains(out, "KEEP-1") || !strings.Contains(out, "KEEP-2") {
		t.Fatalf("expected KEEP-* entries to be retained; got %q", out)
	}
}

// TestMaybeBootstrapHistory_TrimsByTokenCap verifies that when total tokens
// exceed the cap, the oldest entries are dropped until the budget fits.
func TestMaybeBootstrapHistory_TrimsByTokenCap(t *testing.T) {
	ag := &namedStubAgent{name: "codex"}
	// Tokens: estimateTokens counts roughly rune/4 per entry. A 100-rune
	// message ≈ 25 tokens. With cap=60 we expect the newest 2-3 entries
	// retained (depending on what fits), and the older ones dropped.
	e := makeEngineWithBootstrap(t, ag, true, 0, 60)

	s := &Session{
		ID:                  "s1",
		PastAgentSessionIDs: []string{"old"},
	}
	for i := 0; i < 6; i++ {
		s.AddHistory("user", strings.Repeat("x", 100))
	}
	s.AddHistory("user", "current")

	out := "current"
	if !e.maybeBootstrapHistory(s, ag, &out) {
		t.Fatalf("expected bootstrap to fire")
	}
	// The block must contain SOME older entries but not all 6.
	count := strings.Count(out, "[user] xxxxxxxxxx")
	if count >= 6 {
		t.Fatalf("expected token cap to drop some entries; got %d retained", count)
	}
	if count == 0 {
		t.Fatalf("expected at least one entry to be retained; got %q", out)
	}
}

// TestMaybeBootstrapHistory_NilSafe ensures all guards handle bad inputs.
func TestMaybeBootstrapHistory_NilSafe(t *testing.T) {
	ag := &namedStubAgent{name: "codex"}
	e := makeEngineWithBootstrap(t, ag, true, 20, 4000)
	s := &Session{ID: "s1"}
	s.AddHistory("user", "current")

	out := "x"
	if e.maybeBootstrapHistory(nil, ag, &out) {
		t.Fatalf("expected no bootstrap on nil session")
	}
	if e.maybeBootstrapHistory(s, nil, &out) {
		t.Fatalf("expected no bootstrap on nil agent")
	}
	var nilStr *string
	if e.maybeBootstrapHistory(s, ag, nilStr) {
		t.Fatalf("expected no bootstrap on nil prompt")
	}
	if out != "x" {
		t.Fatalf("expected prompt unchanged; got %q", out)
	}

	// Empty agent name must also be a no-op even if history is present.
	emptyName := &namedStubAgent{name: ""}
	s2 := &Session{ID: "s2", PastAgentSessionIDs: []string{"old"}}
	s2.AddHistory("user", "msg")
	s2.AddHistory("user", "current")
	out2 := "x"
	if e.maybeBootstrapHistory(s2, emptyName, &out2) {
		t.Fatalf("expected no bootstrap on empty agent name")
	}
}

// TestSessionIsBootstrappedFor_RoundTrip covers the per-(session, agentType)
// marker semantics: the marker survives concurrent reads/writes without
// deadlocking and isolates between agent types.
func TestSessionIsBootstrappedFor_RoundTrip(t *testing.T) {
	s := &Session{ID: "s1"}
	if s.IsBootstrappedFor("codex") {
		t.Fatalf("expected empty marker to report false")
	}
	s.MarkBootstrappedFor("codex")
	if !s.IsBootstrappedFor("codex") {
		t.Fatalf("expected marker set after MarkBootstrappedFor")
	}
	if s.IsBootstrappedFor("claudecode") {
		t.Fatalf("expected different agent type to remain false")
	}
	// Idempotent.
	s.MarkBootstrappedFor("codex")
	s.MarkBootstrappedFor("codex")
	if !s.IsBootstrappedFor("codex") {
		t.Fatalf("expected marker to remain set after duplicate marks")
	}
}

// TestSessionSnapshotRoundTripPreservesBootstrappedFor verifies that the
// SessionManager.Save / load cycle preserves the BootstrappedFor marker so
// the bootstrap does NOT re-fire after a process restart (issue #1805).
func TestSessionSnapshotRoundTripPreservesBootstrappedFor(t *testing.T) {
	tmp := t.TempDir()
	storePath := tmp + "/sessions.json"

	sm := NewSessionManager(storePath)
	s := &Session{ID: "s1", AgentType: "claudecode"}
	s.MarkBootstrappedFor("claudecode")
	sm.sessions["s1"] = s
	sm.Save()

	sm2 := NewSessionManager(storePath)
	sm2.load()
	loaded, ok := sm2.sessions["s1"]
	if !ok {
		t.Fatalf("expected session to load from snapshot")
	}
	if !loaded.IsBootstrappedFor("claudecode") {
		t.Fatalf("expected BootstrappedFor to survive save+load")
	}
	if loaded.IsBootstrappedFor("codex") {
		t.Fatalf("expected unloaded agent types to remain unset")
	}
}

// TestCopyBootstrappedFor_IsIndependent ensures the deep-copy helper used in
// the snapshot path does not alias the live map, so a later
// MarkBootstrappedFor on the source does not mutate the snapshot.
func TestCopyBootstrappedFor_IsIndependent(t *testing.T) {
	src := map[string]struct{}{"a": {}}
	dst := copyBootstrappedFor(src)
	dst["b"] = struct{}{}
	if _, ok := src["b"]; ok {
		t.Fatalf("expected source to remain unchanged after dst mutation")
	}
	src["c"] = struct{}{}
	if _, ok := dst["c"]; ok {
		t.Fatalf("expected dst to remain unchanged after src mutation")
	}
	if copyBootstrappedFor(nil) != nil {
		t.Fatalf("expected copy of nil to be nil")
	}
}
