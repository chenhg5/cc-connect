package acp

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Exercises real Cursor CLI "agent acp" when installed (~/.local/bin/agent).
// Requires prior `agent login` (or CURSOR_API_KEY / CURSOR_AUTH_TOKEN). Skips if binary missing.
func TestCursorCLI_ACPHandshake(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if os.Getenv("CI") != "" {
		t.Skip("skipping real Cursor CLI ACP handshake in CI (requires local agent and login)")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("home dir:", err)
	}
	agentBin := filepath.Join(home, ".local/bin/agent")
	if _, err := os.Stat(agentBin); err != nil {
		t.Skip("Cursor agent not at", agentBin)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	s, err := newACPSession(ctx, agentBin, []string{"acp"}, nil, t.TempDir(), "", "cursor_login")
	if err != nil {
		t.Fatalf("handshake failed (is `agent login` done?): %v", err)
	}
	defer func() { _ = s.Close() }()

	if s.CurrentSessionID() == "" {
		t.Fatal("empty ACP session id after handshake")
	}
	t.Logf("ACP session id: %s", s.CurrentSessionID())
}
