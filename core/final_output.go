package core

import (
	"log/slog"
	"strings"
)

// displayModeFinal is [display] mode = "final": a turn sends nothing but its
// final answer -- no instant reply, streaming card, preview, progress card,
// thinking or tool message, reply footer, and no system notices such as the
// auto-compress or idle-reset announcements. Errors are still reported and
// permission prompts still ask. Unlike "quiet", text the agent wrote before
// its last tool call or thinking block is not part of the answer.
const displayModeFinal = "final"

// finalOutput reports whether the project is in final output mode.
func (e *Engine) finalOutput() bool {
	return e.display.Mode == displayModeFinal
}

// notice sends a system notice (not an answer) unless the project is in
// final output mode, where only answers reach the chat.
func (e *Engine) notice(p Platform, replyCtx any, content string) {
	if e.finalOutput() {
		slog.Debug("final output: system notice not sent", "content", previewText(content, 120))
		return
	}
	e.send(p, replyCtx, content)
}

// finalTurnText picks a turn's answer in final output mode: the result
// event's text when the agent reports one, else the text after the last
// tool call or thinking block (narration before a tool call is not part of
// the answer).
func finalTurnText(resultContent string, textParts []string, lastSegmentStart int) string {
	if strings.TrimSpace(resultContent) != "" {
		return resultContent
	}
	if lastSegmentStart < 0 || lastSegmentStart > len(textParts) {
		lastSegmentStart = 0
	}
	return strings.Join(textParts[lastSegmentStart:], "")
}
