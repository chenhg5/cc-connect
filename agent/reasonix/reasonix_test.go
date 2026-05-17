package reasonix

import (
	"os/exec"
	"testing"

	"github.com/chenhg5/cc-connect/agent/acp"
)

func TestApplyReasonixDefaults_FillsUnsetFields(t *testing.T) {
	got := applyReasonixDefaults(map[string]any{})
	if got["command"] != "reasonix" {
		t.Errorf("command = %v, want reasonix", got["command"])
	}
	args, ok := got["args"].([]string)
	if !ok || len(args) != 1 || args[0] != "acp" {
		t.Errorf("args = %v, want [acp]", got["args"])
	}
	if got["display_name"] != "Reasonix" {
		t.Errorf("display_name = %v, want Reasonix", got["display_name"])
	}
}

func TestApplyReasonixDefaults_UserOptsWin(t *testing.T) {
	got := applyReasonixDefaults(map[string]any{
		"command":      "/opt/bin/reasonix",
		"args":         []string{"acp", "--yolo"},
		"display_name": "Reasonix (prod)",
	})
	if got["command"] != "/opt/bin/reasonix" {
		t.Errorf("command was overwritten: %v", got["command"])
	}
	args := got["args"].([]string)
	if len(args) != 2 || args[1] != "--yolo" {
		t.Errorf("args were overwritten: %v", got["args"])
	}
	if got["display_name"] != "Reasonix (prod)" {
		t.Errorf("display_name was overwritten: %v", got["display_name"])
	}
}

func TestApplyReasonixDefaults_BlankCommandGetsDefault(t *testing.T) {
	got := applyReasonixDefaults(map[string]any{"command": "   "})
	if got["command"] != "reasonix" {
		t.Errorf("command = %v, want reasonix (blank should fall through)", got["command"])
	}
}

func TestApplyReasonixDefaults_NilOpts(t *testing.T) {
	got := applyReasonixDefaults(nil)
	if got == nil || got["command"] != "reasonix" {
		t.Errorf("nil opts should yield defaults, got %v", got)
	}
}

func TestApplyReasonixDefaults_PreservesOtherAcpOptions(t *testing.T) {
	got := applyReasonixDefaults(map[string]any{
		"work_dir": "/tmp/proj",
		"mode":     "yolo",
		"env":      map[string]string{"DEEPSEEK_API_KEY": "sk_xxx"},
	})
	if got["work_dir"] != "/tmp/proj" {
		t.Errorf("work_dir lost: %v", got["work_dir"])
	}
	if got["mode"] != "yolo" {
		t.Errorf("mode lost: %v", got["mode"])
	}
	if env, ok := got["env"].(map[string]string); !ok || env["DEEPSEEK_API_KEY"] != "sk_xxx" {
		t.Errorf("env lost: %v", got["env"])
	}
}

func TestNew_ReturnsReasonixWrapper(t *testing.T) {
	if _, err := exec.LookPath("true"); err != nil {
		t.Skip("'true' not in PATH - unusual environment, skipping")
	}
	a, err := New(map[string]any{"command": "true"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if got := a.Name(); got != "reasonix" {
		t.Fatalf("Name() = %q, want reasonix", got)
	}
	wrapper, ok := a.(*Agent)
	if !ok {
		t.Fatalf("New() returned %T, want *reasonix.Agent", a)
	}
	var _ *acp.Agent = wrapper.Agent
	if got := wrapper.CLIDisplayName(); got != "Reasonix" {
		t.Fatalf("CLIDisplayName() = %q, want Reasonix", got)
	}
}
