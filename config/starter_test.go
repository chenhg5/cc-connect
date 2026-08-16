package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rawTables indexes a TOML document as table header -> key -> raw value text.
// The starter config is asserted at the text level on purpose: what the user
// opens in an editor is the artifact under test, not a decoded struct.
func rawTables(t *testing.T, doc string) map[string]map[string]string {
	t.Helper()
	tables := map[string]map[string]string{}
	current := ""
	for _, line := range strings.Split(doc, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") {
			current = trimmed
			if _, ok := tables[current]; !ok {
				tables[current] = map[string]string{}
			}
			continue
		}
		key, value, ok := strings.Cut(trimmed, "=")
		if !ok {
			continue
		}
		if tables[current] == nil {
			tables[current] = map[string]string{}
		}
		tables[current][strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return tables
}

func TestStarterConfigTOML_WritesTheRecommendedFeishuProfile(t *testing.T) {
	tables := rawTables(t, StarterConfigTOML())

	for _, setting := range RecommendedFeishuProfile(StarterAgentType) {
		header := setting.TableHeader()
		got, ok := tables[header][setting.Key]
		if !ok {
			t.Fatalf("starter config is missing %s %s (the recommended profile and the first-run template must not drift)", header, setting.Key)
		}
		if got != setting.Value {
			t.Fatalf("starter config %s %s = %s, want %s", header, setting.Key, got, setting.Value)
		}
	}
}

func TestStarterConfigTOML_KeepsPresentationOutOfTheGlobalTable(t *testing.T) {
	tables := rawTables(t, StarterConfigTOML())

	// The recommended profile is written per project. A global [display]
	// table would shadow nothing today but would diverge the moment a
	// second project is added.
	if _, ok := tables["[display]"]; ok {
		t.Fatalf("starter config still has a global [display] table:\n%s", StarterConfigTOML())
	}
}

func TestStarterConfigTOML_LoadsAndValidates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(StarterConfigTOML()), 0o600); err != nil {
		t.Fatalf("write starter config: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load(starter config) error = %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate(starter config) error = %v", err)
	}
	if len(cfg.Projects) != 1 {
		t.Fatalf("starter config projects = %d, want 1", len(cfg.Projects))
	}
	if cfg.Projects[0].Name != StarterProjectName {
		t.Fatalf("starter project name = %q, want %q", cfg.Projects[0].Name, StarterProjectName)
	}
	if cfg.Projects[0].Agent.Type != StarterAgentType {
		t.Fatalf("starter agent type = %q, want %q", cfg.Projects[0].Agent.Type, StarterAgentType)
	}
}

func TestFindStarterPlaceholders_ReportsEveryValueTheTemplateAsksToReplace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(StarterConfigTOML()), 0o600); err != nil {
		t.Fatalf("write starter config: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load(starter config) error = %v", err)
	}

	found := FindStarterPlaceholders(cfg)
	if len(found) != 3 {
		t.Fatalf("FindStarterPlaceholders() = %d findings, want 3: %+v", len(found), found)
	}

	want := map[string]string{
		"projects.agent.options.work_dir":       PlaceholderWorkDir,
		"projects.platforms.options.app_id":     PlaceholderFeishuAppID,
		"projects.platforms.options.app_secret": PlaceholderFeishuAppSecret,
	}
	for _, f := range found {
		if f.Project != StarterProjectName {
			t.Fatalf("finding %+v has project %q, want %q", f, f.Project, StarterProjectName)
		}
		wantValue, ok := want[f.Location()]
		if !ok {
			t.Fatalf("unexpected placeholder location %q", f.Location())
		}
		if f.Value != wantValue {
			t.Fatalf("%s value = %q, want %q", f.Location(), f.Value, wantValue)
		}
		if strings.TrimSpace(f.Fix) == "" {
			t.Fatalf("%s has no next step for the user", f.Location())
		}
		delete(want, f.Location())
	}
	if len(want) != 0 {
		t.Fatalf("placeholders not reported: %v", want)
	}
}

func TestFindStarterPlaceholders_AcceptsRealValues(t *testing.T) {
	doc := strings.NewReplacer(
		PlaceholderWorkDir, t.TempDir(),
		PlaceholderFeishuAppID, "cli_real_app_id",
		PlaceholderFeishuAppSecret, "real-secret",
	).Replace(StarterConfigTOML())

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if found := FindStarterPlaceholders(cfg); len(found) != 0 {
		t.Fatalf("FindStarterPlaceholders() = %+v, want none", found)
	}
}

func TestFindStarterPlaceholders_IsStableAcrossRuns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(StarterConfigTOML()), 0o600); err != nil {
		t.Fatalf("write starter config: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	first := FindStarterPlaceholders(cfg)
	for i := 0; i < 20; i++ {
		again := FindStarterPlaceholders(cfg)
		if len(again) != len(first) {
			t.Fatalf("finding count changed between runs: %d vs %d", len(again), len(first))
		}
		for j := range first {
			if again[j].Location() != first[j].Location() {
				t.Fatalf("finding order changed between runs: %q vs %q", again[j].Location(), first[j].Location())
			}
		}
	}
}
