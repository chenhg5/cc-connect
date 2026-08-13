package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRunMigrateCommandReturnsFailureForMissingSource(t *testing.T) {
	root := t.TempDir()
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := runMigrateCommand([]string{
		"--source", filepath.Join(root, "missing"),
		"--target", filepath.Join(root, ".cc-connect-next"),
	}, &stdout, &stderr)

	if code != 1 {
		t.Fatalf("runMigrateCommand() code = %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "migrate: read source directory") {
		t.Fatalf("stderr = %q, want source error", stderr.String())
	}
}

func TestRunMigrateCommandHelpReturnsSuccess(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if code := runMigrateCommand([]string{"--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("runMigrateCommand(--help) code = %d, want 0", code)
	}
	if !strings.Contains(stderr.String(), "Usage: cc-connect-next migrate") {
		t.Fatalf("help output = %q, want usage", stderr.String())
	}
}

func TestMigrateLegacyDataCopiesPersistentStateAndIsolatesRuntime(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, ".cc-connect-next")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), `data_dir = "`+filepath.ToSlash(source)+`"
language = "zh"

[[projects]]
name = "demo"
[projects.agent]
type = "codex"
[projects.agent.options]
api_key = "keep-this-secret"
`)
	writeMigrationFixture(t, filepath.Join(source, "sessions", "demo.json"), `{"session":"kept"}`)
	writeMigrationFixture(t, filepath.Join(source, "projects", "demo.state.json"), `{"state":"kept"}`)
	writeMigrationFixture(t, filepath.Join(source, "config", "minimax.json"), `{"token":"kept"}`)
	writeMigrationFixture(t, filepath.Join(source, "run", "api.sock"), "volatile")
	writeMigrationFixture(t, filepath.Join(source, "logs", "cc-connect.log"), "volatile")
	writeMigrationFixture(t, filepath.Join(source, "daemon.json"), `{"pid":1}`)

	report, err := migrateLegacyData(source, target, false, false)
	if err != nil {
		t.Fatalf("migrateLegacyData() error = %v", err)
	}
	if report.CopiedFiles != 4 {
		t.Fatalf("copied files = %d, want 4", report.CopiedFiles)
	}

	configBytes, err := os.ReadFile(filepath.Join(target, "config.toml"))
	if err != nil {
		t.Fatalf("read migrated config: %v", err)
	}
	configText := string(configBytes)
	canonicalTarget, err := canonicalDestinationPath(target)
	if err != nil {
		t.Fatalf("canonical target: %v", err)
	}
	if !strings.Contains(configText, `data_dir = "`+filepath.ToSlash(canonicalTarget)+`"`) {
		t.Fatalf("migrated config does not use isolated target data_dir: %q", configText)
	}
	if !strings.Contains(configText, "keep-this-secret") {
		t.Fatalf("migrated config lost existing values: %q", configText)
	}
	if info, err := os.Stat(filepath.Join(target, "config.toml")); err != nil {
		t.Fatalf("stat migrated config: %v", err)
	} else if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("migrated config mode = %#o, want 0600", got)
	}

	for _, rel := range []string{"sessions/demo.json", "projects/demo.state.json", "config/minimax.json"} {
		if _, err := os.Stat(filepath.Join(target, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("persistent file %s was not copied: %v", rel, err)
		}
	}
	for _, rel := range []string{"run", "logs", "daemon.json"} {
		if _, err := os.Stat(filepath.Join(target, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Fatalf("runtime path %s should not be migrated, err=%v", rel, err)
		}
	}
}

func TestMigrateLegacyDataRefusesExistingTargetWithoutForce(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, ".cc-connect-next")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), "language = \"zh\"\n")
	writeMigrationFixture(t, filepath.Join(target, "keep.txt"), "do not overwrite")

	if _, err := migrateLegacyData(source, target, false, false); err == nil {
		t.Fatal("migrateLegacyData() error = nil, want existing-target refusal")
	}
	got, err := os.ReadFile(filepath.Join(target, "keep.txt"))
	if err != nil || string(got) != "do not overwrite" {
		t.Fatalf("existing target was modified: content=%q err=%v", got, err)
	}
}

