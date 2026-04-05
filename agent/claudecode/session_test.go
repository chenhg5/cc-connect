package claudecode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadClaudeSettings(t *testing.T) {
	t.Run("no settings file", func(t *testing.T) {
		tmpDir := t.TempDir()
		settings, err := loadClaudeSettings(tmpDir)
		if err != nil {
			t.Fatalf("expected no error for missing file, got: %v", err)
		}
		if settings != nil {
			t.Fatalf("expected nil settings for missing file, got: %+v", settings)
		}
	})

	t.Run("valid settings with hooks", func(t *testing.T) {
		tmpDir := t.TempDir()
		claudeDir := filepath.Join(tmpDir, ".claude")
		if err := os.MkdirAll(claudeDir, 0o755); err != nil {
			t.Fatal(err)
		}

		settingsData := `{
			"hooks": {
				"UserPromptSubmit": [
					{"command": "echo 'hook1'", "timeout": 1000},
					{"command": "echo 'hook2'"}
				]
			}
		}`
		settingsPath := filepath.Join(claudeDir, "settings.json")
		if err := os.WriteFile(settingsPath, []byte(settingsData), 0o644); err != nil {
			t.Fatal(err)
		}

		settings, err := loadClaudeSettings(tmpDir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if settings == nil {
			t.Fatal("expected settings, got nil")
		}
		if settings.Hooks == nil {
			t.Fatal("expected hooks, got nil")
		}
		if len(settings.Hooks.UserPromptSubmit) != 2 {
			t.Fatalf("expected 2 hooks, got %d", len(settings.Hooks.UserPromptSubmit))
		}

		hook1 := settings.Hooks.UserPromptSubmit[0]
		if hook1.Command != "echo 'hook1'" {
			t.Errorf("expected command 'echo hook1', got: %s", hook1.Command)
		}
		if hook1.Timeout != 1000 {
			t.Errorf("expected timeout 1000, got: %d", hook1.Timeout)
		}

		hook2 := settings.Hooks.UserPromptSubmit[1]
		if hook2.Command != "echo 'hook2'" {
			t.Errorf("expected command 'echo hook2', got: %s", hook2.Command)
		}
		if hook2.Timeout != 0 {
			t.Errorf("expected timeout 0 (default), got: %d", hook2.Timeout)
		}
	})

	t.Run("settings without hooks", func(t *testing.T) {
		tmpDir := t.TempDir()
		claudeDir := filepath.Join(tmpDir, ".claude")
		if err := os.MkdirAll(claudeDir, 0o755); err != nil {
			t.Fatal(err)
		}

		settingsData := `{"permissions": {"allow": ["Bash(ls:*)"]}}`
		settingsPath := filepath.Join(claudeDir, "settings.json")
		if err := os.WriteFile(settingsPath, []byte(settingsData), 0o644); err != nil {
			t.Fatal(err)
		}

		settings, err := loadClaudeSettings(tmpDir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if settings == nil {
			t.Fatal("expected settings, got nil")
		}
		if settings.Hooks != nil {
			t.Errorf("expected nil hooks, got: %+v", settings.Hooks)
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		tmpDir := t.TempDir()
		claudeDir := filepath.Join(tmpDir, ".claude")
		if err := os.MkdirAll(claudeDir, 0o755); err != nil {
			t.Fatal(err)
		}

		settingsData := `{"hooks": {invalid}}`
		settingsPath := filepath.Join(claudeDir, "settings.json")
		if err := os.WriteFile(settingsPath, []byte(settingsData), 0o644); err != nil {
			t.Fatal(err)
		}

		_, err := loadClaudeSettings(tmpDir)
		if err == nil {
			t.Fatal("expected error for invalid JSON, got nil")
		}
	})
}

func TestExecuteUserPromptSubmitHooks(t *testing.T) {
	t.Run("hook receives correct stdin", func(t *testing.T) {
		tmpDir := t.TempDir()
		claudeDir := filepath.Join(tmpDir, ".claude")
		if err := os.MkdirAll(claudeDir, 0o755); err != nil {
			t.Fatal(err)
		}

		// Create a hook script that writes stdin to a file for verification
		hookScript := filepath.Join(tmpDir, "verify_hook.sh")
		hookScriptContent := `#!/bin/sh
cat > "$1"
`
		if err := os.WriteFile(hookScript, []byte(hookScriptContent), 0o755); err != nil {
			t.Fatal(err)
		}

		outputFile := filepath.Join(tmpDir, "hook_output.json")
		settingsData := `{
			"hooks": {
				"UserPromptSubmit": [
					{"command": "` + hookScript + ` ` + outputFile + `", "timeout": 5000}
				]
			}
		}`
		settingsPath := filepath.Join(claudeDir, "settings.json")
		if err := os.WriteFile(settingsPath, []byte(settingsData), 0o644); err != nil {
			t.Fatal(err)
		}

		prompt := "Hello, this is a test message!"
		executeUserPromptSubmitHooks(context.Background(), tmpDir, prompt)

		// Verify the hook received the correct stdin
		outputData, err := os.ReadFile(outputFile)
		if err != nil {
			t.Fatalf("hook output file not created: %v", err)
		}

		var received map[string]string
		if err := json.Unmarshal(outputData, &received); err != nil {
			t.Fatalf("invalid JSON in hook output: %v", err)
		}

		if received["message"] != prompt {
			t.Errorf("expected message '%s', got: '%s'", prompt, received["message"])
		}
	})

	t.Run("hook timeout", func(t *testing.T) {
		tmpDir := t.TempDir()
		claudeDir := filepath.Join(tmpDir, ".claude")
		if err := os.MkdirAll(claudeDir, 0o755); err != nil {
			t.Fatal(err)
		}

		// Create a hook script that sleeps longer than timeout
		hookScript := filepath.Join(tmpDir, "slow_hook.sh")
		hookScriptContent := `#!/bin/sh
sleep 2
`
		if err := os.WriteFile(hookScript, []byte(hookScriptContent), 0o755); err != nil {
			t.Fatal(err)
		}

		// Set timeout to 100ms (shorter than sleep)
		settingsData := `{
			"hooks": {
				"UserPromptSubmit": [
					{"command": "` + hookScript + `", "timeout": 100}
				]
			}
		}`
		settingsPath := filepath.Join(claudeDir, "settings.json")
		if err := os.WriteFile(settingsPath, []byte(settingsData), 0o644); err != nil {
			t.Fatal(err)
		}

		prompt := "test prompt"
		// Should not block - timeout should kick in
		start := time.Now()
		executeUserPromptSubmitHooks(context.Background(), tmpDir, prompt)
		elapsed := time.Since(start)

		// Should complete quickly due to timeout (not wait 2 seconds)
		if elapsed > 500*time.Millisecond {
			t.Errorf("hook took too long: %v (expected < 500ms)", elapsed)
		}
	})

	t.Run("no hooks configured", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Should return quickly without error
		start := time.Now()
		executeUserPromptSubmitHooks(context.Background(), tmpDir, "test")
		elapsed := time.Since(start)

		if elapsed > 100*time.Millisecond {
			t.Errorf("execution took too long with no hooks: %v", elapsed)
		}
	})

	t.Run("hook failure does not block", func(t *testing.T) {
		tmpDir := t.TempDir()
		claudeDir := filepath.Join(tmpDir, ".claude")
		if err := os.MkdirAll(claudeDir, 0o755); err != nil {
			t.Fatal(err)
		}

		// Create a hook script that fails
		settingsData := `{
			"hooks": {
				"UserPromptSubmit": [
					{"command": "exit 1", "timeout": 5000}
				]
			}
		}`
		settingsPath := filepath.Join(claudeDir, "settings.json")
		if err := os.WriteFile(settingsPath, []byte(settingsData), 0o644); err != nil {
			t.Fatal(err)
		}

		// Should not panic or block
		executeUserPromptSubmitHooks(context.Background(), tmpDir, "test")
	})
}
