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

// ContinueSession is a sentinel value for AgentSessionID that tells the agent
// to use --continue (resume most recent session) instead of a specific session ID.
const ContinueSession = "__continue__"

// Session tracks one conversation between a user and the agent.
type Session struct {
	ID             string         `json:"id"`
	Name           string         `json:"name"`
	AgentSessionID string         `json:"agent_session_id"`
	AgentType      string         `json:"agent_type,omitempty"`
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

// SetAgentInfo atomically sets the agent session ID, agent type, and name.
func (s *Session) SetAgentInfo(agentSessionID, agentType, name string) {
	if agentSessionID == ContinueSession {
		agentSessionID = ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.AgentSessionID = agentSessionID
	s.AgentType = agentType
	s.Name = name
}

// GetAgentSessionID atomically reads the agent session ID.
func (s *Session) GetAgentSessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.AgentSessionID
}

// GetName atomically reads the session name.
func (s *Session) GetName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Name
}

// SetAgentSessionID atomically sets the agent session ID and agent type.
// The ContinueSession sentinel is never persisted — it is only used transiently
// when starting an agent (see engine); storing it on disk breaks resume (#255).
func (s *Session) SetAgentSessionID(id, agentType string) {
	if id == ContinueSession {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.AgentSessionID = id
	s.AgentType = agentType
}

// CompareAndSetAgentSessionID sets the agent session ID only if it is currently
// empty or still holds the erroneous persisted ContinueSession sentinel.
// Returns true if the value was set, false if a real session ID was already stored.
func (s *Session) CompareAndSetAgentSessionID(id, agentType string) bool {
	if id == "" || id == ContinueSession {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.AgentSessionID != "" && s.AgentSessionID != ContinueSession {
		return false
	}
	s.AgentSessionID = id
	s.AgentType = agentType
	return true
}

func (s *Session) stripContinueSessionSentinel() {
	s.mu.Lock()
	if s.AgentSessionID == ContinueSession {
		s.AgentSessionID = ""
	}
	s.mu.Unlock()
}

func (s *Session) ClearHistory() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.History = nil
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

// UserMeta stores human-readable display info for a session key.
type UserMeta struct {
	UserName string `json:"user_name,omitempty"`
	ChatName string `json:"chat_name,omitempty"`
}

// sessionSnapshot is the JSON-serializable state of the SessionManager.
type sessionSnapshot struct {
	Sessions      map[string]*Session  `json:"sessions"`
	ActiveSession map[string]string    `json:"active_session"`
	UserSessions  map[string][]string  `json:"user_sessions"`
	Counter       int64                `json:"counter"`
	SessionNames  map[string]string    `json:"session_names,omitempty"` // agent session ID → custom name
	UserMeta      map[string]*UserMeta `json:"user_meta,omitempty"`     // sessionKey → display info
}

// SessionManager supports multiple named sessions per user with active-session tracking.
// It can persist state to a JSON file and reload on startup.
type SessionManager struct {
	mu            sync.RWMutex
	sessions      map[string]*Session
	activeSession map[string]string
	userSessions  map[string][]string
	sessionNames  map[string]string    // agent session ID → custom name
	userMeta      map[string]*UserMeta // sessionKey → display info
	counter       int64
	storePath     string // empty = no persistence
}

func NewSessionManager(storePath string) *SessionManager {
	sm := &SessionManager{
		sessions:      make(map[string]*Session),
		activeSession: make(map[string]string),
		userSessions:  make(map[string][]string),
		sessionNames:  make(map[string]string),
		userMeta:      make(map[string]*UserMeta),
		storePath:     storePath,
	}
	if storePath != "" {
		sm.load()
	}
	return sm
}

// StorePath returns the file path used for session persistence.
func (sm *SessionManager) StorePath() string {
	return sm.storePath
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
	s := sm.createLocked(userKey, "default")
	sm.saveLocked()
	return s
}

func (sm *SessionManager) NewSession(userKey, name string) *Session {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	s := sm.createLocked(userKey, name)
	sm.saveLocked()
	return s
}

// NewSideSession registers a new session for userKey without changing the active
// session. Used for isolated one-off runs (e.g. cron with session_mode=new_per_run)
// so the user's current chat remains the default target for normal messages.
func (sm *SessionManager) NewSideSession(userKey, name string) *Session {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	id := sm.nextID()
	now := time.Now()
	s := &Session{
		ID:        id,
		Name:      name,
		CreatedAt: now,
		UpdatedAt: now,
	}
	sm.sessions[id] = s
	sm.userSessions[userKey] = append(sm.userSessions[userKey], id)
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

// SetSessionName sets a custom display name for an agent session.
func (sm *SessionManager) SetSessionName(agentSessionID, name string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if name == "" {
		delete(sm.sessionNames, agentSessionID)
	} else {
		sm.sessionNames[agentSessionID] = name
	}
	sm.saveLocked()
}

// GetSessionName returns the custom name for an agent session, or "".
func (sm *SessionManager) GetSessionName(agentSessionID string) string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.sessionNames[agentSessionID]
}

// UpdateUserMeta updates the human-readable metadata for a session key.
// Only non-empty fields are applied (merge behavior).
func (sm *SessionManager) UpdateUserMeta(sessionKey, userName, chatName string) {
	if userName == "" && chatName == "" {
		return
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	meta, ok := sm.userMeta[sessionKey]
	if !ok {
		meta = &UserMeta{}
		sm.userMeta[sessionKey] = meta
	}
	if userName != "" {
		meta.UserName = userName
	}
	if chatName != "" {
		meta.ChatName = chatName
	}
}

// GetUserMeta returns a copy of the stored metadata for a session key, or nil.
func (sm *SessionManager) GetUserMeta(sessionKey string) *UserMeta {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	m := sm.userMeta[sessionKey]
	if m == nil {
		return nil
	}
	cp := *m
	return &cp
}

// AllSessions returns all sessions across all user keys.
func (sm *SessionManager) AllSessions() []*Session {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	out := make([]*Session, 0, len(sm.sessions))
	for _, s := range sm.sessions {
		out = append(out, s)
	}
	return out
}

// FindByID looks up a session by its internal ID across all users.
func (sm *SessionManager) FindByID(id string) *Session {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.sessions[id]
}

// DeleteByID removes a session by its internal ID from all tracking structures.
func (sm *SessionManager) DeleteByID(id string) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if _, ok := sm.sessions[id]; !ok {
		return false
	}
	sm.deleteByIDLocked(id)
	sm.saveLocked()
	return true
}

// DeleteByAgentSessionID removes all local sessions mapped to the given
// agent session ID. It returns the number of removed local sessions.
func (sm *SessionManager) DeleteByAgentSessionID(agentSessionID string) int {
	if agentSessionID == "" {
		return 0
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	removed := 0
	for id, s := range sm.sessions {
		s.mu.Lock()
		matched := s.AgentSessionID == agentSessionID
		s.mu.Unlock()
		if !matched {
			continue
		}
		sm.deleteByIDLocked(id)
		removed++
	}
	if removed > 0 {
		sm.saveLocked()
	}
	return removed
}

func (sm *SessionManager) deleteByIDLocked(id string) {
	delete(sm.sessions, id)
	for userKey, ids := range sm.userSessions {
		for i, sid := range ids {
			if sid == id {
				sm.userSessions[userKey] = append(ids[:i], ids[i+1:]...)
				break
			}
		}
		if sm.activeSession[userKey] == id {
			delete(sm.activeSession, userKey)
		}
	}
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

	// Build a deep-copy snapshot to avoid racing with concurrent Session mutations.
	snapSessions := make(map[string]*Session, len(sm.sessions))
	for id, s := range sm.sessions {
		s.mu.Lock()
		agentSID := s.AgentSessionID
		if agentSID == ContinueSession {
			agentSID = ""
			s.AgentSessionID = ""
		}
		snapSessions[id] = &Session{
			ID:             s.ID,
			Name:           s.Name,
			AgentSessionID: agentSID,
			AgentType:      s.AgentType,
			History:        append([]HistoryEntry(nil), s.History...),
			CreatedAt:      s.CreatedAt,
			UpdatedAt:      s.UpdatedAt,
		}
		s.mu.Unlock()
	}

	snap := sessionSnapshot{
		Sessions:      snapSessions,
		ActiveSession: sm.activeSession,
		UserSessions:  sm.userSessions,
		Counter:       sm.counter,
		SessionNames:  sm.sessionNames,
		UserMeta:      sm.userMeta,
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
	if err := AtomicWriteFile(sm.storePath, data, 0o644); err != nil {
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
	sm.sessionNames = snap.SessionNames
	sm.userMeta = snap.UserMeta
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
	if sm.sessionNames == nil {
		sm.sessionNames = make(map[string]string)
	}
	if sm.userMeta == nil {
		sm.userMeta = make(map[string]*UserMeta)
	}

	for _, s := range sm.sessions {
		s.stripContinueSessionSentinel()
	}

	slog.Info("session: loaded from disk", "path", sm.storePath, "sessions", len(sm.sessions))
}

// InvalidateForAgent clears AgentSessionID on all sessions whose AgentType
// does not match the current agent. This handles the case where the user
// switches agent types (e.g. opencode → pi) and stale session IDs from the
// old agent would cause errors.
func (sm *SessionManager) InvalidateForAgent(agentType string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	invalidated := 0
	for _, s := range sm.sessions {
		s.mu.Lock()
		if s.AgentSessionID != "" && s.AgentType != "" && s.AgentType != agentType {
			slog.Info("session: invalidating stale agent session",
				"session", s.ID,
				"old_agent", s.AgentType,
				"new_agent", agentType,
				"old_agent_session_id", s.AgentSessionID,
			)
			s.AgentSessionID = ""
			s.AgentType = agentType
			invalidated++
		}
		s.mu.Unlock()
	}
	if invalidated > 0 {
		sm.saveLocked()
	}
}
