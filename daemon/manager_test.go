package daemon

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func captureSlog(t *testing.T) (get func() string, restore func()) {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return func() string { return buf.String() }, func() { slog.SetDefault(prev) }
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
