package cursor

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// Replays the Glob event shape emitted by Cursor CLI 2026.10.22-27dcf5f.
// The original failure rendered {"globPattern":"*.md"} as the tool input.
func TestCursorGlobToolCall_ProtobufFieldsProduceReadableInput(t *testing.T) {
	for _, tc := range []struct{ name, event, want string }{
		{"implicit_workspace", `{"type":"tool_call","subtype":"started","tool_call":{"globToolCall":{"args":{"globPattern":"*.md"}}}}`, "*.md"},
		{"target_directory", `{"type":"tool_call","subtype":"started","tool_call":{"globToolCall":{"args":{"globPattern":"**/*.go","targetDirectory":"workspace"}}}}`, filepath.Join("workspace", "**/*.go")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cs := newTestSession("ask")
			defer cs.cancel()
			var raw map[string]any
			if err := json.Unmarshal([]byte(tc.event), &raw); err != nil {
				t.Fatal(err)
			}
			cs.handleEvent(raw)
			select {
			case event := <-cs.Events():
				if event.Type != core.EventToolUse || event.ToolName != "Glob" || event.ToolInput != tc.want {
					t.Fatalf("got %+v; want Glob %q", event, tc.want)
				}
			default:
				t.Fatal("Glob progress event was lost")
			}
		})
	}
}

func TestCursorGlobInput_FieldAliasesAndFallbacks(t *testing.T) {
	absolute := filepath.Join(t.TempDir(), "*.md")
	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"snake_case", map[string]any{"glob_pattern": "**/*.md", "target_directory": "workspace"}, filepath.Join("workspace", "**/*.md")},
		{"legacy_pattern", map[string]any{"pattern": "*.go"}, "*.go"},
		{"legacy_path", map[string]any{"pattern": "*.go", "path": "workspace"}, filepath.Join("workspace", "*.go")},
		{"empty_primary", map[string]any{"globPattern": "", "pattern": "*.go"}, "*.go"},
		{"non_string_primary", map[string]any{"globPattern": 42, "pattern": "*.go"}, "*.go"},
		{"absolute_pattern", map[string]any{"globPattern": absolute, "targetDirectory": "ignored"}, absolute},
		{"directory_only", map[string]any{"targetDirectory": "workspace"}, "workspace"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractToolInput("Glob", map[string]any{"args": tc.args}); got != tc.want {
				t.Fatalf("got %q; want %q", got, tc.want)
			}
		})
	}
	// Unknown shapes retain the established description/JSON fallback.
	if got := extractToolInput("Glob", map[string]any{"args": map[string]any{"unknown": true}, "description": "find files"}); got != "find files" {
		t.Fatal(got)
	}
	if got := extractToolInput("Glob", map[string]any{"args": map[string]any{"unknown": true}}); got != `{"unknown":true}` {
		t.Fatal(got)
	}
}
