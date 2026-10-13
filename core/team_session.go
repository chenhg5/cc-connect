package core

import "fmt"

// Model identity is independent of task attempts and visible message roots.
// Each Engine is a member; project/revision also isolate shared directories.
func teamModelSessionKey(p TeamProject, member string) string {
	return fmt.Sprintf("%s:%s:member:%s:%s:%d", p.Platform, p.ChatID, p.ID, member, p.ContextRevision)
}

func (e *Engine) modelSessionKey(route string) string {
	if p, err := e.teamProject(route); err == nil && p != nil {
		return teamModelSessionKey(*p, e.name)
	}
	return route
}

// Long-lived native processes inherit a stable CC_SESSION_KEY. Resolve their
// team commands against the currently executing handoff, never the first or
// most recently received (possibly queued) message's requester.
func (e *Engine) teamActionSource(key string) string {
	if e.modelSessionKey(key) != key {
		return key
	}
	e.interactiveMu.Lock()
	defer e.interactiveMu.Unlock()
	for _, state := range e.interactiveStates {
		state.mu.Lock()
		source := state.sourceSessionKey
		state.mu.Unlock()
		if source != "" && e.modelSessionKey(source) == key {
			return source
		}
	}
	return key
}
