package main

import (
	"context"
	"os"
	"path/filepath"
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

type stubMainProviderAgent struct {
	stubMainAgent
	providers  []core.ProviderConfig
	activeName string
}

func (a *stubMainProviderAgent) SetProviders(providers []core.ProviderConfig) {
	a.providers = append([]core.ProviderConfig(nil), providers...)
}

func (a *stubMainProviderAgent) SetActiveProvider(name string) bool {
	a.activeName = name
	if name == "" {
		return true
	}
	for _, provider := range a.providers {
		if provider.Name == name {
			return true
		}
	}
	return false
}

func (a *stubMainProviderAgent) GetActiveProvider() *core.ProviderConfig {
	for _, provider := range a.providers {
		if provider.Name == a.activeName {
			p := provider
			return &p
		}
	}
	return nil
}

func (a *stubMainProviderAgent) ListProviders() []core.ProviderConfig {
	return append([]core.ProviderConfig(nil), a.providers...)
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

func TestConfigureAgentProvidersUsesTopLevelProviders(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.ProviderConfig{
			{Name: "openai", APIKey: "sk-openai"},
			{Name: "kimi", APIKey: "sk-kimi"},
		},
		Projects: []config.ProjectConfig{{
			Name: "demo",
			Agent: config.AgentConfig{
				Type: "stub-main",
				Options: map[string]any{
					"provider": "openai",
				},
				Providers: []config.ProviderConfig{
					{Name: "legacy", APIKey: "sk-legacy"},
				},
			},
			Platforms: []config.PlatformConfig{{Type: "telegram", Options: map[string]any{"token": "x"}}},
		}},
	}

	agent := &stubMainProviderAgent{}
	updated, err := configureAgentProviders(agent, cfg, "demo", cfg.Projects[0].Agent.Options)
	if err != nil {
		t.Fatalf("configureAgentProviders() error: %v", err)
	}
	if updated != 2 {
		t.Fatalf("updated = %d, want 2", updated)
	}
	if len(agent.providers) != 2 {
		t.Fatalf("provider count = %d, want 2", len(agent.providers))
	}
	if agent.providers[0].Name != "openai" || agent.providers[1].Name != "kimi" {
		t.Fatalf("providers = %#v, want top-level providers", agent.providers)
	}
	if agent.activeName != "openai" {
		t.Fatalf("activeName = %q, want openai", agent.activeName)
	}
}

func TestConfigureAgentProvidersFallsBackToLegacyProjectProviders(t *testing.T) {
	cfg := &config.Config{
		Projects: []config.ProjectConfig{{
			Name: "demo",
			Agent: config.AgentConfig{
				Type: "stub-main",
				Options: map[string]any{
					"provider": "legacy",
				},
				Providers: []config.ProviderConfig{
					{Name: "legacy", APIKey: "sk-legacy"},
					{Name: "backup", APIKey: "sk-backup"},
				},
			},
			Platforms: []config.PlatformConfig{{Type: "telegram", Options: map[string]any{"token": "x"}}},
		}},
	}

	agent := &stubMainProviderAgent{}
	updated, err := configureAgentProviders(agent, cfg, "demo", cfg.Projects[0].Agent.Options)
	if err != nil {
		t.Fatalf("configureAgentProviders() error: %v", err)
	}
	if updated != 2 {
		t.Fatalf("updated = %d, want 2", updated)
	}
	if len(agent.providers) != 2 {
		t.Fatalf("provider count = %d, want 2", len(agent.providers))
	}
	if agent.providers[0].Name != "legacy" || agent.providers[1].Name != "backup" {
		t.Fatalf("providers = %#v, want legacy project providers", agent.providers)
	}
	if agent.activeName != "legacy" {
		t.Fatalf("activeName = %q, want legacy", agent.activeName)
	}
}

func TestReloadConfigUsesTopLevelProviders(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	content := `
[[providers]]
name = "openai"
api_key = "sk-openai"

[[providers]]
name = "kimi"
api_key = "sk-kimi"

[[projects]]
name = "demo"

[projects.agent]
type = "stub-main"

[projects.agent.options]
provider = "kimi"

[[projects.agent.providers]]
name = "legacy"
api_key = "sk-legacy"

[[projects.platforms]]
type = "telegram"

[projects.platforms.options]
token = "test-token"
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	agent := &stubMainProviderAgent{}
	engine := core.NewEngine("demo", agent, nil, filepath.Join(dir, "sessions.json"), core.LangEnglish)
	result, err := reloadConfig(configPath, "demo", engine)
	if err != nil {
		t.Fatalf("reloadConfig() error: %v", err)
	}
	if result.ProvidersUpdated != 2 {
		t.Fatalf("ProvidersUpdated = %d, want 2", result.ProvidersUpdated)
	}
	if len(agent.providers) != 2 {
		t.Fatalf("provider count = %d, want 2", len(agent.providers))
	}
	if agent.providers[0].Name != "openai" || agent.providers[1].Name != "kimi" {
		t.Fatalf("providers = %#v, want top-level providers", agent.providers)
	}
	if agent.activeName != "kimi" {
		t.Fatalf("activeName = %q, want kimi", agent.activeName)
	}
}
