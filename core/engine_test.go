package core

import (
	"context"
	"testing"
)

// --- stubs for Engine tests ---

type stubAgent struct{}

func (a *stubAgent) Name() string                                                { return "stub" }
func (a *stubAgent) StartSession(_ context.Context, _ string) (AgentSession, error) {
	return &stubAgentSession{}, nil
}
func (a *stubAgent) ListSessions(_ context.Context) ([]AgentSessionInfo, error) { return nil, nil }
func (a *stubAgent) Stop() error                                                 { return nil }

type stubAgentSession struct{}

func (s *stubAgentSession) Send(_ string, _ []ImageAttachment) error          { return nil }
func (s *stubAgentSession) RespondPermission(_ string, _ PermissionResult) error { return nil }
func (s *stubAgentSession) Events() <-chan Event                               { return make(chan Event) }
func (s *stubAgentSession) CurrentSessionID() string                           { return "stub-session" }
func (s *stubAgentSession) Alive() bool                                        { return true }
func (s *stubAgentSession) Close() error                                       { return nil }

type stubPlatformEngine struct {
	n    string
	sent []string
}

func (p *stubPlatformEngine) Name() string                                           { return p.n }
func (p *stubPlatformEngine) Start(MessageHandler) error                             { return nil }
func (p *stubPlatformEngine) Reply(_ context.Context, _ any, content string) error   { p.sent = append(p.sent, content); return nil }
func (p *stubPlatformEngine) Send(_ context.Context, _ any, content string) error    { p.sent = append(p.sent, content); return nil }
func (p *stubPlatformEngine) Stop() error                                            { return nil }

func newTestEngine() *Engine {
	return NewEngine("test", &stubAgent{}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangEnglish)
}

// --- alias tests ---

func TestEngine_Alias(t *testing.T) {
	e := newTestEngine()
	e.AddAlias("帮助", "/help")
	e.AddAlias("新建", "/new")

	got := e.resolveAlias("帮助")
	if got != "/help" {
		t.Errorf("resolveAlias('帮助') = %q, want /help", got)
	}

	got = e.resolveAlias("新建 my-session")
	if got != "/new my-session" {
		t.Errorf("resolveAlias('新建 my-session') = %q, want '/new my-session'", got)
	}

	got = e.resolveAlias("random text")
	if got != "random text" {
		t.Errorf("resolveAlias should not modify unmatched content, got %q", got)
	}
}

func TestEngine_ClearAliases(t *testing.T) {
	e := newTestEngine()
	e.AddAlias("帮助", "/help")
	e.ClearAliases()

	got := e.resolveAlias("帮助")
	if got != "帮助" {
		t.Errorf("after ClearAliases, should not resolve, got %q", got)
	}
}

// --- banned words tests ---

func TestEngine_BannedWords(t *testing.T) {
	e := newTestEngine()
	e.SetBannedWords([]string{"spam", "BadWord"})

	if w := e.matchBannedWord("this is spam content"); w != "spam" {
		t.Errorf("expected 'spam', got %q", w)
	}
	if w := e.matchBannedWord("CONTAINS BADWORD HERE"); w != "badword" {
		t.Errorf("expected case-insensitive match 'badword', got %q", w)
	}
	if w := e.matchBannedWord("clean message"); w != "" {
		t.Errorf("expected empty, got %q", w)
	}
}

func TestEngine_BannedWordsEmpty(t *testing.T) {
	e := newTestEngine()
	if w := e.matchBannedWord("anything"); w != "" {
		t.Errorf("no banned words set, should return empty, got %q", w)
	}
}

// --- disabled commands tests ---

func TestEngine_DisabledCommands(t *testing.T) {
	e := newTestEngine()
	e.SetDisabledCommands([]string{"upgrade", "restart"})

	if !e.disabledCmds["upgrade"] {
		t.Error("upgrade should be disabled")
	}
	if !e.disabledCmds["restart"] {
		t.Error("restart should be disabled")
	}
	if e.disabledCmds["help"] {
		t.Error("help should not be disabled")
	}
}

func TestEngine_DisabledCommandsWithSlash(t *testing.T) {
	e := newTestEngine()
	e.SetDisabledCommands([]string{"/upgrade"})

	if !e.disabledCmds["upgrade"] {
		t.Error("upgrade should be disabled even when prefixed with /")
	}
}

