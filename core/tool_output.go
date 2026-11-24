package core

import (
	"strings"
	"unicode/utf8"
)

// ToolOutputTail returns a bounded, UTF-8-safe tail of the original tool output.
// Adapters must call it before truncating ToolResult or Content for display.
// The copy avoids retaining a large output buffer for the lifetime of a card.
func ToolOutputTail(output string) string {
	output = strings.TrimSpace(output)
	start := len(output)
	for count := 0; start > 0 && count < defaultToolMaxLen; count++ {
		_, size := utf8.DecodeLastRuneInString(output[:start])
		start -= size
	}
	return strings.Clone(strings.ToValidUTF8(output[start:], "�"))
}
