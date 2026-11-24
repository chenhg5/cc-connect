package antigravity

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

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

func TestGetModel_PrefersActiveProviderModel(t *testing.T) {
	a := &Agent{
		model: "gemini-2.5-flash",
		providers: []core.ProviderConfig{
			{Name: "google", Model: "gemini-2.5-pro"},
		},
		activeIdx: 0,
	}

	if got := a.GetModel(); got != "gemini-2.5-pro" {
		t.Fatalf("GetModel() = %q, want gemini-2.5-pro", got)
	}
}

func TestAvailableModelsUsesAgySubscriptionCatalog(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	cmd := filepath.Join(t.TempDir(), "agy")
	if err := os.WriteFile(cmd, []byte("#!/bin/sh\nprintf 'gemini-pro-orchestrator-neon\tOrchestrator (Neon)\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	a := &Agent{cmd: cmd, activeIdx: -1, workDir: t.TempDir()}
	got := a.AvailableModels(context.Background())
	if len(got) != 1 || got[0].Name != "gemini-pro-orchestrator-neon" {
		t.Fatalf("subscription catalog = %+v", got)
	}
}

func TestValidateModelRejectsInvalidAndAllowsEffortBaseIDs(t *testing.T) {
	a := &Agent{activeIdx: 0, providers: []core.ProviderConfig{{Models: []core.ModelOption{{Name: "gemini-4-neon-high", Desc: "Gemini 4 Neon (High)"}, {Name: "gemini-pro-orchestrator-neon", Desc: "Orchestrator (Neon)"}}}}}
	for _, id := range []string{"gemini-4-neon", "Gemini 4 Neon (High)", "gemini-pro-orchestrator-neon"} {
		if err := a.ValidateModel(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.ValidateModel(context.Background(), "nonexistent-model"); err == nil {
		t.Fatal("accepted invalid model")
	}
	a.effort = "high"
	a.SetModel("gemini-pro-orchestrator-neon")
	if a.GetReasoningEffort() != "" || len(a.AvailableReasoningEfforts()) != 0 {
		t.Fatal("orchestrator kept effort")
	}
}

func TestOrchestratorSettingsAndStaleEffortReachTheSession(t *testing.T) {
	a := &Agent{cmd: "echo", workDir: t.TempDir(), model: "gemini-pro-orchestrator-neon", effort: "high", planner: "Gemini 4 Neon (High)", executor: "Gemini 4 Hydrogen (High)", mode: "yolo", activeIdx: -1}
	session, err := a.StartSession(context.Background(), "long-native-session")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Errorf("close test session: %v", err)
		}
	})
	s := session.(*antigravitySession)
	args := s.buildAntigravityArgs("long-native-session", true, "yolo", "", "continue")
	for _, arg := range args {
		if arg == "--effort" {
			t.Fatal("orchestrator got stale effort")
		}
	}
	for i, arg := range args {
		if arg == "--settings" && i+1 < len(args) {
			var settings map[string]map[string]string
			if err := json.Unmarshal([]byte(args[i+1]), &settings); err != nil {
				t.Fatal(err)
			}
			if settings["orchestrator"]["planner"] != "Gemini 4 Neon (High)" || settings["orchestrator"]["executor"] != "Gemini 4 Hydrogen (High)" {
				t.Fatalf("settings=%v", settings)
			}
			return
		}
	}
	t.Fatal("missing orchestrator settings")
}

func TestSessionEffortForEffectiveModel(t *testing.T) {
	for _, tc := range []struct {
		name   string
		model  string
		effort string
	}{
		{name: "bare Orchestrator", model: "gemini-pro-orchestrator"},
		{name: "suffixed Orchestrator", model: "gemini-pro-orchestrator-neon"},
		{name: "Orchestrator display label", model: "Orchestrator (Carbon)"},
		{name: "ordinary model", model: "gemini-4-neon", effort: "high"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &Agent{cmd: "echo", workDir: t.TempDir(), model: tc.model, effort: "high", activeIdx: -1}
			if got := len(a.AvailableReasoningEfforts()) > 0; got != (tc.effort != "") {
				t.Errorf("offers effort = %t, want %t", got, tc.effort != "")
			}
			session, err := a.StartSession(context.Background(), "")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := session.Close(); err != nil {
					t.Errorf("close test session: %v", err)
				}
			})
			args := session.(*antigravitySession).buildAntigravityArgs("", true, "yolo", "", "continue")
			got := ""
			for i, arg := range args {
				if arg == "--effort" {
					if i+1 == len(args) {
						t.Fatal("--effort has no value")
					}
					got = args[i+1]
					break
				}
			}
			if got != tc.effort {
				t.Errorf("CLI effort = %q, want %q", got, tc.effort)
			}
		})
	}
}

