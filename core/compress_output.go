package core

import "strings"

// Compression output has a turn-wide byte budget, independent of the final
// status. Omit whole redacted events rather than cutting credentials or JSON.
const maxCompressOutputBytes = 32 * 1024

// Conservative cross-platform payload cap (including tool labels). This is
// at most 1024 UTF-16 units even for non-BMP text, leaving formatting
// headroom for Discord and Telegram. Do not add prefixes after splitting.
const maxCompressMessageRunes = 512

const maxCompressToolResults = 5

type compressOutputBudget struct {
	used    int
	omitted bool
}

func (b *compressOutputBudget) take(text string) string {
	if text == "" || b.omitted {
		return ""
	}
	text = strings.ToValidUTF8(text, "�")
	// Reserve a single omission marker even when many small events fill up.
	if len(text) > maxCompressOutputBytes-len("\n…")-b.used {
		b.omitted = true
		b.used += len("\n…")
		return "\n…"
	}
	b.used += len(text)
	return text
}

func (e *Engine) sendCompressOutput(p Platform, replyCtx any, text string, reply bool) {
	// Inputs are already bounded/redacted; normalize invalid UTF-8 before
	// rune splitting. This bounds raw chunks and reserves formatting headroom;
	// destination formatters may expand them beyond the raw rune cap.
	text = strings.ToValidUTF8(text, "�")
	for _, chunk := range splitMessage(text, maxCompressMessageRunes) {
		if chunk == "" {
			continue
		}
		var err error
		if reply {
			err = e.replyWithError(p, replyCtx, chunk)
		} else {
			err = e.sendWithError(p, replyCtx, chunk)
		}
		if err != nil {
			return
		}
	}
}

// Text events may be stream fragments: do not TrimSpace and join words.
// The size check precedes redaction; oversized raw fragments are wholly omitted.
func boundedCompressText(text string) string {
	if len(text) > maxCompressOutputBytes {
		return "…"
	}
	text = strings.ToValidUTF8(RedactToolText(text), "�")
	if len(text) > maxCompressOutputBytes {
		return "…"
	}
	return text
}
