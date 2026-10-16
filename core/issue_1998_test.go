package core

import (
	"context"
	"strings"
	"testing"
	"time"
)

// issue1998Agent models one agent work directory shared by two platform users.
type issue1998Agent struct {
	stubDeleteAgent
	historyReads []string
}

func (a *issue1998Agent) GetSessionHistory(_ context.Context, id string, _ int) ([]HistoryEntry, error) {
	a.historyReads = append(a.historyReads, id)
	if id == "bob-session" {
		return []HistoryEntry{{Role: "user", Content: "bob-private-transcript", Timestamp: time.Now()}}, nil
	}
	return nil, nil
}

func issue1998Fixture(t *testing.T) (*Engine, *stubPlatformEngine, *issue1998Agent, *Message) {
	t.Helper()
	platform := &stubPlatformEngine{n: "plain"}
	agent := &issue1998Agent{stubDeleteAgent: stubDeleteAgent{stubListAgent: stubListAgent{sessions: []AgentSessionInfo{
		{ID: "alice-session", Summary: "alice-private-summary", MessageCount: 1},
		{ID: "bob-session", Summary: "bob-private-summary", MessageCount: 1},
	}}}}
	engine := NewEngine("shared-project", agent, []Platform{platform}, "", LangEnglish)
	engine.SetFilterExternalSessions(true)
	engine.sessions.GetOrCreateActive("plain:shared:alice").SetAgentSessionID("alice-session", agent.Name())
	engine.sessions.GetOrCreateActive("plain:shared:bob").SetAgentSessionID("bob-session", agent.Name())
	msg := &Message{Platform: "plain", UserID: "alice", SessionKey: "plain:shared:alice", ReplyCtx: "ctx"}
	return engine, platform, agent, msg
}

// Reproduces the cross-user access paths present on main before the fix.
func TestIssue1998SharedProjectSessionBoundary(t *testing.T) {
	t.Run("list must not disclose another user's summary", func(t *testing.T) {
		engine, platform, _, msg := issue1998Fixture(t)
		engine.handleCommand(platform, msg, "/list")
		if got := strings.Join(platform.getSent(), "\n"); strings.Contains(got, "bob-private-summary") {
			t.Errorf("/list disclosed Bob's summary: %q", got)
		}
	})
	t.Run("switch must not bind another user's session", func(t *testing.T) {
		engine, platform, _, msg := issue1998Fixture(t)
		engine.handleCommand(platform, msg, "/switch bob-session")
		if got := engine.sessions.GetOrCreateActive(msg.SessionKey).GetAgentSessionID(); got == "bob-session" {
			t.Errorf("/switch bound Alice to Bob's agent session; reply=%q", strings.Join(platform.getSent(), "\n"))
		}
	})
	t.Run("history must not read another user's transcript after switch", func(t *testing.T) {
		engine, platform, agent, msg := issue1998Fixture(t)
		engine.handleCommand(platform, msg, "/switch bob-session")
		platform.clearSent()
		engine.handleCommand(platform, msg, "/history 10")
		if got := strings.Join(platform.getSent(), "\n"); strings.Contains(got, "bob-private-transcript") {
			t.Errorf("/history disclosed Bob's transcript: %q (history reads: %v)", got, agent.historyReads)
		}
	})
	t.Run("delete must not remove another user's session", func(t *testing.T) {
		engine, platform, agent, msg := issue1998Fixture(t)
		engine.handleCommand(platform, msg, "/delete bob-session")
		for _, id := range agent.deleted {
			if id == "bob-session" {
				t.Errorf("/delete removed Bob's session; reply=%q", strings.Join(platform.getSent(), "\n"))
			}
		}
	})
	t.Run("search must not disclose another user's summary", func(t *testing.T) {
		engine, platform, _, msg := issue1998Fixture(t)
		engine.handleCommand(platform, msg, "/search bob-private")
		if got := strings.Join(platform.getSent(), "\n"); strings.Contains(got, "bob-private-summary") {
			t.Errorf("/search disclosed Bob's summary: %q", got)
		}
	})
	t.Run("batch delete must not remove another user's session", func(t *testing.T) {
		engine, platform, agent, msg := issue1998Fixture(t)
		engine.handleCommand(platform, msg, "/delete 1-2")
		for _, id := range agent.deleted {
			if id == "bob-session" {
				t.Errorf("batch delete removed Bob's session: %v", agent.deleted)
			}
		}
	})
	t.Run("card list and switch must not expose another user's session", func(t *testing.T) {
		engine, _, _, msg := issue1998Fixture(t)
		card := engine.handleCardNav("nav:/list", msg.SessionKey)
		if card == nil || strings.Contains(card.RenderText(), "bob-private-summary") {
			t.Errorf("card list disclosed Bob: %v", card)
		}
		engine.handleCardNav("act:/switch bob-session", msg.SessionKey)
		if got := engine.sessions.GetOrCreateActive(msg.SessionKey).GetAgentSessionID(); got == "bob-session" {
			t.Error("card switch bound Alice to Bob's session")
		}
	})
	t.Run("card delete-mode must not remove another user's session", func(t *testing.T) {
		engine, _, agent, msg := issue1998Fixture(t)
		engine.submitDeleteModeSelection(msg.SessionKey, map[string]struct{}{"bob-session": {}})
		for _, id := range agent.deleted {
			if id == "bob-session" {
				t.Error("card delete-mode removed Bob's session")
			}
		}
	})
	t.Run("card history must not read a poisoned active session", func(t *testing.T) {
		engine, _, agent, msg := issue1998Fixture(t)
		engine.sessions.GetOrCreateActive(msg.SessionKey).SetAgentSessionID("bob-session", agent.Name())
		card := engine.handleCardNav("nav:/history", msg.SessionKey)
		if card == nil || strings.Contains(card.RenderText(), "bob-private-transcript") {
			t.Errorf("card history disclosed Bob: %v", card)
		}
	})
	t.Run("current, status and name must not expose a poisoned active session", func(t *testing.T) {
		engine, platform, agent, msg := issue1998Fixture(t)
		engine.sessions.GetOrCreateActive(msg.SessionKey).SetAgentSessionID("bob-session", agent.Name())
		for _, cmd := range []string{"/current", "/status", "/name stolen"} {
			platform.clearSent()
			engine.handleCommand(platform, msg, cmd)
			if got := strings.Join(platform.getSent(), "\n"); strings.Contains(got, "bob-private-summary") || strings.Contains(got, "bob-session") {
				t.Errorf("%s disclosed Bob's active session: %q", cmd, got)
			}
		}
		if got := engine.sessions.GetSessionName("bob-session"); got != "" {
			t.Errorf("/name renamed Bob's session: %q", got)
		}
	})
}

