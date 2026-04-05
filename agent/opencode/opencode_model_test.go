package opencode

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// writeFakeModelsBin writes a temporary shell script that acts as a fake CLI.
// When invoked with "models", it prints lines to stdout.
// When exitCode != 0, the script exits immediately with that code.
func writeFakeModelsBin(t *testing.T, lines []string, exitCode int) string {
	t.Helper()
	tmpDir := t.TempDir()
	name := filepath.Join(tmpDir, "fake-opencode")

	var body strings.Builder
	body.WriteString("#!/bin/sh\n")
	if exitCode != 0 {
		fmt.Fprintf(&body, "exit %d\n", exitCode)
	} else {
		body.WriteString("if [ \"$1\" = \"models\" ]; then\n")
		for _, line := range lines {
			fmt.Fprintf(&body, "printf '%%s\\n' '%s'\n", line)
		}
		body.WriteString("fi\n")
	}

	if err := os.WriteFile(name, []byte(body.String()), 0755); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestConfiguredModels_BoundaryConditions(t *testing.T) {
	a := &Agent{
		providers: []core.ProviderConfig{
			{Models: []core.ModelOption{{Name: "first"}}},
			{Models: []core.ModelOption{{Name: "second"}}},
		},
	}

	tests := []struct {
		name      string
		activeIdx int
		wantNil   bool
		wantName  string
	}{
		{name: "negative index", activeIdx: -1, wantNil: true},
		{name: "out of range", activeIdx: 2, wantNil: true},
		{name: "valid index", activeIdx: 1, wantName: "second"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a.activeIdx = tt.activeIdx
			got := a.configuredModels()
			if tt.wantNil {
				if got != nil {
					t.Fatalf("configuredModels() = %v, want nil", got)
				}
				return
			}
			if len(got) != 1 || got[0].Name != tt.wantName {
				t.Fatalf("configuredModels() = %v, want %q", got, tt.wantName)
			}
		})
	}
}

