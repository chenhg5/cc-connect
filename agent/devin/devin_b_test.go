package devin

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

const modelsFixture = `{
  "families": [
    {
      "family_label": "SWE-2",
      "family_uid": "swe-2",
      "slug": "swe-2",
      "aliases": ["swe"],
      "variants": [
        {"model_uid": "swe-2-high", "label": "SWE-2 High", "max_context_tokens": 262000},
        {"model_uid": "swe-2-medium", "label": "SWE-2 Medium"}
      ]
    },
    {
      "family_label": "Opus",
      "family_uid": "opus",
      "slug": "opus",
      "variants": [
        {"model_uid": "claude-opus-5-5-high", "label": "Opus High"}
      ]
    }
  ]
}`

func TestInterfaces(t *testing.T) {
	var agent *Agent
	var (
		_ core.ModelSwitcher           = agent
		_ core.ReasoningEffortSwitcher = agent
		_ core.MemoryFileProvider      = agent
		_ core.SkillProvider           = agent
		_ core.ContextCompressor       = agent
		_ core.SessionDeleter          = agent
		_ core.HistoryProvider         = agent
		_ core.ModeSwitcher            = agent
		_ core.WorkDirSwitcher         = agent
		_ core.SessionIDValidator      = agent
		_ core.SessionTitleProvider    = agent
		_ core.AgentDoctorInfo         = agent
	)
}

func TestModeAliases_NormalAndUserOverride(t *testing.T) {
	got := applyDevinDefaults(nil)
	aliases := got["mode_aliases"].(map[string]string)
	if aliases["normal"] != "accept-edits" || aliases["readonly"] != "ask" || aliases["full-auto"] != "bypass" {
		t.Fatalf("defaults = %#v", aliases)
	}
	over := applyDevinDefaults(map[string]any{
		"mode_aliases": map[string]any{"normal": "ask", "custom": "plan"},
	})
	merged := over["mode_aliases"].(map[string]string)
	if merged["normal"] != "ask" || merged["custom"] != "plan" || merged["plan"] != "plan" {
		t.Fatalf("user aliases = %#v", merged)
	}
}

