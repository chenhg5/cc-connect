package wecom

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// WeCom stream/markdown content renders a CommonMark-style delimiter subset.
// A closing ** cannot end a bold span when it is preceded by punctuation and
// followed by a non-whitespace character (flanking rules), so agent-authored
// text like "**标题：**内容" renders the asterisks literally. repairBoldFlanking
// moves trailing closing punctuation outside the bold span ("**标题**：内容"),
// which both strict and lenient parsers render as bold. Links, headings and
// other syntax are untouched.
//
// The match is only a candidate: repairBoldFlankingText checks the character
// after the closing delimiter, so spans that already render correctly (for
// example "**Note.**" at end of line) are left alone.
var reWSCBoldTrailingPunct = regexp.MustCompile(`\*\*([^*\n]+?)([：；，。！？、…—～·:;,.!?~）】》」』”’)\]]+)\*\*`)

// reWSCodeSpan matches single-backtick inline code spans on one line. Their
// contents must not be rewritten — inside code, ** stays literal by design.
var reWSCodeSpan = regexp.MustCompile("`[^`\n]+`")

// wsPairedOpeners maps a closing bracket/quote rune to its opening partner.
// If the opener lives inside the bold span, the closer must stay inside too.
var wsPairedOpeners = map[rune]rune{
	'）': '（', '】': '【', '》': '《', '」': '「', '』': '『', '”': '“', '’': '‘',
	')': '(', ']': '[',
}

// repairBoldFlanking fixes **bold** spans whose closing delimiter is preceded
// by closing punctuation and followed by a non-whitespace character (e.g.
// "**标题：**内容" → "**标题**：内容"). Fenced code blocks and inline code
// spans are left alone.
func repairBoldFlanking(s string) string {
	if !strings.Contains(s, "**") {
		return s
	}
	var out strings.Builder
	out.Grow(len(s))
	inFence := false
	var fenceChar byte
	for _, line := range strings.SplitAfter(s, "\n") {
		isFence, nextInFence, nextFenceChar := wsFenceLine(line, inFence, fenceChar)
		if inFence || isFence {
			out.WriteString(line)
		} else {
			out.WriteString(repairBoldFlankingLine(line))
		}
		inFence, fenceChar = nextInFence, nextFenceChar
	}
	return out.String()
}

// wsFenceLine reports whether line is a block fence opener/closer and returns
// the updated fence state. An opener may carry an info string, but a line with
// another run of the same marker is inline code (for example ```code```), not
// a block fence. A closer must be the marker alone.
func wsFenceLine(line string, inFence bool, fenceChar byte) (isFence, nextInFence bool, nextFenceChar byte) {
	trimmed := strings.TrimSpace(line)
	c, n, ok := wsFenceRun(trimmed)
	if !ok {
		return false, inFence, fenceChar
	}
	rest := trimmed[n:]
	if !inFence {
		if strings.ContainsRune(rest, rune(c)) {
			return false, false, 0
		}
		return true, true, c
	}
	if c == fenceChar && strings.TrimSpace(rest) == "" {
		return true, false, 0
	}
	return false, inFence, fenceChar
}

// wsFenceRun returns the fence marker and its run length when the trimmed line
// starts with at least three backticks or tildes.
func wsFenceRun(trimmed string) (byte, int, bool) {
	if len(trimmed) < 3 {
		return 0, 0, false
	}
	c := trimmed[0]
	if c != '`' && c != '~' {
		return 0, 0, false
	}
	n := 0
	for n < len(trimmed) && trimmed[n] == c {
		n++
	}
	if n < 3 {
		return 0, 0, false
	}
	return c, n, true
}

// repairBoldFlankingLine protects inline code spans with placeholders so a bold
// span may cross them, then restores the original code text.
func repairBoldFlankingLine(line string) string {
	spans := reWSCodeSpan.FindAllStringIndex(line, -1)
	if len(spans) == 0 {
		return repairBoldFlankingText(line)
	}

	var b strings.Builder
	b.Grow(len(line))
	pos := 0
	for i, sp := range spans {
		b.WriteString(line[pos:sp[0]])
		b.WriteString(wsCodePlaceholder(i))
		pos = sp[1]
	}
	b.WriteString(line[pos:])

	out := repairBoldFlankingText(b.String())
	for i, sp := range spans {
		out = strings.Replace(out, wsCodePlaceholder(i), line[sp[0]:sp[1]], 1)
	}
	return out
}

func wsCodePlaceholder(i int) string {
	return "\x00ws-code-" + strconv.Itoa(i) + "\x00"
}

// repairBoldFlankingText rewrites only spans that actually fail the closing
// delimiter's flanking rule: preceded by punctuation and followed by a
// non-whitespace rune. Runs of three or more asterisks are left untouched.
func repairBoldFlankingText(s string) string {
	if !strings.Contains(s, "**") {
		return s
	}
	matches := reWSCBoldTrailingPunct.FindAllStringSubmatchIndex(s, -1)
	if len(matches) == 0 {
		return s
	}

	var b strings.Builder
	b.Grow(len(s))
	pos := 0
	for _, m := range matches {
		start, end := m[0], m[1]
		if start < pos {
			continue
		}
		if !wsBoldSpanNeedsRepair(s, start, end) {
			continue
		}

		inner := s[m[2]:m[3]]
		punct := s[m[4]:m[5]]
		repl := "**" + inner + "**" + punct
		for _, r := range punct {
			if opener, ok := wsPairedOpeners[r]; ok &&
				strings.Count(inner, string(opener)) > strings.Count(inner, string(r)) {
				// The closer pairs with an opener inside the bold span —
				// moving it out would break the pair. Keep everything inside
				// and append a space after the closing ** instead: a closing
				// delimiter followed by whitespace is flanking-valid.
				repl = "**" + inner + punct + "** "
				break
			}
		}

		b.WriteString(s[pos:start])
		b.WriteString(repl)
		pos = end
	}
	if pos == 0 {
		return s
	}
	b.WriteString(s[pos:])
	return b.String()
}

// wsBoldSpanNeedsRepair checks the context around a candidate match. The regex
// guarantees punctuation before the closing delimiter; here we reject
// triple-asterisk runs and spans that are already right-flanking.
func wsBoldSpanNeedsRepair(s string, start, end int) bool {
	// A run of three or more asterisks is bold+italic or a horizontal rule;
	// moving one delimiter would split the run.
	if start > 0 && s[start-1] == '*' {
		return false
	}
	if start+2 < len(s) && s[start+2] == '*' {
		return false
	}
	if end >= 2 && s[end-3] == '*' {
		return false
	}
	if end < len(s) && s[end] == '*' {
		return false
	}

	// Already right-flanking at end of line or before whitespace: the closing
	// delimiter is valid, so leave the span exactly as written.
	if end >= len(s) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(s[end:])
	return !unicode.IsSpace(r)
}