func TestIssue1998UnknownAndAmbiguousOwnershipDenied(t *testing.T) {
	engine, _, agent, msg := issue1998Fixture(t)
	agent.sessions = append(agent.sessions, AgentSessionInfo{ID: "unknown-cli", Summary: "untracked-private"})
	engine.sessions.legacyData = true
	visible := engine.applySessionFilter(agent.sessions, engine.sessions, msg.SessionKey)
	if len(visible) != 1 || visible[0].ID != "alice-session" {
		t.Fatalf("legacy/untracked visibility = %v, want only Alice", visible)
	}
	if !engine.sessions.AgentSessionIDVisible(msg.SessionKey, "alice-session", false) || engine.sessions.AgentSessionIDVisible(msg.SessionKey, "unknown-cli", false) {
		t.Fatal("single-ID check disagrees with legacy/unknown ownership")
	}
	engine.sessions.GetOrCreateActive(msg.SessionKey).SetAgentSessionID("bob-session", agent.Name())
	visible = engine.applySessionFilter(agent.sessions, engine.sessions, msg.SessionKey)
	if len(visible) != 1 || visible[0].ID != "alice-session" {
		t.Fatalf("ambiguous ownership leaked: %v", visible)
	}
	if engine.sessions.AgentSessionIDVisible(msg.SessionKey, "bob-session", true) || engine.sessions.AgentSessionIDVisible("plain:shared:bob", "bob-session", false) {
		t.Fatal("single-ID check allowed ambiguous ownership")
	}
}

func TestIssue1998AdminCrossUserRequiresExplicitOptIn(t *testing.T) {
	engine, platform, agent, msg := issue1998Fixture(t)
	engine.SetAdminFrom("alice")
	engine.handleCommand(platform, msg, "/list")
	if got := strings.Join(platform.getSent(), "\n"); strings.Contains(got, "bob-private-summary") {
		t.Fatalf("admin_from alone disclosed Bob: %q", got)
	}
	engine.SetSessionAdminCrossUser(true)
	platform.clearSent()
	engine.handleCommand(platform, msg, "/list")
	if got := strings.Join(platform.getSent(), "\n"); !strings.Contains(got, "bob-private-summary") {
		t.Fatalf("explicit admin opt-in did not show tracked Bob: %q", got)
	}
	agent.sessions = append(agent.sessions, AgentSessionInfo{ID: "unknown-cli", Summary: "untracked-private"})
	platform.clearSent()
	engine.handleCommand(platform, msg, "/list")
	if got := strings.Join(platform.getSent(), "\n"); strings.Contains(got, "untracked-private") {
		t.Fatalf("admin opt-in disclosed unknown CLI session: %q", got)
	}
	engine.handleCommand(platform, msg, "/switch bob-session")
	if got := engine.sessions.GetOrCreateActive(msg.SessionKey).GetAgentSessionID(); got != "bob-session" {
		t.Fatalf("explicit admin switch selected %q, want Bob", got)
	}
	bob := &Message{Platform: "plain", UserID: "bob", SessionKey: "plain:shared:bob", ReplyCtx: "ctx"}
	platform.clearSent()
	engine.handleCommand(platform, bob, "/list")
	if got := strings.Join(platform.getSent(), "\n"); !strings.Contains(got, "bob-private-summary") {
		t.Fatalf("admin borrowing removed Bob's original ownership: %q", got)
	}
	if got := engine.sessions.VisibleAgentSessionIDs(msg.SessionKey, false); len(got) != 1 {
		t.Fatalf("admin borrowing claimed another user's ID: %v", got)
	}
	engine.SetSessionAdminCrossUser(false)
	platform.clearSent()
	engine.handleCommand(platform, msg, "/history 10")
	if got := strings.Join(platform.getSent(), "\n"); strings.Contains(got, "bob-private-transcript") {
		t.Fatalf("admin privilege removal left borrowed transcript accessible: %q", got)
	}
}