func TestMigrateLegacyDataDryRunDoesNotWrite(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, ".cc-connect-next")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), "language = \"zh\"\n")
	writeMigrationFixture(t, filepath.Join(source, "sessions", "demo.json"), `{}`)

	report, err := migrateLegacyData(source, target, false, true)
	if err != nil {
		t.Fatalf("dry-run error = %v", err)
	}
	if !report.DryRun || report.CopiedFiles != 2 {
		t.Fatalf("dry-run report = %+v, want two planned files", report)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("dry-run created target, err=%v", err)
	}
}

func TestMigrateLegacyDataSkipsSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated privileges")
	}
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, ".cc-connect-next")
	outside := filepath.Join(root, "outside-secret")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), "language = \"zh\"\n")
	writeMigrationFixture(t, outside, "must-not-copy")
	if err := os.Symlink(outside, filepath.Join(source, "linked-secret")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	report, err := migrateLegacyData(source, target, false, false)
	if err != nil {
		t.Fatalf("migrateLegacyData() error = %v", err)
	}
	if report.SkippedSymlinks != 1 {
		t.Fatalf("skipped symlinks = %d, want 1", report.SkippedSymlinks)
	}
	if _, err := os.Stat(filepath.Join(target, "linked-secret")); !os.IsNotExist(err) {
		t.Fatalf("symlink target should not be copied, err=%v", err)
	}
}

func TestMigrateLegacyDataRefusesSymlinkTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated privileges")
	}
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, ".cc-connect-next")
	outside := filepath.Join(root, "existing-next-data")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), "language = \"zh\"\n")
	writeMigrationFixture(t, filepath.Join(outside, "keep.txt"), "must-remain-untouched")
	if err := os.Symlink(outside, target); err != nil {
		t.Fatalf("symlink target: %v", err)
	}

	if _, err := migrateLegacyData(source, target, true, false); err == nil || !strings.Contains(err.Error(), "must not be a symbolic link") {
		t.Fatalf("migrateLegacyData() error = %v, want symlink-target refusal", err)
	}
	if got, err := os.ReadFile(filepath.Join(outside, "keep.txt")); err != nil || string(got) != "must-remain-untouched" {
		t.Fatalf("symlink destination was modified: content=%q err=%v", got, err)
	}
	if info, err := os.Lstat(target); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("target symlink was replaced: info=%v err=%v", info, err)
	}
}

