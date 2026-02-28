package core

import (
	"fmt"
	"sync"
	"time"
)

// Session tracks one conversation between a user and the agent.
// A single user can own multiple sessions and switch between them.
type Session struct {
	ID             string
	Name           string
	AgentSessionID string // opaque ID managed by the agent (e.g. Claude Code --session-id)
	History        []HistoryEntry
	CreatedAt      time.Time
	UpdatedAt      time.Time

	mu   sync.Mutex
	busy bool
}

func (s *Session) TryLock() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy {
		return false
	}
	s.busy = true
	return true
}

func (s *Session) Unlock() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.busy = false
	s.UpdatedAt = time.Now()
}

func (s *Session) AddHistory(role, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.History = append(s.History, HistoryEntry{
		Role:      role,
		Content:   content,
		Timestamp: time.Now(),
	})
}

// GetHistory returns the last n entries. If n <= 0, returns all.
func (s *Session) GetHistory(n int) []HistoryEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := len(s.History)
	if n <= 0 || n > total {
		n = total
	}
	out := make([]HistoryEntry, n)
	copy(out, s.History[total-n:])
	return out
}

// SessionManager supports multiple named sessions per user with active-session tracking.
type SessionManager struct {
	mu             sync.RWMutex
	sessions       map[string]*Session  // sessionID → Session
	activeSession  map[string]string    // userKey → active sessionID
	userSessions   map[string][]string  // userKey → ordered list of sessionIDs
	counter        int64
}

func NewSessionManager() *SessionManager {
	return &SessionManager{
		sessions:      make(map[string]*Session),
		activeSession: make(map[string]string),
		userSessions:  make(map[string][]string),
	}
}

func (sm *SessionManager) nextID() string {
	sm.counter++
	return fmt.Sprintf("s%d", sm.counter)
}

// GetOrCreateActive returns the user's active session, creating a default one if none exists.
func (sm *SessionManager) GetOrCreateActive(userKey string) *Session {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sid, ok := sm.activeSession[userKey]; ok {
		if s, ok := sm.sessions[sid]; ok {
			return s
		}
	}
	return sm.createLocked(userKey, "default")
}

// NewSession creates a new session for the user and makes it active.
func (sm *SessionManager) NewSession(userKey, name string) *Session {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.createLocked(userKey, name)
}

func (sm *SessionManager) createLocked(userKey, name string) *Session {
	id := sm.nextID()
	now := time.Now()
	s := &Session{
		ID:        id,
		Name:      name,
		CreatedAt: now,
		UpdatedAt: now,
	}
	sm.sessions[id] = s
	sm.activeSession[userKey] = id
	sm.userSessions[userKey] = append(sm.userSessions[userKey], id)
	return s
}

// SwitchSession makes the session matching target (by ID or name) active for the user.
func (sm *SessionManager) SwitchSession(userKey, target string) (*Session, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	for _, sid := range sm.userSessions[userKey] {
		s := sm.sessions[sid]
		if s != nil && (s.ID == target || s.Name == target) {
			sm.activeSession[userKey] = s.ID
			return s, nil
		}
	}
	return nil, fmt.Errorf("session %q not found", target)
}

// ListSessions returns all sessions belonging to the user.
func (sm *SessionManager) ListSessions(userKey string) []*Session {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	ids := sm.userSessions[userKey]
	out := make([]*Session, 0, len(ids))
	for _, sid := range ids {
		if s, ok := sm.sessions[sid]; ok {
			out = append(out, s)
		}
	}
	return out
}

// ActiveSessionID returns the ID of the user's currently active session (empty if none).
func (sm *SessionManager) ActiveSessionID(userKey string) string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.activeSession[userKey]
}
