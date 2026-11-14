package discord

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/core"
)

func TestDiscordProgressLine_ShowsOccurrenceTime(t *testing.T) {
	at := time.Date(2026, 10, 23, 14, 3, 7, 0, time.FixedZone("UTC+8", 8*60*60))
	for _, kind := range []core.ProgressCardEntryKind{
		core.ProgressEntryInfo, core.ProgressEntryThinking, core.ProgressEntryToolUse,
		core.ProgressEntryToolResult, core.ProgressEntryError,
	} {
		t.Run(string(kind), func(t *testing.T) {
			item := core.ProgressCardEntry{Kind: kind, Text: "pwd", Tool: "Bash"}
			legacy := buildDiscordProgressLine(item, "en")
			item.Timestamp = at
			if got := buildDiscordProgressLine(item, "en"); got != "14:03:07 · "+legacy {
				t.Fatalf("timestamped line = %q, want prefix plus %q", got, legacy)
			}
			item.Text = strings.Repeat("界", maxDiscordProgressLineLen)
			if got := buildDiscordProgressLine(item, "en"); utf8.RuneCountInString(got) > maxDiscordProgressLineLen {
				t.Fatalf("timestamp exceeds line budget: %q", got)
			}
		})
	}
}

func TestDiscordProgressCard_PreservesSeparateToolOccurrenceTimes(t *testing.T) {
	start := time.Date(2026, 10, 23, 14, 3, 7, 0, time.FixedZone("UTC+8", 8*60*60))
	content := core.BuildProgressCardPayloadV2([]core.ProgressCardEntry{
		{Kind: core.ProgressEntryToolUse, Tool: "Bash", Text: "pwd", Timestamp: start},
		{Kind: core.ProgressEntryToolResult, Tool: "Bash", Text: "ok", Timestamp: start.Add(30 * time.Second)},
	}, false, "Codex", core.LangEnglish, core.ProgressCardStateCompleted)
	message := buildDiscordPreviewMessage(content)
	if len(message.Embeds) != 1 {
		t.Fatalf("expected one progress embed, got %+v", message)
	}
	for _, want := range []string{"14:03:07 · 🔧 Bash", "14:03:37 · 🧾 Bash"} {
		if !strings.Contains(message.Embeds[0].Description, want) {
			t.Fatalf("missing occurrence row %q: %s", want, message.Embeds[0].Description)
		}
	}
}

func TestDiscordProgressLine_LegacyRowHasNoTimestamp(t *testing.T) {
	if got := buildDiscordProgressLine(core.ProgressCardEntry{Text: "legacy"}, "en"); got != "• legacy" {
		t.Fatalf("legacy line = %q, want unchanged text", got)
	}
}
