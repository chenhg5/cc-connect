package core

import "strings"

// ---- maomao fork: per-session model overrides ----
//
// [projects.session_models] maps a session key to a model, so one channel can
// run on a cheaper model while the rest of the project keeps the agent default.
// The agent is shared by all sessions, so the engine sets the model on it right
// before each StartSession, the same way it sets the per-session env.

// SetSessionModels replaces the session key → model map. Blank keys and
// values are dropped.
func (e *Engine) SetSessionModels(m map[string]string) {
	clean := make(map[string]string, len(m))
	for k, v := range m {
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if k != "" && v != "" {
			clean[k] = v
		}
	}
	e.sessionModelsMu.Lock()
	e.sessionModels = clean
	e.sessionModelsMu.Unlock()
}

// sessionModelFor returns the model mapped to sessionKey, or "" for the default.
func (e *Engine) sessionModelFor(sessionKey string) string {
	e.sessionModelsMu.RLock()
	defer e.sessionModelsMu.RUnlock()
	return e.sessionModels[sessionKey]
}

// applySessionModel tells the agent which model the next StartSession uses.
// It always sets a value ("" included) so an override never leaks into the
// next session the shared agent starts.
func (e *Engine) applySessionModel(agent Agent, sessionKey string) {
	if inj, ok := agent.(SessionModelInjector); ok {
		inj.SetSessionModel(e.sessionModelFor(sessionKey))
	}
}

// ---- maomao fork: per-session base instructions ----
//
// [projects.session_instructions] maps a session key to a file that replaces
// the agent's built-in base instructions, so a chat-only channel can drop the
// coding-agent prompt while work channels keep it. Same per-start injection as
// the session model. Shares sessionModelsMu.

// SetSessionInstructions replaces the session key → instructions file map.
// Blank keys and values are dropped.
func (e *Engine) SetSessionInstructions(m map[string]string) {
	clean := make(map[string]string, len(m))
	for k, v := range m {
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if k != "" && v != "" {
			clean[k] = v
		}
	}
	e.sessionModelsMu.Lock()
	e.sessionInstructions = clean
	e.sessionModelsMu.Unlock()
}

// applySessionInstructions tells the agent which instructions file the next
// StartSession uses ("" = built-in), so an override never leaks across sessions.
func (e *Engine) applySessionInstructions(agent Agent, sessionKey string) {
	inj, ok := agent.(SessionInstructionsInjector)
	if !ok {
		return
	}
	e.sessionModelsMu.RLock()
	path := e.sessionInstructions[sessionKey]
	e.sessionModelsMu.RUnlock()
	inj.SetSessionInstructionsFile(path)
}
