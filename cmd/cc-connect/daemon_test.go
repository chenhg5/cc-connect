package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chenhg5/cc-connect/daemon"
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

// TestParseAndResolve_NoCaptureSecretsEndToEnd wires the CLI parser
// straight into daemon.Resolve to prove that --no-capture-secrets
// actually keeps the profile-derived token out of the resulting
// EnvExtra (and the default install path includes it).
func TestParseAndResolve_NoCaptureSecretsEndToEnd(t *testing.T) {
	os.Unsetenv("CC_DAEMON_NO_CAPTURE_SECRETS")

	profileDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(profileDir, "yms-dev.yaml"),
		[]byte("mcp:\n  token_env: E2E_PROFILE_TOK\n"), 0o600); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	t.Setenv("E2E_PROFILE_TOK", "real-token-value")
	t.Setenv("HTTPS_PROXY", "http://1.2.3.4:8080")

	// Default install (no --no-capture-secrets, env unset) → token captured.
	cfg, _, err := parseDaemonInstallArgs([]string{"--force"})
	if err != nil {
		t.Fatalf("parse default: %v", err)
	}
	cfg.ConnectionsDir = profileDir
	cfg.BinaryPath = "/bin/true"
	cfg.WorkDir = t.TempDir()
	if err := daemon.Resolve(&cfg); err != nil {
		t.Fatalf("Resolve default: %v", err)
	}
	if cfg.EnvExtra["E2E_PROFILE_TOK"] != "real-token-value" {
		t.Errorf("default install must capture token; EnvExtra=%+v", cfg.EnvExtra)
	}
	if cfg.EnvExtra["HTTPS_PROXY"] != "http://1.2.3.4:8080" {
		t.Errorf("HTTPS_PROXY must always be captured; EnvExtra=%+v", cfg.EnvExtra)
	}

	// --no-capture-secrets → token NOT captured, proxy still captured.
	cfg2, _, err := parseDaemonInstallArgs([]string{"--no-capture-secrets", "--force"})
	if err != nil {
		t.Fatalf("parse opt-out: %v", err)
	}
	cfg2.ConnectionsDir = profileDir
	cfg2.BinaryPath = "/bin/true"
	cfg2.WorkDir = t.TempDir()
	if err := daemon.Resolve(&cfg2); err != nil {
		t.Fatalf("Resolve opt-out: %v", err)
	}
	if _, present := cfg2.EnvExtra["E2E_PROFILE_TOK"]; present {
		t.Errorf("--no-capture-secrets must skip token; EnvExtra=%+v", cfg2.EnvExtra)
	}
	if cfg2.EnvExtra["HTTPS_PROXY"] != "http://1.2.3.4:8080" {
		t.Errorf("proxy must still be captured under --no-capture-secrets; EnvExtra=%+v", cfg2.EnvExtra)
	}

	// CC_DAEMON_NO_CAPTURE_SECRETS=1 (no flag) → same as opt-out.
	t.Setenv("CC_DAEMON_NO_CAPTURE_SECRETS", "1")
	cfg3, _, err := parseDaemonInstallArgs([]string{"--force"})
	if err != nil {
		t.Fatalf("parse env-opt-out: %v", err)
	}
	cfg3.ConnectionsDir = profileDir
	cfg3.BinaryPath = "/bin/true"
	cfg3.WorkDir = t.TempDir()
	if err := daemon.Resolve(&cfg3); err != nil {
		t.Fatalf("Resolve env-opt-out: %v", err)
	}
	if _, present := cfg3.EnvExtra["E2E_PROFILE_TOK"]; present {
		t.Errorf("CC_DAEMON_NO_CAPTURE_SECRETS=1 must skip token; EnvExtra=%+v", cfg3.EnvExtra)
	}
}

func TestParseDaemonInstallArgs_WorkDirOverridesConfig(t *testing.T) {
	cfg, force, err := parseDaemonInstallArgs([]string{
		"--config", "/tmp/example/config.toml",
		"--work-dir", "/tmp/override",
		"--force",
	})
	if err != nil {
		t.Fatalf("parseDaemonInstallArgs returned error: %v", err)
	}
	if !force {
		t.Fatalf("force = false, want true")
	}

	want := filepath.Clean("/tmp/override")
	if cfg.WorkDir != want {
		t.Fatalf("cfg.WorkDir = %q, want %q", cfg.WorkDir, want)
	}
}
