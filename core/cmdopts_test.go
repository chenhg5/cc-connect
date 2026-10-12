package core

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"sort"
	"testing"
)

// captureSlog redirects the default slog logger to a buffer for the duration
// of a test, and returns the buffer plus a restore func.
func captureSlog(t *testing.T) (*bytes.Buffer, func()) {
	t.Helper()
	prev := slog.Default()
	buf := &bytes.Buffer{}
	handler := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelWarn})
	slog.SetDefault(slog.New(handler))
	return buf, func() { slog.SetDefault(prev) }
}

func TestParseCmdOpts_CmdField(t *testing.T) {
	tests := []struct {
		name       string
		opts       map[string]any
		defaultBin string
		wantCmd    string
		wantArgs   []string
	}{
		{
			name:       "cmd field with no args",
			opts:       map[string]any{"cmd": "claude"},
			defaultBin: "fallback",
			wantCmd:    "claude",
			wantArgs:   nil,
		},
		{
			name:       "cmd field with extra args",
			opts:       map[string]any{"cmd": "my-cli code -t foo"},
			defaultBin: "fallback",
			wantCmd:    "my-cli",
			wantArgs:   []string{"code", "-t", "foo"},
		},
		{
			name:       "cmd field with leading/trailing whitespace",
			opts:       map[string]any{"cmd": "  claude  "},
			defaultBin: "fallback",
			wantCmd:    "claude",
			wantArgs:   nil,
		},
		{
			name:       "cmd field empty falls through to default",
			opts:       map[string]any{"cmd": ""},
			defaultBin: "fallback",
			wantCmd:    "fallback",
			wantArgs:   nil,
		},
		{
			name:       "cmd field whitespace only falls through to default",
			opts:       map[string]any{"cmd": "   \t  "},
			defaultBin: "fallback",
			wantCmd:    "fallback",
			wantArgs:   nil,
		},
		{
			name:       "cmd field non-string non-array falls through to default",
			opts:       map[string]any{"cmd": 12345},
			defaultBin: "fallback",
			wantCmd:    "fallback",
			wantArgs:   nil,
		},
		{
			name:       "no fields set returns default",
			opts:       map[string]any{},
			defaultBin: "pi",
			wantCmd:    "pi",
			wantArgs:   nil,
		},
		{
			name:       "cmd with single extra arg",
			opts:       map[string]any{"cmd": "gemini --model pro"},
			defaultBin: "fallback",
			wantCmd:    "gemini",
			wantArgs:   []string{"--model", "pro"},
		},
		// Array form (issue #1670 regression: qoder agent must accept the
		// unified cmd array shape so users can pass
		// --permission-mode bypass_permissions without a wrapper script).
		{
			name:       "cmd array with no extra args",
			opts:       map[string]any{"cmd": []any{"qodercli"}},
			defaultBin: "fallback",
			wantCmd:    "qodercli",
			wantArgs:   nil,
		},
		{
			name:       "cmd array with permission-mode args (qoder IM use case)",
			opts:       map[string]any{"cmd": []any{"qodercli", "--permission-mode", "bypass_permissions"}},
			defaultBin: "fallback",
			wantCmd:    "qodercli",
			wantArgs:   []string{"--permission-mode", "bypass_permissions"},
		},
		{
			name:       "cmd []string form",
			opts:       map[string]any{"cmd": []string{"claude", "--add-dir", "/parent"}},
			defaultBin: "fallback",
			wantCmd:    "claude",
			wantArgs:   []string{"--add-dir", "/parent"},
		},
		{
			name:       "cmd empty array falls through to default",
			opts:       map[string]any{"cmd": []any{}},
			defaultBin: "fallback",
			wantCmd:    "fallback",
			wantArgs:   nil,
		},
		{
			name:       "cmd array of only whitespace falls through to default",
			opts:       map[string]any{"cmd": []any{"", "  ", ""}},
			defaultBin: "fallback",
			wantCmd:    "fallback",
			wantArgs:   nil,
		},
		{
			name:       "cmd array with mixed empty entries skips them",
			opts:       map[string]any{"cmd": []any{"qodercli", "", "--flag", "  "}},
			defaultBin: "fallback",
			wantCmd:    "qodercli",
			wantArgs:   []string{"--flag"},
		},
		{
			name:       "cmd array with non-string entry falls through to default",
			opts:       map[string]any{"cmd": []any{"qodercli", 42, "--flag"}},
			defaultBin: "fallback",
			wantCmd:    "fallback",
			wantArgs:   nil,
		},
		{
			name:       "cmd array preserves order (argv order matters for some CLIs)",
			opts:       map[string]any{"cmd": []any{"qodercli", "-p", "first", "--flag", "second"}},
			defaultBin: "fallback",
			wantCmd:    "qodercli",
			wantArgs:   []string{"-p", "first", "--flag", "second"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf, restore := captureSlog(t)
			defer restore()
			gotCmd, gotArgs := ParseCmdOpts(tt.opts, tt.defaultBin)
			if gotCmd != tt.wantCmd {
				t.Errorf("cmd: got %q, want %q", gotCmd, tt.wantCmd)
			}
			// Order-sensitive check for the array form; the string form
			// is also order-preserving via strings.Fields, so we just
			// compare order here too.
			if !slicesEqual(gotArgs, tt.wantArgs) {
				t.Errorf("extraArgs: got %v, want %v", gotArgs, tt.wantArgs)
			}
			if buf.Len() != 0 {
				t.Errorf("expected no log output, got: %s", buf.String())
			}
		})
	}
}

