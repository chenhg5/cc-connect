package daemon

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeProfile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func captureSlog(t *testing.T) (get func() string, restore func()) {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return func() string { return buf.String() }, func() { slog.SetDefault(prev) }
}

func TestCaptureDaemonEnvIncludesProfileTokenEnv(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "yms-dev.yaml", "mcp:\n  token_env: CAPTURE_TEST_TOK\n")

	t.Setenv("CAPTURE_TEST_TOK", "shhh")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:10818")

	got := captureDaemonEnv(false, dir)
	if got["CAPTURE_TEST_TOK"] != "shhh" {
		t.Errorf("CAPTURE_TEST_TOK = %q, want %q", got["CAPTURE_TEST_TOK"], "shhh")
	}
	if got["HTTPS_PROXY"] != "http://127.0.0.1:10818" {
		t.Errorf("HTTPS_PROXY missing or wrong: %q", got["HTTPS_PROXY"])
	}
}

func TestCaptureDaemonEnvSkipsTokenWhenNoCapture(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "yms-dev.yaml", "mcp:\n  token_env: CAPTURE_TEST_TOK2\n")

	t.Setenv("CAPTURE_TEST_TOK2", "do-not-capture-me")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:10818")

	got := captureDaemonEnv(true, dir)
	if _, ok := got["CAPTURE_TEST_TOK2"]; ok {
		t.Errorf("CAPTURE_TEST_TOK2 must not be captured under NoCaptureSecrets")
	}
	if got["HTTPS_PROXY"] != "http://127.0.0.1:10818" {
		t.Errorf("HTTPS_PROXY should still be captured: %q", got["HTTPS_PROXY"])
	}
}

func TestCaptureDaemonEnvSkipsUnsetVars(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "yms-dev.yaml", "mcp:\n  token_env: CAPTURE_TEST_UNSET\n")
	os.Unsetenv("CAPTURE_TEST_UNSET")

	got := captureDaemonEnv(false, dir)
	if _, ok := got["CAPTURE_TEST_UNSET"]; ok {
		t.Error("unset env must not appear in captured map")
	}
}

func TestCaptureDaemonEnvDoesNotPanicOnMissingDir(t *testing.T) {
	getLogs, restore := captureSlog(t)
	defer restore()

	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:10818")
	got := captureDaemonEnv(false, filepath.Join(t.TempDir(), "does-not-exist"))
	if got["HTTPS_PROXY"] != "http://127.0.0.1:10818" {
		t.Errorf("HTTPS_PROXY missing despite dir error: %q", got["HTTPS_PROXY"])
	}
	if !strings.Contains(getLogs(), "yms-rca profile discovery had warnings") {
		t.Errorf("expected warning log, got: %s", getLogs())
	}
}

func TestCaptureDaemonEnvDropsInvalidEnvName(t *testing.T) {
	getLogs, restore := captureSlog(t)
	defer restore()

	dir := t.TempDir()
	writeProfile(t, dir, "yms-bad.yaml", "mcp:\n  token_env: \"BAD NAME\"\n")
	writeProfile(t, dir, "yms-ok.yaml", "mcp:\n  token_env: CAPTURE_TEST_OK\n")
	t.Setenv("CAPTURE_TEST_OK", "ok")

	got := captureDaemonEnv(false, dir)
	if got["CAPTURE_TEST_OK"] != "ok" {
		t.Errorf("valid name missing: %+v", got)
	}
	logs := getLogs()
	if !strings.Contains(logs, "yms-bad.yaml") {
		t.Errorf("expected warn mentioning yms-bad.yaml; got: %s", logs)
	}
}

func TestResolvePropagatesNoCaptureSecrets(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "yms-dev.yaml", "mcp:\n  token_env: RESOLVE_TOK\n")
	t.Setenv("RESOLVE_TOK", "v")
	t.Setenv("HTTPS_PROXY", "http://1.2.3.4:8080")

	// NoCaptureSecrets=true → token NOT included.
	cfg := Config{NoCaptureSecrets: true, ConnectionsDir: dir, BinaryPath: "/bin/true", WorkDir: t.TempDir()}
	if err := Resolve(&cfg); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, ok := cfg.EnvExtra["RESOLVE_TOK"]; ok {
		t.Errorf("Resolve must skip token under NoCaptureSecrets; EnvExtra=%+v", cfg.EnvExtra)
	}
	if cfg.EnvExtra["HTTPS_PROXY"] == "" {
		t.Errorf("Resolve must still capture proxy vars; EnvExtra=%+v", cfg.EnvExtra)
	}

	// NoCaptureSecrets=false → token IS included.
	cfg2 := Config{NoCaptureSecrets: false, ConnectionsDir: dir, BinaryPath: "/bin/true", WorkDir: t.TempDir()}
	if err := Resolve(&cfg2); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg2.EnvExtra["RESOLVE_TOK"] != "v" {
		t.Errorf("Resolve must capture token when NoCaptureSecrets=false; EnvExtra=%+v", cfg2.EnvExtra)
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

	cfg := Config{BinaryPath: "/bin/true", WorkDir: workDir, ConnectionsDir: filepath.Join(t.TempDir(), "missing")}
	if err := Resolve(&cfg); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if cfg.EnvExtra["CAPTURE_CONFIG_YOUZONE"] != "youzone-token" {
		t.Errorf("CAPTURE_CONFIG_YOUZONE not captured from config placeholders; EnvExtra=%+v", cfg.EnvExtra)
	}
	if cfg.EnvExtra["CAPTURE_CONFIG_FEISHU_APP_ID"] != "cli_test" {
		t.Errorf("CAPTURE_CONFIG_FEISHU_APP_ID not captured from config placeholders; EnvExtra=%+v", cfg.EnvExtra)
	}
	if cfg.EnvExtra["CAPTURE_CONFIG_FEISHU_SECRET"] != "feishu-secret" {
		t.Errorf("CAPTURE_CONFIG_FEISHU_SECRET not captured from config placeholders; EnvExtra=%+v", cfg.EnvExtra)
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
		ConnectionsDir:   filepath.Join(t.TempDir(), "missing"),
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

	cfg := Config{BinaryPath: "/bin/true", WorkDir: workDir, ConnectionsDir: filepath.Join(t.TempDir(), "missing")}
	if err := Resolve(&cfg); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, ok := cfg.EnvExtra["CAPTURE_CONFIG_COMMENTED"]; ok {
		t.Errorf("commented config placeholder must not be captured; EnvExtra=%+v", cfg.EnvExtra)
	}
}
