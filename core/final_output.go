package core

import (
	"log/slog"
	"strings"
)

// displayModeFinal is [display] mode = "final": a turn sends nothing but its
// final answer -- no instant reply, streaming card, preview, progress card,
// thinking or tool message, reply footer, and no system notices such as the
// auto-compress or idle-reset announcements (the ones whose loss costs the
// user something go through warnNotice and are logged at Warn). Errors are
// still reported and permission prompts still ask. Unlike "quiet", text the
// agent wrote before its last tool call or thinking block is not part of the
// answer. Typing indicators and the TypingIndicatorDone "done" reaction are
// reactions on the user's message, not messages, and are kept: on WPS the
// done step is what removes the busy reaction, so suppressing it would
// leave the message marked busy, and on DingTalk it only fires when
// done_emoji is configured.
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

// warnNotice is notice for a notice whose loss costs the user something --
// a fresh session started instead of the resumed one, a background tool
// call that was refused. Final output mode does not send it either, but
// logs it at Warn with the session key so it stays visible in the logs.
func (e *Engine) warnNotice(p Platform, replyCtx any, sessionKey, content string) {
	if e.finalOutput() {
		slog.Warn("final output: notice not sent to the chat", "session_key", sessionKey, "notice", content)
		return
	}
	e.send(p, replyCtx, content)
}

// finalTurnText picks a turn's answer in final output mode: the result
// event's text when the agent reports one, else the last text segment that
// has any text. segmentStarts holds the index into textParts at which each
// tool call or thinking block started a new segment, in order. clean (nil:
// none) filters the result and each whole segment before it is judged
// empty -- applied to the joined segment, so an agent footer that arrived
// split across several text chunks is recognised -- and the cleaned text is
// what is returned. Narration before a tool call is not part of the
// answer, but a tool call or thinking block with no text after it, or with
// only an agent footer after it, does not erase the answer before it (a
// turn ending "answer, thinking, footer, end" answers with the answer, not
// "(empty response)"). Agents such as Codex and Kimi never put the answer
// in the result event, so for them the segments are the only source.
func finalTurnText(resultContent string, textParts []string, segmentStarts []int, clean func(string) string) string {
	if clean == nil {
		clean = func(s string) string { return s }
	}
	if result := clean(resultContent); strings.TrimSpace(result) != "" {
		return result
	}
	end := len(textParts)
	for i := len(segmentStarts) - 1; i >= -1; i-- {
		start := 0
		if i >= 0 {
			start = segmentStarts[i]
		}
		if start < 0 || start > end {
			continue
		}
		if segment := clean(strings.Join(textParts[start:end], "")); strings.TrimSpace(segment) != "" {
			return segment
		}
		end = start
	}
	return ""
}

// agentText applies the display's filters to text the agent emitted -- an
// EventText chunk, a result body or a joined segment: with
// hide_agent_footer (forced on by final mode) its model/token status footer
// lines go. The foreground turn and the background reader both run agent
// text through it before buffering it, picking the answer or judging
// NO_REPLY, so a footer can neither leak on one path nor turn a silent
// NO_REPLY turn into a message.
func (e *Engine) agentText(text string) string {
	if e.display.HideAgentFooter {
		return stripAgentFooterLines(text)
	}
	return text
}

// finalAnswer is a final-mode turn's answer on every path -- the foreground
// result and channel-close paths and the background result and
// partial-flush paths: finalTurnText with agentText as the per-segment
// filter.
func (e *Engine) finalAnswer(resultContent string, textParts []string, segmentStarts []int) string {
	return finalTurnText(resultContent, textParts, segmentStarts, e.agentText)
}

// unsolicitedReplyText is the text a background turn delivers (before
// NO_REPLY handling): the final-mode answer, else the result body, else
// all buffered text, footers removed. The result and partial-flush paths
// of the background reader share it so they cannot drift apart.
func (e *Engine) unsolicitedReplyText(resultContent string, textParts []string, segmentStarts []int) string {
	if e.finalOutput() {
		return e.finalAnswer(resultContent, textParts, segmentStarts)
	}
	if text := e.agentText(resultContent); text != "" {
		return text
	}
	return e.agentText(strings.Join(textParts, ""))
}
