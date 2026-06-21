package daemon

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func captureSlog(t *testing.T) (get func() string, restore func()) {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return func() string { return buf.String() }, func() { slog.SetDefault(prev) }
}

// withDiscoverer registers d for the duration of the test and resets
// the registry on cleanup so tests cannot pollute each other.
func withDiscoverer(t *testing.T, d EnvDiscoverer) {
	t.Helper()
	ResetEnvDiscoverers()
	RegisterEnvDiscoverer(d)
	t.Cleanup(ResetEnvDiscoverers)
}

func TestCaptureDaemonEnv_IncludesDiscoveredVars(t *testing.T) {
	t.Setenv("CAPTURE_TEST_TOK", "shhh")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:10818")
	withDiscoverer(t, func() (map[string]string, error) {
		return map[string]string{"CAPTURE_TEST_TOK": os.Getenv("CAPTURE_TEST_TOK")}, nil
	})

	got := captureDaemonEnv(false)
	if got["CAPTURE_TEST_TOK"] != "shhh" {
		t.Errorf("CAPTURE_TEST_TOK = %q, want %q", got["CAPTURE_TEST_TOK"], "shhh")
	}
	if got["HTTPS_PROXY"] != "http://127.0.0.1:10818" {
		t.Errorf("HTTPS_PROXY missing or wrong: %q", got["HTTPS_PROXY"])
	}
}

func TestCaptureDaemonEnv_SkipsDiscoverersWhenNoCapture(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:10818")
	withDiscoverer(t, func() (map[string]string, error) {
		return map[string]string{"CAPTURE_TEST_TOK2": "do-not-capture-me"}, nil
	})

	got := captureDaemonEnv(true)
	if _, ok := got["CAPTURE_TEST_TOK2"]; ok {
		t.Errorf("CAPTURE_TEST_TOK2 must not be captured under NoCaptureSecrets")
	}
	if got["HTTPS_PROXY"] != "http://127.0.0.1:10818" {
		t.Errorf("HTTPS_PROXY should still be captured: %q", got["HTTPS_PROXY"])
	}
}

func TestCaptureDaemonEnv_DropsEmptyDiscoveredValues(t *testing.T) {
	withDiscoverer(t, func() (map[string]string, error) {
		return map[string]string{"CAPTURE_TEST_EMPTY": ""}, nil
	})

	got := captureDaemonEnv(false)
	if _, ok := got["CAPTURE_TEST_EMPTY"]; ok {
		t.Error("discoverer returned empty value; must not appear in captured map")
	}
}

func TestCaptureDaemonEnv_LogsDiscovererErrorButContinues(t *testing.T) {
	getLogs, restore := captureSlog(t)
	defer restore()

	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:10818")
	withDiscoverer(t, func() (map[string]string, error) {
		return nil, fmt.Errorf("simulated discovery failure")
	})

	got := captureDaemonEnv(false)
	if got["HTTPS_PROXY"] != "http://127.0.0.1:10818" {
		t.Errorf("HTTPS_PROXY missing despite discoverer error: %q", got["HTTPS_PROXY"])
	}
	if !strings.Contains(getLogs(), "env discoverer reported warnings") {
		t.Errorf("expected warning log, got: %s", getLogs())
	}
}

func TestCaptureDaemonEnv_DropsInvalidEnvName(t *testing.T) {
	getLogs, restore := captureSlog(t)
	defer restore()

	t.Setenv("CAPTURE_TEST_OK", "ok")
	withDiscoverer(t, func() (map[string]string, error) {
		return map[string]string{
			"BAD NAME":        "v",
			"CAPTURE_TEST_OK": os.Getenv("CAPTURE_TEST_OK"),
		}, nil
	})

	got := captureDaemonEnv(false)
	if got["CAPTURE_TEST_OK"] != "ok" {
		t.Errorf("valid name missing: %+v", got)
	}
	if _, ok := got["BAD NAME"]; ok {
		t.Errorf("invalid name must not appear: %+v", got)
	}
	if !strings.Contains(getLogs(), "dropping invalid env name from discoverer") {
		t.Errorf("expected warn about invalid env name; got: %s", getLogs())
	}
}

func TestResolve_PropagatesNoCaptureSecretsToDiscoverers(t *testing.T) {
	t.Setenv("RESOLVE_TOK", "v")
	t.Setenv("HTTPS_PROXY", "http://1.2.3.4:8080")
	withDiscoverer(t, func() (map[string]string, error) {
		return map[string]string{"RESOLVE_TOK": os.Getenv("RESOLVE_TOK")}, nil
	})

	// NoCaptureSecrets=true → discoverer skipped.
	cfg := Config{NoCaptureSecrets: true, BinaryPath: "/bin/true", WorkDir: t.TempDir()}
	if err := Resolve(&cfg); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, ok := cfg.EnvExtra["RESOLVE_TOK"]; ok {
		t.Errorf("Resolve must skip discoverer under NoCaptureSecrets; EnvExtra=%+v", cfg.EnvExtra)
	}
	if cfg.EnvExtra["HTTPS_PROXY"] == "" {
		t.Errorf("Resolve must still capture proxy vars; EnvExtra=%+v", cfg.EnvExtra)
	}

	// NoCaptureSecrets=false → discoverer runs.
	cfg2 := Config{NoCaptureSecrets: false, BinaryPath: "/bin/true", WorkDir: t.TempDir()}
	if err := Resolve(&cfg2); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg2.EnvExtra["RESOLVE_TOK"] != "v" {
		t.Errorf("Resolve must capture discoverer output when NoCaptureSecrets=false; EnvExtra=%+v", cfg2.EnvExtra)
	}
}

