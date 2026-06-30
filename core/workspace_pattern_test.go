package core

import (
	"testing"
)

func init() {
	RegisterAgent("stub", func(opts map[string]any) (Agent, error) {
		return &stubAgent{}, nil
	})
}

func TestWorkspacePatternHelpers(t *testing.T) {
	// Test extractThreadID
	if got := extractThreadID("chatID:123"); got != "123" {
		t.Errorf("extractThreadID(chatID:123) = %q, want %q", got, "123")
	}
	if got := extractThreadID("chatID"); got != "" {
		t.Errorf("extractThreadID(chatID) = %q, want %q", got, "")
	}

	// Test extractThreadIDFromSessionKey
	if got := extractThreadIDFromSessionKey("telegram:chatID:123:userID"); got != "123" {
		t.Errorf("extractThreadIDFromSessionKey(telegram:chatID:123:userID) = %q, want %q", got, "123")
	}
	if got := extractThreadIDFromSessionKey("telegram:chatID:userID"); got != "" {
		t.Errorf("extractThreadIDFromSessionKey(telegram:chatID:userID) = %q, want %q", got, "")
	}

	// Test extractThreadIDFromPath
	pattern := `F:\nexus\worktrees\task-${THREAD_ID}`
	if got := extractThreadIDFromPath(pattern, `F:\nexus\worktrees\task-123`); got != "123" {
		t.Errorf("extractThreadIDFromPath(F:\\nexus\\worktrees\\task-123) = %q, want %q", got, "123")
	}
}

func TestWorkspacePatternRouting(t *testing.T) {
	agent := &stubAgent{}
	p := &stubPlatformEngine{n: "telegram"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	e.SetWorkspacePattern(`F:\nexus\worktrees\task-${THREAD_ID}`)

	msg := &Message{
		SessionKey: "telegram:-1003917051393:123:7664413698",
		ChannelKey: "-1003917051393:123",
		Platform:   "telegram",
	}

	_, _, _, effectiveDir, err := e.commandContextWithWorkspace(p, msg)
	if err != nil {
		t.Fatalf("unexpected error in commandContextWithWorkspace: %v", err)
	}

	wantDir := `F:\nexus\worktrees\task-123`
	if effectiveDir != wantDir {
		t.Errorf("effectiveDir = %q, want %q", effectiveDir, wantDir)
	}
}