func TestParseCmdOpts_DeprecatedCliPath(t *testing.T) {
	buf, restore := captureSlog(t)
	defer restore()
	cmd, extraArgs := ParseCmdOpts(map[string]any{"cli_path": "old-cli --flag"}, "fallback")
	if cmd != "old-cli" {
		t.Errorf("cmd: got %q, want %q", cmd, "old-cli")
	}
	if !equalStrings(extraArgs, []string{"--flag"}) {
		t.Errorf("extraArgs: got %v, want %v", extraArgs, []string{"--flag"})
	}
	if buf.Len() == 0 {
		t.Fatal("expected deprecation warning, got none")
	}
	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec); err != nil {
		t.Fatalf("unmarshal log record: %v\nlog: %s", err, buf.String())
	}
	if rec["deprecated_key"] != "cli_path" {
		t.Errorf("deprecated_key: got %v, want %q", rec["deprecated_key"], "cli_path")
	}
	if rec["new_key"] != "cmd" {
		t.Errorf("new_key: got %v, want %q", rec["new_key"], "cmd")
	}
	if rec["value"] != "old-cli --flag" {
		t.Errorf("value: got %v, want %q", rec["value"], "old-cli --flag")
	}
}

func TestParseCmdOpts_DeprecatedCommand(t *testing.T) {
	buf, restore := captureSlog(t)
	defer restore()
	cmd, extraArgs := ParseCmdOpts(map[string]any{"command": "legacy-cli sub"}, "fallback")
	if cmd != "legacy-cli" {
		t.Errorf("cmd: got %q, want %q", cmd, "legacy-cli")
	}
	if !equalStrings(extraArgs, []string{"sub"}) {
		t.Errorf("extraArgs: got %v, want %v", extraArgs, []string{"sub"})
	}
	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec); err != nil {
		t.Fatalf("unmarshal log record: %v\nlog: %s", err, buf.String())
	}
	if rec["deprecated_key"] != "command" {
		t.Errorf("deprecated_key: got %v, want %q", rec["deprecated_key"], "command")
	}
	if rec["new_key"] != "cmd" {
		t.Errorf("new_key: got %v, want %q", rec["new_key"], "cmd")
	}
}

