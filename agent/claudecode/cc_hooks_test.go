package claudecode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStripJSONC(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain json", `{"a": 1}`, `{"a": 1}`},
		{"line comment", "{\n  \"a\": 1 // comment\n}", "{\n  \"a\": 1 \n}"},
		{"block comment", "{\n  /* block */\n  \"a\": 1\n}", "{\n  \n  \"a\": 1\n}"},
		{"comment in string", `{"url": "http://example.com"}`, `{"url": "http://example.com"}`},
		{"empty", "", ""},
		{"unclosed block comment", `{"a": 1 /* oops`, `{"a": 1 /* oops`},
		{"mixed", `{
  // top comment
  "a": "http://x.com", /* inline */
  "b": 2
}`,
			"{\n  \n  \"a\": \"http://x.com\", \n  \"b\": 2\n}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(stripJSONC([]byte(tt.input)))
			if got != tt.want {
				t.Errorf("stripJSONC() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMatchHookEntry(t *testing.T) {
	tests := []struct {
		matcher  string
		toolName string
		want     bool
	}{
		{"Bash", "Bash", true},
		{"bash", "Bash", true},
		{"Bash", "bash", true},
		{"*", "anything", true},
		{"", "anything", true},
		{"Bash", "Read", false},
		{"Read", "Write", false},
	}
	for _, tt := range tests {
		t.Run(tt.matcher+"_"+tt.toolName, func(t *testing.T) {
			if got := matchHookEntry(tt.matcher, tt.toolName); got != tt.want {
				t.Errorf("matchHookEntry(%q, %q) = %v, want %v", tt.matcher, tt.toolName, got, tt.want)
			}
		})
	}
}

func TestReadSettingsFile(t *testing.T) {
	t.Run("valid json", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "settings.json")
		_ = os.WriteFile(path, []byte(`{"hooks":{"PermissionRequest":[{"matcher":"Bash","hooks":[{"type":"command","command":"echo allow"}]}]}}`), 0644)
		s, err := readSettingsFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(s.Hooks.PermissionRequest) != 1 {
			t.Fatalf("got %d entries, want 1", len(s.Hooks.PermissionRequest))
		}
		if s.Hooks.PermissionRequest[0].Matcher != "Bash" {
			t.Errorf("matcher = %q, want Bash", s.Hooks.PermissionRequest[0].Matcher)
		}
	})

	t.Run("valid jsonc", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "settings.json")
		_ = os.WriteFile(path, []byte(`{
  // comment
  "hooks": {
    "PermissionRequest": [
      {
        "matcher": "Bash",
        "hooks": [{"type": "command", "command": "echo allow"}]
      }
    ]
  }
}`), 0644)
		s, err := readSettingsFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(s.Hooks.PermissionRequest) != 1 {
			t.Fatalf("got %d entries, want 1", len(s.Hooks.PermissionRequest))
		}
	})

	t.Run("missing file", func(t *testing.T) {
		_, err := readSettingsFile("/nonexistent/settings.json")
		if err == nil {
			t.Fatal("expected error for missing file")
		}
	})

	t.Run("malformed json", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "settings.json")
		_ = os.WriteFile(path, []byte(`{bad json`), 0644)
		_, err := readSettingsFile(path)
		if err == nil {
			t.Fatal("expected error for malformed json")
		}
	})

	t.Run("no hooks section", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "settings.json")
		_ = os.WriteFile(path, []byte(`{"other": "value"}`), 0644)
		s, err := readSettingsFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(s.Hooks.PermissionRequest) != 0 {
			t.Errorf("expected 0 hooks, got %d", len(s.Hooks.PermissionRequest))
		}
	})
}

