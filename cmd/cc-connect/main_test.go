package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"
)

type stubMainAgent struct {
	workDir string
}

func (a *stubMainAgent) Name() string { return "stub-main" }

func (a *stubMainAgent) StartSession(_ context.Context, _ string) (core.AgentSession, error) {
	return &stubMainAgentSession{}, nil
}

func (a *stubMainAgent) ListSessions(_ context.Context) ([]core.AgentSessionInfo, error) {
	return nil, nil
}

func (a *stubMainAgent) Stop() error { return nil }

func (a *stubMainAgent) SetWorkDir(dir string) {
	a.workDir = dir
}

func (a *stubMainAgent) GetWorkDir() string {
	return a.workDir
}

type stubMainAgentSession struct{}

func (s *stubMainAgentSession) Send(string, []core.ImageAttachment, []core.FileAttachment) error {
	return nil
}
func (s *stubMainAgentSession) RespondPermission(string, core.PermissionResult) error { return nil }
func (s *stubMainAgentSession) Events() <-chan core.Event                             { return nil }
func (s *stubMainAgentSession) Close() error                                          { return nil }
func (s *stubMainAgentSession) CurrentSessionID() string                              { return "" }
func (s *stubMainAgentSession) Alive() bool                                           { return true }

func TestProjectStatePath(t *testing.T) {
	dataDir := t.TempDir()
	got := projectStatePath(dataDir, "my/project:one")
	want := filepath.Join(dataDir, "projects", "my_project_one.state.json")
	if got != want {
		t.Fatalf("projectStatePath() = %q, want %q", got, want)
	}
}

func TestApplyProjectStateOverride(t *testing.T) {
	baseDir := t.TempDir()
	overrideDir := filepath.Join(t.TempDir(), "override")
	if err := os.Mkdir(overrideDir, 0o755); err != nil {
		t.Fatalf("mkdir override dir: %v", err)
	}

	store := core.NewProjectStateStore(filepath.Join(t.TempDir(), "projects", "demo.state.json"))
	store.SetWorkDirOverride(overrideDir)

	agent := &stubMainAgent{workDir: baseDir}
	got := applyProjectStateOverride("demo", agent, baseDir, store)

	if got != overrideDir {
		t.Fatalf("applyProjectStateOverride() = %q, want %q", got, overrideDir)
	}
	if agent.workDir != overrideDir {
		t.Fatalf("agent workDir = %q, want %q", agent.workDir, overrideDir)
	}
}

type stubProviderRefreshAgent struct {
	stubMainAgent
	providers  []core.ProviderConfig
	activeName string
	calls      []string
	activateOK bool
}

func (a *stubProviderRefreshAgent) SetProviders(providers []core.ProviderConfig) {
	a.providers = append([]core.ProviderConfig(nil), providers...)
	a.calls = append(a.calls, "set_providers")
}

func (a *stubProviderRefreshAgent) SetActiveProvider(name string) bool {
	if !a.activateOK {
		a.calls = append(a.calls, "set_active_provider_failed")
		return false
	}
	a.activeName = name
	a.calls = append(a.calls, "set_active_provider")
	return true
}

func (a *stubProviderRefreshAgent) GetActiveProvider() *core.ProviderConfig {
	for i := range a.providers {
		if a.providers[i].Name == a.activeName {
			return &a.providers[i]
		}
	}
	return nil
}

func (a *stubProviderRefreshAgent) ListProviders() []core.ProviderConfig {
	providers := make([]core.ProviderConfig, len(a.providers))
	copy(providers, a.providers)
	return providers
}

func (a *stubProviderRefreshAgent) StartInitialModelRefresh() {
	a.calls = append(a.calls, "start_initial_model_refresh")
}

func TestBuildAgentOptionsInjectsProjectScope(t *testing.T) {
	proj := config.ProjectConfig{
		Name: "demo-project",
		Agent: config.AgentConfig{
			Options: map[string]any{
				"work_dir": "/tmp/work",
				"model":    "gpt-test",
			},
		},
	}

	got := buildAgentOptions("/tmp/data", proj)

	if got["cc_data_dir"] != "/tmp/data" {
		t.Fatalf("cc_data_dir = %v, want %q", got["cc_data_dir"], "/tmp/data")
	}
	if got["cc_project"] != "demo-project" {
		t.Fatalf("cc_project = %v, want %q", got["cc_project"], "demo-project")
	}
	if got["work_dir"] != "/tmp/work" || got["model"] != "gpt-test" {
		t.Fatalf("buildAgentOptions() lost existing options: %v", got)
	}
	if _, exists := proj.Agent.Options["cc_data_dir"]; exists {
		t.Fatalf("project agent options mutated: %v", proj.Agent.Options)
	}
}