func TestParseCmdOpts_Priority(t *testing.T) {
	tests := []struct {
		name    string
		opts    map[string]any
		wantCmd string
	}{
		{
			name:    "cmd wins over cli_path",
			opts:    map[string]any{"cmd": "new-cli", "cli_path": "old-cli"},
			wantCmd: "new-cli",
		},
		{
			name:    "cmd wins over command",
			opts:    map[string]any{"cmd": "new-cli", "command": "old-cli"},
			wantCmd: "new-cli",
		},
		{
			name:    "cli_path wins over command",
			opts:    map[string]any{"cli_path": "mid-cli", "command": "old-cli"},
			wantCmd: "mid-cli",
		},
		{
			name:    "cli_path wins over default",
			opts:    map[string]any{"cli_path": "mid-cli"},
			wantCmd: "mid-cli",
		},
		{
			name:    "command wins over default",
			opts:    map[string]any{"command": "old-cli"},
			wantCmd: "old-cli",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, restore := captureSlog(t)
			defer restore()
			cmd, _ := ParseCmdOpts(tt.opts, "fallback")
			if cmd != tt.wantCmd {
				t.Errorf("cmd: got %q, want %q", cmd, tt.wantCmd)
			}
		})
	}
}

func TestParseCmdOpts_NoWarningForCanonical(t *testing.T) {
	buf, restore := captureSlog(t)
	defer restore()
	ParseCmdOpts(map[string]any{"cmd": "claude"}, "fallback")
	if buf.Len() != 0 {
		t.Errorf("expected no log output for 'cmd' field, got: %s", buf.String())
	}
}

func TestParseCmdOpts_SlogRestoreNoLeak(t *testing.T) {
	// Sanity check: the restore func actually reverts the default logger.
	// We use a unique sentinel handler and verify it's gone after restore.
	sentinel := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	prev := slog.Default()
	slog.SetDefault(sentinel)
	{
		_, restore := captureSlog(t)
		restore()
	}
	if slog.Default() != sentinel {
		t.Errorf("expected default logger to be restored to sentinel")
	}
	slog.SetDefault(prev) // cleanup
}

func TestParseConfigEnv(t *testing.T) {
	tests := []struct {
		name string
		opts map[string]any
		want []string
	}{
		{
			name: "nil opts",
			opts: nil,
			want: nil,
		},
		{
			name: "no env key",
			opts: map[string]any{"cmd": "x"},
			want: nil,
		},
		{
			name: "env nil value",
			opts: map[string]any{"env": nil},
			want: nil,
		},
		{
			name: "env map string string",
			opts: map[string]any{"env": map[string]string{"FOO": "bar", "BAZ": "qux"}},
			want: []string{"FOO=bar", "BAZ=qux"},
		},
		{
			name: "env map any string values (TOML output)",
			opts: map[string]any{"env": map[string]any{"FOO": "bar", "BAZ": "qux"}},
			want: []string{"FOO=bar", "BAZ=qux"},
		},
		{
			name: "env map any with non-string value is skipped",
			opts: map[string]any{"env": map[string]any{"FOO": "bar", "NUM": 42}},
			want: []string{"FOO=bar"},
		},
		{
			name: "env empty map",
			opts: map[string]any{"env": map[string]string{}},
			want: []string{},
		},
		{
			name: "env unsupported type returns nil",
			opts: map[string]any{"env": []string{"FOO=bar"}},
			want: nil,
		},
		{
			name: "env value containing equals",
			opts: map[string]any{"env": map[string]string{"URL": "https://x?a=1&b=2"}},
			want: []string{"URL=https://x?a=1&b=2"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseConfigEnv(tt.opts)
			if !equalStrings(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseConfigEnv_DoesNotMutateInput(t *testing.T) {
	in := map[string]any{
		"env": map[string]any{
			"FOO": "bar",
		},
	}
	_ = ParseConfigEnv(in)
	// Mutate the input — the next call should still return correct data,
	// proving we don't keep a reference to the inner map.
	in["env"].(map[string]any)["FOO"] = "mutated"
	in["env"].(map[string]any)["NEW"] = "added"
	got := ParseConfigEnv(in)
	if !equalStrings(got, []string{"FOO=mutated", "NEW=added"}) {
		t.Errorf("expected fresh read on each call, got %v", got)
	}
}

// equalStrings compares two string slices for unordered-set equality.
// Order is not guaranteed for map-based inputs.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ac := append([]string(nil), a...)
	bc := append([]string(nil), b...)
	sort.Strings(ac)
	sort.Strings(bc)
	for i := range ac {
		if ac[i] != bc[i] {
			return false
		}
	}
	return true
}

// slicesEqual compares two string slices in order. Used for argv-style
// checks where position matters (e.g. ParseCmdOpts array form).
func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