// --- quiet tests ---

func TestQuietSessionToggle(t *testing.T) {
	e := newTestEngine()
	p := &stubPlatformEngine{n: "test"}
	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}

	// /quiet — per-session toggle on
	e.cmdQuiet(p, msg, nil)

	e.interactiveMu.Lock()
	state := e.interactiveStates["test:user1"]
	e.interactiveMu.Unlock()

	if state == nil {
		t.Fatal("expected interactiveState to be created")
	}
	state.mu.Lock()
	q := state.quiet
	state.mu.Unlock()
	if !q {
		t.Fatal("expected session quiet to be true")
	}

	// /quiet — per-session toggle off
	e.cmdQuiet(p, msg, nil)
	state.mu.Lock()
	q = state.quiet
	state.mu.Unlock()
	if q {
		t.Fatal("expected session quiet to be false after second toggle")
	}
}

func TestQuietSessionResetsOnNewSession(t *testing.T) {
	e := newTestEngine()
	p := &stubPlatformEngine{n: "test"}
	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}

	// Enable per-session quiet
	e.cmdQuiet(p, msg, nil)

	// Simulate /new
	e.cleanupInteractiveState("test:user1")

	// State should be gone, quiet resets
	e.interactiveMu.Lock()
	state := e.interactiveStates["test:user1"]
	e.interactiveMu.Unlock()
	if state != nil {
		t.Fatal("expected interactiveState to be cleaned up")
	}

	// Global quiet should still be off
	e.quietMu.RLock()
	gq := e.quiet
	e.quietMu.RUnlock()
	if gq {
		t.Fatal("expected global quiet to be false")
	}
}

func TestQuietGlobalToggle(t *testing.T) {
	e := newTestEngine()
	p := &stubPlatformEngine{n: "test"}
	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}

	// Default: global quiet is off
	if e.quiet {
		t.Fatal("expected global quiet to be false by default")
	}

	// /quiet global — toggle on
	e.cmdQuiet(p, msg, []string{"global"})
	e.quietMu.RLock()
	q := e.quiet
	e.quietMu.RUnlock()
	if !q {
		t.Fatal("expected global quiet to be true")
	}

	// /quiet global — toggle off
	e.cmdQuiet(p, msg, []string{"global"})
	e.quietMu.RLock()
	q = e.quiet
	e.quietMu.RUnlock()
	if q {
		t.Fatal("expected global quiet to be false after second toggle")
	}
}

func TestQuietGlobalPersistsAcrossSessions(t *testing.T) {
	e := newTestEngine()
	p := &stubPlatformEngine{n: "test"}
	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}

	// Enable global quiet
	e.cmdQuiet(p, msg, []string{"global"})

	// Simulate /new
	e.cleanupInteractiveState("test:user1")

	// Global quiet should still be on
	e.quietMu.RLock()
	q := e.quiet
	e.quietMu.RUnlock()
	if !q {
		t.Fatal("expected global quiet to remain true after session cleanup")
	}
}

func TestQuietGlobalAndSessionCombined(t *testing.T) {
	e := newTestEngine()
	p := &stubPlatformEngine{n: "test"}
	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}

	// Only global quiet on — should suppress
	e.cmdQuiet(p, msg, []string{"global"})
	e.quietMu.RLock()
	gq := e.quiet
	e.quietMu.RUnlock()
	if !gq {
		t.Fatal("expected global quiet on")
	}

	// Session quiet is off (no state yet) — global alone should be enough
	e.interactiveMu.Lock()
	state := e.interactiveStates["test:user1"]
	e.interactiveMu.Unlock()
	if state != nil {
		t.Fatal("expected no session state yet")
	}

	// Turn off global, turn on session
	e.cmdQuiet(p, msg, []string{"global"}) // global off
	e.cmdQuiet(p, msg, nil)                // session on

	e.quietMu.RLock()
	gq = e.quiet
	e.quietMu.RUnlock()
	if gq {
		t.Fatal("expected global quiet off")
	}

	e.interactiveMu.Lock()
	state = e.interactiveStates["test:user1"]
	e.interactiveMu.Unlock()
	state.mu.Lock()
	sq := state.quiet
	state.mu.Unlock()
	if !sq {
		t.Fatal("expected session quiet on")
	}
}
