package wecom

import "unicode/utf8"

// splitByBytes splits text under a UTF-8 byte limit. It prefers readable
// boundaries before falling back to byte-safe hard cuts.
func splitByBytes(s string, maxBytes int) []string {
	if len(s) <= maxBytes || maxBytes <= 0 {
		return []string{s}
	}

	parts := make([]string, 0, len(s)/maxBytes+1)
	for len(s) > maxBytes {
		cut := semanticByteCut(s, maxBytes)
		parts = append(parts, s[:cut])
		s = s[cut:]
	}
	if s != "" {
		parts = append(parts, s)
	}
	return parts
}

func semanticByteCut(s string, maxBytes int) int {
	candidates := splitCandidates(s, maxBytes)
	if cut := selectWindowSplit(candidates.paragraphs, windowStart(maxBytes, 70)); cut > 0 {
		return cut
	}
	if cut := selectWindowSplit(candidates.lines, windowStart(maxBytes, 80)); cut > 0 {
		return cut
	}
	if cut := selectWindowSplit(candidates.sentences, windowStart(maxBytes, 85)); cut > 0 {
		return cut
	}
	if cut := selectWindowSplit(candidates.soft, windowStart(maxBytes, 90)); cut > 0 {
		return cut
	}
	return hardByteCut(s, maxBytes)
}

type byteSplitCandidates struct {
	paragraphs []int
	lines      []int
	sentences  []int
	soft       []int
}

func splitCandidates(s string, maxBytes int) byteSplitCandidates {
	var candidates byteSplitCandidates
	prevNewline := false
	for i, r := range s {
		if i >= maxBytes {
			break
		}

		size := utf8.RuneLen(r)
		if size < 0 {
			size = 1
		}
		next := i + size
		switch {
		case r == '\n' && prevNewline:
			candidates.paragraphs = append(candidates.paragraphs, next)
		case r == '\n':
			candidates.lines = append(candidates.lines, next)
		case isSentenceBoundary(r):
			candidates.sentences = append(candidates.sentences, next)
		case isSoftBoundary(r):
			candidates.soft = append(candidates.soft, next)
		}
		prevNewline = r == '\n'
	}
	return candidates
}

func windowStart(maxBytes, percent int) int {
	start := maxBytes * percent / 100
	if start < 1 {
		return 1
	}
	return start
}

func selectWindowSplit(candidates []int, searchStart int) int {
	best := 0
	for _, cut := range candidates {
		if cut >= searchStart {
			best = cut
		}
	}
	return best
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

func hardByteCut(s string, maxBytes int) int {
	end := maxBytes
	if end > len(s) {
		end = len(s)
	}
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	if end == 0 {
		_, size := utf8.DecodeRuneInString(s)
		return size
	}
	return end
}