func TestValidateModelWithoutCatalogDefersToCLI(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	for _, tc := range []struct {
		name, script string
	}{
		{name: "older CLI shows labels only", script: "printf 'Gemini 4 Neon (High)\n'"},
		{name: "CLI unavailable", script: "exit 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			cmd := filepath.Join(dir, "agy")
			if err := os.WriteFile(cmd, []byte("#!/bin/sh\n"+tc.script+"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			a := &Agent{cmd: cmd, activeIdx: -1, workDir: dir}
			if err := a.ValidateModel(context.Background(), "Gemini 4 Neon (High)"); err != nil {
				t.Fatalf("cannot accept a manual model without a catalog: %v", err)
			}
			for _, unsafe := range []string{"--help", "model\nname"} {
				if err := a.ValidateModel(context.Background(), unsafe); err == nil {
					t.Errorf("accepted malformed model %q", unsafe)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := a.ValidateModel(ctx, "Gemini 4 Neon (High)"); err != context.Canceled {
				t.Errorf("canceled validation = %v, want context.Canceled", err)
			}
		})
	}
}

func TestValidateModelSlowCatalogDoesNotConsumeSwitchDeadline(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	dir := t.TempDir()
	cmd := filepath.Join(dir, "agy")
	if err := os.WriteFile(cmd, []byte("#!/bin/sh\nexec sleep 20\n"), 0700); err != nil {
		t.Fatal(err)
	}
	a := &Agent{cmd: cmd, activeIdx: -1, workDir: dir}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := a.ValidateModel(ctx, "Gemini 4 Neon (High)"); err != nil {
		t.Fatalf("slow catalog blocked manual model switch: %v", err)
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("catalog consumed caller's deadline: %v", err)
	}
}

// The wrapper waits for a child that inherits stdout. Killing the wrapper
// leaves the child's descriptor open, unlike a wrapper that execs sleep.
func TestValidateModelBoundsDescendantHeldCatalogPipe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test requires a POSIX shell")
	}
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")

	for _, tc := range []struct {
		name   string
		cancel bool
	}{
		{name: "manual switch after catalog timeout"},
		{name: "caller cancellation", cancel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			cmd := filepath.Join(dir, "agy")
			script := `#!/bin/sh
(
	: > child-ready
	i=0
	while [ ! -f release-child ] && [ "$i" -lt 120 ]; do
		sleep 0.05
		i=$((i+1))
	done
	: > child-done
) &
printf 'some-other-model\tOther Model\n'
wait
`
			if err := os.WriteFile(cmd, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.WriteFile(filepath.Join(dir, "release-child"), nil, 0600); err != nil {
					t.Error(err)
					return
				}
				awaitCatalogFixtureFile(t, dir, "child-done")
			})

			a := &Agent{cmd: cmd, activeIdx: -1, workDir: dir}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			result := make(chan error, 1)
			start := time.Now()
			go func() { result <- a.ValidateModel(ctx, "manual-model") }()
			awaitCatalogFixtureFile(t, dir, "child-ready")
			if tc.cancel {
				cancel()
			}
			err := <-result
			if tc.cancel {
				if err != context.Canceled {
					t.Fatalf("canceled validation = %v, want context.Canceled", err)
				}
			} else if err != nil || ctx.Err() != nil {
				t.Fatalf("catalog blocked manual switching: validation = %v, caller = %v", err, ctx.Err())
			}
			if elapsed := time.Since(start); elapsed >= 5*time.Second {
				t.Fatalf("catalog waited %v for a descendant to close stdout", elapsed)
			}
		})
	}
}

func awaitCatalogFixtureFile(t *testing.T, dir, name string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("fixture did not create %s", name)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