func TestMigrateLegacyDataIncludesCustomDataDirAndProjectLocalData(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	customData := filepath.Join(root, "official-state")
	target := filepath.Join(root, ".cc-connect-next")
	workDir := filepath.Join(root, "project")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), `data_dir = "`+filepath.ToSlash(customData)+`"

[[projects]]
name = "demo"
[projects.agent]
type = "codex"
[projects.agent.options]
work_dir = "`+filepath.ToSlash(workDir)+`"
`)
	writeMigrationFixture(t, filepath.Join(source, "backups", "config.toml.bak"), "backup")
	writeMigrationFixture(t, filepath.Join(customData, "sessions", "demo.json"), `{"session":"custom"}`)
	writeMigrationFixture(t, filepath.Join(customData, "crons", "jobs.json"), `[{"id":"cron"}]`)
	writeMigrationFixture(t, filepath.Join(customData, "workspace_bindings.json"), `{}`)
	writeMigrationFixture(t, filepath.Join(workDir, ".cc-connect", "images", "input.png"), "image-bytes")
	writeMigrationFixture(t, filepath.Join(workDir, ".cc-connect", "attachments", "prompt.txt"), "attachment")

	report, err := migrateLegacyData(source, target, false, false)
	if err != nil {
		t.Fatalf("migrateLegacyData() error = %v", err)
	}
	canonicalData, err := canonicalExistingDirectory(customData)
	if err != nil {
		t.Fatalf("canonical data dir: %v", err)
	}
	if got, want := report.SourceDataDir, canonicalData; got != want {
		t.Fatalf("source data dir = %q, want %q", got, want)
	}
	if report.ProjectDirectories != 1 {
		t.Fatalf("project directories = %d, want 1", report.ProjectDirectories)
	}

	for _, rel := range []string{
		"backups/config.toml.bak",
		"sessions/demo.json",
		"crons/jobs.json",
		"workspace_bindings.json",
	} {
		if _, err := os.Stat(filepath.Join(target, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("persistent file %s was not copied: %v", rel, err)
		}
	}
	for _, rel := range []string{"images/input.png", "attachments/prompt.txt"} {
		migrated := filepath.Join(workDir, ".cc-connect-next", filepath.FromSlash(rel))
		if _, err := os.Stat(migrated); err != nil {
			t.Fatalf("project-local file %s was not copied: %v", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(workDir, ".cc-connect", "images", "input.png")); err != nil {
		t.Fatalf("official project-local data was modified: %v", err)
	}

	manifestBytes, err := os.ReadFile(filepath.Join(target, migrationManifestFilename))
	if err != nil {
		t.Fatalf("read migration manifest: %v", err)
	}
	var manifest migrationManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("parse migration manifest: %v", err)
	}
	if manifest.SourceDataDir != canonicalData || len(manifest.Files) < 7 {
		t.Fatalf("manifest does not inventory the complete migration: %+v", manifest)
	}
	for _, file := range manifest.Files {
		if file.SHA256 == "" || file.Size < 0 {
			t.Fatalf("manifest file is missing verification metadata: %+v", file)
		}
	}
}

func TestMigrateLegacyDataResolvesRelativePathsFromOfficialRuntimeWorkDir(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, ".cc-connect-next")
	runtimeWorkDir := filepath.Join(root, "official-runtime")
	projectDir := filepath.Join(runtimeWorkDir, "project")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), `data_dir = "state"

[[projects]]
name = "demo"
[projects.agent]
type = "codex"
[projects.agent.options]
work_dir = "project"
`)
	writeMigrationFixture(t, filepath.Join(source, "daemon.json"), `{"work_dir":"`+filepath.ToSlash(runtimeWorkDir)+`"}`)
	writeMigrationFixture(t, filepath.Join(runtimeWorkDir, "state", "sessions", "demo.json"), "relative-state")
	writeMigrationFixture(t, filepath.Join(projectDir, ".cc-connect", "images", "relative.png"), "relative-project-data")

	report, err := migrateLegacyData(source, target, false, false)
	if err != nil {
		t.Fatalf("migrateLegacyData() error = %v", err)
	}
	canonicalRuntime, err := canonicalExistingDirectory(runtimeWorkDir)
	if err != nil {
		t.Fatalf("canonical runtime work dir: %v", err)
	}
	if report.SourceWorkDir != canonicalRuntime {
		t.Fatalf("source runtime work_dir = %q, want %q", report.SourceWorkDir, canonicalRuntime)
	}
	if got, err := os.ReadFile(filepath.Join(target, "sessions", "demo.json")); err != nil || string(got) != "relative-state" {
		t.Fatalf("relative data_dir state was not migrated: content=%q err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(projectDir, ".cc-connect-next", "images", "relative.png")); err != nil || string(got) != "relative-project-data" {
		t.Fatalf("relative project work_dir data was not migrated: content=%q err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(target, "daemon.json")); !os.IsNotExist(err) {
		t.Fatalf("runtime daemon metadata should not be copied, err=%v", err)
	}
}

func TestMigrateLegacyDataDiscoversProjectDataFromStateAndBindings(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, ".cc-connect-next")
	stateProject := filepath.Join(root, "state-project")
	bindingProject := filepath.Join(root, "binding-project")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), "language = \"zh\"\n")
	writeMigrationFixture(t, filepath.Join(source, "projects", "demo.state.json"), `{
  "work_dir_override": "`+filepath.ToSlash(stateProject)+`"
}`)
	writeMigrationFixture(t, filepath.Join(source, "workspace_bindings.json"), `{
  "project:demo": {
    "feishu:chat": {
      "workspace": "`+filepath.ToSlash(bindingProject)+`"
    }
  }
}`)
	writeMigrationFixture(t, filepath.Join(stateProject, ".cc-connect", "attachments", "state.txt"), "state-data")
	writeMigrationFixture(t, filepath.Join(bindingProject, ".cc-connect", "images", "binding.png"), "binding-data")

	report, err := migrateLegacyData(source, target, false, false)
	if err != nil {
		t.Fatalf("migrateLegacyData() error = %v", err)
	}
	if got, want := report.ProjectDirectories, 2; got != want {
		t.Fatalf("project directories = %d, want %d", got, want)
	}
	for _, path := range []string{
		filepath.Join(stateProject, ".cc-connect-next", "attachments", "state.txt"),
		filepath.Join(bindingProject, ".cc-connect-next", "images", "binding.png"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("discovered project-local data was not migrated at %s: %v", path, err)
		}
	}
}

