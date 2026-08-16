package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/timmyagentic/cc-connect-next/config"
	"github.com/timmyagentic/cc-connect-next/core"
)

func loadStarterConfig(t *testing.T) (*config.Config, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := bootstrapConfig(path); err != nil {
		t.Fatalf("bootstrapConfig() error = %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load(bootstrapped config) error = %v", err)
	}
	return cfg, path
}

func TestBootstrapConfig_WritesTheSharedStarterTemplate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := bootstrapConfig(path); err != nil {
		t.Fatalf("bootstrapConfig() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read bootstrap config: %v", err)
	}
	// One definition: the file a new user gets is the starter template the
	// recommended Feishu profile generates, not a second copy of it.
	if string(data) != config.StarterConfigTOML() {
		t.Fatalf("bootstrap config differs from config.StarterConfigTOML():\n--- written ---\n%s\n--- expected ---\n%s", data, config.StarterConfigTOML())
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatalf("stat bootstrap config: %v", err)
	} else if info.Mode().Perm() != 0o600 {
		t.Fatalf("bootstrap config mode = %#o, want 0600", info.Mode().Perm())
	}
}

func TestStarterPlaceholderRefusal_NamesEveryPlaceholderAndItsNextStep(t *testing.T) {
	cfg, path := loadStarterConfig(t)

	msg := starterPlaceholderRefusal(path, config.FindStarterPlaceholders(cfg))
	if msg == "" {
		t.Fatal("a freshly bootstrapped config was accepted; startup would report running with nothing connected")
	}
	if !strings.Contains(msg, path) {
		t.Fatalf("refusal does not say which file to edit:\n%s", msg)
	}
	for _, want := range []string{
		"projects.agent.options.work_dir",
		"projects.platforms.options.app_id",
		"projects.platforms.options.app_secret",
		config.StarterProjectName,
		"cc-connect-next feishu setup",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("refusal is missing %q:\n%s", want, msg)
		}
	}
}

func TestStarterPlaceholderRefusal_AcceptsAnEditedConfig(t *testing.T) {
	doc := strings.NewReplacer(
		config.PlaceholderWorkDir, t.TempDir(),
		config.PlaceholderFeishuAppID, "cli_real_app_id",
		config.PlaceholderFeishuAppSecret, "real-secret",
	).Replace(config.StarterConfigTOML())

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if msg := starterPlaceholderRefusal(path, config.FindStarterPlaceholders(cfg)); msg != "" {
		t.Fatalf("edited config was refused:\n%s", msg)
	}
}

func TestUnusablePlatformWarning_ReportsAConnectionThatFailedAfterStart(t *testing.T) {
	warning := unusablePlatformWarning([]projectPlatformReadiness{
		{project: "alpha", engine: stubStatuses{{Name: "feishu", Ready: true, Err: errors.New("app_id is invalid")}}},
		{project: "beta", engine: stubStatuses{{Name: "telegram", Ready: true}}},
	})
	if warning == "" {
		t.Fatal("a platform that reported ready and then lost its connection produced no warning")
	}
	for _, want := range []string{"alpha", "feishu", "app_id is invalid", "cc-connect-next doctor"} {
		if !strings.Contains(warning, want) {
			t.Fatalf("warning is missing %q:\n%s", want, warning)
		}
	}
	if strings.Contains(warning, "beta") {
		t.Fatalf("warning names a project whose platforms are usable:\n%s", warning)
	}
}

func TestUnusablePlatformWarning_ReportsAPlatformThatNeverConnected(t *testing.T) {
	warning := unusablePlatformWarning([]projectPlatformReadiness{
		{project: "alpha", engine: stubStatuses{{Name: "feishu"}}},
	})
	if !strings.Contains(warning, "never connected") {
		t.Fatalf("warning does not describe a platform that never connected:\n%s", warning)
	}
}

func TestUnusablePlatformWarning_SilentWhenEverythingIsUsable(t *testing.T) {
	warning := unusablePlatformWarning([]projectPlatformReadiness{
		{project: "alpha", engine: stubStatuses{{Name: "feishu", Ready: true}}},
		{project: "beta", engine: stubStatuses{{Name: "slack", Ready: true}}},
	})
	if warning != "" {
		t.Fatalf("warning emitted for a healthy startup: %s", warning)
	}
}

type stubStatuses []core.PlatformStatus

func (s stubStatuses) PlatformStatuses() []core.PlatformStatus { return s }

func configFromTOML(t *testing.T, doc string) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	return cfg
}

func TestInspectWorkDirs_ReportsAPathThatIsNotThere(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-project")
	cfg := configFromTOML(t, `
[[projects]]
name = "alpha"

[projects.agent]
type = "claudecode"

[projects.agent.options]
work_dir = "`+missing+`"

[[projects.platforms]]
type = "feishu"
`)

	problems := inspectWorkDirs(cfg)
	if len(problems) != 1 {
		t.Fatalf("inspectWorkDirs() = %+v, want one problem", problems)
	}
	if problems[0].Project != "alpha" || problems[0].Path != missing {
		t.Fatalf("problem = %+v, want project alpha at %s", problems[0], missing)
	}
	if !strings.Contains(problems[0].Reason, "does not exist") {
		t.Fatalf("problem reason = %q, want it to say the directory is not there", problems[0].Reason)
	}
}

func TestInspectWorkDirs_AcceptsRealDirectoriesAndUnsetPaths(t *testing.T) {
	cfg := configFromTOML(t, `
[[projects]]
name = "alpha"

[projects.agent]
type = "claudecode"

[projects.agent.options]
work_dir = "`+t.TempDir()+`"

[[projects.platforms]]
type = "feishu"

[[projects]]
name = "beta"

[projects.agent]
type = "claudecode"

[[projects.platforms]]
type = "feishu"
`)

	if problems := inspectWorkDirs(cfg); len(problems) != 0 {
		t.Fatalf("inspectWorkDirs() = %+v, want none", problems)
	}
}

func TestInspectWorkDirs_LeavesPlaceholdersToThePlaceholderCheck(t *testing.T) {
	cfg, _ := loadStarterConfig(t)

	// The starter work_dir does not exist either, but reporting it twice
	// buries the one instruction that actually resolves it.
	if problems := inspectWorkDirs(cfg); len(problems) != 0 {
		t.Fatalf("inspectWorkDirs() = %+v, want the placeholder check to own this", problems)
	}
}

func TestInspectWorkDirs_RejectsAFileWhereADirectoryIsExpected(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	cfg := configFromTOML(t, `
[[projects]]
name = "alpha"

[projects.agent]
type = "claudecode"

[projects.agent.options]
work_dir = "`+file+`"

[[projects.platforms]]
type = "feishu"
`)

	problems := inspectWorkDirs(cfg)
	if len(problems) != 1 || !strings.Contains(problems[0].Reason, "not a directory") {
		t.Fatalf("inspectWorkDirs() = %+v, want a not-a-directory problem", problems)
	}
}
