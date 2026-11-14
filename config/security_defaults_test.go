package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSecurityInsecureOptIns(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	data := fmt.Sprintf(`data_dir = %q

[[projects]]
name = "security-smoke"
[projects.agent]
type = "claudecode"
[[projects.platforms]]
type = "matrix"
[projects.platforms.options]
homeserver = "http://127.0.0.1:1"
access_token = "test-only"

[webhook]
enabled = true
insecure = true

[management]
enabled = true
insecure = true
`, dir)
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Webhook.Insecure == nil || !*cfg.Webhook.Insecure {
		t.Fatal("webhook.insecure opt-in was not loaded")
	}
	if cfg.Management.Insecure == nil || !*cfg.Management.Insecure {
		t.Fatal("management.insecure opt-in was not loaded")
	}
}