func TestParseHookOutput(t *testing.T) {
	tests := []struct {
		name         string
		stdout       string
		wantBehavior string
		wantMessage  string
		wantFallthrough bool
		wantErr      bool
	}{
		{"allow", "allow", "allow", "", false, false},
		{"deny", "deny", "deny", "", false, false},
		{"ask", "ask", "", "", true, false},
		{"empty", "", "", "", true, false},
		{"uppercase allow", "ALLOW", "allow", "", false, false},
		{"structured allow", `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}`, "allow", "", false, false},
		{"structured deny", `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny","message":"blocked"}}}`, "deny", "blocked", false, false},
		{"structured deny message", `{"hookSpecificOutput":{"decision":{"behavior":"deny","message":"nope"}}}`, "deny", "nope", false, false},
		{"unknown json", `{"foo":"bar"}`, "", "", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision, err := parseHookOutput([]byte(tt.stdout))
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseHookOutput() error = %v, wantErr %v", err, tt.wantErr)
			}
			if decision.Behavior != tt.wantBehavior {
				t.Errorf("behavior = %q, want %q", decision.Behavior, tt.wantBehavior)
			}
			if decision.Message != tt.wantMessage {
				t.Errorf("message = %q, want %q", decision.Message, tt.wantMessage)
			}
			isFallthrough := decision.Behavior == ""
			if isFallthrough != tt.wantFallthrough {
				t.Errorf("fallthrough = %v, want %v", isFallthrough, tt.wantFallthrough)
			}
		})
	}
}

func TestRunHookCommand(t *testing.T) {
	t.Run("allow", func(t *testing.T) {
		decision, err := runHookCommand(context.Background(), "echo allow", map[string]any{"tool_name": "Bash"})
		if err != nil {
			t.Fatal(err)
		}
		if decision.Behavior != "allow" {
			t.Errorf("behavior = %q, want allow", decision.Behavior)
		}
	})

	t.Run("deny", func(t *testing.T) {
		decision, err := runHookCommand(context.Background(), "echo deny", map[string]any{"tool_name": "Bash"})
		if err != nil {
			t.Fatal(err)
		}
		if decision.Behavior != "deny" {
			t.Errorf("behavior = %q, want deny", decision.Behavior)
		}
	})

	t.Run("exit non-zero", func(t *testing.T) {
		_, err := runHookCommand(context.Background(), "exit 1", map[string]any{})
		if err == nil {
			t.Fatal("expected error for non-zero exit")
		}
	})

	t.Run("command not found", func(t *testing.T) {
		_, err := runHookCommand(context.Background(), "/nonexistent/command", map[string]any{})
		if err == nil {
			t.Fatal("expected error for missing command")
		}
	})

	t.Run("timeout", func(t *testing.T) {
		shortCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_, err := runHookCommand(shortCtx, "sleep 10", map[string]any{})
		if err == nil {
			t.Fatal("expected error for timeout")
		}
	})

	t.Run("env strips CC_CONNECT_PERMISSION_HOOK_SKIP", func(t *testing.T) {
		t.Setenv("CC_CONNECT_PERMISSION_HOOK_SKIP", "1")
		// The hook prints "allow" only if the skip flag is absent.
		decision, err := runHookCommand(context.Background(),
			`if [ -n "$CC_CONNECT_PERMISSION_HOOK_SKIP" ]; then echo deny; else echo allow; fi`, map[string]any{})
		if err != nil {
			t.Fatal(err)
		}
		if decision.Behavior != "allow" {
			t.Errorf("behavior = %q, want allow (skip flag was not stripped)", decision.Behavior)
		}
	})
}

