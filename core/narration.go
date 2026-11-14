package core

import "strings"

// narrationProgress is [display] narration = "progress": the text an agent
// writes on the way to its answer -- before a tool call, a thinking block or
// a permission prompt -- goes into the turn's progress card as an update
// instead of into messages of its own, and the text is not streamed as a
// preview, since until the turn ends nobody knows which segment is the
// answer. The answer is the one message a turn sends; when the turn ends on
// a tool call with no text after it, the last narration is the answer. A
// platform without progress cards gets the narration as messages, as with
// "message".
const narrationProgress = "progress"

// narrationInProgress reports whether narration goes into the progress card.
func (e *Engine) narrationInProgress() bool {
	return e.display.Narration == narrationProgress
}

// narrate puts one finished narration segment into the progress card and
// reports whether it did; false means the caller sends it as a message.
// A blank segment or a bare NO_REPLY is handled by showing nothing.
func narrate(cp *compactProgressWriter, segment string) bool {
	segment = strings.TrimSpace(segment)
	if segment == "" || isSilentReply(segment) {
		return true
	}
	return cp.AppendStructured(ProgressCardEntry{Kind: ProgressEntryInfo, Text: segment}, segment)
}
