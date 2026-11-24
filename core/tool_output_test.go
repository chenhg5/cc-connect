package core

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestToolOutputTail_BoundedUnicodeAndWhitespace(t *testing.T) {
	for _, test := range []struct{ input, want string }{
		{"", ""}, {" \r\n\t", ""}, {" short\r\noutput \n", "short\r\noutput"},
		{strings.Repeat("汉", 600) + "尾", strings.Repeat("汉", 499) + "尾"},
		{strings.Repeat("x", 499) + "汉字尾", strings.Repeat("x", 497) + "汉字尾"},
		{"bad\xff\xfe\n尾", "bad�\n尾"},
	} {
		got := ToolOutputTail(test.input)
		if got != test.want || !utf8.ValidString(got) || utf8.RuneCountInString(got) > 500 {
			t.Fatalf("tail=%q want=%q", got, test.want)
		}
	}
}

func TestMergeRichToolResult_PrefersAdapterTailOverTruncatedSummary(t *testing.T) {
	for _, contentOnly := range []bool{false, true} {
		event := Event{ToolName: "Bash", ToolResult: "head only...", ToolResultTail: "最新输出\nFINAL_ERROR_MARKER", ToolStatus: "completed"}
		if contentOnly {
			event.ToolResult, event.Content = "", event.ToolResult
		}
		steps := mergeRichToolResult([]ToolStep{{Name: "Bash"}}, event, "head only...", 50)
		if steps[0].ResultTail != "最新输出\nFINAL_ERROR_MARKER" || steps[0].Result != "head only..." {
			t.Fatalf("tail or legacy summary changed: %+v", steps)
		}
	}
}
