package codex

import (
	"strings"
	"testing"
)

func TestAppServerFileChangePreview_PreservesProtocolKindAndMovePath(t *testing.T) {
	for _, kind := range []string{"add", "delete", "update"} {
		t.Run(kind, func(t *testing.T) {
			preview := appServerFileChangePreview([]any{map[string]any{"path": "file.go", "kind": map[string]any{"type": kind}, "diff": "@@ -1 +1 @@\n-old\n+new"}})
			if !strings.HasPrefix(preview, kind+" file.go\n") {
				t.Fatalf("lost operation type: %q", preview)
			}
		})
	}
	preview := appServerFileChangePreview([]any{map[string]any{"path": "old.go", "kind": map[string]any{"type": "update", "move_path": "new.go"}, "diff": "@@ -1 +1 @@\n-old\n+new"}})
	if !strings.HasPrefix(preview, "update old.go → new.go\n") {
		t.Fatalf("lost rename target: %q", preview)
	}
}