func TestResolveCapturesConfigEnvPlaceholders(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "config.toml"), []byte(`
[[projects]]
name = "youzone"

[[projects.platforms]]
type = "youzone"

[projects.platforms.options]
access_token = "${CAPTURE_CONFIG_YOUZONE}"
tenant_id = "tenant"
robot_id = "robot"

[[projects]]
name = "feishu"

[[projects.platforms]]
type = "feishu"

[projects.platforms.options]
app_id = "${CAPTURE_CONFIG_FEISHU_APP_ID}"
app_secret = "${CAPTURE_CONFIG_FEISHU_SECRET}"
`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("CAPTURE_CONFIG_YOUZONE", "youzone-token")
	t.Setenv("CAPTURE_CONFIG_FEISHU_APP_ID", "cli_test")
	t.Setenv("CAPTURE_CONFIG_FEISHU_SECRET", "feishu-secret")

	cfg := Config{BinaryPath: "/bin/true", WorkDir: workDir}
	if err := Resolve(&cfg); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if cfg.EnvExtra["CAPTURE_CONFIG_YOUZONE"] != "youzone-token" {
		t.Errorf("CAPTURE_CONFIG_YOUZONE not captured; EnvExtra=%+v", cfg.EnvExtra)
	}
	if cfg.EnvExtra["CAPTURE_CONFIG_FEISHU_APP_ID"] != "cli_test" {
		t.Errorf("CAPTURE_CONFIG_FEISHU_APP_ID not captured; EnvExtra=%+v", cfg.EnvExtra)
	}
	if cfg.EnvExtra["CAPTURE_CONFIG_FEISHU_SECRET"] != "feishu-secret" {
		t.Errorf("CAPTURE_CONFIG_FEISHU_SECRET not captured; EnvExtra=%+v", cfg.EnvExtra)
	}
}

func TestResolveNoCaptureSecretsSkipsConfigEnvPlaceholders(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "config.toml"), []byte(`
[[projects]]
name = "youzone"

[[projects.platforms]]
type = "youzone"

[projects.platforms.options]
access_token = "${CAPTURE_CONFIG_SKIP}"
`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("CAPTURE_CONFIG_SKIP", "secret")
	t.Setenv("HTTPS_PROXY", "http://1.2.3.4:8080")

	cfg := Config{
		NoCaptureSecrets: true,
		BinaryPath:       "/bin/true",
		WorkDir:          workDir,
	}
	if err := Resolve(&cfg); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, ok := cfg.EnvExtra["CAPTURE_CONFIG_SKIP"]; ok {
		t.Errorf("config placeholder must not be captured under NoCaptureSecrets; EnvExtra=%+v", cfg.EnvExtra)
	}
	if cfg.EnvExtra["HTTPS_PROXY"] != "http://1.2.3.4:8080" {
		t.Errorf("proxy should still be captured under NoCaptureSecrets; EnvExtra=%+v", cfg.EnvExtra)
	}
}

func TestResolveIgnoresCommentedConfigEnvPlaceholders(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "config.toml"), []byte(`
# access_token = "${CAPTURE_CONFIG_COMMENTED}"

[log]
level = "info"
`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("CAPTURE_CONFIG_COMMENTED", "secret")

	cfg := Config{BinaryPath: "/bin/true", WorkDir: workDir}
	if err := Resolve(&cfg); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, ok := cfg.EnvExtra["CAPTURE_CONFIG_COMMENTED"]; ok {
		t.Errorf("commented config placeholder must not be captured; EnvExtra=%+v", cfg.EnvExtra)
	}
}

func TestResolveSkipsUnsetEnvPlaceholders(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "config.toml"), []byte(`
[[projects.platforms.options]]
access_token = "${UNSET_PLACEHOLDER_THAT_DOES_NOT_EXIST}"
`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	os.Unsetenv("UNSET_PLACEHOLDER_THAT_DOES_NOT_EXIST")

	cfg := Config{BinaryPath: "/bin/true", WorkDir: workDir}
	if err := Resolve(&cfg); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, ok := cfg.EnvExtra["UNSET_PLACEHOLDER_THAT_DOES_NOT_EXIST"]; ok {
		t.Errorf("unset placeholder must not appear in EnvExtra; EnvExtra=%+v", cfg.EnvExtra)
	}
}
