package core

import (
	"regexp"
	"strings"
)

// Slack mrkdwn regex patterns (compiled once).
var (
	reSlackCodeBlock  = regexp.MustCompile("(?s)```[a-zA-Z]*\n?(.*?)```")
	reSlackInlineCode = regexp.MustCompile("`([^`]+)`")
	reSlackLink       = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	reSlackBoldItalic = regexp.MustCompile(`\*\*\*(.+?)\*\*\*`)
	reSlackBold       = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reSlackStrike     = regexp.MustCompile(`~~(.+?)~~`)
	reSlackHeading    = regexp.MustCompile(`^#{1,6}\s+(.+)$`)
	reSlackImgTag     = regexp.MustCompile(`!\[([^\]]*)\]\(([^)]+)\)`)
)

// MarkdownToSlackMrkdwn converts standard Markdown to Slack mrkdwn format.
//
// Key conversions:
//   - **bold** → *bold*
//   - *italic* → _italic_ (single asterisk → underscore)
//   - ~~strike~~ → ~strike~
//   - [text](url) → <url|text>
//   - # Heading → *Heading*
//   - Code blocks and inline code are preserved as-is.
func MarkdownToSlackMrkdwn(md string) string {
	// Split into code blocks vs non-code segments so we don't
	// accidentally convert syntax inside code.
	type segment struct {
		text   string
		isCode bool
	}

	var segments []segment
	rest := md
	for {
		loc := reSlackCodeBlock.FindStringIndex(rest)
		if loc == nil {
			segments = append(segments, segment{text: rest})
			break
		}
		if loc[0] > 0 {
			segments = append(segments, segment{text: rest[:loc[0]]})
		}
		segments = append(segments, segment{text: rest[loc[0]:loc[1]], isCode: true})
		rest = rest[loc[1]:]
	}

	var b strings.Builder
	b.Grow(len(md) + len(md)/8)

	for _, seg := range segments {
		if seg.isCode {
			b.WriteString(seg.text)
			continue
		}
		b.WriteString(convertSlackInline(seg.text))
	}

	return b.String()
}

// convertSlackInline converts inline Markdown formatting to Slack mrkdwn.
// Must NOT be called on code block content.
func convertSlackInline(s string) string {
	// Protect inline code from further processing.
	type placeholder struct {
		key     string
		content string
	}
	var phs []placeholder
	phIdx := 0

	nextPH := func(content string) string {
		key := "\x00SL" + string(rune('0'+phIdx)) + "\x00"
		phs = append(phs, placeholder{key: key, content: content})
		phIdx++
		return key
	}

	// 1. Protect inline code spans.
	s = reSlackInlineCode.ReplaceAllStringFunc(s, func(m string) string {
		return nextPH(m) // keep as-is
	})

	// 2. Image tags → just the alt text or URL (Slack can't render inline images).
	s = reSlackImgTag.ReplaceAllStringFunc(s, func(m string) string {
		sm := reSlackImgTag.FindStringSubmatch(m)
		if sm[1] != "" {
			return sm[1]
		}
		return sm[2]
	})

	// 3. Links: [text](url) → <url|text>
	s = reSlackLink.ReplaceAllStringFunc(s, func(m string) string {
		sm := reSlackLink.FindStringSubmatch(m)
		if len(sm) < 3 {
			return m
		}
		return nextPH("<" + sm[2] + "|" + sm[1] + ">")
	})

	// 4. Bold-italic: ***text*** → *_text_* (must precede bold)
	s = reSlackBoldItalic.ReplaceAllString(s, "*_${1}_*")

	// 5. Bold: **text** → *text*
	s = reSlackBold.ReplaceAllString(s, "*${1}*")

	// 6. Strikethrough: ~~text~~ → ~text~
	s = reSlackStrike.ReplaceAllString(s, "~${1}~")

	// 7. Headings: # Heading → *Heading* (line-by-line)
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if m := reSlackHeading.FindStringSubmatch(line); m != nil {
			lines[i] = "*" + m[1] + "*"
		}
	}
	s = strings.Join(lines, "\n")

	// Restore placeholders.
	for _, ph := range phs {
		s = strings.Replace(s, ph.key, ph.content, 1)
	}

	return s
}
