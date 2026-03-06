package core

import (
	"regexp"
	"strings"
)

// MarkdownToTelegramHTML converts common Markdown to Telegram-compatible HTML.
// Telegram supports: <b>, <i>, <s>, <code>, <pre>, <a href="">, <blockquote>.
// This is more reliable than Telegram's native Markdown parser which frequently
// fails on content with underscores, asterisks in code, etc.
func MarkdownToTelegramHTML(md string) string {
	var b strings.Builder
	b.Grow(len(md) + len(md)/4)

	lines := strings.Split(md, "\n")
	inCodeBlock := false
	codeLang := ""
	var codeLines []string

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "```") {
			if !inCodeBlock {
				inCodeBlock = true
				codeLang = strings.TrimPrefix(trimmed, "```")
				codeLines = nil
			} else {
				inCodeBlock = false
				if codeLang != "" {
					b.WriteString("<pre><code class=\"language-" + escapeHTML(codeLang) + "\">")
				} else {
					b.WriteString("<pre><code>")
				}
				b.WriteString(escapeHTML(strings.Join(codeLines, "\n")))
				b.WriteString("</code></pre>")
				if i < len(lines)-1 {
					b.WriteByte('\n')
				}
			}
			continue
		}

		if inCodeBlock {
			codeLines = append(codeLines, line)
			continue
		}

		// Headings → bold
		if heading := reHeading.FindString(line); heading != "" {
			rest := strings.TrimPrefix(line, heading)
			b.WriteString("<b>")
			b.WriteString(convertInlineHTML(rest))
			b.WriteString("</b>")
		} else if strings.HasPrefix(trimmed, "> ") || trimmed == ">" {
			quote := strings.TrimPrefix(line, "> ")
			if quote == ">" {
				quote = ""
			}
			b.WriteString("<blockquote>")
			b.WriteString(convertInlineHTML(quote))
			b.WriteString("</blockquote>")
		} else if reHorizontal.MatchString(trimmed) {
			b.WriteString("———")
		} else {
			b.WriteString(convertInlineHTML(line))
		}

		if i < len(lines)-1 {
			b.WriteByte('\n')
		}
	}

	// Handle unclosed code block
	if inCodeBlock && len(codeLines) > 0 {
		b.WriteString("<pre><code>")
		b.WriteString(escapeHTML(strings.Join(codeLines, "\n")))
		b.WriteString("</code></pre>")
	}

	return b.String()
}

var (
	reInlineCodeHTML = regexp.MustCompile("`([^`]+)`")
	reBoldAstHTML    = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reBoldUndHTML    = regexp.MustCompile(`__(.+?)__`)
	reItalicAstHTML  = regexp.MustCompile(`(?:^|[^*])\*([^*]+?)\*(?:[^*]|$)`)
	reStrikeHTML     = regexp.MustCompile(`~~(.+?)~~`)
	reLinkHTML       = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
)

// convertInlineHTML converts inline Markdown formatting to HTML.
// Processing order matters: links first (to protect URLs), then code, bold, italic, strikethrough.
func convertInlineHTML(s string) string {
	// Protect inline code first — extract, replace with placeholders, process rest, then restore.
	type codeSpan struct {
		placeholder string
		html        string
	}
	var codes []codeSpan
	codeIdx := 0
	s = reInlineCodeHTML.ReplaceAllStringFunc(s, func(m string) string {
		inner := m[1 : len(m)-1]
		ph := "\x00CODE" + string(rune('0'+codeIdx)) + "\x00"
		codes = append(codes, codeSpan{placeholder: ph, html: "<code>" + escapeHTML(inner) + "</code>"})
		codeIdx++
		return ph
	})

	// Links [text](url) → <a href="url">text</a>
	s = reLinkHTML.ReplaceAllStringFunc(s, func(m string) string {
		sm := reLinkHTML.FindStringSubmatch(m)
		if len(sm) < 3 {
			return escapeHTML(m)
		}
		return `<a href="` + escapeHTML(sm[2]) + `">` + escapeHTML(sm[1]) + `</a>`
	})

	// Bold **text** and __text__
	s = reBoldAstHTML.ReplaceAllString(s, "<b>$1</b>")
	s = reBoldUndHTML.ReplaceAllString(s, "<b>$1</b>")

	// Strikethrough ~~text~~
	s = reStrikeHTML.ReplaceAllString(s, "<s>$1</s>")

	// Italic *text* — be careful not to match ** (already processed)
	s = reItalicAstHTML.ReplaceAllStringFunc(s, func(m string) string {
		// The regex may capture surrounding non-* chars; find the actual *content*
		idx := strings.Index(m, "*")
		if idx < 0 {
			return m
		}
		lastIdx := strings.LastIndex(m, "*")
		if lastIdx <= idx {
			return m
		}
		prefix := m[:idx]
		inner := m[idx+1 : lastIdx]
		suffix := m[lastIdx+1:]
		return prefix + "<i>" + inner + "</i>" + suffix
	})

	// Escape remaining special chars that aren't already in tags
	// (We skip full escape here since tags are already inserted)

	// Restore code placeholders
	for _, c := range codes {
		s = strings.Replace(s, c.placeholder, c.html, 1)
	}

	return s
}

func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// SplitMessageCodeFenceAware splits text into chunks respecting code fence boundaries.
// When a chunk boundary falls inside a code block, the fence is closed at the end of
// the chunk and re-opened at the start of the next chunk.
func SplitMessageCodeFenceAware(text string, maxLen int) []string {
	if len(text) <= maxLen {
		return []string{text}
	}

	lines := strings.Split(text, "\n")
	var chunks []string
	var current []string
	currentLen := 0
	openFence := "" // the ``` opening line, or "" if outside code block

	for _, line := range lines {
		lineLen := len(line) + 1 // +1 for newline

		if currentLen+lineLen > maxLen && len(current) > 0 {
			chunk := strings.Join(current, "\n")
			if openFence != "" {
				chunk += "\n```"
			}
			chunks = append(chunks, chunk)

			current = nil
			currentLen = 0
			if openFence != "" {
				current = append(current, openFence)
				currentLen = len(openFence) + 1
			}
		}

		current = append(current, line)
		currentLen += lineLen

		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if openFence != "" {
				openFence = ""
			} else {
				openFence = trimmed
			}
		}
	}

	if len(current) > 0 {
		chunk := strings.Join(current, "\n")
		if openFence != "" {
			chunk += "\n```"
		}
		chunks = append(chunks, chunk)
	}

	return chunks
}
