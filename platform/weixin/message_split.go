package weixin

import "unicode/utf8"

// splitUTF8 splits text under a rune limit. It prefers readable boundaries
// before falling back to a UTF-8-safe hard cut.
func splitUTF8(s string, maxRunes int) []string {
	if maxRunes <= 0 || utf8.RuneCountInString(s) <= maxRunes {
		return []string{s}
	}

	parts := make([]string, 0, utf8.RuneCountInString(s)/maxRunes+1)
	for utf8.RuneCountInString(s) > maxRunes {
		cut := semanticRuneCut(s, maxRunes)
		parts = append(parts, s[:cut])
		s = s[cut:]
	}
	if s != "" {
		parts = append(parts, s)
	}
	return parts
}

func semanticRuneCut(s string, maxRunes int) int {
	candidates := runeSplitCandidates(s, maxRunes)
	for _, priority := range []struct {
		points []runeSplitPoint
		min    int
	}{
		{points: candidates.paragraphs, min: percentCeil(maxRunes, 70)},
		{points: candidates.lines, min: percentCeil(maxRunes, 80)},
		{points: candidates.sentences, min: percentCeil(maxRunes, 85)},
		{points: candidates.soft, min: percentCeil(maxRunes, 90)},
	} {
		if cut := selectRuneSplitPoint(priority.points, priority.min); cut > 0 {
			return cut
		}
	}
	return hardRuneCut(s, maxRunes)
}

type runeSplitPoint struct {
	runes int
	bytes int
}

type runeSplitCandidateSet struct {
	paragraphs []runeSplitPoint
	lines      []runeSplitPoint
	sentences  []runeSplitPoint
	soft       []runeSplitPoint
}

func runeSplitCandidates(s string, maxRunes int) runeSplitCandidateSet {
	var candidates runeSplitCandidateSet
	prevNewline := false
	runeCount := 0
	for i, r := range s {
		runeCount++
		if runeCount > maxRunes {
			break
		}

		point := runeSplitPoint{runes: runeCount, bytes: runeByteEnd(s, i)}
		switch {
		case r == '\n' && prevNewline:
			candidates.paragraphs = append(candidates.paragraphs, point)
		case r == '\n':
			candidates.lines = append(candidates.lines, point)
		case isSentenceBoundary(r):
			candidates.sentences = append(candidates.sentences, point)
		case isSoftBoundary(r):
			candidates.soft = append(candidates.soft, point)
		}
		prevNewline = r == '\n'
	}
	return candidates
}

func selectRuneSplitPoint(points []runeSplitPoint, minRunes int) int {
	for i := len(points) - 1; i >= 0; i-- {
		if points[i].runes >= minRunes {
			return points[i].bytes
		}
	}
	return 0
}

func percentCeil(n, percent int) int {
	if n <= 0 {
		return 0
	}
	return (n*percent + 99) / 100
}

func isSentenceBoundary(r rune) bool {
	switch r {
	case '。', '！', '？', '；', '.', '!', '?', ';':
		return true
	default:
		return false
	}
}

func isSoftBoundary(r rune) bool {
	switch r {
	case '，', ',', ' ', '\t':
		return true
	default:
		return false
	}
}

func hardRuneCut(s string, maxRunes int) int {
	runeCount := 0
	for i := range s {
		runeCount++
		if runeCount == maxRunes {
			return runeByteEnd(s, i)
		}
	}
	return len(s)
}

func runeByteEnd(s string, start int) int {
	_, size := utf8.DecodeRuneInString(s[start:])
	if size <= 0 {
		return start + 1
	}
	return start + size
}
