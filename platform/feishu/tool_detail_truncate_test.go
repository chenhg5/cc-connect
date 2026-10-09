package feishu

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func numberedLines(n int) string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = fmt.Sprintf("line-%d", i+1)
	}
	return strings.Join(lines, "\n")
}

// A heredoc'd script used to put hundreds of lines into the card. The command
// keeps its head, says how much was left out, and the short fields after it
// (cwd, timeout) stay visible.
func TestBuildToolDisplay_CapsLongCommandButKeepsOtherFields(t *testing.T) {
	input, err := json.Marshal(map[string]any{"cmd": numberedLines(100), "cwd": "/tmp/project", "timeout": 120000})
	if err != nil {
		t.Fatal(err)
	}
	got := buildToolDisplay("exec", string(input)).Detail
	if !strings.Contains(got, "line-15\n") || strings.Contains(got, "line-16") {
		t.Fatalf("want the first %d lines only: %q", maxToolDetailLines, got)
	}
	for _, want := range []string{"… (+85 more lines)", "cwd: /tmp/project", "timeout: 120000"} {
		if !strings.Contains(got, want) {
			t.Errorf("detail %q does not contain %q", got, want)
		}
	}
	if strings.Count(got, "more lines") != 1 {
		t.Fatalf("the command must be capped once, not again as a whole: %q", got)
	}
}

// Claude Code sends its Bash input as JSON too ({"command": ...}): a heredoc'd
// script is capped the same way.
func TestBuildToolDisplay_CapsLongStructuredBashCommand(t *testing.T) {
	input, err := json.Marshal(map[string]any{"command": "python3 - <<'EOF'\n" + numberedLines(300) + "\nEOF", "description": "patch the file"})
	if err != nil {
		t.Fatal(err)
	}
	got := buildToolDisplay("Bash", string(input)).Detail
	if !strings.Contains(got, "… (+287 more lines)") || strings.Contains(got, "line-15\n") {
		t.Fatalf("detail = %q, want the script capped at %d lines", got, maxToolDetailLines)
	}
	if !strings.Contains(got, "description: patch the file") {
		t.Fatalf("detail = %q, want the description kept", got)
	}
}

func TestBuildToolDisplay_CapsLongSingleLineByRunes(t *testing.T) {
	got := buildToolDisplay("bash", strings.Repeat("界", maxToolDetailRunes+100)).Detail
	if !strings.HasSuffix(got, "\n… (truncated)") {
		t.Fatalf("want a rune cap note: %q", got)
	}
	if n := len([]rune(strings.TrimSuffix(got, "\n… (truncated)"))); n != maxToolDetailRunes {
		t.Fatalf("kept runes = %d, want %d", n, maxToolDetailRunes)
	}
}

// A large patch keeps its head; the file it edits stays visible.
func TestBuildToolDisplay_CapsLongPatchText(t *testing.T) {
	input, err := json.Marshal(map[string]any{"filePath": "/repo/main.go", "patchText": numberedLines(200)})
	if err != nil {
		t.Fatal(err)
	}
	got := buildToolDisplay("apply_patch", string(input)).Detail
	if !strings.Contains(got, "filePath: /repo/main.go") || !strings.Contains(got, "… (+185 more lines)") || strings.Contains(got, "line-16") {
		t.Fatalf("detail = %q, want the file path and a capped patch", got)
	}
}

func TestBuildToolDisplay_LeavesShortCommandsAlone(t *testing.T) {
	cmd := "cd /repo && git status --short\ngit log --oneline -3"
	input, err := json.Marshal(map[string]any{"cmd": cmd})
	if err != nil {
		t.Fatal(err)
	}
	if got := buildToolDisplay("exec", string(input)).Detail; got != "cmd: "+cmd {
		t.Fatalf("detail = %q, want the command unchanged", got)
	}
}

// Classification looks at the first line, so a capped script is still labelled.
func TestBuildToolDisplay_CappedCommandKeepsItsClassification(t *testing.T) {
	got := buildToolDisplay("Bash", "go test ./...\n"+numberedLines(50))
	if got.Title != "Run tests" {
		t.Fatalf("title = %q, want Run tests", got.Title)
	}
}