func TestIssue1998AdminListOnCardPlatformUsesTextPrivilege(t *testing.T) {
	platform := &stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	agent := &issue1998Agent{stubDeleteAgent: stubDeleteAgent{stubListAgent: stubListAgent{sessions: []AgentSessionInfo{
		{ID: "alice-session", Summary: "alice-private-summary"},
		{ID: "bob-session", Summary: "bob-private-summary"},
	}}}}
	engine := NewEngine("shared-project", agent, []Platform{platform}, "", LangEnglish)
	engine.sessions.GetOrCreateActive("feishu:chat:alice").SetAgentSessionID("alice-session", agent.Name())
	engine.sessions.GetOrCreateActive("feishu:chat:bob").SetAgentSessionID("bob-session", agent.Name())
	engine.SetAdminFrom("alice")
	engine.SetSessionAdminCrossUser(true)
	alice := &Message{Platform: "feishu", UserID: "alice", SessionKey: "feishu:chat:alice", ReplyCtx: "ctx"}
	engine.handleCommand(platform, alice, "/list")
	if got := strings.Join(platform.getSent(), "\n"); !strings.Contains(got, "bob-private-summary") {
		t.Fatalf("explicit admin /list on card platform did not show tracked Bob in text: %q", got)
	}
	if len(platform.repliedCards) != 0 {
		t.Fatal("admin cross-user list rendered a card that could lose user authority")
	}
	card := engine.handleCardNav("nav:/list", alice.SessionKey)
	if card == nil || strings.Contains(card.RenderText(), "bob-private-summary") {
		t.Fatalf("card navigation widened to admin cross-user access: %v", card)
	}
}

func TestIssue1998SharedSessionModeBlocksHistoryCommands(t *testing.T) {
	engine, platform, agent, msg := issue1998Fixture(t)
	engine.SetSharedSessionCommandPlatforms([]string{"plain"})
	for _, cmd := range []string{"/list", "/switch bob-session", "/history 10", "/delete bob-session", "/search bob-private", "/name renamed"} {
		platform.clearSent()
		engine.handleCommand(platform, msg, cmd)
		if got := strings.Join(platform.getSent(), "\n"); !strings.Contains(got, "shared-channel mode") {
			t.Errorf("%s was not blocked: %q", cmd, got)
		}
	}
	if len(agent.deleted) != 0 || len(agent.historyReads) != 0 {
		t.Fatalf("shared mode reached agent history/deletion: deleted=%v read=%v", agent.deleted, agent.historyReads)
	}
	for _, action := range []string{"nav:/list", "act:/switch bob-session", "nav:/history", "act:/delete-mode confirm"} {
		card := engine.handleCardNav(action, msg.SessionKey)
		if card == nil || !strings.Contains(card.RenderText(), "shared-channel mode") {
			t.Errorf("card action %s not blocked: %v", action, card)
		}
	}
}

// A real Engine/SessionManager journey through the same inbound entrypoint as
// a platform: Alice can inspect her own session while Bob's stays invisible.
func TestCUJ_B13_SharedProjectUserSessionIsolation(t *testing.T) {
	engine, platform, agent, alice := issue1998Fixture(t)
	for _, command := range []string{"/list", "/search bob-private", "/switch bob-session", "/history 10", "/delete bob-session"} {
		platform.clearSent()
		message := *alice
		message.Content = command
		engine.ReceiveMessage(platform, &message)
		visible := strings.Join(platform.getSent(), "\n")
		if strings.Contains(visible, "bob-private-summary") || strings.Contains(visible, "bob-private-transcript") {
			t.Fatalf("%s leaked Bob's data to Alice: %q", command, visible)
		}
		if command == "/list" && !strings.Contains(visible, "alice-private-summary") {
			t.Fatalf("Alice cannot see her own session: %q", visible)
		}
	}
	for _, id := range append(agent.deleted, agent.historyReads...) {
		if id == "bob-session" {
			t.Fatalf("Bob's data was accessed: deleted=%v reads=%v", agent.deleted, agent.historyReads)
		}
	}
	bob := &Message{Platform: "plain", UserID: "bob", SessionKey: "plain:shared:bob", ReplyCtx: "ctx"}
	for _, command := range []string{"/list", "/history 10"} {
		platform.clearSent()
		message := *bob
		message.Content = command
		engine.ReceiveMessage(platform, &message)
		visible := strings.Join(platform.getSent(), "\n")
		if !strings.Contains(visible, "bob-private") || strings.Contains(visible, "alice-private") {
			t.Fatalf("Bob's %s visibility was incorrect: %q", command, visible)
		}
	}
}
