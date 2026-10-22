package core

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
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

	// flushTable renders buffered table rows inside a <pre> block with aligned columns.
	//
	// Inline formatting in cells (bold/italic/inline-code/strikethrough/links)
	// is rendered as Telegram HTML tags; Telegram permits <b>, <i>, <u>, <s>,
	// <code>, <a> inside <pre>, so `**foo**` becomes a bold "foo" rather than
	// four literal asterisks. Column widths are computed from the *visual*
	// (post-strip) rune length so that ` | ` separators still line up even
	// though the rendered HTML bytes are longer than the plain text.
	flushTable := func() {
		if len(tblLines) == 0 {
			return
		}

		// Parse all rows into cells, skipping separator rows.
		type row struct {
			cells []string
			isSep bool
		}
		var rows []row
		for _, tl := range tblLines {
			tl = strings.TrimSpace(tl)
			if reTableSep.MatchString(tl) {
				rows = append(rows, row{isSep: true})
				continue
			}
			inner := tl
			if strings.HasPrefix(tl, "|") && strings.HasSuffix(tl, "|") && len(tl) >= 2 {
				inner = strings.TrimSpace(tl[1 : len(tl)-1])
			}
			cells := strings.Split(inner, "|")
			for k := range cells {
				cells[k] = strings.TrimSpace(cells[k])
			}
			rows = append(rows, row{cells: cells})
		}

		// Compute max width per column using the visual rune length of each
		// cell (markdown markers stripped). This keeps ASCII columns aligned
		// even after `**x**` expands to `<b>x</b>` in the rendered output.
		numCols := 0
		for _, r := range rows {
			if !r.isSep && len(r.cells) > numCols {
				numCols = len(r.cells)
			}
		}
		colWidths := make([]int, numCols)
		for _, r := range rows {
			if r.isSep {
				continue
			}
			for k, c := range r.cells {
				if k < numCols {
					if w := tableCellVisualWidth(c); w > colWidths[k] {
						colWidths[k] = w
					}
				}
			}
		}

		// Render inside <pre>.
		b.WriteString("<pre>")
		first := true
		for _, r := range rows {
			if !first {
				b.WriteByte('\n')
			}
			first = false
			if r.isSep {
				// Draw separator line matching column widths.
				for k, w := range colWidths {
					if k > 0 {
						b.WriteString("-+-")
					}
					b.WriteString(strings.Repeat("-", w))
				}
			} else {
				for k := 0; k < numCols; k++ {
					if k > 0 {
						b.WriteString(" | ")
					}
					cell := ""
					if k < len(r.cells) {
						cell = r.cells[k]
					}
					// Render inline formatting to HTML tags (Telegram accepts
					// <b>/<i>/<code>/<a>/etc. inside <pre>). Falls back to
					// plain HTML-escaped text when there is no formatting.
					b.WriteString(convertInlineHTML(cell))
					// Pad to column width using the *visual* length so the
					// `|` separators still line up in the rendered message.
					if pad := colWidths[k] - tableCellVisualWidth(cell); pad > 0 {
						b.WriteString(strings.Repeat(" ", pad))
					}
				}
			}
		}
		b.WriteString("</pre>")
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
	reInlineCodeHTML = regexp.MustCompile("`([^`]+)`")
	reBoldItalicHTML = regexp.MustCompile(`\*\*\*(.+?)\*\*\*`)
	reBoldAstHTML    = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reBoldUndHTML    = regexp.MustCompile(`__(.+?)__`)
	reItalicAstHTML  = regexp.MustCompile(`(?:^|[^*])\*([^*]+?)\*(?:[^*]|$)`)
	reStrikeHTML     = regexp.MustCompile(`~~(.+?)~~`)
	reLinkHTML       = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	reWikilinkHTML   = regexp.MustCompile(`\[\[([^\]|]+)\|([^\]]+)\]\]|\[\[([^\]]+)\]\]`)
	reUnorderedList  = regexp.MustCompile(`^(\s*)[-*]\s+(.*)$`)
	reOrderedList    = regexp.MustCompile(`^(\s*)\d+\.\s+(.*)$`)
	reTableSep       = regexp.MustCompile(`^\|[\s:|-]+\|$`)
	reCallout        = regexp.MustCompile(`^\[!(\w+)\]\s*(.*)$`)
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

	// The placeholder key embeds phIdx as decimal digits. The previous
	// `string(rune('0'+phIdx))` form rolled past '9' once phIdx hit 10, so
	// phIdx == 12 produced a key containing '<' and phIdx == 14 produced one
	// containing '>'. Step 3 (escapeHTML on the entire string) then rewrote
	// '<'/'>' inside those keys to "&lt;"/"&gt;" before step 8 could restore
	// them, leaking literal "\x00PH<\x00" / "\x00PH>\x00" fragments into the
	// rendered Telegram message and dropping the original code/link content.
	// Decimal digits stay in the safe ASCII range regardless of phIdx, and
	// no two indices collide.
	nextPH := func(html string) string {
		key := "\x00PH" + strconv.Itoa(phIdx) + "\x00"
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

// tableCellVisualWidth returns the rune count of `cell` after stripping the
// markdown markers that convertInlineHTML would remove when rendering. Used
// to compute column widths for <pre>-wrapped tables so that ` | ` separators
// still line up even though the rendered HTML bytes are longer than the
// visible text.
//
// This is deliberately approximate: it counts each rune as one column, so
// East-Asian wide characters (which occupy two monospace cells on most
// clients) will misalign by the same amount the previous byte-based code
// did. Callers that need exact visual width can switch to unicode width
// tables later; this helper's contract is "strip formatting markers, count
// runes".
func tableCellVisualWidth(cell string) int {
	// Strip bold ***x***, **x**, __x__ and bold-italic.
	cell = reBoldItalicHTML.ReplaceAllString(cell, "$1")
	cell = reBoldAstHTML.ReplaceAllString(cell, "$1")
	cell = reBoldUndHTML.ReplaceAllString(cell, "$1")
	// Strip strikethrough ~~x~~.
	cell = reStrikeHTML.ReplaceAllString(cell, "$1")
	// Strip inline code `x`.
	cell = reInlineCodeHTML.ReplaceAllString(cell, "$1")
	// Strip links [text](url) — keep link text only.
	cell = reLinkHTML.ReplaceAllString(cell, "$1")
	// Italic is matched with boundary chars in reItalicAstHTML, which would
	// swallow the boundary on replace. Use a local, boundary-free pattern
	// since cell content is already trimmed and we only need to drop *x*.
	cell = reTableCellItalic.ReplaceAllString(cell, "$1")
	return utf8.RuneCountInString(cell)
}

// reTableCellItalic is used ONLY by tableCellVisualWidth to strip `*x*` from
// a cell for width measurement. It is NOT used for rendering — rendering
// still goes through the main convertInlineHTML path with its stricter
// boundary-aware italic regex.
var reTableCellItalic = regexp.MustCompile(`\*([^*]+)\*`)

// SplitMessageCodeFenceAware splits text into chunks no larger than maxLen runes,
// preferring line boundaries. When a chunk boundary falls inside a code block,
// the fence is closed at the end of the chunk and re-opened at the start of the
// next chunk. If a single line exceeds maxLen, it is split within the line at
// rune boundaries.
func SplitMessageCodeFenceAware(text string, maxLen int) []string {
	if utf8.RuneCountInString(text) <= maxLen {
		return []string{text}
	}

	// closingFence is appended when flushing a chunk that is inside a code block.
	const closingFence = "\n```"
	const closingFenceLen = 4 // rune count of "\n```"

	lines := strings.Split(text, "\n")
	var chunks []string
	var current []string
	// currentLen tracks rune count of strings.Join(current, "\n") + 1.
	// The invariant is: actual_chunk_runes = currentLen - 1.
	// This +1 accounting lets the fit check use a single expression.
	currentLen := 0
	openFence := "" // opening ``` line when inside a code block, else ""

	// effectiveLimit returns the number of "currentLen units" available before
	// the chunk (plus closingFence if needed) would exceed maxLen.
	effectiveLimit := func() int {
		if openFence != "" {
			return maxLen - closingFenceLen
		}
		return maxLen
	}

	// flush emits the current chunk and resets state, re-seeding with the open
	// fence header so the next chunk is a valid continuation of the code block.
	flush := func() {
		if len(current) == 0 {
			return
		}
		chunk := strings.Join(current, "\n")
		if openFence != "" {
			chunk += closingFence
		}
		chunks = append(chunks, chunk)
		current = nil
		currentLen = 0
		if openFence != "" {
			current = append(current, openFence)
			currentLen = utf8.RuneCountInString(openFence) + 1
		}
	}

	for _, line := range lines {
		lineRunes := []rune(line)
		limit := effectiveLimit()

		// Fast path: line fits in the current chunk.
		if currentLen+len(lineRunes)+1 <= limit {
			current = append(current, line)
			currentLen += len(lineRunes) + 1
		} else {
			// Line doesn't fit; flush and try again with a fresh chunk.
			flush()
			limit = effectiveLimit()

			if currentLen+len(lineRunes)+1 <= limit {
				// Fits after flush.
				current = append(current, line)
				currentLen += len(lineRunes) + 1
			} else {
				// The line itself exceeds the limit; split within the line at
				// rune boundaries, flushing between parts as needed.
				remaining := lineRunes
				for len(remaining) > 0 {
					limit = effectiveLimit()
					// avail = how many runes we can add to current.
					// Since currentLen = actual_runes + 1, actual_runes = currentLen - 1,
					// and a new part adds 1 joining \n plus its own runes:
					//   new_actual = (currentLen-1) + 1 + avail = currentLen + avail
					// We need new_actual <= limit, so avail <= limit - currentLen.
					avail := limit - currentLen
					if avail <= 0 {
						// Shouldn't happen after flush, but guard against it.
						flush()
						limit = effectiveLimit()
						avail = limit - currentLen
					}
					if avail > len(remaining) {
						avail = len(remaining)
					}
					current = append(current, string(remaining[:avail]))
					currentLen += avail + 1
					remaining = remaining[avail:]
					if len(remaining) > 0 {
						flush()
					}
				}
			}
		}

		// Track code fence state after processing the line.
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
