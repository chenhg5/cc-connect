package wecom

import (
	"regexp"
	"strings"
)

// WeCom stream/markdown content renders a CommonMark-style delimiter subset.
// A closing ** cannot end a bold span when it is preceded by punctuation and
// followed by a letter (flanking rules), so agent-authored text like
// "**标题：**内容" — the fullwidth colon inside the markers — renders the
// asterisks literally. repairBoldFlanking moves trailing closing punctuation
// outside the bold span ("**标题**：内容"), which both strict and lenient
// parsers render as bold. Links, headings and other syntax are untouched.
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
// by closing punctuation (e.g. "**标题：**" → "**标题**："). Fenced code
// blocks and inline code spans are left alone.
func repairBoldFlanking(s string) string {
	if !strings.Contains(s, "**") {
		return s
	}
	var out strings.Builder
	out.Grow(len(s))
	inFence := false
	for _, line := range strings.SplitAfter(s, "\n") {
		if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			out.WriteString(line)
			continue
		}
		if inFence {
			out.WriteString(line)
			continue
		}
		out.WriteString(repairBoldFlankingLine(line))
	}
	return out.String()
}

func repairBoldFlankingLine(line string) string {
	spans := reWSCodeSpan.FindAllStringIndex(line, -1)
	if len(spans) == 0 {
		return repairBoldFlankingText(line)
	}
	var b strings.Builder
	b.Grow(len(line))
	pos := 0
	for _, sp := range spans {
		b.WriteString(repairBoldFlankingText(line[pos:sp[0]]))
		b.WriteString(line[sp[0]:sp[1]])
		pos = sp[1]
	}
	b.WriteString(repairBoldFlankingText(line[pos:]))
	return b.String()
}

func repairBoldFlankingText(s string) string {
	return reWSCBoldTrailingPunct.ReplaceAllStringFunc(s, func(m string) string {
		sub := reWSCBoldTrailingPunct.FindStringSubmatch(m)
		inner, punct := sub[1], sub[2]
		for _, r := range punct {
			if opener, ok := wsPairedOpeners[r]; ok &&
				strings.Count(inner, string(opener)) > strings.Count(inner, string(r)) {
				// The closer pairs with an opener inside the bold span —
				// moving it out would break the pair. Keep everything inside
				// and append a space after the closing ** instead: a closing
				// delimiter followed by whitespace is flanking-valid.
				return "**" + inner + punct + "** "
			}
		}
		return "**" + inner + "**" + punct
	})
}