func TestNew_ModeNormalAndStaticModes(t *testing.T) {
	if _, err := exec.LookPath("true"); err != nil {
		t.Skip("true not in PATH")
	}
	a, err := New(map[string]any{"command": "true", "mode": "normal"})
	if err != nil {
		t.Fatal(err)
	}
	agent := a.(*Agent)
	if agent.GetMode() != "accept-edits" {
		t.Fatalf("mode = %q, want accept-edits", agent.GetMode())
	}
	if agent.Name() != "devin" || agent.CLIBinaryName() != "devin" || agent.CompressCommand() != "/compact" {
		t.Fatalf("identity name=%s bin=%s compact=%s", agent.Name(), agent.CLIBinaryName(), agent.CompressCommand())
	}
	modes := agent.PermissionModes()
	if len(modes) != 4 || modes[0].Key != "accept-edits" || modes[0].NameZh != "编码" || modes[3].Name != "Bypass Permissions" {
		t.Fatalf("static modes = %#v", modes)
	}
	live := []core.PermissionModeInfo{{Key: "ask", Name: "Ask", NameZh: "Ask"}}
	if got := permissionModesOrStatic(live); len(got) != 1 || got[0].Key != "ask" {
		t.Fatalf("live modes = %#v", got)
	}

	asked, err := New(map[string]any{
		"command":      "true",
		"mode":         "normal",
		"mode_aliases": map[string]string{"normal": "ask"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if asked.(*Agent).GetMode() != "ask" {
		t.Fatalf("user alias mode = %q", asked.(*Agent).GetMode())
	}
}

func TestMain(m *testing.M) {
	modelsRunnerDefault = func(context.Context, string, []string) ([]byte, error) {
		return nil, errors.New("devin models list disabled in tests")
	}
	dir, err := os.MkdirTemp("", "devin-models-cache-")
	if err != nil {
		panic(err)
	}
	modelsCacheDir = dir
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func isolateModelCache(t *testing.T) {
	t.Helper()
	prev := modelsCacheDir
	modelsCacheDir = t.TempDir()
	t.Cleanup(func() { modelsCacheDir = prev })
}

func waitModelsIdle(t *testing.T, a *Agent) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if a.fetchInFlight() == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("models fetch still in flight")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestParseDevinModels(t *testing.T) {
	opts, families, err := parseDevinModels([]byte(modelsFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(opts) != 2 || len(families) != 2 {
		t.Fatalf("len opts=%d families=%d %#v", len(opts), len(families), opts)
	}
	if opts[0].Name != "swe-2-medium" || opts[0].Desc != "SWE-2" || opts[0].Alias != "swe-2" {
		t.Fatalf("family = %#v", opts[0])
	}
	if opts[1].Name != "claude-opus-5-5-high" || opts[1].Desc != "Opus" || opts[1].Alias != "opus" {
		t.Fatalf("opus family = %#v", opts[1])
	}
	if !containsString(families[0].Variants, "swe-2-high") || !containsString(families[0].Variants, "swe-2-medium") {
		t.Fatalf("variants = %#v", families[0].Variants)
	}
	if !containsString(families[0].Aliases, "swe") {
		t.Fatalf("aliases = %#v", families[0].Aliases)
	}
	dup, dupFamilies, err := parseDevinModels([]byte(`{"families":[
		{"family_label":"A","slug":"same","variants":[{"model_uid":"a-medium","label":"A"}]},
		{"family_label":"B","slug":"same","variants":[{"model_uid":"b-high","label":"B"}]},
		{"family_label":"C","slug":"  ","variants":[{"model_uid":"c-high","label":"C"}]}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(dup) != 3 || dup[0].Alias != "same" || dup[1].Alias != "" || dup[2].Alias != "" {
		t.Fatalf("aliases = %#v", dup)
	}
	if len(dupFamilies) != 3 || !containsString(dupFamilies[1].Variants, "b-high") {
		t.Fatal("duplicate-alias family variant missing from catalog")
	}
	if _, _, err := parseDevinModels([]byte("{")); err == nil {
		t.Fatal("invalid json should fail")
	}
}

func TestAvailableModels_RunnerCacheAndFallback(t *testing.T) {
	if _, err := exec.LookPath("true"); err != nil {
		t.Skip("true not in PATH")
	}
	isolateModelCache(t)
	a, err := New(map[string]any{
		"command": "true",
		"models":  []any{map[string]any{"model": "configured", "alias": "c"}},
		"env":     map[string]string{"XDG_CONFIG_HOME": "/tmp/devin-xdg"},
	})
	if err != nil {
		t.Fatal(err)
	}
	agent := a.(*Agent)
	waitModelsIdle(t, agent)
	var calls int
	var gotBin string
	var sawXDG bool
	agent.SetModelsRunner(func(_ context.Context, binary string, env []string) ([]byte, error) {
		calls++
		gotBin = binary
		for _, e := range env {
			if e == "XDG_CONFIG_HOME=/tmp/devin-xdg" {
				sawXDG = true
			}
		}
		return []byte(modelsFixture), nil
	})
	got := agent.AvailableModels(context.Background())
	if calls != 1 || len(got) != 2 || got[0].Name != "swe-2-medium" || got[1].Alias != "opus" {
		t.Fatalf("calls=%d models=%#v", calls, got)
	}
	if filepath.Base(gotBin) != "devin" || gotBin == "true" {
		t.Fatalf("models binary = %q", gotBin)
	}
	if !sawXDG {
		t.Fatal("agent env was not passed to the models runner")
	}
	_ = agent.AvailableModels(context.Background())
	if calls != 1 {
		t.Fatalf("cache missed, calls=%d", calls)
	}

	agent.modelsCachedAt = agent.modelsCachedAt.Add(-modelsCacheTTL - time.Second)
	agent.SetModelsRunner(func(context.Context, string, []string) ([]byte, error) {
		return nil, errors.New("offline")
	})
	stale := agent.AvailableModels(context.Background())
	if len(stale) != 2 || stale[0].Name != "swe-2-medium" {
		t.Fatalf("stale catalog = %#v", stale)
	}

	custom, err := New(map[string]any{
		"command":    "true",
		"models_cmd": "devin-models-cmd-missing-xyz",
	})
	if err != nil {
		t.Fatal(err)
	}
	var customBin string
	custom.(*Agent).SetModelsRunner(func(_ context.Context, binary string, _ []string) ([]byte, error) {
		customBin = binary
		return []byte(`{"families":[]}`), nil
	})
	_ = custom.(*Agent).AvailableModels(context.Background())
	if customBin != "devin-models-cmd-missing-xyz" {
		t.Fatalf("models_cmd binary = %q", customBin)
	}
}

func TestModelsPrewarm_WaitsOnlyUntilCallerDeadline(t *testing.T) {
	if _, err := exec.LookPath("true"); err != nil {
		t.Skip("true not in PATH")
	}
	isolateModelCache(t)
	a, err := New(map[string]any{
		"command": "true",
		"models":  []any{map[string]any{"model": "configured"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	agent := a.(*Agent)
	waitModelsIdle(t, agent)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	agent.SetModelsRunner(func(ctx context.Context, _ string, _ []string) ([]byte, error) {
		if calls.Add(1) == 1 {
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) < 120*time.Second {
				return nil, errors.New("catalog fetch is not the 150s budget")
			}
			close(started)
			<-release
		}
		return []byte(modelsFixture), nil
	})
	short, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	began := time.Now()
	got := agent.AvailableModels(short)
	cancel()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("catalog fetch did not start")
	}
	if time.Since(began) > time.Second {
		t.Fatal("AvailableModels waited past its deadline")
	}
	if len(got) != 1 || got[0].Name != "configured" {
		t.Fatalf("in-flight fallback = %#v", got)
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		got = agent.AvailableModels(ctx)
		cancel()
		if len(got) == 2 && got[0].Name == "swe-2-medium" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("cached models = %#v", got)
		}
		time.Sleep(5 * time.Millisecond)
	}
	_ = agent.AvailableModels(context.Background())
	if calls.Load() != 1 {
		t.Fatalf("catalog fetch ran %d times", calls.Load())
	}
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func TestEffortUsesLastReportedModel(t *testing.T) {
	if _, err := exec.LookPath("true"); err != nil {
		t.Skip("true not in PATH")
	}
	a, err := New(map[string]any{"command": "true", "reasoning_effort": "high"})
	if err != nil {
		t.Fatal(err)
	}
	agent := a.(*Agent)
	if value, effort := agent.resolveModelValue("", []string{"swe-2-high"}); value != "" || effort != "" {
		t.Fatalf("empty input = %q %q", value, effort)
	}
	agent.RememberReportedModel("swe-2-medium")
	if agent.GetModel() != "swe-2-medium" {
		t.Fatalf("display model = %q", agent.GetModel())
	}
	if _, ok := agent.WorkspaceAgentOptions()["model"]; ok {
		t.Fatal("snapshot stored the reported model")
	}
	if agent.GetReasoningEffort() != "high" {
		t.Fatalf("effort = %q", agent.GetReasoningEffort())
	}
	if _, ok := agent.WorkspaceAgentOptions()["reasoning_effort"]; !ok {
		t.Fatal("configured effort was not snapshotted")
	}
	plain, err := New(map[string]any{"command": "true"})
	if err != nil {
		t.Fatal(err)
	}
	value, effort := plain.(*Agent).resolveModelValue("swe-2-medium", []string{"swe-2-high"})
	if value != "swe-2-high" || effort != "medium" {
		t.Fatalf("base match = %q %q", value, effort)
	}
}

func TestReasoningEffort(t *testing.T) {
	options := []string{"swe-2-high", "claude-opus-5-5-medium", "adaptive", "swe-1-7-lightning-medium"}
	_, families, err := parseDevinModels([]byte(modelsFixture))
	if err != nil {
		t.Fatal(err)
	}
	agent := &Agent{families: families}
	if value, effort := agent.resolveModelValue("swe-2-medium", options); value != "swe-2-high" || effort != "medium" {
		t.Fatalf("suffix = %q %q", value, effort)
	}
	if value, effort := agent.resolveModelValue("adaptive", options); value != "adaptive" || effort != "" {
		t.Fatalf("exact base = %q %q", value, effort)
	}
	if value, effort := agent.resolveModelValue("opus", options); value != "claude-opus-5-5-medium" || effort != "" {
		t.Fatalf("alias = %q %q", value, effort)
	}
	if value, effort := agent.resolveModelValue("swe", options); value != "swe-2-high" || effort != "" {
		t.Fatalf("family alias = %q %q", value, effort)
	}
	if value, effort := agent.resolveModelValue("SWE-2", options); value != "swe-2-high" || effort != "" {
		t.Fatalf("label = %q %q", value, effort)
	}
	if value, effort := agent.resolveModelValue("bogus-model", options); value != "" || effort != "" {
		t.Fatalf("unknown = %q %q", value, effort)
	}
	if _, err := exec.LookPath("true"); err != nil {
		t.Skip("true not in PATH")
	}
	a, err := New(map[string]any{"command": "true", "model": "swe-2-low", "reasoning_effort": "HIGH"})
	if err != nil {
		t.Fatal(err)
	}
	live := a.(*Agent)
	if live.GetModel() != "swe-2-low" || live.GetReasoningEffort() != "high" {
		t.Fatalf("model=%q effort=%q", live.GetModel(), live.GetReasoningEffort())
	}
	efforts := strings.Join(live.AvailableReasoningEfforts(), ",")
	if efforts != "low,medium,high,xhigh,max" {
		t.Fatalf("efforts = %s", efforts)
	}
	if live.CompactCommand != "/compact" {
		t.Fatalf("compact = %q", live.CompactCommand)
	}
	if live.CompactionNotifyMethod != "_cognition.ai/compaction" {
		t.Fatalf("compaction notify = %q", live.CompactionNotifyMethod)
	}
	live.SetReasoningEffort("max")
	if live.GetReasoningEffort() != "max" {
		t.Fatalf("effort = %q", live.GetReasoningEffort())
	}
	live.SetReasoningEffort("nope")
	if live.GetReasoningEffort() != "max" {
		t.Fatalf("unknown effort replaced stored value: %q", live.GetReasoningEffort())
	}
}

func TestAvailableModels_CatalogBeforeSessionOptions(t *testing.T) {
	if _, err := exec.LookPath("true"); err != nil {
		t.Skip("true not in PATH")
	}
	isolateModelCache(t)
	a, err := New(map[string]any{"command": "true"})
	if err != nil {
		t.Fatal(err)
	}
	agent := a.(*Agent)
	waitModelsIdle(t, agent)
	opts, families, err := parseDevinModels([]byte(modelsFixture))
	if err != nil {
		t.Fatal(err)
	}
	agent.storeModels(opts, families)
	agent.SetModelsRunner(func(context.Context, string, []string) ([]byte, error) {
		t.Fatal("fresh catalog must not fetch")
		return nil, nil
	})
	agent.RememberModelOptions("swe-2-high", []core.ModelOption{
		{Name: "swe-2-high", Desc: "SWE-2"},
		{Name: "adaptive", Desc: "Adaptive"},
	})
	got := agent.AvailableModels(context.Background())
	if len(got) != 2 || got[0].Name != "swe-2-medium" || got[1].Alias != "opus" {
		t.Fatalf("catalog = %#v", got)
	}

	agent.mu.Lock()
	agent.modelsCache = nil
	agent.families = nil
	agent.modelsCachedAt = time.Time{}
	agent.mu.Unlock()
	agent.SetModelsRunner(func(context.Context, string, []string) ([]byte, error) {
		return nil, errors.New("offline")
	})
	got = agent.AvailableModels(context.Background())
	if len(got) != 2 || got[0].Name != "swe-2-high" || got[0].Alias != "" || got[1].Name != "adaptive" {
		t.Fatalf("session fallback = %#v", got)
	}
}

func TestDiskCache_StaleLoadsImmediately(t *testing.T) {
	if _, err := exec.LookPath("true"); err != nil {
		t.Skip("true not in PATH")
	}
	dir := t.TempDir()
	prev := modelsCacheDir
	modelsCacheDir = dir
	t.Cleanup(func() { modelsCacheDir = prev })
	bin, err := exec.LookPath("true")
	if err != nil {
		t.Fatal(err)
	}
	xdg := filepath.Join(t.TempDir(), "xdg")
	path := catalogFilePath(bin, xdg, dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(modelsFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-modelsCacheTTL - time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	a, err := New(map[string]any{
		"command":    "true",
		"models_cmd": "true",
		"models":     []any{map[string]any{"model": "configured"}},
		"env":        map[string]string{"XDG_CONFIG_HOME": xdg},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	got := a.(*Agent).AvailableModels(ctx)
	if len(got) != 2 || got[0].Name != "swe-2-medium" {
		t.Fatalf("disk catalog = %#v", got)
	}
}

func TestMemoryAndSkills(t *testing.T) {
	if _, err := exec.LookPath("true"); err != nil {
		t.Skip("true not in PATH")
	}
	work := t.TempDir()
	optHome := filepath.Join(t.TempDir(), "from-opt")
	procHome := filepath.Join(t.TempDir(), "from-env")
	if err := os.MkdirAll(filepath.Join(work, ".devin", "skills", "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(optHome, "devin", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(procHome, "devin", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", procHome)
	a, err := New(map[string]any{
		"command":  "true",
		"work_dir": work,
		"env":      map[string]string{"XDG_CONFIG_HOME": optHome},
	})
	if err != nil {
		t.Fatal(err)
	}
	agent := a.(*Agent)
	mem := agent.ProjectMemoryFile()
	if filepath.Base(mem) != "AGENTS.md" || !strings.Contains(mem, filepath.Base(work)) {
		t.Fatalf("project memory = %q", mem)
	}
	global := agent.GlobalMemoryFile()
	if global != filepath.Join(optHome, "devin", "AGENTS.md") {
		t.Fatalf("global memory = %q", global)
	}
	dirs := agent.SkillDirs()
	joined := strings.Join(dirs, "\n")
	if !strings.Contains(joined, filepath.Join(agent.absWorkDir(), ".devin", "skills")) {
		t.Fatalf("missing project skill dir in %q", joined)
	}
	if !strings.Contains(joined, filepath.Join(optHome, "devin", "skills")) {
		t.Fatalf("missing xdg skill dir in %q", joined)
	}
	if strings.Contains(joined, procHome) {
		t.Fatalf("process XDG leaked into %q", joined)
	}
	if strings.Contains(joined, filepath.Join(agent.absWorkDir(), ".agents", "skills")) {
		t.Fatalf("missing dir was returned: %q", joined)
	}
}

func TestWorkspaceOptionsRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("true"); err != nil {
		t.Skip("true not in PATH")
	}
	a, err := New(map[string]any{
		"command":          "true",
		"model":            "swe-2-medium",
		"reasoning_effort": "high",
		"models_cmd":       "devin",
		"mode_aliases":     map[string]string{"normal": "ask"},
	})
	if err != nil {
		t.Fatal(err)
	}
	opts := a.(*Agent).WorkspaceAgentOptions()
	clone, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	got := clone.(*Agent)
	if got.GetModel() != "swe-2-medium" || got.GetReasoningEffort() != "high" {
		t.Fatalf("clone model=%q effort=%q", got.GetModel(), got.GetReasoningEffort())
	}
	got.SetMode("normal")
	if got.GetMode() != "ask" {
		t.Fatalf("clone alias mode = %q", got.GetMode())
	}
	if opts["models_cmd"] != "devin" {
		t.Fatalf("models_cmd = %#v", opts["models_cmd"])
	}
	if opts["compaction_notify_method"] != "_cognition.ai/compaction" {
		t.Fatalf("compaction_notify_method = %#v", opts["compaction_notify_method"])
	}
	if got.CompactionNotifyMethod != "_cognition.ai/compaction" {
		t.Fatalf("clone notify = %q", got.CompactionNotifyMethod)
	}
}

// TestNew_WrapperCmdProbesRealDevin: when cmd is a wrapper script, probes must
// go straight to the real `devin acp` so a slow wrapper preflight cannot make
// /list, /history or /delete time out.
func TestNew_WrapperCmdProbesRealDevin(t *testing.T) {
	devinPath, err := exec.LookPath("devin")
	if err != nil {
		t.Skip("devin not in PATH")
	}
	a, err := New(map[string]any{"command": "true"})
	if err != nil {
		t.Fatal(err)
	}
	if got := a.(*Agent).ProbeCommand; got != devinPath {
		t.Fatalf("ProbeCommand = %q, want %q", got, devinPath)
	}
	d, err := New(map[string]any{"command": devinPath})
	if err != nil {
		t.Fatal(err)
	}
	if got := d.(*Agent).ProbeCommand; got != "" {
		t.Fatalf("ProbeCommand = %q, want empty when cmd is devin itself", got)
	}
	w, err := New(map[string]any{"cmd": "true"})
	if err != nil {
		t.Fatal(err)
	}
	if got := w.(*Agent).ProbeCommand; got != devinPath {
		t.Fatalf("cmd= wrapper: ProbeCommand = %q, want %q", got, devinPath)
	}
}
