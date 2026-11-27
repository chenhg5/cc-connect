package core

import "strings"

// relayCommentary sends a complete user-facing update as a new message. Card
// edits do not notify users, so these updates must bypass progress aggregation
// and remain separate from the final answer, even when thinking is hidden.
// It reports whether non-empty commentary was handled (sent, or intentionally
// suppressed by commentary_messages = false).
func (e *Engine) relayCommentary(p Platform, replyCtx any, content, workspaceDir string) bool {
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
	if e.display.CommentaryMessages != nil && !*e.display.CommentaryMessages {
		return true
	}
	for _, chunk := range SplitMessageCodeFenceAware(content, maxPlatformMessageLen) {
		e.sendForWorkspace(p, replyCtx, chunk, workspaceDir)
	}
	return true
}
