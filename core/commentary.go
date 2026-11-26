package core

import "strings"

// commentaryEventType keeps the legacy text/card path unless standalone updates
// were explicitly enabled.
func (e *Engine) commentaryEventType(kind EventType) EventType {
	if kind == EventCommentary &&
		(e.display.CommentaryMessages == nil || !*e.display.CommentaryMessages) {
		return EventText
	}
	return kind
}

// relayCommentary sends an explicitly enabled update outside progress cards and
// the final answer, even when thinking is hidden. Card edits do not notify users.
// It reports whether a message was sent.
func (e *Engine) relayCommentary(p Platform, replyCtx any, content, workspaceDir string) bool {
	if e.display.CommentaryMessages == nil || !*e.display.CommentaryMessages {
		return false
	}
	if e.display.HideAgentFooter {
		content = stripAgentFooterLines(content)
	}
	if isSilentReply(content) {
		return false
	}
	content, _ = stripTrailingSilent(content)
	content = strings.TrimSpace(content)
	if content == "" || isEllipsisOnly(content) {
		return false
	}
	for _, chunk := range SplitMessageCodeFenceAware(content, maxPlatformMessageLen) {
		e.sendForWorkspace(p, replyCtx, chunk, workspaceDir)
	}
	return true
}
