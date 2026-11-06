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
		return nil, err
	}
	return history, nil
}

func (e *Engine) historyTimestamp(timestamp time.Time) string {
	if timestamp.IsZero() {
		return e.i18n.T(MsgHistoryTimeUnknown)
	}
	return timestamp.Local().Format("15:04:05")
}
