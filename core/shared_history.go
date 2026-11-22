package core

import (
	"context"
	"log/slog"
	"time"
)

func (e *Engine) historyForSession(agent Agent, session *Session, limit int) ([]HistoryEntry, error) {
	entries := session.GetHistory(limit)
	id := session.GetAgentSessionID()
	provider, ok := agent.(HistoryProvider)
	if !ok || id == "" || (len(entries) > 0 && !defaultSettingsOnly(agent)) {
		return entries, nil
	}
	ctx, cancel := context.WithTimeout(e.ctx, 5*time.Second)
	defer cancel()
	history, err := provider.GetSessionHistory(ctx, id, limit)
	if err != nil {
		slog.Warn("read agent session history", "project", e.name, "agent_session_id", id, "error", err)
		return entries, err
	}
	return history, nil
}

func (e *Engine) historyTimestamp(timestamp time.Time) string {
	if timestamp.IsZero() {
		return e.i18n.T(MsgHistoryTimeUnknown)
	}
	return timestamp.Local().Format("15:04:05")
}

// Find the existing owner without creating a workspace or starting an agent.
// A workspace session ID must never be read through the project's default cwd.
func (e *Engine) findSessionHistoryOwner(id string) (Agent, *SessionManager, *Session, string) {
	if session := e.sessions.FindByID(id); session != nil {
		return e.agent, e.sessions, session, ""
	}
	e.interactiveMu.Lock()
	pool := e.workspacePool
	e.interactiveMu.Unlock()
	if pool != nil {
		for workspace, ws := range pool.All() {
			ws.mu.Lock()
			agent, sessions := ws.agent, ws.sessions
			ws.mu.Unlock()
			if sessions != nil {
				if session := sessions.FindByID(id); session != nil {
					return agent, sessions, session, workspace
				}
			}
		}
	}
	return nil, nil, nil, ""
}
