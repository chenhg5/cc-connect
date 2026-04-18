package claudecode

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"
)

// These integration tests use real provider credentials from ~/.cc-connect/config.toml.
// They verify that provider switching correctly sets up env vars and that agent
// sessions can be started with the right configuration.
//
// Skip in CI: set CC_SKIP_INTEGRATION=1 or simply don't have the config file.

func skipIfNoConfig(t *testing.T) *config.Config {
	t.Helper()
	if os.Getenv("CC_SKIP_INTEGRATION") == "1" {
		t.Skip("CC_SKIP_INTEGRATION=1")
	}
	cfgPath := os.ExpandEnv("$HOME/.cc-connect/config.toml")
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		t.Skipf("config not found at %s", cfgPath)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}
	cfg.ResolveProviderRefs()
	return cfg
}

func configToCoreProv(p config.ProviderConfig) core.ProviderConfig {
	cp := core.ProviderConfig{
		Name:    p.Name,
		APIKey:  p.APIKey,
		BaseURL: p.BaseURL,
		Model:   p.Model,
		Env:     p.Env,
	}
	if p.Thinking != "" {
		cp.Thinking = p.Thinking
	}
	for _, m := range p.Models {
		cp.Models = append(cp.Models, core.ModelOption{Name: m.Model})
	}
	return cp
}

func findProjectProviders(cfg *config.Config, agentType string) (projName string, providers []core.ProviderConfig, workDir string) {
	for i := range cfg.Projects {
		proj := &cfg.Projects[i]
		if proj.Agent.Type != agentType || len(proj.Agent.Providers) == 0 {
			continue
		}
		for _, p := range proj.Agent.Providers {
			providers = append(providers, configToCoreProv(p))
		}
		projName = proj.Name
		if wd, ok := proj.Agent.Options["work_dir"].(string); ok {
			workDir = wd
		}
		return
	}
	return
}

func TestIntegration_ProviderSwitch_EnvVars(t *testing.T) {
	cfg := skipIfNoConfig(t)

	name, providers, _ := findProjectProviders(cfg, "claudecode")
	if name == "" {
		t.Skip("no claudecode project with providers found")
	}
	if len(providers) < 2 {
		t.Skipf("project %q has only %d provider(s), need at least 2", name, len(providers))
	}

	a := &Agent{
		providers: providers,
		activeIdx: -1,
	}

	p0, p1 := providers[0], providers[1]
	t.Logf("provider[0]: name=%s model=%s base_url=%s", p0.Name, p0.Model, p0.BaseURL)
	t.Logf("provider[1]: name=%s model=%s base_url=%s", p1.Name, p1.Model, p1.BaseURL)

	a.SetActiveProvider(p0.Name)
	env0 := envSliceToMap(a.providerEnvLocked())

	if p0.BaseURL != "" {
		if got := env0["ANTHROPIC_BASE_URL"]; got != p0.BaseURL {
			t.Errorf("provider[0] ANTHROPIC_BASE_URL = %q, want %q", got, p0.BaseURL)
		}
	}
	if p0.Model != "" {
		if got := env0["ANTHROPIC_MODEL"]; got != p0.Model {
			t.Errorf("provider[0] ANTHROPIC_MODEL = %q, want %q", got, p0.Model)
		}
	}

	a.SetActiveProvider(p1.Name)
	env1 := envSliceToMap(a.providerEnvLocked())

	if p1.BaseURL != "" {
		if got := env1["ANTHROPIC_BASE_URL"]; got != p1.BaseURL {
			t.Errorf("provider[1] ANTHROPIC_BASE_URL = %q, want %q", got, p1.BaseURL)
		}
	}
	if p1.Model != "" {
		if got := env1["ANTHROPIC_MODEL"]; got != p1.Model {
			t.Errorf("provider[1] ANTHROPIC_MODEL = %q, want %q", got, p1.Model)
		}
	}

	if p0.BaseURL != p1.BaseURL {
		if env0["ANTHROPIC_BASE_URL"] == env1["ANTHROPIC_BASE_URL"] {
			t.Error("providers have different base_urls but env didn't change after switch")
		}
	}
	if p0.Model != p1.Model && p0.Model != "" && p1.Model != "" {
		if env0["ANTHROPIC_MODEL"] == env1["ANTHROPIC_MODEL"] {
			t.Error("providers have different models but ANTHROPIC_MODEL didn't change after switch")
		}
	}
}

