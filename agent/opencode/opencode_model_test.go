package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

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

func writePersistentModelCache(t *testing.T, cachePath string, models []core.ModelOption, updatedAt time.Time) string {
	t.Helper()

	type persistentModelCache struct {
		Models     []core.ModelOption `json:"models"`
		UpdatedAt  time.Time          `json:"updated_at"`
		ContextKey string             `json:"context_key,omitempty"`
	}

	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		t.Fatal(err)
	}

	defaultSnapshot := opencodeModelDiscoverySnapshot{workDir: "."}
	data, err := json.Marshal(persistentModelCache{Models: models, UpdatedAt: updatedAt, ContextKey: modelDiscoveryContextKey(defaultSnapshot)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cachePath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return cachePath
}

func writePersistentModelCacheWithSnapshot(t *testing.T, cachePath string, snapshot opencodeModelDiscoverySnapshot, models []core.ModelOption, updatedAt time.Time) string {
	t.Helper()

	type persistentModelCache struct {
		Models      []core.ModelOption `json:"models"`
		UpdatedAt   time.Time          `json:"updated_at"`
		ProviderKey string             `json:"provider_key,omitempty"`
		ContextKey  string             `json:"context_key,omitempty"`
	}

	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		t.Fatal(err)
	}

	data, err := json.Marshal(persistentModelCache{
		Models:      models,
		UpdatedAt:   updatedAt,
		ProviderKey: snapshot.providerKey,
		ContextKey:  modelDiscoveryContextKey(snapshot),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cachePath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return cachePath
}

func writeBlockingModelsBin(t *testing.T, gatePath string, lines []string) string {
	t.Helper()
	tmpDir := t.TempDir()
	name := filepath.Join(tmpDir, "fake-opencode")

	var body strings.Builder
	body.WriteString("#!/bin/sh\n")
	body.WriteString("if [ \"$1\" = \"models\" ]; then\n")
	if gatePath != "" {
		fmt.Fprintf(&body, "  while [ ! -f '%s' ]; do\n", gatePath)
		body.WriteString("    sleep 0.01\n")
		body.WriteString("  done\n")
	}
	for _, line := range lines {
		fmt.Fprintf(&body, "  printf '%%s\\n' '%s'\n", line)
	}
	body.WriteString("fi\n")

	if err := os.WriteFile(name, []byte(body.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	return name
}

func writeCountingModelsBin(t *testing.T, countPath, gatePath string, lines []string, requireEnvKey string, exitCode int) string {
	t.Helper()
	tmpDir := t.TempDir()
	name := filepath.Join(tmpDir, "fake-opencode")

	var body strings.Builder
	body.WriteString("#!/bin/sh\n")
	body.WriteString("if [ \"$1\" = \"models\" ]; then\n")
	if countPath != "" {
		fmt.Fprintf(&body, "  count=0\n  if [ -f '%s' ]; then count=$(cat '%s'); fi\n", countPath, countPath)
		fmt.Fprintf(&body, "  count=$((count + 1))\n  printf '%%s' \"$count\" > '%s'\n", countPath)
	}
	if gatePath != "" {
		fmt.Fprintf(&body, "  while [ ! -f '%s' ]; do\n", gatePath)
		body.WriteString("    sleep 0.01\n")
		body.WriteString("  done\n")
	}
	if requireEnvKey != "" {
		fmt.Fprintf(&body, "  if [ -z \"$%s\" ]; then exit 0; fi\n", requireEnvKey)
	}
	if exitCode != 0 {
		fmt.Fprintf(&body, "  exit %d\n", exitCode)
	} else {
		for _, line := range lines {
			fmt.Fprintf(&body, "  printf '%%s\\n' '%s'\n", line)
		}
	}
	body.WriteString("fi\n")

	if err := os.WriteFile(name, []byte(body.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	return name
}

func waitForModelsInPersistentCache(t *testing.T, cachePath string, want []string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		cache, err := loadOpencodePersistentModelCache(cachePath)
		if err == nil && cache != nil && len(cache.Models) == len(want) {
			match := true
			for i, model := range cache.Models {
				if model.Name != want[i] {
					match = false
					break
				}
			}
			if match {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	cache, err := loadOpencodePersistentModelCache(cachePath)
	if err != nil {
		t.Fatalf("loadOpencodePersistentModelCache(%q) error = %v", cachePath, err)
	}
	t.Fatalf("persistent cache models = %v, want %v", cache, want)
}

func waitForFileContent(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && strings.TrimSpace(string(data)) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) error = %v", path, err)
	}
	t.Fatalf("file %q content = %q, want %q", path, strings.TrimSpace(string(data)), want)
}

func readPersistentModelCachePayload(t *testing.T, cachePath string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) error = %v", cachePath, err)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("json.Unmarshal(%q) error = %v", cachePath, err)
	}
	return payload
}

func providerCacheKeyOf(t *testing.T, a *Agent) string {
	t.Helper()
	key := a.activeProviderKey()
	if key == "" {
		t.Fatal("activeProviderKey() = empty, want signature")
	}
	if strings.Contains(key, "provider-") || strings.Contains(key, "present") || strings.Contains(key, "secret") {
		t.Fatalf("activeProviderKey() leaked raw provider data: %q", key)
	}
	return key
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

func TestAvailableModels_ConfiguredFallbackUsesSnapshot(t *testing.T) {
	a := &Agent{
		providers: []core.ProviderConfig{
			{Name: "provider-a", Models: []core.ModelOption{{Name: "configured-a"}}},
			{Name: "provider-b", Models: []core.ModelOption{{Name: "configured-b"}}},
		},
		activeIdx: 0,
	}

	snapshot := a.modelDiscoverySnapshot()
	if !a.SetActiveProvider("provider-b") {
		t.Fatal("SetActiveProvider(provider-b) = false, want true")
	}

	got := a.configuredModelsForSnapshot(snapshot)
	if len(got) != 1 || got[0].Name != "configured-a" {
		t.Fatalf("configuredModelsForSnapshot() = %v, want [configured-a]", got)
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

func TestProjectModelCachePath_SanitizesProjectName(t *testing.T) {
	got := opencodeProjectModelCachePath("/tmp/data", " ../team/demo project:alpha?beta ")
	if filepath.Dir(got) != filepath.Join("/tmp/data", "projects") {
		t.Fatalf("opencodeProjectModelCachePath() dir = %q, want %q", filepath.Dir(got), filepath.Join("/tmp/data", "projects"))
	}
	base := filepath.Base(got)
	if !strings.HasPrefix(base, "team-demo-project-alpha-beta-") {
		t.Fatalf("opencodeProjectModelCachePath() base = %q, want sanitized prefix", base)
	}
	if !strings.HasSuffix(base, ".opencode-models.json") {
		t.Fatalf("opencodeProjectModelCachePath() base = %q, want .opencode-models.json suffix", base)
	}
	hashSuffix := strings.TrimSuffix(strings.TrimPrefix(base, "team-demo-project-alpha-beta-"), ".opencode-models.json")
	if len(hashSuffix) != 16 {
		t.Fatalf("opencodeProjectModelCachePath() hash suffix = %q, want 16 hex chars", hashSuffix)
	}
}

func TestProjectModelCachePath_DistinguishesSanitizeCollisions(t *testing.T) {
	pathA := opencodeProjectModelCachePath("/tmp/data", "team/demo")
	pathB := opencodeProjectModelCachePath("/tmp/data", "team-demo")
	if pathA == pathB {
		t.Fatalf("opencodeProjectModelCachePath() collision: %q == %q", pathA, pathB)
	}
}

func TestLoadPersistentModelCache_NormalizesModels(t *testing.T) {
	cachePath := opencodeProjectModelCachePath(t.TempDir(), "demo")
	writePersistentModelCache(t, cachePath, []core.ModelOption{
		{Name: "  z-model  ", Desc: "z"},
		{Name: ""},
		{Name: "   "},
		{Name: "a-model", Desc: "first"},
		{Name: "a-model", Desc: "duplicate"},
		{Name: "  m-model", Desc: "m"},
	}, time.Now())

	cache, err := loadOpencodePersistentModelCache(cachePath)
	if err != nil {
		t.Fatalf("loadOpencodePersistentModelCache() error = %v", err)
	}
	if cache == nil {
		t.Fatal("loadOpencodePersistentModelCache() = nil, want cache")
	}

	got := cache.Models
	if len(got) != 3 {
		t.Fatalf("normalized cache models len = %d, want 3: %v", len(got), got)
	}
	want := []string{"a-model", "m-model", "z-model"}
	for i, model := range got {
		if model.Name != want[i] {
			t.Fatalf("normalized cache models[%d] = %q, want %q (all: %v)", i, model.Name, want[i], got)
		}
	}
	if got[0].Desc != "first" {
		t.Fatalf("normalized cache kept desc %q, want %q", got[0].Desc, "first")
	}
}

func TestNew_SurfacesPersistentModelCacheViaAvailableModels(t *testing.T) {
	dataDir := t.TempDir()
	cachePath := opencodeProjectModelCachePath(dataDir, "demo")
	writePersistentModelCache(t, cachePath, []core.ModelOption{{Name: "cached/model"}}, time.Now())
	fakeEmptyBin := writeFakeModelsBin(t, []string{}, 0)

	agent, err := New(map[string]any{
		"cmd":         fakeEmptyBin,
		"cc_data_dir": dataDir,
		"cc_project":  "demo",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	switcher, ok := agent.(core.ModelSwitcher)
	if !ok {
		t.Fatalf("New() agent does not implement core.ModelSwitcher")
	}

	got := switcher.AvailableModels(context.Background())
	if len(got) != 1 || got[0].Name != "cached/model" {
		t.Fatalf("AvailableModels() = %v, want [cached/model]", got)
	}
}

func TestAvailableModels_PrefersPersistentCacheOverDiscoveredModels(t *testing.T) {
	dataDir := t.TempDir()
	cachePath := opencodeProjectModelCachePath(dataDir, "demo")
	writePersistentModelCache(t, cachePath, []core.ModelOption{{Name: "cached/model"}}, time.Now())
	bin := writeFakeModelsBin(t, []string{"fresh/model"}, 0)

	agent, err := New(map[string]any{
		"cmd":         bin,
		"cc_data_dir": dataDir,
		"cc_project":  "demo",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	switcher, ok := agent.(core.ModelSwitcher)
	if !ok {
		t.Fatalf("New() agent does not implement core.ModelSwitcher")
	}

	got := switcher.AvailableModels(context.Background())
	if len(got) != 1 || got[0].Name != "cached/model" {
		t.Fatalf("AvailableModels() = %v, want [cached/model]", got)
	}
}

func TestAvailableModels_ReturnsPersistentCacheWhenDiscoveryFails(t *testing.T) {
	dataDir := t.TempDir()
	cachePath := opencodeProjectModelCachePath(dataDir, "demo")
	writePersistentModelCache(t, cachePath, []core.ModelOption{{Name: "cached/model"}}, time.Now())
	failingBin := writeFakeModelsBin(t, nil, 1)

	agent, err := New(map[string]any{
		"cmd":         failingBin,
		"cc_data_dir": dataDir,
		"cc_project":  "demo",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	switcher, ok := agent.(core.ModelSwitcher)
	if !ok {
		t.Fatalf("New() agent does not implement core.ModelSwitcher")
	}

	got := switcher.AvailableModels(context.Background())
	if len(got) != 1 || got[0].Name != "cached/model" {
		t.Fatalf("AvailableModels() = %v, want [cached/model]", got)
	}
}

func TestAvailableModels_PersistsDiscoveryOnColdStart(t *testing.T) {
	dataDir := t.TempDir()
	bin := writeFakeModelsBin(t, []string{"fresh/model", "second/model"}, 0)

	agent, err := New(map[string]any{
		"cmd":         bin,
		"cc_data_dir": dataDir,
		"cc_project":  "demo",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	a := agent.(*Agent)

	got := a.AvailableModels(context.Background())
	if len(got) != 2 || got[0].Name != "fresh/model" || got[1].Name != "second/model" {
		t.Fatalf("AvailableModels() = %v, want discovered models", got)
	}

	cachePath := opencodeProjectModelCachePath(dataDir, "demo")
	cache, err := loadOpencodePersistentModelCache(cachePath)
	if err != nil {
		t.Fatalf("loadOpencodePersistentModelCache(%q) error = %v", cachePath, err)
	}
	if cache == nil {
		t.Fatalf("loadOpencodePersistentModelCache(%q) = nil, want persisted cache", cachePath)
	}
	if len(cache.Models) != 2 || cache.Models[0].Name != "fresh/model" || cache.Models[1].Name != "second/model" {
		t.Fatalf("persisted cache models = %v, want discovered models", cache.Models)
	}

	inMemory := a.persistentModels()
	if len(inMemory) != 2 || inMemory[0].Name != "fresh/model" || inMemory[1].Name != "second/model" {
		t.Fatalf("in-memory persistent models = %v, want discovered models", inMemory)
	}
}

func TestAvailableModels_BackgroundRefreshUpdatesDiskCache(t *testing.T) {
	dataDir := t.TempDir()
	cachePath := opencodeProjectModelCachePath(dataDir, "demo")
	writePersistentModelCache(t, cachePath, []core.ModelOption{{Name: "cached/model"}}, time.Now())
	gatePath := filepath.Join(t.TempDir(), "refresh-ready")
	bin := writeBlockingModelsBin(t, gatePath, []string{"fresh/model", "second/model"})

	agent, err := New(map[string]any{
		"cmd":         bin,
		"cc_data_dir": dataDir,
		"cc_project":  "demo",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	switcher, ok := agent.(core.ModelSwitcher)
	if !ok {
		t.Fatalf("New() agent does not implement core.ModelSwitcher")
	}

	got := switcher.AvailableModels(context.Background())
	if len(got) != 1 || got[0].Name != "cached/model" {
		t.Fatalf("AvailableModels() = %v, want immediate cached result", got)
	}

	if err := os.WriteFile(gatePath, []byte("ok"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", gatePath, err)
	}

	waitForModelsInPersistentCache(t, cachePath, []string{"fresh/model", "second/model"})

	got = switcher.AvailableModels(context.Background())
	if len(got) != 2 || got[0].Name != "fresh/model" || got[1].Name != "second/model" {
		t.Fatalf("AvailableModels() after refresh = %v, want refreshed cache", got)
	}
}

func TestAvailableModels_BackgroundRefreshFailurePreservesCache(t *testing.T) {
	dataDir := t.TempDir()
	cachePath := opencodeProjectModelCachePath(dataDir, "demo")
	writePersistentModelCache(t, cachePath, []core.ModelOption{{Name: "cached/model"}}, time.Now())
	countPath := filepath.Join(t.TempDir(), "refresh-count")
	gatePath := filepath.Join(t.TempDir(), "refresh-ready")
	bin := writeCountingModelsBin(t, countPath, gatePath, nil, "", 1)

	agent, err := New(map[string]any{
		"cmd":         bin,
		"cc_data_dir": dataDir,
		"cc_project":  "demo",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	a := agent.(*Agent)

	got := a.AvailableModels(context.Background())
	if len(got) != 1 || got[0].Name != "cached/model" {
		t.Fatalf("AvailableModels() = %v, want immediate cached result", got)
	}
	if err := os.WriteFile(gatePath, []byte("ok"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", gatePath, err)
	}
	waitForFileContent(t, countPath, "1")

	cache, err := loadOpencodePersistentModelCache(cachePath)
	if err != nil {
		t.Fatalf("loadOpencodePersistentModelCache(%q) error = %v", cachePath, err)
	}
	if cache == nil || len(cache.Models) != 1 || cache.Models[0].Name != "cached/model" {
		t.Fatalf("persistent cache after failed refresh = %v, want cached/model preserved", cache)
	}
	inMemory := a.persistentModels()
	if len(inMemory) != 1 || inMemory[0].Name != "cached/model" {
		t.Fatalf("in-memory cache after failed refresh = %v, want cached/model preserved", inMemory)
	}
}

func TestAvailableModels_BackgroundRefreshSingleFlight(t *testing.T) {
	dataDir := t.TempDir()
	cachePath := opencodeProjectModelCachePath(dataDir, "demo")
	writePersistentModelCache(t, cachePath, []core.ModelOption{{Name: "cached/model"}}, time.Now())
	countPath := filepath.Join(t.TempDir(), "refresh-count")
	gatePath := filepath.Join(t.TempDir(), "refresh-ready")
	bin := writeCountingModelsBin(t, countPath, gatePath, []string{"fresh/model"}, "", 0)

	agent, err := New(map[string]any{
		"cmd":         bin,
		"cc_data_dir": dataDir,
		"cc_project":  "demo",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	a := agent.(*Agent)

	for i := 0; i < 5; i++ {
		got := a.AvailableModels(context.Background())
		if len(got) != 1 || got[0].Name != "cached/model" {
			t.Fatalf("AvailableModels() call %d = %v, want cached/model", i, got)
		}
	}
	waitForFileContent(t, countPath, "1")
	if err := os.WriteFile(gatePath, []byte("ok"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", gatePath, err)
	}
	waitForModelsInPersistentCache(t, cachePath, []string{"fresh/model"})
	data, err := os.ReadFile(countPath)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) error = %v", countPath, err)
	}
	if strings.TrimSpace(string(data)) != "1" {
		t.Fatalf("refresh count = %q, want 1", strings.TrimSpace(string(data)))
	}
}

func TestStartInitialModelRefresh_UsesCurrentProviderWiring(t *testing.T) {
	dataDir := t.TempDir()
	cachePath := opencodeProjectModelCachePath(dataDir, "demo")
	writePersistentModelCache(t, cachePath, []core.ModelOption{{Name: "cached/model"}}, time.Now())
	countPath := filepath.Join(t.TempDir(), "refresh-count")
	gatePath := filepath.Join(t.TempDir(), "refresh-ready")
	bin := writeCountingModelsBin(t, countPath, gatePath, []string{"provider/model"}, "MODEL_DISCOVERY_TOKEN", 0)

	agent, err := New(map[string]any{
		"cmd":         bin,
		"cc_data_dir": dataDir,
		"cc_project":  "demo",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	a := agent.(*Agent)
	a.SetProviders([]core.ProviderConfig{{
		Name: "provider-a",
		Env:  map[string]string{"MODEL_DISCOVERY_TOKEN": "present"},
	}})
	if !a.SetActiveProvider("provider-a") {
		t.Fatal("SetActiveProvider(provider-a) = false, want true")
	}

	a.StartInitialModelRefresh()
	waitForFileContent(t, countPath, "1")
	if err := os.WriteFile(gatePath, []byte("ok"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", gatePath, err)
	}
	waitForModelsInPersistentCache(t, cachePath, []string{"provider/model"})
}

func TestStartInitialModelRefresh_PrewarmsColdStartCacheAfterProviderWiring(t *testing.T) {
	dataDir := t.TempDir()
	cachePath := opencodeProjectModelCachePath(dataDir, "demo")
	countPath := filepath.Join(t.TempDir(), "refresh-count")
	gatePath := filepath.Join(t.TempDir(), "refresh-ready")
	bin := writeCountingModelsBin(t, countPath, gatePath, []string{"provider/model"}, "MODEL_DISCOVERY_TOKEN", 0)

	agent, err := New(map[string]any{
		"cmd":         bin,
		"cc_data_dir": dataDir,
		"cc_project":  "demo",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	a := agent.(*Agent)
	a.SetProviders([]core.ProviderConfig{{
		Name: "provider-a",
		Env:  map[string]string{"MODEL_DISCOVERY_TOKEN": "present"},
	}})
	if !a.SetActiveProvider("provider-a") {
		t.Fatal("SetActiveProvider(provider-a) = false, want true")
	}
	if cache, err := loadOpencodePersistentModelCache(cachePath); err != nil {
		t.Fatalf("loadOpencodePersistentModelCache(%q) error = %v", cachePath, err)
	} else if cache != nil {
		t.Fatalf("persistent cache before prewarm = %v, want nil", cache)
	}

	a.StartInitialModelRefresh()
	waitForFileContent(t, countPath, "1")
	if err := os.WriteFile(gatePath, []byte("ok"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", gatePath, err)
	}
	waitForModelsInPersistentCache(t, cachePath, []string{"provider/model"})
}

func TestAvailableModels_DiscoveryUsesProviderEnv(t *testing.T) {
	countPath := filepath.Join(t.TempDir(), "refresh-count")
	bin := writeCountingModelsBin(t, countPath, "", []string{"provider/model"}, "MODEL_DISCOVERY_TOKEN", 0)
	a := &Agent{
		cmd:     bin,
		workDir: t.TempDir(),
		providers: []core.ProviderConfig{{
			Name:  "provider-a",
			Model: "provider/model",
			Env:   map[string]string{"MODEL_DISCOVERY_TOKEN": "present"},
		}},
		activeIdx: 0,
	}

	got := a.AvailableModels(context.Background())
	if len(got) != 1 || got[0].Name != "provider/model" {
		t.Fatalf("AvailableModels() = %v, want provider/model discovered via provider env", got)
	}
}

func TestAvailableModels_PersistsProviderKeyOnColdStartDiscovery(t *testing.T) {
	dataDir := t.TempDir()
	cachePath := opencodeProjectModelCachePath(dataDir, "demo")
	bin := writeCountingModelsBin(t, "", "", []string{"provider/model"}, "MODEL_DISCOVERY_TOKEN", 0)
	a := &Agent{
		cmd:            bin,
		workDir:        t.TempDir(),
		modelCachePath: cachePath,
		providers: []core.ProviderConfig{{
			Name: "provider-a",
			Env:  map[string]string{"MODEL_DISCOVERY_TOKEN": "present"},
		}},
		activeIdx: 0,
	}

	got := a.AvailableModels(context.Background())
	if len(got) != 1 || got[0].Name != "provider/model" {
		t.Fatalf("AvailableModels() = %v, want provider/model", got)
	}
	wantKey := providerCacheKeyOf(t, a)
	payload := readPersistentModelCachePayload(t, cachePath)
	if payload["provider_key"] != wantKey {
		t.Fatalf("provider_key = %v, want %q", payload["provider_key"], wantKey)
	}
}

func TestAvailableModels_IgnoresPersistentCacheForProviderMismatch(t *testing.T) {
	dataDir := t.TempDir()
	cachePath := opencodeProjectModelCachePath(dataDir, "demo")
	bin := writeFakeModelsBin(t, []string{"fresh/provider-b"}, 0)

	agent, err := New(map[string]any{
		"cmd":         bin,
		"cc_data_dir": dataDir,
		"cc_project":  "demo",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	a := agent.(*Agent)
	a.SetProviders([]core.ProviderConfig{{Name: "provider-a"}, {Name: "provider-b"}})
	providerASnapshot := func() opencodeModelDiscoverySnapshot {
		a.activeIdx = 0
		providerCacheKeyOf(t, a)
		return a.modelDiscoverySnapshot()
	}()
	writePersistentModelCacheWithSnapshot(t, cachePath, providerASnapshot, []core.ModelOption{{Name: "cached/provider-a"}}, time.Now())
	a.persistentModelCache = &opencodePersistentModelCache{Models: []core.ModelOption{{Name: "cached/provider-a"}}, UpdatedAt: time.Now(), ProviderKey: providerASnapshot.providerKey, ContextKey: modelDiscoveryContextKey(providerASnapshot)}
	if !a.SetActiveProvider("provider-b") {
		t.Fatal("SetActiveProvider(provider-b) = false, want true")
	}

	got := a.AvailableModels(context.Background())
	if len(got) != 1 || got[0].Name != "fresh/provider-b" {
		t.Fatalf("AvailableModels() = %v, want provider-mismatched cache ignored", got)
	}
	cache, err := loadOpencodePersistentModelCache(cachePath)
	if err != nil {
		t.Fatalf("loadOpencodePersistentModelCache(%q) error = %v", cachePath, err)
	}
	if cache == nil || len(cache.Models) != 1 || cache.Models[0].Name != "fresh/provider-b" {
		t.Fatalf("persistent cache after provider mismatch refresh = %v, want fresh/provider-b", cache)
	}
}

func TestAvailableModels_BackgroundRefreshPersistsProviderKey(t *testing.T) {
	dataDir := t.TempDir()
	cachePath := opencodeProjectModelCachePath(dataDir, "demo")
	gatePath := filepath.Join(t.TempDir(), "refresh-ready")
	bin := writeCountingModelsBin(t, "", gatePath, []string{"fresh/model"}, "MODEL_DISCOVERY_TOKEN", 0)

	a := &Agent{
		cmd:            bin,
		workDir:        t.TempDir(),
		modelCachePath: cachePath,
		providers: []core.ProviderConfig{{
			Name: "provider-a",
			Env:  map[string]string{"MODEL_DISCOVERY_TOKEN": "present"},
		}},
		activeIdx: 0,
	}
	providerAKey := providerCacheKeyOf(t, a)
	providerASnapshot := a.modelDiscoverySnapshot()
	writePersistentModelCacheWithSnapshot(t, cachePath, providerASnapshot, []core.ModelOption{{Name: "cached/model"}}, time.Now())
	a.persistentModelCache = &opencodePersistentModelCache{Models: []core.ModelOption{{Name: "cached/model"}}, UpdatedAt: time.Now(), ProviderKey: providerAKey, ContextKey: modelDiscoveryContextKey(providerASnapshot)}

	got := a.AvailableModels(context.Background())
	if len(got) != 1 || got[0].Name != "cached/model" {
		t.Fatalf("AvailableModels() = %v, want cached/model", got)
	}
	if err := os.WriteFile(gatePath, []byte("ok"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", gatePath, err)
	}
	waitForModelsInPersistentCache(t, cachePath, []string{"fresh/model"})
	payload := readPersistentModelCachePayload(t, cachePath)
	if payload["provider_key"] != providerAKey {
		t.Fatalf("provider_key = %v, want %q", payload["provider_key"], providerAKey)
	}
}

func TestAvailableModels_BackgroundRefreshUsesProviderSnapshot(t *testing.T) {
	dataDir := t.TempDir()
	cachePath := opencodeProjectModelCachePath(dataDir, "demo")
	countPath := filepath.Join(t.TempDir(), "refresh-count")
	gatePath := filepath.Join(t.TempDir(), "refresh-ready")
	bin := writeCountingModelsBin(t, countPath, gatePath, []string{"provider-a/model"}, "MODEL_DISCOVERY_TOKEN", 0)
	a := &Agent{
		cmd:            bin,
		workDir:        t.TempDir(),
		modelCachePath: cachePath,
		providers: []core.ProviderConfig{
			{Name: "provider-a", Env: map[string]string{"MODEL_DISCOVERY_TOKEN": "present"}},
			{Name: "provider-b", Env: map[string]string{}},
		},
		activeIdx: 0,
	}
	providerAKey := providerCacheKeyOf(t, a)
	providerASnapshot := a.modelDiscoverySnapshot()
	writePersistentModelCacheWithSnapshot(t, cachePath, providerASnapshot, []core.ModelOption{{Name: "cached/model"}}, time.Now())
	a.persistentModelCache = &opencodePersistentModelCache{Models: []core.ModelOption{{Name: "cached/model"}}, UpdatedAt: time.Now(), ProviderKey: providerAKey, ContextKey: modelDiscoveryContextKey(providerASnapshot)}

	got := a.AvailableModels(context.Background())
	if len(got) != 1 || got[0].Name != "cached/model" {
		t.Fatalf("AvailableModels() = %v, want cached/model", got)
	}
	waitForFileContent(t, countPath, "1")
	if !a.SetActiveProvider("provider-b") {
		t.Fatal("SetActiveProvider(provider-b) = false, want true")
	}
	if err := os.WriteFile(gatePath, []byte("ok"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", gatePath, err)
	}
	waitForModelsInPersistentCache(t, cachePath, []string{"provider-a/model"})
	payload := readPersistentModelCachePayload(t, cachePath)
	if payload["provider_key"] != providerAKey {
		t.Fatalf("provider_key after provider switch = %v, want %q snapshot", payload["provider_key"], providerAKey)
	}
}

func TestAvailableModels_IgnoresPersistentCacheForSameProviderNameDifferentConfig(t *testing.T) {
	dataDir := t.TempDir()
	cachePath := opencodeProjectModelCachePath(dataDir, "demo")
	bin := writeCountingModelsBin(t, "", "", []string{"fresh/provider-a"}, "MODEL_DISCOVERY_TOKEN", 0)
	a := &Agent{
		cmd:            bin,
		workDir:        t.TempDir(),
		modelCachePath: cachePath,
		providers: []core.ProviderConfig{{
			Name:    "provider-a",
			APIKey:  "new-secret",
			BaseURL: "https://new.example",
			Env:     map[string]string{"MODEL_DISCOVERY_TOKEN": "present", "EXTRA": "new"},
		}},
		activeIdx: 0,
	}
	stale := &Agent{providers: []core.ProviderConfig{{Name: "provider-a", APIKey: "old-secret", BaseURL: "https://old.example", Env: map[string]string{"MODEL_DISCOVERY_TOKEN": "old", "EXTRA": "old"}}}, activeIdx: 0}
	staleKey := providerCacheKeyOf(t, stale)
	staleSnapshot := stale.modelDiscoverySnapshot()
	writePersistentModelCacheWithSnapshot(t, cachePath, staleSnapshot, []core.ModelOption{{Name: "cached/stale"}}, time.Now())
	a.persistentModelCache = &opencodePersistentModelCache{Models: []core.ModelOption{{Name: "cached/stale"}}, UpdatedAt: time.Now(), ProviderKey: staleKey, ContextKey: modelDiscoveryContextKey(staleSnapshot)}

	got := a.AvailableModels(context.Background())
	if len(got) != 1 || got[0].Name != "fresh/provider-a" {
		t.Fatalf("AvailableModels() = %v, want stale cache ignored for changed config", got)
	}
	payload := readPersistentModelCachePayload(t, cachePath)
	wantKey := providerCacheKeyOf(t, a)
	if payload["provider_key"] != wantKey {
		t.Fatalf("provider_key = %v, want %q", payload["provider_key"], wantKey)
	}
}

func TestAvailableModels_IgnoresPersistentCacheForWorkDirMismatch(t *testing.T) {
	dataDir := t.TempDir()
	cachePath := opencodeProjectModelCachePath(dataDir, "demo")
	countPath := filepath.Join(t.TempDir(), "refresh-count")
	workDirA := filepath.Join(t.TempDir(), "workspace-a")
	workDirB := filepath.Join(t.TempDir(), "workspace-b")
	if err := os.MkdirAll(workDirA, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(%q) error = %v", workDirA, err)
	}
	if err := os.MkdirAll(workDirB, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(%q) error = %v", workDirB, err)
	}
	bin := writeCountingModelsBin(t, countPath, "", []string{"fresh/workspace-b"}, "MODEL_DISCOVERY_TOKEN", 0)
	a := &Agent{
		cmd:            bin,
		workDir:        workDirB,
		modelCachePath: cachePath,
		providers: []core.ProviderConfig{{
			Name: "provider-a",
			Env:  map[string]string{"MODEL_DISCOVERY_TOKEN": "present"},
		}},
		activeIdx: 0,
	}
	stale := &Agent{
		workDir: workDirA,
		providers: []core.ProviderConfig{{
			Name: "provider-a",
			Env:  map[string]string{"MODEL_DISCOVERY_TOKEN": "present"},
		}},
		activeIdx: 0,
	}
	staleSnapshot := stale.modelDiscoverySnapshot()
	writePersistentModelCacheWithSnapshot(t, cachePath, staleSnapshot, []core.ModelOption{{Name: "cached/workspace-a"}}, time.Now())
	a.persistentModelCache = &opencodePersistentModelCache{
		Models:      []core.ModelOption{{Name: "cached/workspace-a"}},
		UpdatedAt:   time.Now(),
		ProviderKey: staleSnapshot.providerKey,
		ContextKey:  modelDiscoveryContextKey(staleSnapshot),
	}

	got := a.AvailableModels(context.Background())
	if len(got) != 1 || got[0].Name != "fresh/workspace-b" {
		t.Fatalf("AvailableModels() = %v, want stale cache ignored for changed workdir", got)
	}
	waitForFileContent(t, countPath, "1")
	payload := readPersistentModelCachePayload(t, cachePath)
	if payload["provider_key"] != staleSnapshot.providerKey {
		t.Fatalf("provider_key = %v, want %q", payload["provider_key"], staleSnapshot.providerKey)
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
