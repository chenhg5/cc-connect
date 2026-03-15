package core

import (
	"path/filepath"
	"sync"
	"testing"
)

func TestSessionManager_GetOrCreateActive(t *testing.T) {
	sm := NewSessionManager("")
	s1 := sm.GetOrCreateActive("user1")
	if s1 == nil {
		t.Fatal("expected non-nil session")
	}
	s2 := sm.GetOrCreateActive("user1")
	if s1.ID != s2.ID {
		t.Error("same user should get same active session")
	}

	s3 := sm.GetOrCreateActive("user2")
	if s3.ID == s1.ID {
		t.Error("different user should get different session")
	}
}

func TestSessionManager_NewSession(t *testing.T) {
	sm := NewSessionManager("")
	s1 := sm.NewSession("user1", "chat-a")
	s2 := sm.NewSession("user1", "chat-b")

	if s1.ID == s2.ID {
		t.Error("new sessions should have different IDs")
	}
	if s1.Name != "chat-a" || s2.Name != "chat-b" {
		t.Error("session names should match")
	}

	active := sm.GetOrCreateActive("user1")
	if active.ID != s2.ID {
		t.Error("latest session should be active")
	}
}

func TestSessionManager_SwitchSession(t *testing.T) {
	sm := NewSessionManager("")
	s1 := sm.NewSession("user1", "first")
	s2 := sm.NewSession("user1", "second")

	if sm.ActiveSessionID("user1") != s2.ID {
		t.Error("active should be s2")
	}

	switched, err := sm.SwitchSession("user1", s1.ID)
	if err != nil {
		t.Fatalf("SwitchSession: %v", err)
	}
	if switched.ID != s1.ID {
		t.Error("should have switched to s1")
	}
	if sm.ActiveSessionID("user1") != s1.ID {
		t.Error("active should now be s1")
	}
}

func TestSessionManager_SwitchByName(t *testing.T) {
	sm := NewSessionManager("")
	sm.NewSession("user1", "alpha")
	sm.NewSession("user1", "beta")

	switched, err := sm.SwitchSession("user1", "alpha")
	if err != nil {
		t.Fatalf("SwitchSession by name: %v", err)
	}
	if switched.Name != "alpha" {
		t.Error("should have switched to alpha")
	}
}

func TestSessionManager_SwitchNotFound(t *testing.T) {
	sm := NewSessionManager("")
	sm.NewSession("user1", "only")

	_, err := sm.SwitchSession("user1", "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent session")
	}
}

func TestSessionManager_ListSessions(t *testing.T) {
	sm := NewSessionManager("")
	sm.NewSession("user1", "a")
	sm.NewSession("user1", "b")
	sm.NewSession("user2", "c")

	list := sm.ListSessions("user1")
	if len(list) != 2 {
		t.Errorf("user1 should have 2 sessions, got %d", len(list))
	}

	list2 := sm.ListSessions("user2")
	if len(list2) != 1 {
		t.Errorf("user2 should have 1 session, got %d", len(list2))
	}
}

func TestSessionManager_SessionNames(t *testing.T) {
	sm := NewSessionManager("")
	sm.SetSessionName("agent-123", "my-chat")

	if got := sm.GetSessionName("agent-123"); got != "my-chat" {
		t.Errorf("got %q, want my-chat", got)
	}

	sm.SetSessionName("agent-123", "")
	if got := sm.GetSessionName("agent-123"); got != "" {
		t.Errorf("got %q, want empty after clear", got)
	}
}

func TestSessionManager_Persistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")

	sm1 := NewSessionManager(path)
	sm1.NewSession("user1", "persisted")
	sm1.SetSessionName("agent-x", "custom-name")

	sm2 := NewSessionManager(path)
	list := sm2.ListSessions("user1")
	if len(list) != 1 {
		t.Fatalf("expected 1 session after reload, got %d", len(list))
	}
	if list[0].Name != "persisted" {
		t.Errorf("session name = %q, want persisted", list[0].Name)
	}
	if got := sm2.GetSessionName("agent-x"); got != "custom-name" {
		t.Errorf("session name after reload = %q, want custom-name", got)
	}
}

func TestSession_TryLockUnlock(t *testing.T) {
	s := &Session{}
	if !s.TryLock() {
		t.Error("first TryLock should succeed")
	}
	if s.TryLock() {
		t.Error("second TryLock should fail")
	}
	s.Unlock()
	if !s.TryLock() {
		t.Error("TryLock after Unlock should succeed")
	}
}