func TestTryHook(t *testing.T) {
	// Isolate from user's actual ~/.claude/settings.json by pointing
	// CLAUDE_CONFIG_DIR to an empty temp dir.
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())

	t.Run("matching hook returns allow", func(t *testing.T) {
		dir := t.TempDir()
		claudeDir := filepath.Join(dir, ".claude")
		_ = os.MkdirAll(claudeDir, 0755)
		_ = os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(`{
			"hooks": {
				"PermissionRequest": [{
					"matcher": "Bash",
					"hooks": [{"type": "command", "command": "echo allow"}]
				}]
			}
		}`), 0644)

		r := newCCPermissionHookRunner(dir)
		hctx := hookContext{sessionID: "test-session", toolName: "Bash", toolInput: map[string]any{"command": "ls"}, cwd: dir}
		decision, ok := r.tryHook(context.Background(), hctx)
		if !ok {
			t.Fatal("expected hook to match")
		}
		if decision.Behavior != "allow" {
			t.Errorf("behavior = %q, want allow", decision.Behavior)
		}
	})

	t.Run("no matching hook", func(t *testing.T) {
		dir := t.TempDir()
		claudeDir := filepath.Join(dir, ".claude")
		_ = os.MkdirAll(claudeDir, 0755)
		_ = os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(`{
			"hooks": {
				"PermissionRequest": [{
					"matcher": "Read",
					"hooks": [{"type": "command", "command": "echo allow"}]
				}]
			}
		}`), 0644)

		r := newCCPermissionHookRunner(dir)
		hctx := hookContext{sessionID: "test-session", toolName: "Bash", toolInput: map[string]any{"command": "ls"}, cwd: dir}
		_, ok := r.tryHook(context.Background(), hctx)
		if ok {
			t.Fatal("expected no match")
		}
	})

	t.Run("hook returns ask falls through", func(t *testing.T) {
		dir := t.TempDir()
		claudeDir := filepath.Join(dir, ".claude")
		_ = os.MkdirAll(claudeDir, 0755)
		_ = os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(`{
			"hooks": {
				"PermissionRequest": [{
					"matcher": "*",
					"hooks": [{"type": "command", "command": "echo ask"}]
				}]
			}
		}`), 0644)

		r := newCCPermissionHookRunner(dir)
		hctx := hookContext{sessionID: "test-session", toolName: "Bash", toolInput: map[string]any{}, cwd: dir}
		_, ok := r.tryHook(context.Background(), hctx)
		if ok {
			t.Fatal("expected fallthrough for 'ask'")
		}
	})

	t.Run("wildcard matcher", func(t *testing.T) {
		dir := t.TempDir()
		claudeDir := filepath.Join(dir, ".claude")
		_ = os.MkdirAll(claudeDir, 0755)
		_ = os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(`{
			"hooks": {
				"PermissionRequest": [{
					"matcher": "*",
					"hooks": [{"type": "command", "command": "echo deny"}]
				}]
			}
		}`), 0644)

		r := newCCPermissionHookRunner(dir)
		hctx := hookContext{sessionID: "test-session", toolName: "AnyTool", toolInput: map[string]any{}, cwd: dir}
		decision, ok := r.tryHook(context.Background(), hctx)
		if !ok {
			t.Fatal("expected wildcard match")
		}
		if decision.Behavior != "deny" {
			t.Errorf("behavior = %q, want deny", decision.Behavior)
		}
	})

	t.Run("no settings files", func(t *testing.T) {
		r := newCCPermissionHookRunner("/nonexistent/path")
		hctx := hookContext{sessionID: "test-session", toolName: "Bash", toolInput: map[string]any{}}
		_, ok := r.tryHook(context.Background(), hctx)
		if ok {
			t.Fatal("expected no match when no settings exist")
		}
	})

	t.Run("multi-entry fallthrough", func(t *testing.T) {
		dir := t.TempDir()
		claudeDir := filepath.Join(dir, ".claude")
		_ = os.MkdirAll(claudeDir, 0755)
		_ = os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(`{
			"hooks": {
				"PermissionRequest": [
					{"matcher": "*", "hooks": [{"type": "command", "command": "echo ask"}]},
					{"matcher": "*", "hooks": [{"type": "command", "command": "echo allow"}]}
				]
			}
		}`), 0644)

		r := newCCPermissionHookRunner(dir)
		hctx := hookContext{sessionID: "test-session", toolName: "Bash", toolInput: map[string]any{}, cwd: dir}
		decision, ok := r.tryHook(context.Background(), hctx)
		if !ok {
			t.Fatal("expected second entry to match")
		}
		if decision.Behavior != "allow" {
			t.Errorf("behavior = %q, want allow (should fall through from first 'ask')", decision.Behavior)
		}
	})

	t.Run("settings merging across files", func(t *testing.T) {
		configDir := t.TempDir()
		t.Setenv("CLAUDE_CONFIG_DIR", configDir)
		_ = os.WriteFile(filepath.Join(configDir, "settings.json"), []byte(`{
			"hooks": {
				"PermissionRequest": [
					{"matcher": "*", "hooks": [{"type": "command", "command": "echo ask"}]}
				]
			}
		}`), 0644)

		dir := t.TempDir()
		claudeDir := filepath.Join(dir, ".claude")
		_ = os.MkdirAll(claudeDir, 0755)
		_ = os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(`{
			"hooks": {
				"PermissionRequest": [
					{"matcher": "*", "hooks": [{"type": "command", "command": "echo allow"}]}
				]
			}
		}`), 0644)

		r := newCCPermissionHookRunner(dir)
		hctx := hookContext{sessionID: "test-session", toolName: "Bash", toolInput: map[string]any{}, cwd: dir}
		decision, ok := r.tryHook(context.Background(), hctx)
		if !ok {
			t.Fatal("expected merged settings to produce a match")
		}
		if decision.Behavior != "allow" {
			t.Errorf("behavior = %q, want allow (project settings should override global)", decision.Behavior)
		}
	})
}

