package copilot

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestNew_MissingBinary(t *testing.T) {
	_, err := New(map[string]any{
		"cli_path": "copilot-nonexistent-binary-xyz",
	})
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
}

func TestAgent_Name(t *testing.T) {
	a := &Agent{}
	if a.Name() != "copilot" {
		t.Fatalf("Name() = %q, want copilot", a.Name())
	}
}

func TestNormalizeMode(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"default", "default"},
		{"bypassPermissions", "bypassPermissions"},
		{"bypass-permissions", "bypassPermissions"},
		{"bypass_permissions", "bypassPermissions"},
		{"yolo", "bypassPermissions"},
		{"YOLO", "bypassPermissions"},
		{"", "default"},
		{"unknown", "default"},
	}
	for _, tt := range tests {
		got := normalizeMode(tt.input)
		if got != tt.want {
			t.Errorf("normalizeMode(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestAgent_SetGetModel(t *testing.T) {
	a := &Agent{}
	a.SetModel("gpt-4o")
	if got := a.GetModel(); got != "gpt-4o" {
		t.Fatalf("GetModel() = %q, want gpt-4o", got)
	}
}

func TestAgent_SetGetMode(t *testing.T) {
	a := &Agent{}
	a.SetMode("yolo")
	if got := a.GetMode(); got != "bypassPermissions" {
		t.Fatalf("GetMode() = %q, want bypassPermissions", got)
	}
	a.SetMode("default")
	if got := a.GetMode(); got != "default" {
		t.Fatalf("GetMode() = %q, want default", got)
	}
}

func TestAgent_SetGetWorkDir(t *testing.T) {
	a := &Agent{}
	a.SetWorkDir("/tmp/test")
	if got := a.GetWorkDir(); got != "/tmp/test" {
		t.Fatalf("GetWorkDir() = %q, want /tmp/test", got)
	}
}

func TestAgent_PermissionModes(t *testing.T) {
	a := &Agent{}
	modes := a.PermissionModes()
	if len(modes) != 2 {
		t.Fatalf("PermissionModes() len = %d, want 2", len(modes))
	}
	if modes[0].Key != "default" {
		t.Errorf("modes[0].Key = %q, want default", modes[0].Key)
	}
	if modes[1].Key != "bypassPermissions" {
		t.Errorf("modes[1].Key = %q, want bypassPermissions", modes[1].Key)
	}
}

func TestAgent_DeleteSession(t *testing.T) {
	a := &Agent{}
	err := a.DeleteSession(context.Background(), "some-session-id")
	if err != nil {
		t.Fatalf("DeleteSession() = %v, want nil", err)
	}
}

func TestAgent_GetSessionHistory(t *testing.T) {
	a := &Agent{}
	entries, err := a.GetSessionHistory(context.Background(), "some-session-id", 10)
	if err != nil {
		t.Fatalf("GetSessionHistory() error = %v", err)
	}
	if entries != nil {
		t.Fatalf("GetSessionHistory() = %v, want nil", entries)
	}
}

func TestAgent_CompressCommand(t *testing.T) {
	a := &Agent{}
	if got := a.CompressCommand(); got != "" {
		t.Fatalf("CompressCommand() = %q, want empty", got)
	}
}

func TestAgent_AvailableModels(t *testing.T) {
	a := &Agent{}
	models := a.AvailableModels(context.Background())
	if len(models) == 0 {
		t.Fatal("AvailableModels() returned empty")
	}
}

func TestAgent_ProviderSwitcher(t *testing.T) {
	a := &Agent{activeIdx: -1}
	if p := a.GetActiveProvider(); p != nil {
		t.Fatalf("GetActiveProvider() = %v, want nil", p)
	}
	if got := a.SetActiveProvider("nonexistent"); got {
		t.Fatal("SetActiveProvider(nonexistent) = true, want false")
	}
}

func TestAgent_ProviderConfigLocked(t *testing.T) {
	a := &Agent{activeIdx: -1}
	a.SetProviders([]core.ProviderConfig{
		{
			Name:             "byok",
			APIKey:           "sk-test",
			BaseURL:          "https://provider.example/v1",
			Model:            "gpt-5.2",
			CodexWireAPI:     "responses",
			CodexHTTPHeaders: map[string]string{"X-Provider": "test"},
			Env: map[string]string{
				"COPILOT_PROVIDER_TYPE":         "anthropic",
				"COPILOT_PROVIDER_WIRE_MODEL":   "deployment-1",
				"COPILOT_PROVIDER_BEARER_TOKEN": "bearer-token",
			},
		},
	})
	if !a.SetActiveProvider("byok") {
		t.Fatal("SetActiveProvider(byok) = false, want true")
	}

	provider := a.providerConfigLocked()
	if provider == nil {
		t.Fatal("providerConfigLocked() = nil")
	}
	if provider.Type != "anthropic" {
		t.Fatalf("Type = %q, want anthropic", provider.Type)
	}
	if provider.BaseURL != "https://provider.example/v1" {
		t.Fatalf("BaseURL = %q", provider.BaseURL)
	}
	if provider.APIKey != "sk-test" {
		t.Fatalf("APIKey = %q", provider.APIKey)
	}
	if provider.ModelID != "gpt-5.2" {
		t.Fatalf("ModelID = %q", provider.ModelID)
	}
	if provider.WireAPI != "responses" {
		t.Fatalf("WireAPI = %q", provider.WireAPI)
	}
	if provider.WireModel != "deployment-1" {
		t.Fatalf("WireModel = %q", provider.WireModel)
	}
	if provider.BearerToken != "bearer-token" {
		t.Fatalf("BearerToken = %q", provider.BearerToken)
	}
	if provider.Headers["X-Provider"] != "test" {
		t.Fatalf("Headers = %v", provider.Headers)
	}
}

func TestAgent_ProviderEnvLocked(t *testing.T) {
	a := &Agent{activeIdx: -1}
	a.SetProviders([]core.ProviderConfig{
		{
			Name:         "byok",
			APIKey:       "sk-test",
			BaseURL:      "https://provider.example/v1",
			Model:        "gpt-5.2",
			CodexWireAPI: "responses",
			Env:          map[string]string{"EXTRA": "1"},
		},
	})
	if !a.SetActiveProvider("byok") {
		t.Fatal("SetActiveProvider(byok) = false, want true")
	}
	env := envMap(a.providerEnvLocked())
	if env["COPILOT_PROVIDER_BASE_URL"] != "https://provider.example/v1" {
		t.Fatalf("COPILOT_PROVIDER_BASE_URL = %q", env["COPILOT_PROVIDER_BASE_URL"])
	}
	if env["COPILOT_PROVIDER_API_KEY"] != "sk-test" {
		t.Fatalf("COPILOT_PROVIDER_API_KEY = %q", env["COPILOT_PROVIDER_API_KEY"])
	}
	if env["COPILOT_MODEL"] != "gpt-5.2" {
		t.Fatalf("COPILOT_MODEL = %q", env["COPILOT_MODEL"])
	}
	if env["COPILOT_PROVIDER_WIRE_API"] != "responses" {
		t.Fatalf("COPILOT_PROVIDER_WIRE_API = %q", env["COPILOT_PROVIDER_WIRE_API"])
	}
	if env["EXTRA"] != "1" {
		t.Fatalf("EXTRA = %q", env["EXTRA"])
	}
}

func TestAgent_ListSessions(t *testing.T) {
	a := &Agent{}
	sessions, err := a.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if sessions != nil {
		t.Fatalf("ListSessions() = %v, want nil", sessions)
	}
}

func TestAgent_CLIBinaryName(t *testing.T) {
	a := &Agent{cmd: "copilot"}
	if got := a.CLIBinaryName(); got != "copilot" {
		t.Fatalf("CLIBinaryName() = %q, want copilot", got)
	}
}

func TestAgent_CLIDisplayName(t *testing.T) {
	a := &Agent{}
	if got := a.CLIDisplayName(); got != "GitHub Copilot" {
		t.Fatalf("CLIDisplayName() = %q, want 'GitHub Copilot'", got)
	}
}

func TestAgent_WorkspaceAgentOptions(t *testing.T) {
	a := &Agent{mode: "bypassPermissions", model: "gpt-4o", cmd: "copilot"}
	opts := a.WorkspaceAgentOptions()
	if opts["mode"] != "bypassPermissions" {
		t.Fatalf("mode = %v, want bypassPermissions", opts["mode"])
	}
	if opts["model"] != "gpt-4o" {
		t.Fatalf("model = %v, want gpt-4o", opts["model"])
	}
	// cmd is "copilot" (default) so cmd should not be set
	if _, ok := opts["cmd"]; ok {
		t.Fatal("cmd should not be set for default binary name")
	}

	// Custom CLI path should be included
	a2 := &Agent{mode: "default", cmd: "/custom/copilot"}
	opts2 := a2.WorkspaceAgentOptions()
	if opts2["cmd"] != "/custom/copilot" {
		t.Fatalf("cmd = %v, want /custom/copilot", opts2["cmd"])
	}
}

// ---------------------------------------------------------------------------
// ListSessions / DeleteSession tests using a fake process helper
// ---------------------------------------------------------------------------

// TestAgent_ListSessions_NoBinary verifies graceful nil return when binary is missing.
func TestAgent_ListSessions_NoBinary(t *testing.T) {
	a := &Agent{cmd: "copilot-nonexistent-xyz", workDir: "."}
	sessions, err := a.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions with missing binary returned error: %v", err)
	}
	if sessions != nil {
		t.Fatalf("expected nil sessions, got %v", sessions)
	}
}

// TestAgent_DeleteSession_NoBinary verifies graceful nil return when binary is missing.
func TestAgent_DeleteSession_NoBinary(t *testing.T) {
	a := &Agent{cmd: "copilot-nonexistent-xyz", workDir: "."}
	if err := a.DeleteSession(context.Background(), "some-session-id"); err != nil {
		t.Fatalf("DeleteSession with missing binary returned error: %v", err)
	}
}

// TestAgent_DeleteSession_EmptyID verifies empty session ID returns nil immediately.
func TestAgent_DeleteSession_EmptyID(t *testing.T) {
	a := &Agent{cmd: "copilot-nonexistent-xyz", workDir: "."}
	if err := a.DeleteSession(context.Background(), ""); err != nil {
		t.Fatalf("DeleteSession with empty ID returned error: %v", err)
	}
}

// TestAgent_ListSessions_RPC tests ListSessions by using a real fake-process
// approach: we re-exec the test binary as a "mock copilot" that speaks the
// JSON-RPC protocol over stdio and responds to ping + session.list.
//
// The fake process is selected by the CC_MOCK_COPILOT_MODE env var.
func TestAgent_ListSessions_RPC(t *testing.T) {
	bin, err := os.Executable()
	if err != nil {
		t.Skip("cannot get test executable path")
	}

	// Point agent at the test binary itself acting as a mock copilot
	a := &Agent{
		cmd:  bin,
		workDir: ".",
	}

	t.Setenv("CC_MOCK_COPILOT_MODE", "list_sessions")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sessions, err := a.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions returned error: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d: %v", len(sessions), sessions)
	}
	if sessions[0].ID != "sess-001" {
		t.Fatalf("sessions[0].ID = %q, want sess-001", sessions[0].ID)
	}
	if sessions[1].ID != "sess-002" {
		t.Fatalf("sessions[1].ID = %q, want sess-002", sessions[1].ID)
	}
}

// TestAgent_DeleteSession_RPC tests DeleteSession using the same mock process approach.
func TestAgent_DeleteSession_RPC(t *testing.T) {
	bin, err := os.Executable()
	if err != nil {
		t.Skip("cannot get test executable path")
	}

	a := &Agent{
		cmd:  bin,
		workDir: ".",
	}

	t.Setenv("CC_MOCK_COPILOT_MODE", "delete_session")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := a.DeleteSession(ctx, "sess-001"); err != nil {
		t.Fatalf("DeleteSession returned error: %v", err)
	}
}

func envMap(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, entry := range env {
		for i := range entry {
			if entry[i] == '=' {
				out[entry[:i]] = entry[i+1:]
				break
			}
		}
	}
	return out
}
