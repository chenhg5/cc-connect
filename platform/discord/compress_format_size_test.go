package discord

import (
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/chenhg5/cc-connect/core"
)

// Match the compaction sender's 512-rune cap and exercise the actual Discord
// table formatter and fence-aware splitter. No SDK client is constructed.
func TestCompactionSizedChunks_AfterDiscordFormatting(t *testing.T) {
	for _, input := range []string{
		strings.Repeat("😀", 3000),
		strings.Repeat("| A | B |\n|---|---|\n| 😀 | value |\n\n", 100),
		"```text\n" + strings.Repeat("ordinary output 😀\n", 200) + "```",
	} {
		runes := []rune(input)
		for len(runes) > 0 {
			n := 512
			if len(runes) < n {
				n = len(runes)
			}
			chunk := string(runes[:n])
			runes = runes[n:]
			formatted := wrapTablesInCodeBlocks(chunk)
			for _, payload := range core.SplitMessageCodeFenceAware(formatted, maxDiscordLen) {
				if len(utf16.Encode([]rune(payload))) > 2000 {
					t.Fatal("post-format Discord payload exceeded UTF-16 limit")
				}
			}
		}
	}
}
