package discord

import (
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestProgressListKeepsLinesAfterDone(t *testing.T) {
	payload := &core.ProgressCardPayload{
		Lang: "zh", State: core.ProgressCardStateCompleted, Steps: 3, ElapsedSec: 700, LineLen: 200,
		Items: []core.ProgressCardEntry{
			{Kind: core.ProgressEntryInfo, Text: "……你要是还在，说句话就行，不用回长的。"},
			{Kind: core.ProgressEntryToolUse, Tool: "Bash", Text: "ls"},
		},
	}
	desc := buildDiscordProgressDescription(payload)
	if !strings.Contains(desc, "你要是还在") || !strings.HasPrefix(desc, "第 3 步 · 11分40秒") {
		t.Fatalf("done card lost the narration: %q", desc)
	}
	payload.LineLen = 0
	if got := buildDiscordProgressDescription(payload); strings.Contains(got, "你要是还在") {
		t.Fatalf("one-line mode should stay a bare head when done: %q", got)
	}
}

func TestProgressListLineLen(t *testing.T) {
	long := strings.Repeat("字", 150)
	item := core.ProgressCardEntry{Kind: core.ProgressEntryInfo, Text: long}
	if n := len([]rune(buildDiscordProgressLineN(item, "zh", 200))); n < 150 {
		t.Fatalf("line cut at %d runes, want the full 150", n)
	}
	if n := len([]rune(buildDiscordProgressLine(item, "zh"))); n > maxDiscordProgressLineLen {
		t.Fatalf("default line too long: %d", n)
	}
}
