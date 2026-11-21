//go:build smoke

package dsh

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// TestSmoke_RoundTrip runs the adapter against a REAL dsh web server
// (default http://127.0.0.1:3080, override via DSH_SMOKE_URL). It creates a
// fresh session in this project's directory, sends one prompt, and verifies
// the streamed events come back and the turn completes.
//
// Run: go test -tags smoke ./agent/dsh/ -run TestSmoke -v
func TestSmoke_RoundTrip(t *testing.T) {
	baseURL := "http://127.0.0.1:3080"
	if v := envOr("DSH_SMOKE_URL", ""); v != "" {
		baseURL = v
	}
	workDir := envOr("DSH_SMOKE_WORKDIR", ".")

	agent, err := New(map[string]any{
		"base_url": baseURL,
		"work_dir": workDir,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// Fresh session.
	s, err := agent.StartSession(ctx, "")
	if err != nil {
		t.Fatalf("StartSession: %v (is `dsh web` running at %s?)", err, baseURL)
	}
	defer s.Close()
	ds := s.(*dshSession)
	t.Logf("session id: %s", ds.sessionID)

	if err := ds.Send("Reply with exactly: SMOKE_OK", "smoke-msg", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	var text strings.Builder
	var done bool
	deadline := time.After(90 * time.Second)
	for !done {
		select {
		case evt, ok := <-ds.events:
			if !ok {
				t.Fatal("events channel closed before turn end")
			}
			switch evt.Type {
			case core.EventText:
				text.WriteString(evt.Content)
			case core.EventThinking:
				t.Logf("thinking: %s", truncate(evt.Content, 80))
			case core.EventToolUse:
				t.Logf("tool: %s %s", evt.ToolName, truncate(evt.ToolInput, 80))
			case core.EventError:
				t.Logf("error event: %v", evt.Error)
			case core.EventResult:
				done = true
				if evt.Error != nil {
					t.Logf("turn ended with error: %v", evt.Error)
				}
			}
		case <-deadline:
			t.Fatal("timed out waiting for turn end")
		}
	}
	t.Logf("assistant text: %q", text.String())
	if !strings.Contains(text.String(), "SMOKE_OK") {
		t.Errorf("expected SMOKE_OK in reply, got %q", text.String())
	}

	// List + validation against the real server.
	sessions, err := agent.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	t.Logf("sessions in %s: %d", workDir, len(sessions))
	validator := agent.(core.SessionIDValidator)
	if !validator.ValidateSessionID(ctx, ds.sessionID) {
		t.Errorf("ValidateSessionID should accept our own session")
	}
	if validator.ValidateSessionID(ctx, "session-does-not-exist") {
		t.Errorf("ValidateSessionID must reject unknown sessions")
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