func TestBuildHookStdin(t *testing.T) {
	hctx := hookContext{
		sessionID:          "sess-123",
		toolName:           "Bash",
		toolInput:          map[string]any{"command": "ls"},
		cwd:                "/workdir",
		permissionMode:     "default",
		transcriptPath:     "/tmp/transcript.jsonl",
		permissionSuggestions: []any{},
	}
	data := buildHookStdin(hctx)
	if data["tool_name"] != "Bash" {
		t.Errorf("tool_name = %v, want Bash", data["tool_name"])
	}
	if data["hook_event_name"] != "PermissionRequest" {
		t.Errorf("hook_event_name = %v, want PermissionRequest", data["hook_event_name"])
	}
	if data["cwd"] != "/workdir" {
		t.Errorf("cwd = %v, want /workdir", data["cwd"])
	}
	if data["session_id"] != "sess-123" {
		t.Errorf("session_id = %v, want sess-123", data["session_id"])
	}
	if data["permission_mode"] != "default" {
		t.Errorf("permission_mode = %v, want default", data["permission_mode"])
	}
	if data["transcript_path"] != "/tmp/transcript.jsonl" {
		t.Errorf("transcript_path = %v, want /tmp/transcript.jsonl", data["transcript_path"])
	}
	input, ok := data["tool_input"].(map[string]any)
	if !ok {
		t.Fatal("tool_input is not a map")
	}
	if input["command"] != "ls" {
		t.Errorf("tool_input.command = %v, want ls", input["command"])
	}

	// Ensure it's valid JSON.
	_, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	// Verify optional fields are omitted when empty.
	minimal := buildHookStdin(hookContext{sessionID: "s", toolName: "Read", toolInput: map[string]any{}})
	if _, ok := minimal["permission_mode"]; ok {
		t.Error("permission_mode should be omitted when empty")
	}
	if _, ok := minimal["transcript_path"]; ok {
		t.Error("transcript_path should be omitted when empty")
	}
	if _, ok := minimal["agent_id"]; ok {
		t.Error("agent_id should be omitted when empty")
	}
	if _, ok := minimal["agent_type"]; ok {
		t.Error("agent_type should be omitted when empty")
	}
}
