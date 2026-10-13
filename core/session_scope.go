package core

import "time"

// ShareConversation maps display conversations in one channel to a single
// model conversation. Routing still uses the original message key. Callers
// configure this before admitting turns; all session operations use the scope.
func (sm *SessionManager) ShareConversation(channel, canonical string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.conversationScopes[channel] == canonical {
		return
	}
	if sm.conversationScopes == nil {
		sm.conversationScopes = map[string]string{}
	}
	// Upgrade existing per-thread sessions once, choosing the most recently
	// used context. Keep all old records; never concatenate native transcripts.
	if sm.conversationScopes[channel] == "" && sm.activeSession[canonical] == "" {
		var latest *Session
		var latestAt time.Time
		for key, id := range sm.activeSession {
			if extractWorkspaceChannelKey(key) != channel {
				continue
			}
			candidate := sm.sessions[id]
			if candidate == nil {
				continue
			}
			candidate.mu.Lock()
			at := candidate.UpdatedAt
			candidate.mu.Unlock()
			if latest == nil || at.After(latestAt) || (at.Equal(latestAt) && candidate.ID > latest.ID) {
				latest, latestAt = candidate, at
			}
		}
		if latest != nil {
			sm.activeSession[canonical] = latest.ID
			sm.userSessions[canonical] = append(sm.userSessions[canonical], latest.ID)
		}
	}
	sm.conversationScopes[channel] = canonical
	sm.saveLocked()
}

// Caller holds sm.mu. Scope configuration is persisted with session state so
// /new, /switch and process restarts agree on the same active conversation.
func (sm *SessionManager) conversationKeyLocked(key string) string {
	if canonical := sm.conversationScopes[extractWorkspaceChannelKey(key)]; canonical != "" {
		return canonical
	}
	return key
}
