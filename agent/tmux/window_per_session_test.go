package tmux

import (
	"os/exec"
	"testing"
)

// requireTmux skips tests that must call New(), which probes for the tmux
// binary; CI hosts without tmux would otherwise fail before exercising logic.
func requireTmux(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed; New() requires the tmux binary")
	}
}

func TestNewParsesWindowPerSession(t *testing.T) {
	requireTmux(t)
	base := map[string]any{"session": "claude-work"}

	a, err := New(base)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a.(*Agent).windowPerSession {
		t.Fatalf("windowPerSession should default to false")
	}

	base["window_per_session"] = true
	a, err = New(base)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !a.(*Agent).windowPerSession {
		t.Fatalf("windowPerSession should be true when configured")
	}
}

func TestWindowForSession_Disabled(t *testing.T) {
	a := &Agent{windowPerSession: false}

	// Real workDir -> per-directory window, sessionID passes through unchanged.
	win, id := a.windowForSession("claude-work", "0", "/repo/app", "")
	if win != uniqueWindowName("/repo/app") {
		t.Errorf("window = %q, want %q", win, uniqueWindowName("/repo/app"))
	}
	if id != "" {
		t.Errorf("effectiveID = %q, want empty (unchanged)", id)
	}

	// No workDir -> legacy pane.
	win, _ = a.windowForSession("claude-work", "0", "", "")
	if win != "0" {
		t.Errorf("window = %q, want legacy pane %q", win, "0")
	}
}

func TestWindowForSession_EnabledResumeReusesWindow(t *testing.T) {
	a := &Agent{windowPerSession: true}

	// A non-empty (resumed) sessionID maps straight back to its own window,
	// independent of workDir — so the same thread always reuses one window.
	win, id := a.windowForSession("claude-work", "0", "/repo/app", "cc-7")
	if win != "cc-7" || id != "cc-7" {
		t.Errorf("got window=%q id=%q, want both %q", win, id, "cc-7")
	}
}

// WorkspaceAgentOptions is the snapshot used to rebuild a per-workspace agent
// in multi-workspace mode; window_per_session must survive the round-trip or
// workspace sessions silently lose isolation. Construct Agent directly so the
// test does not depend on the tmux binary.
func TestWorkspaceAgentOptionsPreservesWindowPerSession(t *testing.T) {
	a := &Agent{sessionName: "claude-work", windowPerSession: true}
	opts := a.WorkspaceAgentOptions()
	if opts["window_per_session"] != true {
		t.Fatalf("WorkspaceAgentOptions dropped window_per_session: %v", opts["window_per_session"])
	}
}
