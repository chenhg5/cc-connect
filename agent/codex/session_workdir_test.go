package codex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestAgentGetSessionWorkDir_ResolvesBeforeStartWithoutChangingDefault(t *testing.T) {
	for _, latestTurn := range []bool{false, true} {
		name := "session_metadata"
		if latestTurn {
			name = "latest_turn"
		}
		t.Run(name, func(t *testing.T) {
			home, configured, original, latest := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
			body := resumeWorkDirRecord(t, "session_meta", map[string]any{
				"id": "saved-chat", "cwd": original, "source": "exec", "originator": "codex_exec",
			})
			want := original
			if latestTurn {
				body += resumeWorkDirRecord(t, "turn_context", map[string]any{"cwd": latest})
				want = latest
			}
			path := writeResumeWorkDirRollout(t, home, "rollout-saved-chat.jsonl", body)
			// An unavailable app-server binary makes any accidental process
			// startup fail; directory lookup only needs the persisted rollout.
			a := &Agent{
				workDir: configured, codexHome: home, listAllSessions: true,
				backend: "app_server", cmd: filepath.Join(t.TempDir(), "missing-codex"), activeIdx: -1,
			}
			got, err := a.GetSessionWorkDir(context.Background(), "saved-chat", "")
			if err != nil || got != want {
				t.Fatalf("saved session directory = %q, %v; want %q", got, err, want)
			}
			if got := a.GetWorkDir(); got != configured {
				t.Fatalf("default directory changed to %q, want %q", got, configured)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != body {
				t.Fatalf("lookup changed the rollout: %q, %v", data, err)
			}
		})
	}
}

func TestAgentGetSessionWorkDir_UsesEffectiveProfileWithoutCrossingProfiles(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("USERPROFILE", root)
	homes, dirs := map[string]string{}, map[string]string{}
	for _, source := range []string{"process", "project", "provider", "session", "explicit", "default"} {
		home := filepath.Join(root, source)
		if source == "default" {
			home = filepath.Join(root, ".codex")
		}
		homes[source], dirs[source] = home, t.TempDir()
		// The same ID in each profile must resolve to that profile's directory.
		writeAllSessionsRollout(t, home, "shared-chat", dirs[source], "cli")
		writeAllSessionsRollout(t, home, "only-"+source, dirs[source], "cli")
	}
	t.Setenv("CODEX_HOME", homes["process"])
	for _, tc := range []struct {
		name, project, provider, session, explicit, want string
		clear                                            bool
	}{
		{name: "process", want: "process"},
		{name: "project", project: "project", want: "project"},
		{name: "provider", project: "project", provider: "provider", want: "provider"},
		{name: "session", project: "project", provider: "provider", session: "session", want: "session"},
		{name: "explicit", project: "project", provider: "provider", session: "session", explicit: "explicit", want: "explicit"},
		{name: "cleared", project: "project", clear: true, want: "default"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configured := t.TempDir()
			a := &Agent{workDir: configured, listAllSessions: true, activeIdx: -1}
			if tc.project != "" {
				a.configEnv = []string{"CODEX_HOME=" + homes[tc.project]}
			}
			if tc.provider != "" {
				a.SetProviders([]core.ProviderConfig{{Name: "test-provider", Env: map[string]string{"CODEX_HOME": homes[tc.provider]}}})
				if !a.SetActiveProvider("test-provider") {
					t.Fatal("provider not found")
				}
			}
			if tc.session != "" {
				a.SetSessionEnv([]string{"CODEX_HOME=" + homes[tc.session]})
			}
			if tc.explicit != "" {
				a.codexHome = homes[tc.explicit]
			}
			if tc.clear {
				a.SetSessionEnv([]string{"CODEX_HOME="})
			}
			got, err := a.GetSessionWorkDir(context.Background(), "shared-chat", "")
			if err != nil || got != dirs[tc.want] {
				t.Fatalf("effective profile directory = %q, %v; want %q", got, err, dirs[tc.want])
			}
			other := "process"
			if tc.want == other {
				other = "project"
			}
			if got, err := a.GetSessionWorkDir(context.Background(), "only-"+other, ""); err == nil || got != "" {
				t.Fatalf("lookup escaped the effective profile: %q, %v", got, err)
			}
			if a.GetWorkDir() != configured {
				t.Fatal("profile lookup changed the configured directory")
			}
		})
	}
}

func TestAgentGetSessionWorkDir_PinsRelativeProfileBeforeChangingDirectory(t *testing.T) {
	root, configured, saved := t.TempDir(), t.TempDir(), t.TempDir()
	t.Chdir(root)
	home := filepath.Join(root, "relative-profile")
	writeAllSessionsRollout(t, home, "saved-chat", saved, "cli")
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "environment", true: "explicit"}[explicit], func(t *testing.T) {
			a := &Agent{workDir: configured, listAllSessions: true, activeIdx: -1}
			if explicit {
				a.codexHome = "relative-profile"
				t.Setenv("CODEX_HOME", filepath.Join(root, "wrong-profile"))
			} else {
				t.Setenv("CODEX_HOME", "relative-profile")
			}
			got, err := a.GetSessionWorkDir(context.Background(), "saved-chat", "")
			if err != nil || got != saved {
				t.Fatalf("relative profile directory = %q, %v; want %q", got, err, saved)
			}
		})
	}
}

