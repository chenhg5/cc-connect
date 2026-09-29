package core

import (
	"context"
	"log/slog"
	"strings"
)

// syncSessionNamesFromAgent imports Cursor CLI session titles (/rename, IDE
// rename) into cc-connect's session_names map. Terminal-originated names win
// over stale cc-connect labels when they differ.
func (e *Engine) syncSessionNamesFromAgent(agent Agent, sessions *SessionManager, listed []AgentSessionInfo) {
	renamer, ok := agent.(SessionRenamer)
	if !ok || len(listed) == 0 {
		return
	}
	for _, info := range listed {
		cursorName, err := renamer.GetSessionDisplayName(e.ctx, info.ID)
		if err != nil {
			slog.Debug("session name sync: read failed", "session_id", info.ID, "error", err)
			continue
		}
		cursorName = strings.TrimSpace(cursorName)
		if !meaningfulAgentSessionName(cursorName) {
			continue
		}
		if sessions.GetSessionName(info.ID) == cursorName {
			continue
		}
		sessions.SetSessionName(info.ID, cursorName)
	}
}

func (e *Engine) exportSessionNameToAgent(agent Agent, sessionID, name string) {
	renamer, ok := agent.(SessionRenamer)
	if !ok {
		return
	}
	name = strings.TrimSpace(name)
	if sessionID == "" || !meaningfulAgentSessionName(name) {
		return
	}
	if err := renamer.SetSessionDisplayName(context.Background(), sessionID, name); err != nil {
		slog.Warn("session name sync: export to agent failed",
			"session_id", sessionID, "name", name, "error", err)
	}
}

func meaningfulAgentSessionName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	switch strings.ToLower(name) {
	case "new agent", "new chat":
		return false
	}
	return true
}
