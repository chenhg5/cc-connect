package claudecode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
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

func TestHandleUserToolResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cs := &claudeSession{
		events:    make(chan core.Event, 8),
		ctx:       ctx,
		toolIDMap: make(map[string]string),
	}
	cs.sessionID.Store("test-session")
	cs.alive.Store(true)

	// First, simulate assistant sending a tool_use to populate toolIDMap
	assistantRaw := map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"role": "assistant",
			"content": []any{
				map[string]any{
					"type": "tool_use",
					"id":   "toolu_12345",
					"name": "Bash",
					"input": map[string]any{
						"command": "ls -la",
					},
				},
			},
		},
	}
	cs.handleAssistant(assistantRaw)

	// Verify tool_use event was sent
	select {
	case evt := <-cs.events:
		if evt.Type != core.EventToolUse {
			t.Errorf("expected EventToolUse, got %v", evt.Type)
		}
		if evt.ToolName != "Bash" {
			t.Errorf("expected ToolName 'Bash', got %v", evt.ToolName)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for tool_use event")
	}

	// Verify toolIDMap was populated
	cs.toolIDMapMu.RLock()
	toolName := cs.toolIDMap["toolu_12345"]
	cs.toolIDMapMu.RUnlock()
	if toolName != "Bash" {
		t.Errorf("expected toolIDMap['toolu_12345'] = 'Bash', got %v", toolName)
	}

	// Now simulate user message with tool_result
	userRaw := map[string]any{
		"type": "user",
		"message": map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{
					"type":        "tool_result",
					"tool_use_id": "toolu_12345",
					"content":     "total 8\ndrwxr-xr-x 20 user user 4096 Mar 19 10:00 .\n",
				},
			},
		},
	}
	cs.handleUser(userRaw)

	// Verify tool_result event was sent
	select {
	case evt := <-cs.events:
		if evt.Type != core.EventToolResult {
			t.Errorf("expected EventToolResult, got %v", evt.Type)
		}
		if evt.ToolName != "Bash" {
			t.Errorf("expected ToolName 'Bash', got %v", evt.ToolName)
		}
		if evt.ToolResult == "" {
			t.Error("expected non-empty ToolResult")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for tool_result event")
	}
}

func TestHandleUserToolResultWithError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cs := &claudeSession{
		events:    make(chan core.Event, 8),
		ctx:       ctx,
		toolIDMap: make(map[string]string),
	}
	cs.sessionID.Store("test-session")
	cs.alive.Store(true)

	// Populate toolIDMap
	cs.toolIDMapMu.Lock()
	cs.toolIDMap["toolu_error"] = "Read"
	cs.toolIDMapMu.Unlock()

	// Simulate tool_result with error
	userRaw := map[string]any{
		"type": "user",
		"message": map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{
					"type":        "tool_result",
					"tool_use_id": "toolu_error",
					"is_error":    true,
					"content":     "File not found: /nonexistent",
				},
			},
		},
	}
	cs.handleUser(userRaw)

	// Verify tool_result event was sent with error status
	select {
	case evt := <-cs.events:
		if evt.Type != core.EventToolResult {
			t.Errorf("expected EventToolResult, got %v", evt.Type)
		}
		if evt.ToolStatus != "error" {
			t.Errorf("expected ToolStatus 'error', got %v", evt.ToolStatus)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for tool_result event")
	}
}

func TestHandleUserToolResultWithArrayContent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cs := &claudeSession{
		events:    make(chan core.Event, 8),
		ctx:       ctx,
		toolIDMap: make(map[string]string),
	}
	cs.sessionID.Store("test-session")
	cs.alive.Store(true)

	// Populate toolIDMap
	cs.toolIDMapMu.Lock()
	cs.toolIDMap["toolu_array"] = "Write"
	cs.toolIDMapMu.Unlock()

	// Simulate tool_result with array content (Claude sometimes uses this format)
	userRaw := map[string]any{
		"type": "user",
		"message": map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{
					"type":        "tool_result",
					"tool_use_id": "toolu_array",
					"content": []any{
						map[string]any{
							"type": "text",
							"text": "File written successfully",
						},
					},
				},
			},
		},
	}
	cs.handleUser(userRaw)

	// Verify tool_result event was sent
	select {
	case evt := <-cs.events:
		if evt.Type != core.EventToolResult {
			t.Errorf("expected EventToolResult, got %v", evt.Type)
		}
		if evt.ToolResult != "File written successfully" {
			t.Errorf("expected ToolResult 'File written successfully', got %v", evt.ToolResult)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for tool_result event")
	}
}

func TestHandleUserToolResultUnknownToolID(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cs := &claudeSession{
		events:    make(chan core.Event, 8),
		ctx:       ctx,
		toolIDMap: make(map[string]string),
	}
	cs.sessionID.Store("test-session")
	cs.alive.Store(true)

	// Simulate tool_result with unknown tool_use_id (should not emit event)
	userRaw := map[string]any{
		"type": "user",
		"message": map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{
					"type":        "tool_result",
					"tool_use_id": "unknown_id",
					"content":     "some result",
				},
			},
		},
	}
	cs.handleUser(userRaw)

	// No event should be sent
	select {
	case evt := <-cs.events:
		t.Errorf("unexpected event: %v", evt)
	case <-time.After(100 * time.Millisecond):
		// Expected: no event
	}
}

func TestTruncateStr(t *testing.T) {
	tests := []struct {
		input    string
		max      int
		expected string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"this is a longer string", 10, "this is a ..."},
		{"", 5, ""},
	}

	for _, tt := range tests {
		result := truncateStr(tt.input, tt.max)
		if result != tt.expected {
			t.Errorf("truncateStr(%q, %d) = %q, want %q", tt.input, tt.max, result, tt.expected)
		}
	}
}
