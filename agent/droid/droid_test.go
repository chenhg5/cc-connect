package droid

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestNormalizeAuto(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"low", "low"},
		{"LOW", "low"},
		{"medium", "medium"},
		{"high", "high"},
		{"invalid", ""},
	}

	for _, tt := range tests {
		if got := normalizeAuto(tt.in); got != tt.want {
			t.Fatalf("normalizeAuto(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestNormalizeReasoningEffort(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"none", "none"},
		{"off", "off"},
		{"medium", "medium"},
		{"xhigh", "xhigh"},
		{"MAX", "max"},
		{"invalid", ""},
	}

	for _, tt := range tests {
		if got := normalizeReasoningEffort(tt.in); got != tt.want {
			t.Fatalf("normalizeReasoningEffort(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSetMode(t *testing.T) {
	a := &Agent{}

	a.SetMode("default")
	if got := a.GetMode(); got != "default" {
		t.Fatalf("GetMode() = %q, want default", got)
	}

	a.SetMode("low")
	if got := a.GetMode(); got != "low" {
		t.Fatalf("GetMode() = %q, want low", got)
	}

	a.SetMode("medium")
	if got := a.GetMode(); got != "medium" {
		t.Fatalf("GetMode() = %q, want medium", got)
	}

	a.SetMode("high")
	if got := a.GetMode(); got != "high" {
		t.Fatalf("GetMode() = %q, want high", got)
	}

	a.SetMode("yolo")
	if got := a.GetMode(); got != "yolo" {
		t.Fatalf("GetMode() = %q, want yolo", got)
	}
}

func TestProvidersAndModelSelection(t *testing.T) {
	a := &Agent{model: "gpt-5.4", activeIdx: -1}
	providers := []core.ProviderConfig{
		{Name: "default", Model: "custom:provider-model", APIKey: "k1", BaseURL: "https://example.invalid", Env: map[string]string{"FOO": "bar"}},
	}

	a.SetProviders(providers)
	if !a.SetActiveProvider("default") {
		t.Fatalf("SetActiveProvider(default) = false, want true")
	}

	if got := a.GetModel(); got != "custom:provider-model" {
		t.Fatalf("GetModel() = %q, want custom:provider-model", got)
	}

	env := a.providerEnvLocked()
	want := []string{"OPENAI_API_KEY=k1", "OPENAI_BASE_URL=https://example.invalid", "FOO=bar"}
	if !reflect.DeepEqual(env, want) {
		t.Fatalf("providerEnvLocked() = %#v, want %#v", env, want)
	}

	a.SetActiveProvider("")
	if got := a.GetModel(); got != "gpt-5.4" {
		t.Fatalf("GetModel() after clear = %q, want gpt-5.4", got)
	}
}

func TestCompressCommand(t *testing.T) {
	a := &Agent{}
	if got := a.CompressCommand(); got != "/compress" {
		t.Fatalf("CompressCommand() = %q, want /compress", got)
	}
}

func TestNew_CommandAndWorkspaceOptions(t *testing.T) {
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []any{bin + " --wrapper", []string{bin, "--wrapper"}, []any{bin, "--wrapper"}} {
		original, err := New(map[string]any{
			"cmd": command, "work_dir": t.TempDir(), "model": "custom:model",
			"reasoning_effort": "high", "mode": "medium",
			"env": map[string]any{"PROJECT_KEY": "project=value"},
		})
		if err != nil {
			t.Fatal(err)
		}
		a := original.(*Agent)
		a.SetSessionEnv([]string{"CC_SESSION_KEY=original"})
		opts := a.WorkspaceAgentOptions()
		if _, ok := opts["work_dir"]; ok {
			t.Fatal("snapshot must not override the target workspace")
		}
		opts["work_dir"] = t.TempDir()
		cloned, err := New(opts)
		if err != nil {
			t.Fatal(err)
		}
		b := cloned.(*Agent)
		if !reflect.DeepEqual(b.cliExtraArgs, []string{"--wrapper"}) || b.cmd != bin {
			t.Fatalf("clone command = %q %v", b.cmd, b.cliExtraArgs)
		}
		if b.GetMode() != "medium" || b.GetModel() != "custom:model" || b.GetReasoningEffort() != "high" {
			t.Fatalf("clone lost mode/model/reasoning: %q %q %q", b.GetMode(), b.GetModel(), b.GetReasoningEffort())
		}
		if !reflect.DeepEqual(b.configEnv, []string{"PROJECT_KEY=project=value"}) || len(b.sessionEnv) != 0 {
			t.Fatalf("clone env = %v, session env = %v", b.configEnv, b.sessionEnv)
		}
		opts["cmd"].([]string)[0] = "mutated"
		opts["env"].(map[string]string)["PROJECT_KEY"] = "mutated"
		if a.cmd != bin || !reflect.DeepEqual(a.configEnv, []string{"PROJECT_KEY=project=value"}) {
			t.Fatal("snapshot mutation changed original agent")
		}
	}
}

func TestNew_ModePrecedence(t *testing.T) {
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"default", "low", "medium", "high", "yolo"} {
		agent, err := New(map[string]any{"cmd": []string{bin}, "mode": mode, "auto": "high", "skip_permissions_unsafe": true})
		if err != nil {
			t.Fatal(err)
		}
		if got := agent.(*Agent).GetMode(); got != mode {
			t.Fatalf("mode = %q, want %q", got, mode)
		}
	}
	agent, err := New(map[string]any{"cmd": []string{bin}, "auto": "high", "skip_permissions_unsafe": true})
	if err != nil {
		t.Fatal(err)
	}
	if a := agent.(*Agent); a.auto != "" || a.GetMode() != "yolo" {
		t.Fatal("unsafe mode must not also pass the incompatible auto flag")
	}
}

func TestWorkspacePathsFollowWorkDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a := &Agent{workDir: t.TempDir()}
	dir := t.TempDir()
	a.SetWorkDir(dir)
	if got := a.ProjectMemoryFile(); got != filepath.Join(dir, "AGENTS.md") {
		t.Fatalf("memory path = %q", got)
	}
	if got := a.SkillDirs()[0]; got != filepath.Join(dir, ".factory", "skills") {
		t.Fatalf("skill path = %q", got)
	}
}
