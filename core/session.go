package core

import (
	"sync"
	"time"
)

// Session tracks the mapping between a platform conversation and an agent session.
type Session struct {
	Key            string
	AgentSessionID string
	CreatedAt      time.Time
	UpdatedAt      time.Time

	mu   sync.Mutex
	busy bool
}

// TryLock attempts to mark this session as busy. Returns false if already processing.
func (s *Session) TryLock() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy {
		return false
	}
	s.busy = true
	return true
}

// Unlock marks this session as idle.
func (s *Session) Unlock() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.busy = false
	s.UpdatedAt = time.Now()
}

// SessionManager provides thread-safe session lookup and creation.
type SessionManager struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

func NewSessionManager() *SessionManager {
	return &SessionManager{
		sessions: make(map[string]*Session),
	}
}

func (sm *SessionManager) GetOrCreate(key string) *Session {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if s, ok := sm.sessions[key]; ok {
		return s
	}

	s := &Session{
		Key:       key,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	sm.sessions[key] = s
	return s
}

func (sm *SessionManager) Delete(key string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	delete(sm.sessions, key)
}
