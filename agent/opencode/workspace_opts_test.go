package opencode

import (
	"reflect"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// TestWorkspaceAgentOptions_PreservesProjectEnv is a regression test for the
// multi-workspace env propagation gap: project-level [projects.agent.options.env]
// (HOME isolation, OPENCODE_CONFIG_CONTENT, provider API keys) must survive the
// per-workspace agent copy in core.Engine.getOrCreateWorkspaceAgent. Without
// WorkspaceAgentOptions the spawned opencode CLI falls back to the user's
// default config and fails with "UnknownError: Unexpected server error".
func TestWorkspaceAgentOptions_PreservesProjectEnv(t *testing.T) {
	cfg := []string{
		"HOME=/Users/ids/.opencode/aiapi-home",
		"OPENCODE_CONFIG_CONTENT={\"provider\":{}}",
		"DEEPSEEK_API_KEY=sk-test",
	}
	a := &Agent{
		mode:      "yolo",
		model:     "aiapi/glm-5.3",
		agentName: "build",
		configEnv: cfg,
	}

	opts := a.WorkspaceAgentOptions()

	if opts["mode"] != "yolo" {
		t.Fatalf("mode = %v, want yolo", opts["mode"])
	}
	if opts["model"] != "aiapi/glm-5.3" {
		t.Fatalf("model = %v, want aiapi/glm-5.3", opts["model"])
	}
	if opts["agent"] != "build" {
		t.Fatalf("agent = %v, want build", opts["agent"])
	}
	env, ok := opts["env"].(map[string]string)
	if !ok {
		t.Fatalf("env type = %T, want map[string]string", opts["env"])
	}
	wantEnv := map[string]string{
		"HOME":                   "/Users/ids/.opencode/aiapi-home",
		"OPENCODE_CONFIG_CONTENT": `{"provider":{}}`,
		"DEEPSEEK_API_KEY":        "sk-test",
	}
	if !reflect.DeepEqual(env, wantEnv) {
		t.Fatalf("env = %v, want %v", env, wantEnv)
	}
}

// TestWorkspaceAgentOptions_OmitsEmptyFields verifies that empty optional
// fields do not leak into the workspace opts snapshot.
func TestWorkspaceAgentOptions_OmitsEmptyFields(t *testing.T) {
	a := &Agent{
		mode: "default",
	}
	opts := a.WorkspaceAgentOptions()

	if opts["mode"] != "default" {
		t.Fatalf("mode = %v, want default", opts["mode"])
	}
	if _, ok := opts["model"]; ok {
		t.Fatalf("model unexpectedly present: %v", opts["model"])
	}
	if _, ok := opts["agent"]; ok {
		t.Fatalf("agent unexpectedly present: %v", opts["agent"])
	}
	if _, ok := opts["env"]; ok {
		t.Fatalf("env unexpectedly present: %v", opts["env"])
	}
}

// TestWorkspaceAgentOptions_RoundTripThroughParseConfigEnv ensures the env
// snapshot survives the reverse path: WorkspaceAgentOptions emits
// map[string]string, and ParseConfigEnv (used by New) must reconstruct the
// same k=v entries.
func TestWorkspaceAgentOptions_RoundTripThroughParseConfigEnv(t *testing.T) {
	cfg := []string{
		"HOME=/x",
		"A=B",
	}
	a := &Agent{mode: "yolo", configEnv: cfg}
	opts := a.WorkspaceAgentOptions()

	parsed := core.ParseConfigEnv(opts)
	want := map[string]string{"HOME": "/x", "A": "B"}
	got := make(map[string]string, len(parsed))
	for _, kv := range parsed {
		if k, v, ok := strings.Cut(kv, "="); ok {
			got[k] = v
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round-trip env = %v, want %v", got, want)
	}
}
