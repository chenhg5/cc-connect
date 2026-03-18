package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// mockChannelResolver implements both Platform and ChannelNameResolver.
type mockChannelResolver struct {
	names map[string]string
}

func (m *mockChannelResolver) Name() string                                          { return "mock" }
func (m *mockChannelResolver) Start(MessageHandler) error                            { return nil }
func (m *mockChannelResolver) Reply(_ context.Context, _ any, _ string) error        { return nil }
func (m *mockChannelResolver) Send(_ context.Context, _ any, _ string) error         { return nil }
func (m *mockChannelResolver) Stop() error                                           { return nil }
func (m *mockChannelResolver) ResolveChannelName(channelID string) (string, error) {
	if name, ok := m.names[channelID]; ok {
		return name, nil
	}
	return "", fmt.Errorf("unknown channel %s", channelID)
}

func newTestEngineWithMultiWorkspace(t *testing.T, baseDir string) *Engine {
	t.Helper()
	tmpDir := t.TempDir()
	bindingPath := filepath.Join(tmpDir, "bindings.json")
	e := NewEngine("test", nil, nil, "", LangEnglish)
	e.SetMultiWorkspace(baseDir, bindingPath)
	return e
}

func TestMultiWorkspaceResolution_ConventionMatch(t *testing.T) {
	baseDir := t.TempDir()
	channelName := "my-project"
	channelID := "C001"

	// Create a directory matching the channel name
	if err := os.MkdirAll(filepath.Join(baseDir, channelName), 0o755); err != nil {
		t.Fatal(err)
	}

	e := newTestEngineWithMultiWorkspace(t, baseDir)
	p := &mockChannelResolver{names: map[string]string{channelID: channelName}}

	ws, name, err := e.resolveWorkspace(p, channelID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != channelName {
		t.Errorf("expected channel name %q, got %q", channelName, name)
	}
	// resolveWorkspace returns normalizeWorkspacePath'd result; use it for comparison
	expectedWS := normalizeWorkspacePath(filepath.Join(baseDir, channelName))
	if ws != expectedWS {
		t.Errorf("expected workspace %q, got %q", expectedWS, ws)
	}

	// Verify auto-binding was persisted
	b := e.workspaceBindings.Lookup("project:test", channelID)
	if b == nil {
		t.Fatal("expected binding to be created by convention match")
	}
	if b.Workspace != expectedWS {
		t.Errorf("binding workspace = %q, want %q", b.Workspace, expectedWS)
	}
}

func TestMultiWorkspaceResolution_NoMatch(t *testing.T) {
	baseDir := t.TempDir() // empty directory — no convention match possible

	e := newTestEngineWithMultiWorkspace(t, baseDir)
	p := &mockChannelResolver{names: map[string]string{"C002": "nonexistent-project"}}

	ws, name, err := e.resolveWorkspace(p, "C002")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ws != "" {
		t.Errorf("expected empty workspace, got %q", ws)
	}
	if name != "nonexistent-project" {
		t.Errorf("expected channel name %q, got %q", "nonexistent-project", name)
	}
}

func TestMultiWorkspaceResolution_ExistingBinding(t *testing.T) {
	baseDir := t.TempDir()
	channelID := "C003"
	channelName := "bound-channel"

	// Create the workspace directory the binding points to
	wsDir := filepath.Join(baseDir, "some-workspace")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	e := newTestEngineWithMultiWorkspace(t, baseDir)
	e.workspaceBindings.Bind("project:test", channelID, channelName, wsDir)

	// Platform that does NOT know this channel — binding should still work
	p := &mockChannelResolver{names: map[string]string{}}

	ws, name, err := e.resolveWorkspace(p, channelID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// resolveWorkspace normalizes the path
	expectedWS := normalizeWorkspacePath(wsDir)
	if ws != expectedWS {
		t.Errorf("expected workspace %q, got %q", expectedWS, ws)
	}
	if name != channelName {
		t.Errorf("expected channel name %q, got %q", channelName, name)
	}
}

func TestMultiWorkspaceResolution_MissingDirRemovesBinding(t *testing.T) {
	baseDir := t.TempDir()
	channelID := "C004"
	channelName := "stale-channel"
	missingDir := filepath.Join(baseDir, "deleted-workspace")

	e := newTestEngineWithMultiWorkspace(t, baseDir)
	e.workspaceBindings.Bind("project:test", channelID, channelName, missingDir)

	p := &mockChannelResolver{names: map[string]string{}}

	ws, name, err := e.resolveWorkspace(p, channelID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ws != "" {
		t.Errorf("expected empty workspace for missing dir, got %q", ws)
	}
	if name != channelName {
		t.Errorf("expected channel name %q, got %q", channelName, name)
	}

	// Verify binding was removed
	if b := e.workspaceBindings.Lookup("project:test", channelID); b != nil {
		t.Error("expected binding to be removed after missing directory")
	}
}

func TestExtractRepoName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"https://github.com/org/my-repo.git", "my-repo"},
		{"https://github.com/org/my-repo", "my-repo"},
		{"git@github.com:org/my-repo.git", "my-repo"},
		{"git@github.com:org/my-repo", "my-repo"},
		{"https://gitlab.com/group/subgroup/project.git", "project"},
		{"ssh://git@github.com/org/repo.git", "repo"},
		{"https://github.com/org/repo", "repo"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := extractRepoName(tt.input)
			if got != tt.want {
				t.Errorf("extractRepoName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestLooksLikeGitURL(t *testing.T) {
	valid := []string{
		"https://github.com/org/repo",
		"http://github.com/org/repo",
		"git@github.com:org/repo.git",
		"ssh://git@github.com/org/repo",
	}
	for _, s := range valid {
		if !looksLikeGitURL(s) {
			t.Errorf("looksLikeGitURL(%q) = false, want true", s)
		}
	}

	invalid := []string{
		"not-a-url",
		"ftp://files.example.com/repo",
		"/local/path/to/repo",
		"",
		"github.com/org/repo",
	}
	for _, s := range invalid {
		if looksLikeGitURL(s) {
			t.Errorf("looksLikeGitURL(%q) = true, want false", s)
		}
	}
}

func TestWorkspaceInitFlow_SlashCommandCleansUpExistingFlow(t *testing.T) {
	baseDir := t.TempDir()
	e := newTestEngineWithMultiWorkspace(t, baseDir)
	p := &mockChannelResolver{names: map[string]string{"C010": "test-channel"}}

	channelID := "C010"

	// Seed a flow in "awaiting_url" state to simulate a prior regular message
	// that triggered the init flow.
	e.initFlowsMu.Lock()
	e.initFlows[channelID] = &workspaceInitFlow{
		state:       "awaiting_url",
		channelName: "test-channel",
	}
	e.initFlowsMu.Unlock()

	msg := &Message{Content: "/workspace bind my-project"}

	consumed := e.handleWorkspaceInitFlow(p, msg, channelID, "test-channel")
	if consumed {
		t.Fatal("expected handleWorkspaceInitFlow to return false for slash command, but it returned true")
	}

	// Verify the flow was cleaned up.
	e.initFlowsMu.Lock()
	_, stillExists := e.initFlows[channelID]
	e.initFlowsMu.Unlock()
	if stillExists {
		t.Error("expected init flow to be deleted after slash command, but it still exists")
	}
}