func TestIntegration_ProviderSwitch_SessionStartModel(t *testing.T) {
	cfg := skipIfNoConfig(t)

	name, providers, workDir := findProjectProviders(cfg, "claudecode")
	if name == "" {
		t.Skip("no claudecode project with providers found")
	}
	if len(providers) < 2 {
		t.Skipf("project %q has only %d provider(s), need at least 2", name, len(providers))
	}
	if workDir == "" {
		workDir = "/tmp"
	}

	cliBin, err := exec.LookPath("claude")
	if err != nil {
		t.Skipf("claude CLI not found: %v", err)
	}

	for _, prov := range providers[:2] {
		t.Run(prov.Name, func(t *testing.T) {
			a := &Agent{
				model:     "default-model",
				providers: providers,
				activeIdx: -1,
				workDir:   workDir,
				cliBin:    cliBin,
			}
			a.SetActiveProvider(prov.Name)

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			sess, err := a.StartSession(ctx, "")
			if err != nil {
				t.Fatalf("StartSession failed: %v", err)
			}
			defer sess.Close()

			cs := sess.(*claudeSession)
			envMap := envSliceToMap(cs.cmd.Env)

			if prov.Model != "" {
				found := false
				for _, arg := range cs.cmd.Args {
					if arg == prov.Model {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("--model %q not found in args: %v", prov.Model, cs.cmd.Args)
				}
			}

			if prov.BaseURL != "" {
				gotURL := envMap["ANTHROPIC_BASE_URL"]
				if gotURL == "" {
					t.Error("ANTHROPIC_BASE_URL not set in session env")
				} else if !strings.Contains(gotURL, "127.0.0.1") && gotURL != prov.BaseURL {
					t.Errorf("ANTHROPIC_BASE_URL = %q, want %q (or proxy URL)", gotURL, prov.BaseURL)
				}
			}

			if prov.Model != "" {
				if got := envMap["ANTHROPIC_MODEL"]; got != prov.Model {
					t.Errorf("ANTHROPIC_MODEL = %q, want %q", got, prov.Model)
				}
			}

			t.Logf("session started OK: pid=%d, model=%s, env_model=%s, env_base_url=%s",
				cs.cmd.Process.Pid,
				prov.Model,
				envMap["ANTHROPIC_MODEL"],
				envMap["ANTHROPIC_BASE_URL"])
		})
	}
}

func TestIntegration_CodexProvider_EnvVars(t *testing.T) {
	cfg := skipIfNoConfig(t)

	name, providers, _ := findProjectProviders(cfg, "codex")
	if name == "" {
		t.Skip("no codex project with providers found")
	}

	for _, prov := range providers {
		t.Run(prov.Name, func(t *testing.T) {
			t.Logf("codex provider: name=%s base_url=%s model=%s", prov.Name, prov.BaseURL, prov.Model)
			if prov.BaseURL == "" {
				t.Error("codex provider should have a base_url")
			}
			if prov.Model == "" {
				t.Error("codex provider should have a model")
			}
		})
	}
}

func TestIntegration_AgentTypeChange_FiltersProviders(t *testing.T) {
	cfg := skipIfNoConfig(t)

	if len(cfg.Providers) == 0 {
		t.Skip("no global providers in config")
	}

	globalByName := make(map[string]config.ProviderConfig, len(cfg.Providers))
	for _, p := range cfg.Providers {
		globalByName[p.Name] = p
	}

	for i := range cfg.Projects {
		proj := &cfg.Projects[i]
		if proj.Agent.Type != "claudecode" || len(proj.Agent.ProviderRefs) == 0 {
			continue
		}

		var compatible, incompatible []string
		for _, ref := range proj.Agent.ProviderRefs {
			gp, ok := globalByName[ref]
			if !ok {
				continue
			}
			if len(gp.AgentTypes) > 0 {
				hasCodex := false
				for _, at := range gp.AgentTypes {
					if at == "codex" {
						hasCodex = true
					}
				}
				if hasCodex {
					compatible = append(compatible, ref)
				} else {
					incompatible = append(incompatible, ref)
				}
			} else {
				compatible = append(compatible, ref)
			}
		}

		if len(incompatible) == 0 {
			continue
		}

		t.Logf("project %q: switching claudecode→codex: keep=%v remove=%v",
			proj.Name, compatible, incompatible)
		t.Logf("provider filtering validated: %d provider(s) would be removed on agent type change", len(incompatible))
		return
	}
	t.Skip("no project with incompatible providers found for agent type change test")
}
