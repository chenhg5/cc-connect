package devin

import (
	"os/exec"
	"testing"

	"github.com/chenhg5/cc-connect/agent/acp"
)

// TestApplyDevinDefaults_FillsUnsetFields verifies the three Devin-
// specific defaults are applied when the user provides a minimal
// [projects.agent.options] block. This is the path most users hit —
// config.example.toml shows a bare `type = "devin"` section and we
// want that to just work.
func TestApplyDevinDefaults_FillsUnsetFields(t *testing.T) {
	got := applyDevinDefaults(map[string]any{})
	if got["command"] != "devin" {
		t.Errorf("command = %v, want devin", got["command"])
	}
	args, ok := got["args"].([]string)
	if !ok || len(args) != 1 || args[0] != "acp" {
		t.Errorf("args = %v, want [acp]", got["args"])
	}
	if got["display_name"] != "Devin" {
		t.Errorf("display_name = %v, want Devin", got["display_name"])
	}
}

// TestApplyDevinDefaults_UserOptsWin ensures we never stomp on
// explicit user config. Common reason to override `command`: absolute
// path for launchd / systemd deployments where ~/.local/bin isn't on
// $PATH. Common reason to override `display_name`: running multiple
// Devin instances against different Windsurf workspaces.
func TestApplyDevinDefaults_UserOptsWin(t *testing.T) {
	got := applyDevinDefaults(map[string]any{
		"command":      "/usr/local/bin/devin",
		"args":         []string{"acp", "--verbose"},
		"display_name": "Devin (staging)",
	})
	if got["command"] != "/usr/local/bin/devin" {
		t.Errorf("command was overwritten: %v", got["command"])
	}
	args := got["args"].([]string)
	if len(args) != 2 || args[1] != "--verbose" {
		t.Errorf("args were overwritten: %v", got["args"])
	}
	if got["display_name"] != "Devin (staging)" {
		t.Errorf("display_name was overwritten: %v", got["display_name"])
	}
}

// TestApplyDevinDefaults_BlankCommandGetsDefault covers a subtle TOML
// quirk: `command = ""` (explicit blank) should be treated as "use
// the default" rather than surfacing a cryptic "command is required"
// error. Matches how the rest of cc-connect treats whitespace-only
// string options.
func TestApplyDevinDefaults_BlankCommandGetsDefault(t *testing.T) {
	got := applyDevinDefaults(map[string]any{"command": "   "})
	if got["command"] != "devin" {
		t.Errorf("command = %v, want devin (blank should fall through)", got["command"])
	}
}

// TestApplyDevinDefaults_NilOpts guards against nil-map panics at
// registry level. core.CreateAgent may in principle pass nil if a
// project entry has no [projects.agent.options] table at all.
func TestApplyDevinDefaults_NilOpts(t *testing.T) {
	got := applyDevinDefaults(nil)
	if got == nil || got["command"] != "devin" {
		t.Errorf("nil opts should yield defaults, got %v", got)
	}
}

// TestApplyDevinDefaults_PreservesOtherAcpOptions ensures pass-through
// of ACP-level knobs (mode, auth_method, env, work_dir) that the
// wrapper must not touch. These are handled by agent/acp.
func TestApplyDevinDefaults_PreservesOtherAcpOptions(t *testing.T) {
	got := applyDevinDefaults(map[string]any{
		"work_dir":    "/tmp/proj",
		"mode":        "plan",
		"auth_method": "windsurf-api-key",
		"env":         map[string]string{"WINDSURF_API_KEY": "wk_xxx"},
	})
	if got["work_dir"] != "/tmp/proj" {
		t.Errorf("work_dir lost: %v", got["work_dir"])
	}
	if got["mode"] != "plan" {
		t.Errorf("mode lost: %v", got["mode"])
	}
	if got["auth_method"] != "windsurf-api-key" {
		t.Errorf("auth_method lost: %v", got["auth_method"])
	}
	if env, ok := got["env"].(map[string]string); !ok || env["WINDSURF_API_KEY"] != "wk_xxx" {
		t.Errorf("env lost: %v", got["env"])
	}
}

// TestNew_ReturnsDevinWrapper verifies the full New() → acp.New()
// path produces a *devin.Agent that shadows the embedded *acp.Agent's
// Name(). Uses `command: "true"` (a POSIX builtin guaranteed to be in
// PATH on both Linux and macOS, CI included) to bypass agent/acp's
// exec.LookPath check without requiring a real `devin` binary.
func TestNew_ReturnsDevinWrapper(t *testing.T) {
	if _, err := exec.LookPath("true"); err != nil {
		t.Skip("'true' not in PATH — unusual environment, skipping")
	}
	a, err := New(map[string]any{"command": "true"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if got := a.Name(); got != "devin" {
		t.Fatalf("Name() = %q, want devin (must shadow embedded acp.Name)", got)
	}
	wrapper, ok := a.(*Agent)
	if !ok {
		t.Fatalf("New() returned %T, want *devin.Agent", a)
	}
	// Sanity: the embedded acp.Agent is the backing implementation.
	var _ *acp.Agent = wrapper.Agent
	// Display name still reflects the Devin default even when command
	// was overridden to "true".
	if got := wrapper.CLIDisplayName(); got != "Devin" {
		t.Fatalf("CLIDisplayName() = %q, want Devin", got)
	}
}

// TestNew_DisplayNameOverride locks in that a user-provided
// display_name reaches the embedded acp.Agent unchanged (relevant for
// multi-project setups where the bot's `/status` output needs to
// distinguish several concurrent Devin sessions).
func TestNew_DisplayNameOverride(t *testing.T) {
	if _, err := exec.LookPath("true"); err != nil {
		t.Skip("'true' not in PATH — skipping")
	}
	a, err := New(map[string]any{
		"command":      "true",
		"display_name": "Devin (prod)",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if got := a.(*Agent).CLIDisplayName(); got != "Devin (prod)" {
		t.Fatalf("CLIDisplayName() = %q, want %q", got, "Devin (prod)")
	}
}
