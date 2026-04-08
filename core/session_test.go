package core

import (
	"os"
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

func TestSessionManager_NewSideSession(t *testing.T) {
	sm := NewSessionManager("")
	main := sm.GetOrCreateActive("user1")
	side := sm.NewSideSession("user1", "cron-job")

	if side.ID == main.ID {
		t.Fatal("side session should be a new record")
	}
	if sm.ActiveSessionID("user1") != main.ID {
		t.Errorf("active session should stay main %q, got %q", main.ID, sm.ActiveSessionID("user1"))
	}
	list := sm.ListSessions("user1")
	if len(list) != 2 {
		t.Fatalf("want 2 sessions for user1, got %d", len(list))
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

func TestSessionManager_GetOrCreateActive_Persists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")

	sm1 := NewSessionManager(path)
	s := sm1.GetOrCreateActive("user1")
	if s == nil {
		t.Fatal("expected non-nil session")
	}

	// Reload from disk — session should survive
	sm2 := NewSessionManager(path)
	list := sm2.ListSessions("user1")
	if len(list) != 1 {
		t.Fatalf("expected 1 session after reload, got %d", len(list))
	}
	if list[0].ID != s.ID {
		t.Errorf("reloaded session ID = %q, want %q", list[0].ID, s.ID)
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

func TestSession_SetAgentSessionID_RejectsContinueSentinel(t *testing.T) {
	s := &Session{}
	s.SetAgentSessionID("real", "ag")
	s.SetAgentSessionID(ContinueSession, "ag")
	if got := s.GetAgentSessionID(); got != "real" {
		t.Fatalf("ContinueSession must not clobber stored id, got %q", got)
	}
	s.SetAgentSessionID("", "")
	if got := s.GetAgentSessionID(); got != "" {
		t.Fatalf("expected clear, got %q", got)
	}
}

func TestSession_CompareAndSet_ReplacesContinueSentinel(t *testing.T) {
	s := &Session{}
	s.mu.Lock()
	s.AgentSessionID = ContinueSession
	s.mu.Unlock()
	if !s.CompareAndSetAgentSessionID("uuid-1", "pi") {
		t.Fatal("expected CompareAndSet to replace erroneous ContinueSession slot")
	}
	if s.GetAgentSessionID() != "uuid-1" {
		t.Fatalf("GetAgentSessionID = %q, want uuid-1", s.GetAgentSessionID())
	}
	if s.CompareAndSetAgentSessionID("uuid-2", "pi") {
		t.Fatal("expected second CompareAndSet to fail when real id already set")
	}
}

func TestSession_SetAgentInfo_NormalizesContinueSentinel(t *testing.T) {
	s := &Session{}
	s.SetAgentInfo(ContinueSession, "pi", "n")
	if s.GetAgentSessionID() != "" {
		t.Fatalf("SetAgentInfo(ContinueSession) should store empty id, got %q", s.GetAgentSessionID())
	}
}

func TestSessionManager_Load_SanitizesContinueSentinel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")
	raw := `{
  "sessions": {
    "s1": {
      "id": "s1",
      "name": "default",
      "agent_session_id": "__continue__",
      "agent_type": "pi",
      "history": [],
      "created_at": "2020-01-01T00:00:00Z",
      "updated_at": "2020-01-01T00:00:00Z"
    }
  },
  "active_session": {"user1": "s1"},
  "user_sessions": {"user1": ["s1"]},
  "counter": 1
}`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	sm := NewSessionManager(path)
	s := sm.GetOrCreateActive("user1")
	if got := s.GetAgentSessionID(); got != "" {
		t.Fatalf("loaded session should clear ContinueSession, got %q", got)
	}
}

func TestSessionManager_Save_StripsContinueSentinel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")
	sm := NewSessionManager(path)
	sm.NewSession("u1", "x")
	s := sm.GetOrCreateActive("u1")
	s.mu.Lock()
	s.AgentSessionID = ContinueSession
	s.AgentType = "pi"
	s.mu.Unlock()
	sm.Save()
	sm2 := NewSessionManager(path)
	// Same user key should reload the same logical session without sentinel.
	s2 := sm2.GetOrCreateActive("u1")
	if got := s2.GetAgentSessionID(); got != "" {
		t.Fatalf("after save+reload want empty agent_session_id, got %q", got)
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

func TestSessionManager_UserMeta(t *testing.T) {
	sm := NewSessionManager("")
	sm.GetOrCreateActive("feishu:oc_abc:ou_xyz")

	// Set UserName
	sm.UpdateUserMeta("feishu:oc_abc:ou_xyz", "Zhang San", "")
	meta := sm.GetUserMeta("feishu:oc_abc:ou_xyz")
	if meta == nil || meta.UserName != "Zhang San" {
		t.Errorf("expected UserName='Zhang San', got %+v", meta)
	}
	if meta.ChatName != "" {
		t.Errorf("expected empty ChatName, got %q", meta.ChatName)
	}

	// Merge: add ChatName without losing UserName
	sm.UpdateUserMeta("feishu:oc_abc:ou_xyz", "", "Test Group")
	meta = sm.GetUserMeta("feishu:oc_abc:ou_xyz")
	if meta.UserName != "Zhang San" || meta.ChatName != "Test Group" {
		t.Errorf("expected merge, got %+v", meta)
	}

	// No-op for empty values
	sm.UpdateUserMeta("feishu:oc_abc:ou_xyz", "", "")
	meta = sm.GetUserMeta("feishu:oc_abc:ou_xyz")
	if meta.UserName != "Zhang San" || meta.ChatName != "Test Group" {
		t.Errorf("expected no change, got %+v", meta)
	}

	// Unknown key returns nil
	if m := sm.GetUserMeta("nonexistent"); m != nil {
		t.Errorf("expected nil for unknown key, got %+v", m)
	}
}

func TestSessionManager_UserMetaPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")

	sm1 := NewSessionManager(path)
	sm1.NewSession("feishu:oc_abc:ou_xyz", "test")
	sm1.UpdateUserMeta("feishu:oc_abc:ou_xyz", "Zhang San", "Group Name")
	sm1.Save()

	sm2 := NewSessionManager(path)
	meta := sm2.GetUserMeta("feishu:oc_abc:ou_xyz")
	if meta == nil || meta.UserName != "Zhang San" || meta.ChatName != "Group Name" {
		t.Errorf("expected persisted meta, got %+v", meta)
	}
}

func TestSessionManager_DeleteByAgentSessionID(t *testing.T) {
	sm := NewSessionManager("")

	s1 := sm.NewSession("user1", "one")
	s1.SetAgentSessionID("agent-1", "codex")

	s2 := sm.NewSession("user2", "two")
	s2.SetAgentSessionID("agent-2", "codex")

	s3 := sm.NewSession("user3", "three")
	s3.SetAgentSessionID("agent-1", "codex")

	if removed := sm.DeleteByAgentSessionID("agent-1"); removed != 2 {
		t.Fatalf("removed = %d, want 2", removed)
	}
	if got := sm.FindByID(s1.ID); got != nil {
		t.Fatalf("expected s1 removed, got %+v", got)
	}
	if got := sm.FindByID(s3.ID); got != nil {
		t.Fatalf("expected s3 removed, got %+v", got)
	}
	if got := sm.FindByID(s2.ID); got == nil {
		t.Fatal("expected s2 preserved")
	}
	if got := sm.ActiveSessionID("user1"); got != "" {
		t.Fatalf("user1 active session = %q, want empty", got)
	}
	if got := sm.ActiveSessionID("user3"); got != "" {
		t.Fatalf("user3 active session = %q, want empty", got)
	}
	if list := sm.ListSessions("user2"); len(list) != 1 || list[0].ID != s2.ID {
		t.Fatalf("user2 sessions = %+v, want only s2", list)
	}

	if removed := sm.DeleteByAgentSessionID("missing"); removed != 0 {
		t.Fatalf("removed missing = %d, want 0", removed)
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

func TestSessionManager_StorePath(t *testing.T) {
	sm := NewSessionManager("/var/data/sessions")
	if got := sm.StorePath(); got != "/var/data/sessions" {
		t.Errorf("StorePath() = %q, want %q", got, "/var/data/sessions")
	}

	sm2 := NewSessionManager("")
	if got := sm2.StorePath(); got != "" {
		t.Errorf("StorePath() empty = %q, want empty string", got)
	}
}
