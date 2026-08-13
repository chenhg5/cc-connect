package main

import (
	"bytes"
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
	if !strings.Contains(configText, `data_dir = "`+filepath.ToSlash(target)+`"`) {
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

func writeMigrationFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}
