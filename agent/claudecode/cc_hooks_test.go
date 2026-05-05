package claudecode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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
		os.WriteFile(path, []byte(`{"hooks":{"PermissionRequest":[{"matcher":"Bash","hooks":[{"type":"command","command":"echo allow"}]}]}}`), 0644)
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
		os.WriteFile(path, []byte(`{
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
		os.WriteFile(path, []byte(`{bad json`), 0644)
		_, err := readSettingsFile(path)
		if err == nil {
			t.Fatal("expected error for malformed json")
		}
	})

	t.Run("no hooks section", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "settings.json")
		os.WriteFile(path, []byte(`{"other": "value"}`), 0644)
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
		name       string
		stdout     string
		wantBehavior string
		wantFallthrough bool
		wantErr    bool
	}{
		{"allow", "allow", "allow", false, false},
		{"deny", "deny", "deny", false, false},
		{"ask", "ask", "", true, false},
		{"empty", "", "", true, false},
		{"uppercase allow", "ALLOW", "allow", false, false},
		{"structured allow", `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}`, "allow", false, false},
		{"structured deny", `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny","message":"blocked"}}}`, "deny", false, false},
		{"structured deny message", `{"hookSpecificOutput":{"decision":{"behavior":"deny","message":"nope"}}}`, "deny", false, false},
		{"unknown json", `{"foo":"bar"}`, "", true, false},
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
}

func TestTryHook(t *testing.T) {
	// Isolate from user's actual ~/.claude/settings.json by pointing
	// CLAUDE_CONFIG_DIR to an empty temp dir.
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())

	t.Run("matching hook returns allow", func(t *testing.T) {
		dir := t.TempDir()
		claudeDir := filepath.Join(dir, ".claude")
		os.MkdirAll(claudeDir, 0755)
		os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(`{
			"hooks": {
				"PermissionRequest": [{
					"matcher": "Bash",
					"hooks": [{"type": "command", "command": "echo allow"}]
				}]
			}
		}`), 0644)

		r := newCCPermissionHookRunner(dir)
		decision, ok := r.tryHook(context.Background(), "Bash", map[string]any{"command": "ls"}, "test-session")
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
		os.MkdirAll(claudeDir, 0755)
		os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(`{
			"hooks": {
				"PermissionRequest": [{
					"matcher": "Read",
					"hooks": [{"type": "command", "command": "echo allow"}]
				}]
			}
		}`), 0644)

		r := newCCPermissionHookRunner(dir)
		_, ok := r.tryHook(context.Background(), "Bash", map[string]any{"command": "ls"}, "test-session")
		if ok {
			t.Fatal("expected no match")
		}
	})

	t.Run("hook returns ask falls through", func(t *testing.T) {
		dir := t.TempDir()
		claudeDir := filepath.Join(dir, ".claude")
		os.MkdirAll(claudeDir, 0755)
		os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(`{
			"hooks": {
				"PermissionRequest": [{
					"matcher": "*",
					"hooks": [{"type": "command", "command": "echo ask"}]
				}]
			}
		}`), 0644)

		r := newCCPermissionHookRunner(dir)
		_, ok := r.tryHook(context.Background(), "Bash", map[string]any{}, "test-session")
		if ok {
			t.Fatal("expected fallthrough for 'ask'")
		}
	})

	t.Run("wildcard matcher", func(t *testing.T) {
		dir := t.TempDir()
		claudeDir := filepath.Join(dir, ".claude")
		os.MkdirAll(claudeDir, 0755)
		os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(`{
			"hooks": {
				"PermissionRequest": [{
					"matcher": "*",
					"hooks": [{"type": "command", "command": "echo deny"}]
				}]
			}
		}`), 0644)

		r := newCCPermissionHookRunner(dir)
		decision, ok := r.tryHook(context.Background(), "AnyTool", map[string]any{}, "test-session")
		if !ok {
			t.Fatal("expected wildcard match")
		}
		if decision.Behavior != "deny" {
			t.Errorf("behavior = %q, want deny", decision.Behavior)
		}
	})

	t.Run("no settings files", func(t *testing.T) {
		r := newCCPermissionHookRunner("/nonexistent/path")
		_, ok := r.tryHook(context.Background(), "Bash", map[string]any{}, "test-session")
		if ok {
			t.Fatal("expected no match when no settings exist")
		}
	})
}

func TestBuildHookStdin(t *testing.T) {
	data := buildHookStdin("Bash", map[string]any{"command": "ls"}, "/workdir", "sess-123")
	if data["tool_name"] != "Bash" {
		t.Errorf("tool_name = %v, want Bash", data["tool_name"])
	}
	if data["cwd"] != "/workdir" {
		t.Errorf("cwd = %v, want /workdir", data["cwd"])
	}
	if data["session_id"] != "sess-123" {
		t.Errorf("session_id = %v, want sess-123", data["session_id"])
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
}
