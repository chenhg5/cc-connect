package core

import (
	"regexp"
	"strings"
)

var (
	reCodeBlock   = regexp.MustCompile("(?s)```[a-zA-Z]*\n?(.*?)```")
	reInlineCode  = regexp.MustCompile("`([^`]+)`")
	reBoldAst     = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reBoldUnd     = regexp.MustCompile(`__(.+?)__`)
	reItalicAst   = regexp.MustCompile(`\*(.+?)\*`)
	reItalicUnd   = regexp.MustCompile(`_(.+?)_`)
	reStrike      = regexp.MustCompile(`~~(.+?)~~`)
	reLink        = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	reHeading     = regexp.MustCompile(`(?m)^#{1,6}\s+`)
	reHorizontal  = regexp.MustCompile(`(?m)^---+\s*$`)
	reBlockquote  = regexp.MustCompile(`(?m)^>\s?`)
)

// StripMarkdown converts Markdown-formatted text to clean plain text.
// Useful for platforms that don't support Markdown rendering (WeChat, LINE, etc.).
func StripMarkdown(s string) string {
	// Preserve code block content but remove fences
	s = reCodeBlock.ReplaceAllString(s, "$1")

	// Inline code — remove backticks
	s = reInlineCode.ReplaceAllString(s, "$1")

	// Bold / italic / strikethrough — keep text
	s = reBoldAst.ReplaceAllString(s, "$1")
	s = reBoldUnd.ReplaceAllString(s, "$1")
	s = reItalicAst.ReplaceAllString(s, "$1")
	s = reItalicUnd.ReplaceAllString(s, "$1")
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