func TestAgentGetSessionWorkDir_NoOverrideWithoutSelectedSessionOrOptIn(t *testing.T) {
	for _, tc := range []struct {
		name, id string
		enabled  bool
	}{
		{name: "disabled", id: "saved-chat"},
		{name: "new_session", enabled: true},
		{name: "continue_sentinel", id: core.ContinueSession, enabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &Agent{workDir: t.TempDir(), codexHome: filepath.Join(t.TempDir(), "missing-profile"), listAllSessions: tc.enabled}
			got, err := a.GetSessionWorkDir(context.Background(), tc.id, "")
			if err != nil || got != "" {
				t.Fatalf("unselected/disabled directory override = %q, %v; want none", got, err)
			}
		})
	}
}

func TestAgentGetSessionWorkDir_RejectsMissingOrInvalidSavedDirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "ordinary-file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, cwd, wantError string
	}{
		{name: "missing_session", wantError: "not found"},
		{name: "missing_cwd", wantError: "absolute working directory"},
		{name: "relative_cwd", cwd: "relative/project", wantError: "absolute working directory"},
		{name: "deleted_cwd", cwd: filepath.Join(t.TempDir(), "deleted"), wantError: "unavailable"},
		{name: "file_cwd", cwd: file, wantError: "not a directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, configured := t.TempDir(), t.TempDir()
			id := "saved-chat"
			if tc.name == "missing_session" {
				id = "unrelated-chat"
			}
			writeAllSessionsRollout(t, home, id, tc.cwd, "cli")
			a := &Agent{workDir: configured, codexHome: home, listAllSessions: true}
			got, err := a.GetSessionWorkDir(context.Background(), "saved-chat", "")
			if got != "" || err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("directory lookup = %q, %v; want error containing %q", got, err, tc.wantError)
			}
			if a.GetWorkDir() != configured {
				t.Fatal("failed lookup changed the configured directory")
			}
		})
	}
}

func TestAgentGetSessionWorkDir_RespectsCanceledContext(t *testing.T) {
	home, saved := t.TempDir(), t.TempDir()
	writeAllSessionsRollout(t, home, "saved-chat", saved, "cli")
	a := &Agent{codexHome: home, listAllSessions: true}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := a.GetSessionWorkDir(ctx, "saved-chat", "")
	if got != "" || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled lookup = %q, %v; want context.Canceled", got, err)
	}
}

func TestAgentGetSessionWorkDir_SavedProviderDoesNotChangeActiveProvider(t *testing.T) {
	homes, dirs := map[string]string{}, map[string]string{}
	for _, name := range []string{"project", "active", "saved", "session", "explicit"} {
		homes[name], dirs[name] = t.TempDir(), t.TempDir()
		writeAllSessionsRollout(t, homes[name], "shared-chat", dirs[name], "cli")
	}
	for _, tc := range []struct {
		name, provider, session, explicit, want, wantDefault string
	}{
		{name: "saved_provider", provider: "saved", want: "saved", wantDefault: "active"},
		{name: "project_env_without_provider_override", want: "project", wantDefault: "active"},
		{name: "session_env", provider: "saved", session: "session", want: "session", wantDefault: "session"},
		{name: "explicit_home", provider: "saved", session: "session", explicit: "explicit", want: "explicit", wantDefault: "explicit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configured := t.TempDir()
			a := &Agent{workDir: configured, listAllSessions: true, configEnv: []string{"CODEX_HOME=" + homes["project"]}, activeIdx: -1}
			saved := core.ProviderConfig{Name: "saved"}
			if tc.provider != "" {
				saved.Env = map[string]string{"CODEX_HOME": homes[tc.provider]}
			}
			a.SetProviders([]core.ProviderConfig{{Name: "active", Env: map[string]string{"CODEX_HOME": homes["active"]}}, saved})
			if !a.SetActiveProvider("active") {
				t.Fatal("active provider not found")
			}
			if tc.session != "" {
				a.SetSessionEnv([]string{"CODEX_HOME=" + homes[tc.session]})
			}
			if tc.explicit != "" {
				a.codexHome = homes[tc.explicit]
			}
			got, err := a.GetSessionWorkDir(context.Background(), "shared-chat", "saved")
			if err != nil || got != dirs[tc.want] {
				t.Fatalf("saved provider directory = %q, %v; want %q", got, err, dirs[tc.want])
			}
			if active := a.GetActiveProvider(); active == nil || active.Name != "active" {
				t.Fatalf("lookup changed active provider: %v", active)
			}
			if a.GetWorkDir() != configured {
				t.Fatal("lookup changed the shared default directory")
			}
			got, err = a.GetSessionWorkDir(context.Background(), "shared-chat", "")
			if err != nil || got != dirs[tc.wantDefault] {
				t.Fatalf("default profile changed after lookup: %q, %v; want %q", got, err, dirs[tc.wantDefault])
			}
		})
	}
}

func TestAgentGetSessionWorkDir_UnknownSavedProviderDoesNotUseDefaultProfile(t *testing.T) {
	home, configured := t.TempDir(), t.TempDir()
	writeAllSessionsRollout(t, home, "shared-chat", configured, "cli")
	a := &Agent{workDir: configured, codexHome: home, listAllSessions: true, activeIdx: -1}
	got, err := a.GetSessionWorkDir(context.Background(), "shared-chat", "removed-provider")
	if got != "" || err == nil || !strings.Contains(err.Error(), "removed-provider") {
		t.Fatalf("unknown provider lookup = %q, %v; want an error identifying the saved provider", got, err)
	}
	if a.GetActiveProvider() != nil || a.GetWorkDir() != configured {
		t.Fatal("failed saved-provider lookup changed the agent defaults")
	}
}
