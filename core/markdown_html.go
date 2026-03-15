package core

import (
	"regexp"
	"strings"
)

// MarkdownToSimpleHTML converts common Markdown to a simplified HTML subset.
// Supported tags: <b>, <i>, <s>, <code>, <pre>, <a href="">, <blockquote>.
// Useful for platforms that accept a limited set of HTML (e.g. Telegram).
func MarkdownToSimpleHTML(md string) string {
	var b strings.Builder
	b.Grow(len(md) + len(md)/4)

	lines := strings.Split(md, "\n")
	inCodeBlock := false
	codeLang := ""
	var codeLines []string
	inBlockquote := false
	var bqLines []string
	inTable := false
	var tblLines []string

	// flushBlockquote merges buffered blockquote lines into a single <blockquote>.
	// Supports Obsidian-style callouts: > [!type] Title
	flushBlockquote := func() {
		if len(bqLines) == 0 {
			return
		}
		b.WriteString("<blockquote>")
		startIdx := 0
		// Check for callout syntax in the first line
		if len(bqLines) > 0 {
			if m := reCallout.FindStringSubmatch(bqLines[0]); m != nil {
				calloutType := m[1]
				calloutTitle := m[2]
				if calloutTitle != "" {
					b.WriteString("<b>" + escapeHTML(calloutType) + ": " + escapeHTML(calloutTitle) + "</b>")
				} else {
					b.WriteString("<b>" + escapeHTML(calloutType) + "</b>")
				}
				startIdx = 1
				if startIdx < len(bqLines) {
					b.WriteByte('\n')
				}
			}
		}
		for j := startIdx; j < len(bqLines); j++ {
			if j > startIdx {
				b.WriteByte('\n')
			}
			b.WriteString(convertInlineHTML(bqLines[j]))
		}
		b.WriteString("</blockquote>")
		bqLines = bqLines[:0]
		inBlockquote = false
	}

	// flushTable renders buffered table rows as readable text.
	flushTable := func() {
		if len(tblLines) == 0 {
			return
		}
		for j, tl := range tblLines {
			if j > 0 {
				b.WriteByte('\n')
			}
			tl = strings.TrimSpace(tl)
			if reTableSep.MatchString(tl) {
				b.WriteString("——————————")
			} else {
				inner := tl[1 : len(tl)-1]
				cells := strings.Split(inner, "|")
				for k := range cells {
					cells[k] = strings.TrimSpace(cells[k])
				}
				row := strings.Join(cells, " | ")
				b.WriteString(convertInlineHTML(row))
			}
		}
		tblLines = tblLines[:0]
		inTable = false
	}

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "```") {
			if !inCodeBlock {
				if inBlockquote {
					flushBlockquote()
					b.WriteByte('\n')
				}
				if inTable {
					flushTable()
					b.WriteByte('\n')
				}
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

		// Determine line type for blockquote/table buffering
		isQuote := strings.HasPrefix(trimmed, "> ") || trimmed == ">"
		isTable := len(trimmed) > 2 && trimmed[0] == '|' && trimmed[len(trimmed)-1] == '|'

		// Flush blockquote when leaving
		if !isQuote && inBlockquote {
			flushBlockquote()
			b.WriteByte('\n')
		}
		// Flush table when leaving
		if !isTable && inTable {
			flushTable()
			b.WriteByte('\n')
		}

		// Buffer blockquote lines into a single block
		if isQuote {
			quoteContent := strings.TrimPrefix(trimmed, "> ")
			if trimmed == ">" {
				quoteContent = ""
			}
			bqLines = append(bqLines, quoteContent)
			inBlockquote = true
			continue
		}

		// Buffer table lines
		if isTable {
			tblLines = append(tblLines, trimmed)
			inTable = true
			continue
		}

		// Headings → bold
		if heading := reHeading.FindString(line); heading != "" {
			rest := strings.TrimPrefix(line, heading)
			b.WriteString("<b>")
			b.WriteString(convertInlineHTML(rest))
			b.WriteString("</b>")
		} else if reHorizontal.MatchString(trimmed) {
			b.WriteString("——————————")
		} else if m := reUnorderedList.FindStringSubmatch(line); m != nil {
			indent := strings.Repeat("  ", len(m[1])/2)
			b.WriteString(indent + "• " + convertInlineHTML(m[2]))
		} else if m := reOrderedList.FindStringSubmatch(line); m != nil {
			indent := strings.Repeat("  ", len(m[1])/2)
			numDot := strings.TrimSpace(line[:len(line)-len(m[2])])
			b.WriteString(indent + escapeHTML(numDot) + " " + convertInlineHTML(m[2]))
		} else {
			b.WriteString(convertInlineHTML(line))
		}

		if i < len(lines)-1 {
			b.WriteByte('\n')
		}
	}

	// Flush any remaining buffered state
	if inBlockquote {
		flushBlockquote()
	}
	if inTable {
		flushTable()
	}
	if inCodeBlock && len(codeLines) > 0 {
		b.WriteString("<pre><code>")
		b.WriteString(escapeHTML(strings.Join(codeLines, "\n")))
		b.WriteString("</code></pre>")
	}

	return b.String()
}