func TestNormalizeMode(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"yolo", "yolo"},
		{"YOLO", "yolo"},
		{"auto", "yolo"},
		{"AUTO", "yolo"},
		{"force", "yolo"},
		{"bypasspermissions", "yolo"},
		{"default", "default"},
		{"DEFAULT", "default"},
		{"", "default"},
		{"unknown", "default"},
		{"  yolo  ", "yolo"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeMode(tt.input)
			if got != tt.expected {
				t.Errorf("normalizeMode(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestAgent_Name(t *testing.T) {
	a := &Agent{}
	if got := a.Name(); got != "opencode" {
		t.Errorf("Name() = %q, want %q", got, "opencode")
	}
}

func TestAgent_SetModel(t *testing.T) {
	a := &Agent{}
	a.SetModel("gpt-4")
	if got := a.GetModel(); got != "gpt-4" {
		t.Errorf("GetModel() = %q, want %q", got, "gpt-4")
	}
}

func TestAgent_SetMode(t *testing.T) {
	a := &Agent{}
	a.SetMode("yolo")
	if got := a.GetMode(); got != "yolo" {
		t.Errorf("GetMode() = %q, want %q", got, "yolo")
	}
}

func TestAgent_GetActiveProvider(t *testing.T) {
	a := &Agent{
		providers: []core.ProviderConfig{
			{Name: "openai"},
			{Name: "anthropic"},
		},
		activeIdx: 1,
	}
	got := a.GetActiveProvider()
	if got == nil {
		t.Fatal("GetActiveProvider() returned nil")
	}
	if got.Name != "anthropic" {
		t.Errorf("GetActiveProvider().Name = %q, want %q", got.Name, "anthropic")
	}
}

func TestAgent_GetActiveProvider_NoActive(t *testing.T) {
	a := &Agent{
		providers: []core.ProviderConfig{
			{Name: "openai"},
		},
		activeIdx: -1,
	}
	if got := a.GetActiveProvider(); got != nil {
		t.Errorf("GetActiveProvider() = %v, want nil", got)
	}
}

func TestAgent_ListProviders(t *testing.T) {
	providers := []core.ProviderConfig{
		{Name: "openai"},
		{Name: "anthropic"},
	}
	a := &Agent{providers: providers}
	got := a.ListProviders()
	if len(got) != 2 {
		t.Errorf("ListProviders() returned %d providers, want 2", len(got))
	}
}

func TestAgent_SetActiveProvider(t *testing.T) {
	a := &Agent{
		providers: []core.ProviderConfig{
			{Name: "openai"},
			{Name: "anthropic"},
		},
	}
	if !a.SetActiveProvider("anthropic") {
		t.Error("SetActiveProvider(\"anthropic\") returned false")
	}
	if got := a.GetActiveProvider(); got == nil || got.Name != "anthropic" {
		t.Errorf("GetActiveProvider().Name = %q, want %q", got.Name, "anthropic")
	}
}

func TestAgent_SetActiveProvider_Invalid(t *testing.T) {
	a := &Agent{
		providers: []core.ProviderConfig{
			{Name: "openai"},
		},
	}
	if a.SetActiveProvider("nonexistent") {
		t.Error("SetActiveProvider(\"nonexistent\") returned true, want false")
	}
}

// ---------- dynamic discovery tests ----------

// TestAvailableModels_UsesDynamicDiscovery verifies that AvailableModels returns
// the model list produced by `opencode models` when it succeeds.
func TestAvailableModels_UsesDynamicDiscovery(t *testing.T) {
	bin := writeFakeModelsBin(t, []string{"anthropic/claude-3-5-sonnet", "openai/gpt-4o"}, 0)
	a := &Agent{cmd: bin, activeIdx: -1}

	got := a.AvailableModels(context.Background())
	if len(got) != 2 {
		t.Fatalf("AvailableModels() = %v (len %d), want 2 models", got, len(got))
	}
	// results must be sorted
	if got[0].Name != "anthropic/claude-3-5-sonnet" {
		t.Errorf("got[0].Name = %q, want %q", got[0].Name, "anthropic/claude-3-5-sonnet")
	}
	if got[1].Name != "openai/gpt-4o" {
		t.Errorf("got[1].Name = %q, want %q", got[1].Name, "openai/gpt-4o")
	}
}

// TestAvailableModels_DynamicTakesPriorityOverConfigured verifies discovery beats
// provider-configured models.
func TestAvailableModels_DynamicTakesPriorityOverConfigured(t *testing.T) {
	bin := writeFakeModelsBin(t, []string{"discovered/model"}, 0)
	a := &Agent{
		cmd: bin,
		providers: []core.ProviderConfig{
			{Models: []core.ModelOption{{Name: "configured/model"}}},
		},
		activeIdx: 0,
	}

	got := a.AvailableModels(context.Background())
	if len(got) != 1 || got[0].Name != "discovered/model" {
		t.Errorf("AvailableModels() = %v, want [discovered/model]", got)
	}
}

// TestAvailableModels_FallsBackToConfiguredOnDiscoveryFail verifies fallback to
// provider-configured models when `opencode models` exits non-zero.
func TestAvailableModels_FallsBackToConfiguredOnDiscoveryFail(t *testing.T) {
	bin := writeFakeModelsBin(t, nil, 1) // exits with error
	a := &Agent{
		cmd: bin,
		providers: []core.ProviderConfig{
			{Models: []core.ModelOption{{Name: "configured-model"}}},
		},
		activeIdx: 0,
	}

	got := a.AvailableModels(context.Background())
	if len(got) != 1 || got[0].Name != "configured-model" {
		t.Errorf("AvailableModels() = %v, want [configured-model]", got)
	}
}

// TestAvailableModels_FallsBackToBuiltinWhenBothUnavailable verifies the final
// fallback to the hardcoded built-in model list.
func TestAvailableModels_FallsBackToBuiltinWhenBothUnavailable(t *testing.T) {
	bin := writeFakeModelsBin(t, nil, 1)
	a := &Agent{cmd: bin, activeIdx: -1}

	got := a.AvailableModels(context.Background())
	if len(got) == 0 {
		t.Fatal("AvailableModels() returned empty list, want built-in fallback")
	}
	found := false
	for _, m := range got {
		if m.Name == "anthropic/claude-sonnet-4-20250514" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("AvailableModels() built-in fallback missing expected model; got: %v", got)
	}
}

// TestAvailableModels_DeduplicatesDiscoveredModels verifies that duplicate model
// names from the CLI output appear only once.
func TestAvailableModels_DeduplicatesDiscoveredModels(t *testing.T) {
	bin := writeFakeModelsBin(t, []string{"openai/gpt-4o", "openai/gpt-4o", "anthropic/claude"}, 0)
	a := &Agent{cmd: bin, activeIdx: -1}

	got := a.AvailableModels(context.Background())
	if len(got) != 2 {
		t.Fatalf("AvailableModels() = %v (len %d), want 2 after dedup", got, len(got))
	}
}

// TestAvailableModels_SortsDiscoveredModels verifies lexicographic sort order.
func TestAvailableModels_SortsDiscoveredModels(t *testing.T) {
	bin := writeFakeModelsBin(t, []string{"z-model", "a-model", "m-model"}, 0)
	a := &Agent{cmd: bin, activeIdx: -1}

	got := a.AvailableModels(context.Background())
	if len(got) != 3 {
		t.Fatalf("AvailableModels() = %v, want 3 models", got)
	}
	names := make([]string, len(got))
	for i, m := range got {
		names[i] = m.Name
	}
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	for i := range names {
		if names[i] != sorted[i] {
			t.Errorf("AvailableModels() not sorted: got %v", names)
			break
		}
	}
}

// TestAvailableModels_EmptyDiscoveryOutputFallsBackToConfigured verifies that an
// exit-0 but empty-output binary still triggers the fallback chain.
func TestAvailableModels_EmptyDiscoveryOutputFallsBackToConfigured(t *testing.T) {
	bin := writeFakeModelsBin(t, []string{}, 0) // exits 0 but no output
	a := &Agent{
		cmd: bin,
		providers: []core.ProviderConfig{
			{Models: []core.ModelOption{{Name: "fallback-model"}}},
		},
		activeIdx: 0,
	}

	got := a.AvailableModels(context.Background())
	if len(got) != 1 || got[0].Name != "fallback-model" {
		t.Errorf("AvailableModels() empty discovery = %v, want [fallback-model]", got)
	}
}

// TestAvailableModels_CustomCmdUsedForDiscovery verifies that a.cmd (not the
// literal string "opencode") is used when running the models sub-command.
func TestAvailableModels_CustomCmdUsedForDiscovery(t *testing.T) {
	tmpDir := t.TempDir()
	customBin := filepath.Join(tmpDir, "my-ai-cli")
	script := "#!/bin/sh\nif [ \"$1\" = \"models\" ]; then\nprintf '%s\\n' 'custom/model-a'\nfi\n"
	if err := os.WriteFile(customBin, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	a := &Agent{cmd: customBin, activeIdx: -1}
	got := a.AvailableModels(context.Background())
	if len(got) != 1 || got[0].Name != "custom/model-a" {
		t.Errorf("AvailableModels() with custom cmd = %v, want [custom/model-a]", got)
	}
}

// ---------- DeleteSession tests ----------

// writeFakeDeleteBin writes a temporary shell script that acts as a fake opencode CLI.
// When invoked with "session delete <id>", it either succeeds (exitCode=0) or fails.
// If wantID is non-empty the script validates the session ID matches.
func writeFakeDeleteBin(t *testing.T, wantID string, exitCode int, stderr string) string {
	t.Helper()
	tmpDir := t.TempDir()
	name := filepath.Join(tmpDir, "fake-opencode")

	var body strings.Builder
	body.WriteString("#!/bin/sh\n")
	body.WriteString("if [ \"$1\" = \"session\" ] && [ \"$2\" = \"delete\" ]; then\n")
	if wantID != "" {
		fmt.Fprintf(&body, "  if [ \"$3\" != \"%s\" ]; then\n", wantID)
		fmt.Fprintf(&body, "    printf 'unexpected session id: %%s\\n' \"$3\" >&2\n")
		body.WriteString("    exit 1\n")
		body.WriteString("  fi\n")
	}
	if stderr != "" {
		fmt.Fprintf(&body, "  printf '%s\\n' >&2\n", stderr)
	}
	fmt.Fprintf(&body, "  exit %d\n", exitCode)
	body.WriteString("fi\n")
	body.WriteString("exit 0\n")

	if err := os.WriteFile(name, []byte(body.String()), 0755); err != nil {
		t.Fatal(err)
	}
	return name
}

// TestDeleteSession_Success verifies that DeleteSession calls
// `opencode session delete <id>` and returns nil on success.
func TestDeleteSession_Success(t *testing.T) {
	sessionID := "ses_abc123"
	bin := writeFakeDeleteBin(t, sessionID, 0, "")
	a := &Agent{cmd: bin, workDir: t.TempDir()}

	if err := a.DeleteSession(context.Background(), sessionID); err != nil {
		t.Fatalf("DeleteSession() unexpected error: %v", err)
	}
}

// TestDeleteSession_CLIError verifies that DeleteSession propagates CLI failures.
func TestDeleteSession_CLIError(t *testing.T) {
	bin := writeFakeDeleteBin(t, "", 1, "session not found")
	a := &Agent{cmd: bin, workDir: t.TempDir()}

	err := a.DeleteSession(context.Background(), "ses_missing")
	if err == nil {
		t.Fatal("DeleteSession() expected error, got nil")
	}
	if !strings.Contains(err.Error(), "ses_missing") {
		t.Errorf("error %q should mention the session ID", err.Error())
	}
}

// TestDeleteSession_ImplementsInterface is a compile-time check that Agent
// satisfies core.SessionDeleter.
var _ core.SessionDeleter = (*Agent)(nil)

// ---------- interface / compile-time checks ----------

// verify Agent implements core.Agent
var _ core.Agent = (*Agent)(nil)