func TestWireAgentProvidersStartsRefreshAfterProviderWiring(t *testing.T) {
	agent := &stubProviderRefreshAgent{activateOK: true}
	proj := config.ProjectConfig{
		Agent: config.AgentConfig{
			Options: map[string]any{"provider": "provider-b"},
			Providers: []config.ProviderConfig{
				{Name: "provider-a", APIKey: "key-a"},
				{Name: "provider-b", APIKey: "key-b", Model: "model-b"},
			},
		},
	}

	result := wireAgentProviders(agent, proj.Agent)
	startInitialRefreshIfReady(agent, result)

	wantCalls := []string{"set_providers", "set_active_provider", "start_initial_model_refresh"}
	if !reflect.DeepEqual(agent.calls, wantCalls) {
		t.Fatalf("call order = %v, want %v", agent.calls, wantCalls)
	}
	if len(agent.providers) != 2 {
		t.Fatalf("provider count = %d, want 2", len(agent.providers))
	}
	if agent.activeName != "provider-b" {
		t.Fatalf("active provider = %q, want %q", agent.activeName, "provider-b")
	}
}

func TestWireAgentProviders_SkipsRefreshWhenExplicitProviderActivationFails(t *testing.T) {
	agent := &stubProviderRefreshAgent{}
	agent.activateOK = false
	agent.workDir = "/tmp/original"
	proj := config.ProjectConfig{
		Agent: config.AgentConfig{
			Options:   map[string]any{"provider": "missing-provider"},
			Providers: []config.ProviderConfig{{Name: "provider-a", APIKey: "key-a"}},
		},
	}

	result := wireAgentProviders(agent, proj.Agent)

	if result.canStartInitialRefresh {
		t.Fatal("canStartInitialRefresh = true, want false")
	}
	if !result.explicitProviderRequested {
		t.Fatal("explicitProviderRequested = false, want true")
	}
	if result.activeProviderApplied {
		t.Fatal("activeProviderApplied = true, want false")
	}
	wantCalls := []string{"set_providers", "set_active_provider_failed"}
	if !reflect.DeepEqual(agent.calls, wantCalls) {
		t.Fatalf("call order = %v, want %v", agent.calls, wantCalls)
	}
}

func TestWireAgentProviders_AllowsRefreshWithoutProviders(t *testing.T) {
	agent := &stubProviderRefreshAgent{stubMainAgent: stubMainAgent{workDir: "/tmp/original"}}
	proj := config.ProjectConfig{Agent: config.AgentConfig{Options: map[string]any{}}}

	result := wireAgentProviders(agent, proj.Agent)

	if !result.canStartInitialRefresh {
		t.Fatal("canStartInitialRefresh = false, want true")
	}
	if result.explicitProviderRequested {
		t.Fatal("explicitProviderRequested = true, want false")
	}
	if result.activeProviderApplied {
		t.Fatal("activeProviderApplied = true, want false")
	}
	if len(agent.calls) != 0 {
		t.Fatalf("calls = %v, want no provider wiring calls", agent.calls)
	}
}

func TestStartInitialRefresh_AfterProjectStateOverride(t *testing.T) {
	agent := &stubProviderRefreshAgent{activateOK: true, stubMainAgent: stubMainAgent{workDir: "/tmp/original"}}
	overrideDir := filepath.Join(t.TempDir(), "override")
	if err := os.Mkdir(overrideDir, 0o755); err != nil {
		t.Fatalf("mkdir override dir: %v", err)
	}
	store := core.NewProjectStateStore(filepath.Join(t.TempDir(), "projects", "demo.state.json"))
	store.SetWorkDirOverride(overrideDir)
	proj := config.ProjectConfig{
		Name: "demo",
		Agent: config.AgentConfig{
			Options:   map[string]any{"provider": "provider-b", "work_dir": "/tmp/original"},
			Providers: []config.ProviderConfig{{Name: "provider-a"}, {Name: "provider-b"}},
		},
	}

	result := wireAgentProviders(agent, proj.Agent)
	applyProjectStateOverride(proj.Name, agent, "/tmp/original", store)
	startInitialRefreshIfReady(agent, result)

	wantCalls := []string{"set_providers", "set_active_provider", "start_initial_model_refresh"}
	if !reflect.DeepEqual(agent.calls, wantCalls) {
		t.Fatalf("call order = %v, want %v", agent.calls, wantCalls)
	}
	if agent.workDir != overrideDir {
		t.Fatalf("agent workDir at refresh = %q, want %q", agent.workDir, overrideDir)
	}
}