func TestMigrateLegacyDataDiscoversProjectDataUnderMultiWorkspaceBase(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, ".cc-connect-next")
	baseDir := filepath.Join(root, "workspaces")
	workspace := filepath.Join(baseDir, "team-project")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), `[[projects]]
name = "multi"
mode = "multi-workspace"
base_dir = "`+filepath.ToSlash(baseDir)+`"
`)
	writeMigrationFixture(t, filepath.Join(workspace, ".cc-connect", "attachments", "context.txt"), "workspace-data")

	report, err := migrateLegacyData(source, target, false, false)
	if err != nil {
		t.Fatalf("migrateLegacyData() error = %v", err)
	}
	if got, want := report.ProjectDirectories, 1; got != want {
		t.Fatalf("project directories = %d, want %d", got, want)
	}
	if got, err := os.ReadFile(filepath.Join(workspace, ".cc-connect-next", "attachments", "context.txt")); err != nil || string(got) != "workspace-data" {
		t.Fatalf("multi-workspace project data was not migrated: content=%q err=%v", got, err)
	}
}

func TestMigrateLegacyDataArchivesConflictingConfigRootState(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	customData := filepath.Join(root, "official-state")
	target := filepath.Join(root, ".cc-connect-next")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), `data_dir = "`+filepath.ToSlash(customData)+`"`+"\n")
	writeMigrationFixture(t, filepath.Join(source, "sessions", "demo.json"), "stale-config-root-session")
	writeMigrationFixture(t, filepath.Join(customData, "sessions", "demo.json"), "effective-data-dir-session")

	if _, err := migrateLegacyData(source, target, false, false); err != nil {
		t.Fatalf("migrateLegacyData() error = %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(target, "sessions", "demo.json")); err != nil || string(got) != "effective-data-dir-session" {
		t.Fatalf("effective data_dir did not remain authoritative: content=%q err=%v", got, err)
	}
	archive := filepath.Join(target, "migration-archive", "config-root", "sessions", "demo.json")
	if got, err := os.ReadFile(archive); err != nil || string(got) != "stale-config-root-session" {
		t.Fatalf("conflicting config-root state was not preserved: content=%q err=%v", got, err)
	}
}

func TestMigrateLegacyDataForceCreatesRecoverableBackup(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, ".cc-connect-next")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), "language = \"zh\"\n")
	writeMigrationFixture(t, filepath.Join(source, "sessions", "demo.json"), "new-session")
	writeMigrationFixture(t, filepath.Join(target, "keep.txt"), "existing-target")
	writeMigrationFixture(t, filepath.Join(target, "sessions", "demo.json"), "old-session")

	report, err := migrateLegacyData(source, target, true, false)
	if err != nil {
		t.Fatalf("migrateLegacyData(force) error = %v", err)
	}
	if report.BackupDir == "" {
		t.Fatal("force migration did not report a backup directory")
	}
	canonicalTarget, err := canonicalDestinationPath(target)
	if err != nil {
		t.Fatalf("canonical target: %v", err)
	}
	if len(report.Backups) != 1 || report.Backups[0].Target != canonicalTarget || report.Backups[0].Backup != report.BackupDir {
		t.Fatalf("force migration backup report = %+v, want target and backup path", report.Backups)
	}
	if got, err := os.ReadFile(filepath.Join(target, "keep.txt")); err != nil || string(got) != "existing-target" {
		t.Fatalf("non-conflicting target file was not preserved: content=%q err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(target, "sessions", "demo.json")); err != nil || string(got) != "new-session" {
		t.Fatalf("legacy source did not replace matching target state: content=%q err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(report.BackupDir, "sessions", "demo.json")); err != nil || string(got) != "old-session" {
		t.Fatalf("backup does not preserve the pre-migration target: content=%q err=%v", got, err)
	}
}

func TestMigrateLegacyDataPreflightFailureLeavesTargetUntouched(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, ".cc-connect-next")
	missingData := filepath.Join(root, "missing-state")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), `data_dir = "`+filepath.ToSlash(missingData)+`"`+"\n")

	if _, err := migrateLegacyData(source, target, false, false); err == nil {
		t.Fatal("migrateLegacyData() error = nil, want missing custom data_dir failure")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("failed preflight created or modified target, err=%v", err)
	}
}

func writeMigrationFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}
