package core

import (
	"regexp"
	"strings"
)

var (
	reCodeBlock   = regexp.MustCompile("(?s)```[a-zA-Z]*\n?(.*?)```")
	reInlineCode  = regexp.MustCompile("`([^`]+)`")
	reBoldAst     = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reItalicAst   = regexp.MustCompile(`\*(.+?)\*`)
	reStrike      = regexp.MustCompile(`~~(.+?)~~`)
	reLink        = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	reHeading     = regexp.MustCompile(`(?m)^#{1,6}\s+`)
	reHorizontal  = regexp.MustCompile(`(?m)^---+\s*$`)
	reBlockquote  = regexp.MustCompile(`(?m)^>\s?`)
)

// StripMarkdown converts Markdown-formatted text to clean plain text.
// Useful for platforms that don't support Markdown rendering (WeChat, LINE, etc.).
//
// Note: the underscore forms of bold (`__bold__`) and italic (`_italic_`) are
// intentionally not stripped. The patterns `_..._` and `__...__` are
// indistinguishable from snake_case and dunder identifiers (`my_func_name`,
// Python's `__init__`), and stripping them silently corrupted code-related
// text into things like `myfuncname` or `init`. Agents reliably emit the
// asterisk forms (`**bold**`, `*italic*`), so dropping the underscore forms
// is the safe trade-off here. Inputs containing literal `_italic_` will keep
// their underscores; this is a small cosmetic loss vs. losing identifier
// integrity in TTS / LINE / WeChat output.
func StripMarkdown(s string) string {
	// Preserve code block content but remove fences
	s = reCodeBlock.ReplaceAllString(s, "$1")

	// Inline code — remove backticks
	s = reInlineCode.ReplaceAllString(s, "$1")

	// Bold / italic / strikethrough — keep text
	s = reBoldAst.ReplaceAllString(s, "$1")
	s = reItalicAst.ReplaceAllString(s, "$1")
	s = reStrike.ReplaceAllString(s, "$1")

	// Links [text](url) → text (url)
	s = reLink.ReplaceAllString(s, "$1 ($2)")

	// Headings — remove # prefix
	s = reHeading.ReplaceAllString(s, "")

	// Horizontal rules
	s = reHorizontal.ReplaceAllString(s, "")

	// Blockquotes
	s = reBlockquote.ReplaceAllString(s, "")

	// Collapse 3+ consecutive blank lines into 2
	s = regexp.MustCompile(`\n{3,}`).ReplaceAllString(s, "\n\n")

	return strings.TrimSpace(s)
}
