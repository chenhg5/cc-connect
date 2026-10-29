package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// commandWorkDir follows the conversation selected in this topic without
// changing the agent's default directory or starting an agent process.
func (e *Engine) commandWorkDir(agent Agent, sessions *SessionManager, interactiveKey string, msg *Message) (string, error) {
	dir, err := e.sessionCommandWorkDir(agent, sessions, interactiveKey, msg.SessionKey)
	if err != nil {
		return "", err
	}
	if dir == "" {
		return e.defaultCommandWorkDir(agent, msg), nil
	}
	// Never silently run a command in the project default if a selected
	// conversation's directory has disappeared or is otherwise unusable.
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("session working directory %q is not absolute", dir)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("session working directory %q: %w", dir, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("session working directory %q is not a directory", dir)
	}
	return filepath.Clean(dir), nil
}

func (e *Engine) sessionCommandWorkDir(agent Agent, sessions *SessionManager, interactiveKey, sessionKey string) (string, error) {
	if sessions == nil {
		return "", nil
	}
	selected := sessions.GetOrCreateActive(sessionKey)
	selected.mu.Lock()
	id, providerName := selected.AgentSessionID, selected.ActiveProvider
	selected.mu.Unlock()

	e.interactiveMu.Lock()
	var live AgentSession
	if state := e.interactiveStates[interactiveKey]; state != nil {
		state.mu.Lock()
		if !state.stopped {
			live = state.agentSession
		}
		state.mu.Unlock()
	}
	e.interactiveMu.Unlock()

	// A /switch or /new may leave an old process shutting down. Only a
	// process for the currently selected conversation can supply its cwd.
	if live != nil && live.Alive() && live.CurrentSessionID() == id {
		if wd, ok := live.(interface{ GetWorkDir() string }); ok {
			if dir := strings.TrimSpace(wd.GetWorkDir()); dir != "" {
				// Live sessions can retain a configured relative path. Like
				// process startup, resolve it against the bridge's cwd.
				absoluteDir, err := filepath.Abs(dir)
				if err != nil {
					return "", fmt.Errorf("resolve live session working directory %q: %w", dir, err)
				}
				return absoluteDir, nil
			}
		}
	}
	if id == "" || id == ContinueSession {
		return "", nil
	}
	if provider, ok := agent.(SessionWorkDirProvider); ok {
		dir, err := provider.GetSessionWorkDir(e.ctx, id, providerName)
		if err != nil {
			return "", fmt.Errorf("resolve session working directory: %w", err)
		}
		return strings.TrimSpace(dir), nil
	}
	return "", nil
}
