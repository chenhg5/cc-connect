package core

import (
	"strings"
	"testing"
)

func TestResolvePassthroughCmds_Wildcard(t *testing.T) {
	m := resolvePassthroughCmds([]string{"*"})
	if !m["*"] {
		t.Fatalf("wildcard not set: %+v", m)
	}
}

func TestResolvePassthroughCmds_NormalizesBuiltinAliases(t *testing.T) {
	m := resolvePassthroughCmds([]string{"/STATUS", "help"})
	if !m["status"] {
		t.Errorf("expected 'status' from '/STATUS': %+v", m)
	}
	if !m["help"] {
		t.Errorf("expected 'help' from 'help': %+v", m)
	}
}

func TestResolvePassthroughCmds_UnknownNamesAreKept(t *testing.T) {
	m := resolvePassthroughCmds([]string{"/loop", "agent-only"})
	if !m["loop"] {
		t.Errorf("expected 'loop' to be retained: %+v", m)
	}
	if !m["agent-only"] && !m["agent_only"] {
		t.Errorf("expected normalized agent-only key: %+v", m)
	}
}

func TestResolvePassthroughCmds_TrimsAndSkipsEmpty(t *testing.T) {
	m := resolvePassthroughCmds([]string{"", "   ", "/help"})
	if len(m) != 1 || !m["help"] {
		t.Errorf("unexpected map: %+v", m)
	}
}

func TestShouldPassthroughCommand_WildcardCoversEverything(t *testing.T) {
	m := resolvePassthroughCmds([]string{"*"})
	if !shouldPassthroughCommand("loop", "", m) {
		t.Error("wildcard should pass unknown commands through")
	}
	if !shouldPassthroughCommand("status", "status", m) {
		t.Error("wildcard should pass builtin commands through")
	}
}

func TestShouldPassthroughCommand_MatchesCanonicalID(t *testing.T) {
	m := resolvePassthroughCmds([]string{"status"})
	if !shouldPassthroughCommand("status", "status", m) {
		t.Error("canonical id should match")
	}
	if shouldPassthroughCommand("help", "help", m) {
		t.Error("non-listed command should not pass through")
	}
}

func TestShouldPassthroughCommand_MatchesNormalizedName(t *testing.T) {
	m := resolvePassthroughCmds([]string{"/agent-only"})
	if !shouldPassthroughCommand("agent-only", "", m) {
		t.Error("agent-side name should match passthrough")
	}
	if !shouldPassthroughCommand("Agent_Only", "", m) {
		t.Error("normalized name should match passthrough")
	}
}

func TestShouldPassthroughCommand_EmptyMap(t *testing.T) {
	if shouldPassthroughCommand("status", "status", nil) {
		t.Error("nil map must not pass through")
	}
	if shouldPassthroughCommand("status", "status", map[string]bool{}) {
		t.Error("empty map must not pass through")
	}
}

func TestSetPassthroughCommands_HandleCommandForwardsUnknown(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetPassthroughCommands([]string{"loop"})

	msg := &Message{UserID: "u", Platform: "test", ReplyCtx: "rctx"}
	handled := e.handleCommand(p, msg, "/loop check the deploy")
	if handled {
		t.Fatalf("handleCommand returned true; expected passthrough to fall through to agent")
	}
	if sent := p.getSent(); len(sent) != 0 {
		t.Fatalf("platform received messages on passthrough; want zero, got %#v", sent)
	}
}

func TestSetPassthroughCommands_HandleCommandForwardsBuiltin(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetPassthroughCommands([]string{"status"})

	msg := &Message{UserID: "u", Platform: "test", ReplyCtx: "rctx"}
	handled := e.handleCommand(p, msg, "/status")
	if handled {
		t.Fatalf("passthrough builtin should fall through to agent, got handled=true")
	}
	if sent := p.getSent(); len(sent) != 0 {
		t.Fatalf("status reply should be suppressed when passthrough applies; got %#v", sent)
	}
}

func TestSetPassthroughCommands_DisabledStillBlocksPassthroughCommand(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetDisabledCommands([]string{"status"})
	e.SetPassthroughCommands([]string{"status"})

	msg := &Message{UserID: "u", Platform: "test", ReplyCtx: "rctx"}
	handled := e.handleCommand(p, msg, "/status")
	if !handled {
		t.Fatalf("disabled command must be blocked even if also listed for passthrough")
	}
	sent := p.getSent()
	if len(sent) == 0 || !strings.Contains(strings.ToLower(sent[0]), "disabled") {
		t.Fatalf("expected disabled reply; got %#v", sent)
	}
}

func TestSetPassthroughCommands_AdminGateAppliesBeforePassthrough(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetAdminFrom("")
	e.SetPassthroughCommands([]string{"shell"})

	msg := &Message{UserID: "u", Platform: "test", ReplyCtx: "rctx"}
	handled := e.handleCommand(p, msg, "/shell echo hi")
	if !handled {
		t.Fatalf("privileged command must be blocked for non-admin even if passthrough is set")
	}
}
