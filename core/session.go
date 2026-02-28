package core

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Session tracks one conversation between a user and the agent.
type Session struct {
	ID             string         `json:"id"`
	Name           string         `json:"name"`
	AgentSessionID string         `json:"agent_session_id"`
	History        []HistoryEntry `json:"history"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`

	mu   sync.Mutex `json:"-"`
	busy bool       `json:"-"`
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

// sessionSnapshot is the JSON-serializable state of the SessionManager.
type sessionSnapshot struct {
	Sessions      map[string]*Session `json:"sessions"`
	ActiveSession map[string]string   `json:"active_session"`
	UserSessions  map[string][]string `json:"user_sessions"`
	Counter       int64               `json:"counter"`
}

// SessionManager supports multiple named sessions per user with active-session tracking.
// It can persist state to a JSON file and reload on startup.
type SessionManager struct {
	mu            sync.RWMutex
	sessions      map[string]*Session
	activeSession map[string]string
	userSessions  map[string][]string
	counter       int64
	storePath     string // empty = no persistence
}

func NewSessionManager(storePath string) *SessionManager {
	sm := &SessionManager{
		sessions:      make(map[string]*Session),
		activeSession: make(map[string]string),
		userSessions:  make(map[string][]string),
		storePath:     storePath,
	}
	if storePath != "" {
		sm.load()
	}
	return sm
}

func (sm *SessionManager) nextID() string {
	sm.counter++
	return fmt.Sprintf("s%d", sm.counter)
}

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

func (sm *SessionManager) NewSession(userKey, name string) *Session {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	s := sm.createLocked(userKey, name)
	sm.saveLocked()
	return s
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

func (sm *SessionManager) SwitchSession(userKey, target string) (*Session, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	for _, sid := range sm.userSessions[userKey] {
		s := sm.sessions[sid]
		if s != nil && (s.ID == target || s.Name == target) {
			sm.activeSession[userKey] = s.ID
			sm.saveLocked()
			return s, nil
		}
	}
	return nil, fmt.Errorf("session %q not found", target)
}

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

func (sm *SessionManager) ActiveSessionID(userKey string) string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.activeSession[userKey]
}

// Save persists current state to disk. Safe to call from outside (e.g. after message processing).
func (sm *SessionManager) Save() {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	sm.saveLocked()
}

func (sm *SessionManager) saveLocked() {
	if sm.storePath == "" {
		return
	}
	snap := sessionSnapshot{
		Sessions:      sm.sessions,
		ActiveSession: sm.activeSession,
		UserSessions:  sm.userSessions,
		Counter:       sm.counter,
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		slog.Error("session: failed to marshal", "error", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(sm.storePath), 0o755); err != nil {
		slog.Error("session: failed to create dir", "error", err)
		return
	}
	if err := os.WriteFile(sm.storePath, data, 0o644); err != nil {
		slog.Error("session: failed to write", "path", sm.storePath, "error", err)
	}
}

func (sm *SessionManager) load() {
	data, err := os.ReadFile(sm.storePath)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Error("session: failed to read", "path", sm.storePath, "error", err)
		}
		return
	}
	var snap sessionSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		slog.Error("session: failed to unmarshal", "path", sm.storePath, "error", err)
		return
	}
	sm.sessions = snap.Sessions
	sm.activeSession = snap.ActiveSession
	sm.userSessions = snap.UserSessions
	sm.counter = snap.Counter

	if sm.sessions == nil {
		sm.sessions = make(map[string]*Session)
	}
	if sm.activeSession == nil {
		sm.activeSession = make(map[string]string)
	}
	if sm.userSessions == nil {
		sm.userSessions = make(map[string][]string)
	}

	slog.Info("session: loaded from disk", "path", sm.storePath, "sessions", len(sm.sessions))
}
