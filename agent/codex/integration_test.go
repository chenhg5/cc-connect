package codex

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestIntegration_CodexProviderFlow verifies the full provider config flow:
// 1. ensureCodexProviderConfig writes correct config.toml
// 2. ensureCodexAuth writes correct auth.json
// 3. Codex CLI can authenticate and respond using the written config
//
// Requires: SHENGSUANYUN_API_KEY env var and `codex` CLI in PATH.
// Skip with: go test ./agent/codex/ -run TestIntegration -v
func TestIntegration_CodexProviderFlow(t *testing.T) {
	apiKey := os.Getenv("SHENGSUANYUN_API_KEY")
	if apiKey == "" {
		t.Skip("SHENGSUANYUN_API_KEY not set, skipping integration test")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex CLI not in PATH, skipping integration test")
	}

	home := filepath.Join(t.TempDir(), ".codex")

	// Step 1: write config.toml via our function
	err := ensureCodexProviderConfig(home, "shengsuanyun-codex",
		"https://router.shengsuanyun.com/api/v1", "responses", nil)
	if err != nil {
		t.Fatalf("ensureCodexProviderConfig: %v", err)
	}

	// Verify config.toml content
	cfgData, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatalf("read config.toml: %v", err)
	}
	cfgContent := string(cfgData)
	t.Logf("Generated config.toml:\n%s", cfgContent)

	for _, want := range []string{
		`[model_providers.shengsuanyun-codex]`,
		`base_url = "https://router.shengsuanyun.com/api/v1"`,
		`wire_api = "responses"`,
		`env_key = "OPENAI_API_KEY"`,
	} {
		if !strings.Contains(cfgContent, want) {
			t.Errorf("config.toml missing: %s", want)
		}
	}

	// Step 2: write auth.json via our function
	err = ensureCodexAuth(home, apiKey)
	if err != nil {
		t.Fatalf("ensureCodexAuth: %v", err)
	}

	// Verify auth.json content
	authData, err := os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil {
		t.Fatalf("read auth.json: %v", err)
	}
	var authMap map[string]any
	if err := json.Unmarshal(authData, &authMap); err != nil {
		t.Fatalf("parse auth.json: %v", err)
	}
	if authMap["auth_mode"] != "apikey" {
		t.Errorf("auth_mode = %v, want apikey", authMap["auth_mode"])
	}
	if authMap["OPENAI_API_KEY"] != apiKey {
		t.Errorf("OPENAI_API_KEY not set correctly in auth.json")
	}

	// Step 3: run codex exec with the generated config
	workDir := filepath.Join(t.TempDir(), "repo")
	os.MkdirAll(workDir, 0o755)
	gitInit := exec.Command("git", "init", "-q")
	gitInit.Dir = workDir
	if err := gitInit.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	cmd := exec.Command("codex", "exec",
		"--model", "openai/gpt-5.3-codex",
		"-c", `model_provider="shengsuanyun-codex"`,
		"-c", `openai_base_url="https://router.shengsuanyun.com/api/v1"`,
		"--full-auto",
	)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
		"CODEX_HOME="+home,
		"OPENAI_API_KEY="+apiKey,
	)
	cmd.Stdin = strings.NewReader("reply with exactly 'integration-ok' and nothing else")

	output, err := cmd.CombinedOutput()
	t.Logf("Codex output:\n%s", string(output))

	if err != nil {
		t.Fatalf("codex exec failed: %v\noutput: %s", err, output)
	}
	if !strings.Contains(string(output), "integration-ok") {
		t.Errorf("expected 'integration-ok' in output, got:\n%s", output)
	}
}