var (
	reInlineCodeHTML  = regexp.MustCompile("`([^`]+)`")
	reBoldItalicHTML  = regexp.MustCompile(`\*\*\*(.+?)\*\*\*`)
	reBoldAstHTML     = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reBoldUndHTML     = regexp.MustCompile(`__(.+?)__`)
	reItalicAstHTML   = regexp.MustCompile(`(?:^|[^*])\*([^*]+?)\*(?:[^*]|$)`)
	reStrikeHTML      = regexp.MustCompile(`~~(.+?)~~`)
	reLinkHTML        = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	reWikilinkHTML    = regexp.MustCompile(`\[\[([^\]|]+)\|([^\]]+)\]\]|\[\[([^\]]+)\]\]`)
	reUnorderedList   = regexp.MustCompile(`^(\s*)[-*]\s+(.*)$`)
	reOrderedList     = regexp.MustCompile(`^(\s*)\d+\.\s+(.*)$`)
	reTableSep        = regexp.MustCompile(`^\|[\s:|-]+\|$`)
	reCallout         = regexp.MustCompile(`^\[!(\w+)\]\s*(.*)$`)
)

// convertInlineHTML converts inline Markdown formatting to Telegram-compatible HTML.
//
// Each formatting pass (bold, strikethrough) protects its output as placeholders
// so that subsequent passes (italic) cannot match across HTML tag boundaries.
func convertInlineHTML(s string) string {
	type placeholder struct {
		key  string
		html string
	}
	var phs []placeholder
	phIdx := 0

	nextPH := func(html string) string {
		key := "\x00PH" + string(rune('0'+phIdx)) + "\x00"
		phs = append(phs, placeholder{key: key, html: html})
		phIdx++
		return key
	}

	// 1. Extract inline code → placeholder (content escaped)
	s = reInlineCodeHTML.ReplaceAllStringFunc(s, func(m string) string {
		inner := m[1 : len(m)-1]
		return nextPH("<code>" + escapeHTML(inner) + "</code>")
	})

	// 2. Extract links → placeholder (text & URL escaped)
	s = reLinkHTML.ReplaceAllStringFunc(s, func(m string) string {
		sm := reLinkHTML.FindStringSubmatch(m)
		if len(sm) < 3 {
			return m
		}
		return nextPH(`<a href="` + escapeHTML(sm[2]) + `">` + escapeHTML(sm[1]) + `</a>`)
	})

	// 2b. Wikilinks: [[Link|Text]] → Text, [[Link]] → Link
	// Don't escape here — step 3 will HTML-escape the whole remaining text.
	s = reWikilinkHTML.ReplaceAllStringFunc(s, func(m string) string {
		sm := reWikilinkHTML.FindStringSubmatch(m)
		if sm[1] != "" && sm[2] != "" {
			return sm[2]
		}
		if sm[3] != "" {
			return sm[3]
		}
		return m
	})

	// 3. HTML-escape the entire remaining text.
	s = escapeHTML(s)

	// 4. Bold-italic (***text***) → placeholder (must be before bold)
	s = reBoldItalicHTML.ReplaceAllStringFunc(s, func(m string) string {
		inner := m[3 : len(m)-3]
		return nextPH("<b><i>" + inner + "</i></b>")
	})

	// 5. Bold → placeholder (so italic regex can't cross bold boundaries)
	s = reBoldAstHTML.ReplaceAllStringFunc(s, func(m string) string {
		inner := m[2 : len(m)-2]
		return nextPH("<b>" + inner + "</b>")
	})
	s = reBoldUndHTML.ReplaceAllStringFunc(s, func(m string) string {
		inner := m[2 : len(m)-2]
		return nextPH("<b>" + inner + "</b>")
	})

	// 6. Strikethrough → placeholder
	s = reStrikeHTML.ReplaceAllStringFunc(s, func(m string) string {
		inner := m[2 : len(m)-2]
		return nextPH("<s>" + inner + "</s>")
	})

	// 7. Italic (applied last, on text with bold/strike already protected)
	s = reItalicAstHTML.ReplaceAllStringFunc(s, func(m string) string {
		idx := strings.Index(m, "*")
		if idx < 0 {
			return m
		}
		lastIdx := strings.LastIndex(m, "*")
		if lastIdx <= idx {
			return m
		}
		return m[:idx] + "<i>" + m[idx+1:lastIdx] + "</i>" + m[lastIdx+1:]
	})

	// 8. Restore all placeholders (may be nested, so iterate until stable).
	for i := 0; i <= len(phs); i++ {
		changed := false
		for _, ph := range phs {
			if strings.Contains(s, ph.key) {
				s = strings.Replace(s, ph.key, ph.html, 1)
				changed = true
			}
		}
		if !changed {
			break
		}
	}

	return s
}

func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
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