func TestSession_History(t *testing.T) {
	s := &Session{}
	s.AddHistory("user", "hello")
	s.AddHistory("assistant", "hi there")
	s.AddHistory("user", "bye")

	all := s.GetHistory(0)
	if len(all) != 3 {
		t.Errorf("expected 3 entries, got %d", len(all))
	}

	last2 := s.GetHistory(2)
	if len(last2) != 2 {
		t.Errorf("expected 2 entries, got %d", len(last2))
	}
	if last2[0].Content != "hi there" {
		t.Errorf("expected 'hi there', got %q", last2[0].Content)
	}

	s.ClearHistory()
	if h := s.GetHistory(0); len(h) != 0 {
		t.Errorf("expected empty history after clear, got %d", len(h))
	}
}

func TestSession_ConcurrentHistory(t *testing.T) {
	s := &Session{}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.AddHistory("user", "msg")
		}()
	}
	wg.Wait()
	if h := s.GetHistory(0); len(h) != 50 {
		t.Errorf("expected 50 entries, got %d", len(h))
	}
}

func TestSession_GetAgentSessionID(t *testing.T) {
	s := &Session{}
	if got := s.GetAgentSessionID(); got != "" {
		t.Errorf("initial GetAgentSessionID = %q, want empty", got)
	}
	s.SetAgentSessionID("sess-1", "test")
	if got := s.GetAgentSessionID(); got != "sess-1" {
		t.Errorf("GetAgentSessionID = %q, want %q", got, "sess-1")
	}
}

func TestSession_GetName(t *testing.T) {
	s := &Session{Name: "test-session"}
	if got := s.GetName(); got != "test-session" {
		t.Errorf("GetName = %q, want %q", got, "test-session")
	}
}

func TestSessionManager_InvalidateForAgent(t *testing.T) {
	sm := NewSessionManager("")

	// Create sessions with different agent types
	s1 := sm.NewSession("user1", "sess1")
	s1.SetAgentSessionID("old-id-1", "opencode")

	s2 := sm.NewSession("user2", "sess2")
	s2.SetAgentSessionID("old-id-2", "claudecode")

	s3 := sm.NewSession("user3", "sess3")
	s3.SetAgentSessionID("old-id-3", "") // pre-migration, no agent type

	s4 := sm.NewSession("user4", "sess4") // no agent session ID at all

	sm.InvalidateForAgent("claudecode")

	// s1: opencode → should be invalidated
	if got := s1.GetAgentSessionID(); got != "" {
		t.Errorf("s1 (opencode) AgentSessionID = %q, want empty (should be invalidated)", got)
	}
	if s1.AgentType != "claudecode" {
		t.Errorf("s1 AgentType = %q, want %q after invalidation", s1.AgentType, "claudecode")
	}

	// s2: claudecode → should be untouched
	if got := s2.GetAgentSessionID(); got != "old-id-2" {
		t.Errorf("s2 (claudecode) AgentSessionID = %q, want %q (should be preserved)", got, "old-id-2")
	}
	if s2.AgentType != "claudecode" {
		t.Errorf("s2 AgentType = %q, want %q", s2.AgentType, "claudecode")
	}

	// s3: empty agent type → should be untouched (backward compat)
	if got := s3.GetAgentSessionID(); got != "old-id-3" {
		t.Errorf("s3 (empty type) AgentSessionID = %q, want %q (migration-safe)", got, "old-id-3")
	}
	if s3.AgentType != "" {
		t.Errorf("s3 AgentType = %q, want empty (pre-migration should be untouched)", s3.AgentType)
	}

	// s4: no agent session ID → should be untouched
	if got := s4.GetAgentSessionID(); got != "" {
		t.Errorf("s4 (no session ID) AgentSessionID = %q, want empty", got)
	}
}

func TestSession_ConcurrentGetSet(t *testing.T) {
	s := &Session{}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			s.SetAgentSessionID("id", "test")
		}()
		go func() {
			defer wg.Done()
			_ = s.GetAgentSessionID()
		}()
	}
	wg.Wait()
	if got := s.GetAgentSessionID(); got != "id" {
		t.Errorf("final GetAgentSessionID = %q, want %q", got, "id")
	}
}
