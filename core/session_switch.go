package core

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

// blockInteractiveStart leaves the saved conversation and its history intact.
// The caller owns interactiveMu, just as on the successful startup path.
func (e *Engine) blockInteractiveStart(key string, p Platform, replyCtx any, session *Session, agent Agent, text string) *interactiveState {
	state := &interactiveState{
		platform: p, replyCtx: replyCtx, agent: agent, busySession: session,
		eventsNeedResync: true, startupError: text,
	}
	adoptPendingFromPlaceholder(e.interactiveStates[key], state)
	e.interactiveStates[key] = state
	return state
}

// Remember the affected backend ID rather than only the chat key: a retry
// must remain blocked, while an explicit /new can safely use another ID.
func (e *Engine) markUnsafeSessionID(key, id string) {
	if key == "" {
		return
	}
	if id == "" {
		e.markUnsafeResume(key)
		return
	}
	e.markUnsafeResume(key + "\x00" + id)
}

func (e *Engine) isUnsafeSessionID(key, id string) bool {
	e.closingMu.Lock()
	defer e.closingMu.Unlock()
	return e.unsafeResume[key+"\x00"+id]
}

func (e *Engine) sessionSwitchFeedback(agent Agent, sessions *SessionManager, session *Session, info *AgentSessionInfo) string {
	name := sessions.GetSessionName(info.ID)
	if name == "" {
		name = info.Summary
	}
	id := info.ID
	if len(id) > 12 {
		id = id[:12]
	}
	text := e.i18n.Tf(MsgSwitchSuccess, name, id, info.MessageCount)
	text += "\n\n" + e.i18n.T(MsgSwitchResumePending)
	entries := session.GetHistory(1)
	if hp, ok := agent.(HistoryProvider); ok {
		// Prefer the backend transcript: another window may have appended
		// messages since cc-connect last used this conversation.
		ctx, cancel := context.WithTimeout(e.ctx, 5*time.Second)
		defer cancel()
		var err error
		entries, err = hp.GetSessionHistory(ctx, info.ID, 1)
		if err != nil {
			slog.Warn("switch: latest message unavailable", "session_id", info.ID, "error", err)
			return text + "\n\n" + e.i18n.T(MsgSwitchPreviewUnavailable)
		}
	}
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		if (entry.Role != "user" && entry.Role != "assistant") || strings.TrimSpace(entry.Content) == "" {
			continue
		}
		icon := "👤"
		if entry.Role == "assistant" {
			icon = "🤖"
		}
		stamp := ""
		if !entry.Timestamp.IsZero() {
			stamp = entry.Timestamp.Format("2006-01-02 15:04")
		}
		preview := truncateHistoryEntry(entry.Content, 600)
		return text + "\n\n" + e.i18n.Tf(MsgSwitchLatestMessage, icon, stamp, preview)
	}
	return text + "\n\n" + e.i18n.T(MsgSwitchPreviewEmpty)
}

// Card switches return their own confirmation instead of merely repainting
// the session list, so both text and button users can verify their selection.
func (e *Engine) handleSessionSwitchCardAction(query, key string) *Card {
	agent, sessions := e.sessionContextForKey(key)
	list, err := agent.ListSessions(e.ctx)
	if err != nil {
		return e.simpleCard(e.i18n.T(MsgCardTitleCurrentSession), "red", e.i18n.Tf(MsgError, err))
	}
	list = e.applySessionFilter(list, sessions, key)
	query = strings.TrimSpace(query)
	var matched *AgentSessionInfo
	if strings.HasPrefix(query, "--id ") {
		query = strings.TrimSpace(strings.TrimPrefix(query, "--id "))
		for i := range list {
			if list[i].ID == query {
				matched = &list[i]
				break
			}
		}
	} else {
		// Preserve compatibility with already-sent cards using list numbers.
		matched = e.matchSession(list, sessions, query)
	}
	if matched == nil {
		return e.simpleCard(e.i18n.T(MsgCardTitleCurrentSession), "red", e.i18n.Tf(MsgSwitchNoMatch, query))
	}
	e.cleanupInteractiveState(e.interactiveKeyForSessionKey(key))
	session := sessions.SwitchToAgentSession(key, matched.ID, agent.Name(), matched.Summary)
	return NewCard().Title(e.i18n.T(MsgCardTitleCurrentSession), "turquoise").
		Markdown(e.sessionSwitchFeedback(agent, sessions, session, matched)).
		Buttons(DefaultBtn(e.i18n.T(MsgCardTitleHistory), "nav:/history"),
			DefaultBtn(e.i18n.T(MsgCardBack), "nav:/list 1")).Build()
}
