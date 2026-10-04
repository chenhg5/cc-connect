package core

import (
	"fmt"
	"net/http"
)

func (s *APIServer) applyAgentToolBinding(w http.ResponseWriter, project, sessionKey *string, id string) bool {
	p, key, err := s.bindAgentToolRequest(*project, *sessionKey, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return false
	}
	*project, *sessionKey = p, key
	return true
}

// Resolve only live, unambiguous shared connections. Never use "latest" or
// single-project defaults when a caller supplied an unbound agent session ID.
// The ID is local routing metadata, not authorization or model input.
func (s *APIServer) bindAgentToolRequest(project, sessionKey, agentSessionID string) (string, string, error) {
	if agentSessionID == "" || sessionKey != "" {
		return project, sessionKey, nil
	}
	s.mu.RLock()
	engines := make(map[string]*Engine, len(s.engines))
	for name, engine := range s.engines {
		if project == "" || name == project {
			engines[name] = engine
		}
	}
	s.mu.RUnlock()
	type destination struct{ project, key string }
	matches := map[destination]bool{}
	for name, engine := range engines {
		engine.interactiveMu.Lock()
		states := make([]*interactiveState, 0, len(engine.interactiveStates))
		for _, state := range engine.interactiveStates {
			states = append(states, state)
		}
		engine.interactiveMu.Unlock()
		for _, state := range states {
			state.mu.Lock()
			as, key, stopped := state.agentSession, state.sharedSessionKey, state.stopped
			state.mu.Unlock()
			shared, ok := as.(SharedAgentSession)
			if !ok || stopped || key == "" || !shared.Alive() {
				continue
			}
			runtime := shared.RuntimeState()
			if runtime.Connected && runtime.SessionID == agentSessionID {
				matches[destination{name, key}] = true
			}
		}
	}
	if len(matches) == 0 {
		return "", "", fmt.Errorf("agent session has no live CC Connect binding; attach it or provide explicit project/session parameters")
	}
	if len(matches) != 1 {
		return "", "", fmt.Errorf("agent session is bound to multiple conversations; provide explicit project/session parameters")
	}
	for target := range matches {
		return target.project, target.key, nil
	}
	return "", "", fmt.Errorf("agent session binding is unavailable")
}
