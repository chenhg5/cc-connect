package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseDaemonInstallArgs_ConfigSetsWorkDir(t *testing.T) {
	cfg, force, err := parseDaemonInstallArgs([]string{"--config", "/tmp/example/config.toml"})
	if err != nil {
		t.Fatalf("parseDaemonInstallArgs returned error: %v", err)
	}
	if force {
		t.Fatalf("force = true, want false")
	}

	want := filepath.Clean("/tmp/example")
	if cfg.WorkDir != want {
		t.Fatalf("cfg.WorkDir = %q, want %q", cfg.WorkDir, want)
	}
}

func TestParseDaemonInstallArgs_ConfigEqualsFormSetsWorkDir(t *testing.T) {
	cfg, _, err := parseDaemonInstallArgs([]string{"--config=/tmp/example/config.toml"})
	if err != nil {
		t.Fatalf("parseDaemonInstallArgs returned error: %v", err)
	}

	want := filepath.Clean("/tmp/example")
	if cfg.WorkDir != want {
		t.Fatalf("cfg.WorkDir = %q, want %q", cfg.WorkDir, want)
	}
}

func TestParseDaemonInstallArgs_NoCaptureSecretsFlag(t *testing.T) {
	os.Unsetenv("CC_DAEMON_NO_CAPTURE_SECRETS")

	cfg, _, err := parseDaemonInstallArgs([]string{"--no-capture-secrets"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !cfg.NoCaptureSecrets {
		t.Fatal("flag should set NoCaptureSecrets=true")
	}

	cfg2, _, err := parseDaemonInstallArgs(nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg2.NoCaptureSecrets {
		t.Fatal("default must be false when flag and env are unset")
	}
}

func TestParseDaemonInstallArgs_NoCaptureSecretsEnv(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", "yes", "on"} {
		t.Run("truthy="+v, func(t *testing.T) {
			t.Setenv("CC_DAEMON_NO_CAPTURE_SECRETS", v)
			cfg, _, err := parseDaemonInstallArgs(nil)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if !cfg.NoCaptureSecrets {
				t.Fatalf("env=%q should opt out", v)
			}
		})
	}
	for _, v := range []string{"0", "false", "", "no", "off"} {
		t.Run("falsy="+v, func(t *testing.T) {
			t.Setenv("CC_DAEMON_NO_CAPTURE_SECRETS", v)
			cfg, _, err := parseDaemonInstallArgs(nil)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if cfg.NoCaptureSecrets {
				t.Fatalf("env=%q should NOT opt out", v)
			}
		})
	}
}

func TestParseDaemonInstallArgs_NoCaptureSecretsFlagAndEnvCombine(t *testing.T) {
	// OR semantics: env=truthy + flag=present → still true.
	t.Setenv("CC_DAEMON_NO_CAPTURE_SECRETS", "1")
	cfg, _, err := parseDaemonInstallArgs([]string{"--no-capture-secrets", "--force"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !cfg.NoCaptureSecrets {
		t.Fatal("flag+env both should leave NoCaptureSecrets=true")
	}
	// env=truthy without flag → still true.
	cfg2, _, err := parseDaemonInstallArgs([]string{"--force"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !cfg2.NoCaptureSecrets {
		t.Fatal("env=1 alone should opt out")
	}
}

func TestParseDaemonInstallArgs_WorkDirOverridesConfig(t *testing.T) {
	// Use native paths: a hardcoded POSIX path is not comparable to what
	// parseDaemonInstallArgs stores (it keeps --work-dir verbatim but routes
	// --config through filepath.Dir), so the assertion would only hold on unix.
	configPath := filepath.Join(t.TempDir(), "config.toml")
	overrideDir := t.TempDir()
	if filepath.Dir(configPath) == overrideDir {
		t.Fatal("test setup: override dir must differ from the config dir")
	}

	cfg, force, err := parseDaemonInstallArgs([]string{
		"--config", configPath,
		"--work-dir", overrideDir,
		"--force",
	})
	if err != nil {
		t.Fatalf("parseDaemonInstallArgs returned error: %v", err)
	}
	if !force {
		t.Fatalf("force = false, want true")
	}

	// Last flag wins: both write cfg.WorkDir, so a --work-dir that follows
	// --config overrides the directory implied by the config path.
	if cfg.WorkDir != overrideDir {
		t.Fatalf("cfg.WorkDir = %q, want %q", cfg.WorkDir, overrideDir)
	}
}

// TestParseDaemonInstallArgs_ConfigSetsConfigPath pins the producing end of
// the CC_CONFIG hand-off. The unit/plist templates only emit an explicit
// config path when cfg.ConfigPath is set, and resolveConfigPath reads it back
// at startup — so if the parser stopped populating this field the daemon
// would silently fall back to ~/.cc-connect/config.toml, with no other test
// failing. All three accepted flag forms must set it.
func TestParseDaemonInstallArgs_ConfigSetsConfigPath(t *testing.T) {
	forms := []struct {
		name string
		args []string
	}{
		{"space form", []string{"--config", "/tmp/example/config.toml"}},
		{"equals form", []string{"--config=/tmp/example/config.toml"}},
		{"single-dash equals form", []string{"-config=/tmp/example/config.toml"}},
	}
	for _, f := range forms {
		t.Run(f.name, func(t *testing.T) {
			cfg, _, err := parseDaemonInstallArgs(f.args)
			if err != nil {
				t.Fatalf("parseDaemonInstallArgs returned error: %v", err)
			}
			want := filepath.Clean("/tmp/example/config.toml")
			if cfg.ConfigPath != want {
				t.Errorf("cfg.ConfigPath = %q, want %q", cfg.ConfigPath, want)
			}
		})
	}
}

// TestParseDaemonInstallArgs_NoConfigLeavesConfigPathEmpty is the negative
// case: without --config the templates must not emit CC_CONFIG, so the
// daemon keeps its normal ./config.toml → ~/.cc-connect/config.toml lookup.
func TestParseDaemonInstallArgs_NoConfigLeavesConfigPathEmpty(t *testing.T) {
	cfg, _, err := parseDaemonInstallArgs([]string{"--work-dir", "/tmp/wd"})
	if err != nil {
		t.Fatalf("parseDaemonInstallArgs returned error: %v", err)
	}
	if cfg.ConfigPath != "" {
		t.Errorf("cfg.ConfigPath = %q, want empty", cfg.ConfigPath)
	}
}

// TestParseDaemonInstallArgs_WorkDirOverridesConfigWithoutClobberingConfigPath
// pins that --work-dir still wins for WorkingDirectory while the explicit
// config path is preserved — the two fields serve different purposes and
// must not interfere.
func TestParseDaemonInstallArgs_WorkDirOverridesConfigWithoutClobberingConfigPath(t *testing.T) {
	cfg, _, err := parseDaemonInstallArgs([]string{
		"--config", "/tmp/example/config.toml",
		"--work-dir", "/tmp/override",
	})
	if err != nil {
		t.Fatalf("parseDaemonInstallArgs returned error: %v", err)
	}
	if want := filepath.Clean("/tmp/override"); cfg.WorkDir != want {
		t.Errorf("cfg.WorkDir = %q, want %q", cfg.WorkDir, want)
	}
	if want := filepath.Clean("/tmp/example/config.toml"); cfg.ConfigPath != want {
		t.Errorf("cfg.ConfigPath = %q, want %q", cfg.ConfigPath, want)
	}
}
