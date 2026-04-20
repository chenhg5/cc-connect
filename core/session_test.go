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

func TestKnownAgentSessionIDs(t *testing.T) {
	sm := NewSessionManager("")
	s1 := sm.NewSession("user1", "a")
	s1.SetAgentSessionID("uuid-aaa", "claude")
	s2 := sm.NewSession("user1", "b")
	s2.SetAgentSessionID("uuid-bbb", "claude")
	sm.NewSession("user1", "c") // no agent session id

	known := sm.KnownAgentSessionIDs()
	if len(known) != 2 {
		t.Fatalf("KnownAgentSessionIDs len = %d, want 2", len(known))
	}
	if _, ok := known["uuid-aaa"]; !ok {
		t.Fatal("expected uuid-aaa in known set")
	}
	if _, ok := known["uuid-bbb"]; !ok {
		t.Fatal("expected uuid-bbb in known set")
	}
}

func TestFilterOwnedSessions_FiltersUnknown(t *testing.T) {
	all := []AgentSessionInfo{
		{ID: "owned-1"},
		{ID: "external-1"},
		{ID: "owned-2"},
		{ID: "external-2"},
	}
	known := map[string]struct{}{
		"owned-1": {},
		"owned-2": {},
	}
	filtered := filterOwnedSessions(all, known)
	if len(filtered) != 2 {
		t.Fatalf("filterOwnedSessions len = %d, want 2", len(filtered))
	}
	if filtered[0].ID != "owned-1" || filtered[1].ID != "owned-2" {
		t.Fatalf("filtered = %v, want owned-1 and owned-2", filtered)
	}
}

func TestFilterOwnedSessions_EmptyKnownReturnsAll(t *testing.T) {
	all := []AgentSessionInfo{
		{ID: "session-1"},
		{ID: "session-2"},
	}
	filtered := filterOwnedSessions(all, map[string]struct{}{})
	if len(filtered) != 2 {
		t.Fatalf("filterOwnedSessions with empty known = %d, want 2", len(filtered))
	}
}

func TestSwitchToAgentSession_PreservesOldSession(t *testing.T) {
	dir := t.TempDir()
	sm := NewSessionManager(dir + "/sessions.json")
	userKey := "user:alice"

	s1 := sm.GetOrCreateActive(userKey)
	s1.SetAgentInfo("agent-A", "claude", "session A")

	known := sm.KnownAgentSessionIDs()
	if _, ok := known["agent-A"]; !ok {
		t.Fatal("agent-A should be in KnownAgentSessionIDs before switch")
	}

	s2 := sm.SwitchToAgentSession(userKey, "agent-B", "claude", "session B")
	if s2.GetAgentSessionID() != "agent-B" {
		t.Fatalf("switched session AgentSessionID = %q, want agent-B", s2.GetAgentSessionID())
	}

	known = sm.KnownAgentSessionIDs()
	if _, ok := known["agent-A"]; !ok {
		t.Fatal("agent-A should still be in KnownAgentSessionIDs after switch")
	}
	if _, ok := known["agent-B"]; !ok {
		t.Fatal("agent-B should be in KnownAgentSessionIDs after switch")
	}
}

func TestSwitchToAgentSession_ReusesExisting(t *testing.T) {
	dir := t.TempDir()
	sm := NewSessionManager(dir + "/sessions.json")
	userKey := "user:bob"

	s1 := sm.GetOrCreateActive(userKey)
	s1.SetAgentInfo("agent-A", "claude", "session A")

	sm.SwitchToAgentSession(userKey, "agent-B", "claude", "session B")

	s3 := sm.SwitchToAgentSession(userKey, "agent-A", "claude", "session A")
	if s3.ID != s1.ID {
		t.Fatalf("switching back to agent-A should reuse session %s, got %s", s1.ID, s3.ID)
	}
}

func TestPastAgentSessionIDs_ClearPreservesHistory(t *testing.T) {
	s := &Session{}
	s.SetAgentSessionID("thread-1", "codex")
	s.SetAgentSessionID("", "")

	if len(s.PastAgentSessionIDs) != 1 || s.PastAgentSessionIDs[0] != "thread-1" {
		t.Fatalf("PastAgentSessionIDs = %v, want [thread-1]", s.PastAgentSessionIDs)
	}
}

func TestPastAgentSessionIDs_ReplacePreservesHistory(t *testing.T) {
	s := &Session{}
	s.SetAgentSessionID("thread-1", "codex")
	s.SetAgentSessionID("thread-2", "codex")

	if len(s.PastAgentSessionIDs) != 1 || s.PastAgentSessionIDs[0] != "thread-1" {
		t.Fatalf("PastAgentSessionIDs = %v, want [thread-1]", s.PastAgentSessionIDs)
	}
	if s.AgentSessionID != "thread-2" {
		t.Fatalf("AgentSessionID = %q, want thread-2", s.AgentSessionID)
	}
}

func TestPastAgentSessionIDs_NoDuplicates(t *testing.T) {
	s := &Session{}
	s.SetAgentSessionID("thread-1", "codex")
	s.SetAgentSessionID("", "")
	s.SetAgentSessionID("thread-1", "codex")
	s.SetAgentSessionID("", "")

	if len(s.PastAgentSessionIDs) != 1 {
		t.Fatalf("PastAgentSessionIDs has duplicates: %v", s.PastAgentSessionIDs)
	}
}

func TestPastAgentSessionIDs_ContinueSentinelNotRecorded(t *testing.T) {
	s := &Session{}
	s.SetAgentSessionID(ContinueSession, "codex")
	s.SetAgentSessionID("real-id", "codex")
	s.SetAgentSessionID("", "")

	for _, past := range s.PastAgentSessionIDs {
		if past == ContinueSession {
			t.Fatal("ContinueSession sentinel should not be in PastAgentSessionIDs")
		}
	}
	if len(s.PastAgentSessionIDs) != 1 || s.PastAgentSessionIDs[0] != "real-id" {
		t.Fatalf("PastAgentSessionIDs = %v, want [real-id]", s.PastAgentSessionIDs)
	}
}

func TestSetAgentInfo_PreservesHistory(t *testing.T) {
	s := &Session{}
	s.SetAgentInfo("thread-1", "codex", "session 1")
	s.SetAgentInfo("thread-2", "codex", "session 2")

	if len(s.PastAgentSessionIDs) != 1 || s.PastAgentSessionIDs[0] != "thread-1" {
		t.Fatalf("SetAgentInfo PastAgentSessionIDs = %v, want [thread-1]", s.PastAgentSessionIDs)
	}
}

func TestKnownAgentSessionIDs_IncludesPast(t *testing.T) {
	sm := NewSessionManager("")
	s1 := sm.NewSession("user1", "a")
	s1.SetAgentSessionID("thread-aaa", "codex")
	s1.SetAgentSessionID("", "")

	s2 := sm.NewSession("user1", "b")
	s2.SetAgentSessionID("thread-bbb", "codex")

	known := sm.KnownAgentSessionIDs()
	if _, ok := known["thread-aaa"]; !ok {
		t.Fatal("expected thread-aaa (past ID) in known set")
	}
	if _, ok := known["thread-bbb"]; !ok {
		t.Fatal("expected thread-bbb (current ID) in known set")
	}
}

// TestKnownAgentSessionIDs_ReproducesNewCommandBug simulates the exact user
// reproduction steps: repeated /new commands progressively clear AgentSessionIDs.
// Before the PastAgentSessionIDs fix, only the latest session would remain visible.
func TestKnownAgentSessionIDs_ReproducesNewCommandBug(t *testing.T) {
	sm := NewSessionManager("")
	userKey := "user:test"

	agentSessions := []AgentSessionInfo{
		{ID: "codex-thread-1"},
		{ID: "codex-thread-2"},
		{ID: "codex-thread-3"},
	}

	s1 := sm.GetOrCreateActive(userKey)
	s1.SetAgentSessionID("codex-thread-1", "codex")

	s1.SetAgentSessionID("", "")
	s2 := sm.NewSession(userKey, "session 2")
	s2.SetAgentSessionID("codex-thread-2", "codex")

	s2.SetAgentSessionID("", "")
	s3 := sm.NewSession(userKey, "session 3")
	s3.SetAgentSessionID("codex-thread-3", "codex")

	known := sm.KnownAgentSessionIDs()
	filtered := filterOwnedSessions(agentSessions, known)

	if len(filtered) != 3 {
		t.Fatalf("filterOwnedSessions returned %d sessions, want 3 (all should be visible)\nknown IDs: %v",
			len(filtered), known)
	}
}

// TestKnownAgentSessionIDs_ResetAllSessionsBug simulates resetAllSessions
// clearing all IDs (management API provider switch). Past IDs should keep
// all sessions visible.
func TestKnownAgentSessionIDs_ResetAllSessionsBug(t *testing.T) {
	sm := NewSessionManager("")
	userKey := "user:test"

	s1 := sm.NewSession(userKey, "a")
	s1.SetAgentSessionID("thread-1", "codex")
	s2 := sm.NewSession(userKey, "b")
	s2.SetAgentSessionID("thread-2", "codex")
	s3 := sm.NewSession(userKey, "c")
	s3.SetAgentSessionID("thread-3", "codex")

	for _, s := range sm.AllSessions() {
		s.SetAgentSessionID("", "")
	}

	known := sm.KnownAgentSessionIDs()
	for _, id := range []string{"thread-1", "thread-2", "thread-3"} {
		if _, ok := known[id]; !ok {
			t.Fatalf("expected %s in known set after resetAllSessions, known = %v", id, known)
		}
	}

	agentSessions := []AgentSessionInfo{
		{ID: "thread-1"}, {ID: "thread-2"}, {ID: "thread-3"},
	}
	filtered := filterOwnedSessions(agentSessions, known)
	if len(filtered) != 3 {
		t.Fatalf("filterOwnedSessions returned %d, want 3", len(filtered))
	}
}

func TestPastAgentSessionIDs_Persistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")

	sm1 := NewSessionManager(path)
	s := sm1.NewSession("user1", "test")
	s.SetAgentSessionID("thread-old", "codex")
	s.SetAgentSessionID("thread-new", "codex")
	sm1.Save()

	sm2 := NewSessionManager(path)
	known := sm2.KnownAgentSessionIDs()
	if _, ok := known["thread-old"]; !ok {
		t.Fatal("past ID thread-old not persisted/loaded")
	}
	if _, ok := known["thread-new"]; !ok {
		t.Fatal("current ID thread-new not persisted/loaded")
	}
}

// TestKnownAgentSessionIDs_LegacyDataDisablesFilter simulates loading a
// session file written by the old code (before PastAgentSessionIDs tracking).
// The filter must be disabled so sessions with lost IDs remain visible.
func TestKnownAgentSessionIDs_LegacyDataDisablesFilter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")

	legacyJSON := `{
		"sessions": {
			"s1": {"id":"s1","name":"old","agent_session_id":"","history":null,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"},
			"s2": {"id":"s2","name":"","agent_session_id":"","history":null,"created_at":"2026-01-02T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"},
			"s3": {"id":"s3","name":"active","agent_session_id":"thread-3","agent_type":"codex","history":null,"created_at":"2026-01-03T00:00:00Z","updated_at":"2026-01-03T00:00:00Z"}
		},
		"active_session": {"user1":"s3"},
		"user_sessions": {"user1":["s1","s2","s3"]},
		"counter": 3
	}`
	if err := os.WriteFile(path, []byte(legacyJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	sm := NewSessionManager(path)
	known := sm.KnownAgentSessionIDs()

	if known != nil {
		t.Fatalf("legacy data should return nil known IDs to disable filter, got %v", known)
	}

	agentSessions := []AgentSessionInfo{
		{ID: "thread-1"}, {ID: "thread-2"}, {ID: "thread-3"},
	}
	filtered := filterOwnedSessions(agentSessions, known)
	if len(filtered) != 3 {
		t.Fatalf("filterOwnedSessions with legacy data returned %d, want 3 (all visible)", len(filtered))
	}
}

// TestKnownAgentSessionIDs_NewDataEnablesFilter verifies that data saved by
// the new code (with PastIDTracking=true) enables normal filtering.
func TestKnownAgentSessionIDs_NewDataEnablesFilter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")

	sm1 := NewSessionManager(path)
	s1 := sm1.NewSession("user1", "a")
	s1.SetAgentSessionID("thread-1", "codex")
	sm1.NewSession("user1", "b")
	sm1.Save()

	sm2 := NewSessionManager(path)
	known := sm2.KnownAgentSessionIDs()

	if known == nil {
		t.Fatal("new data should not return nil known IDs")
	}
	if _, ok := known["thread-1"]; !ok {
		t.Fatal("thread-1 should be in known set")
	}

	agentSessions := []AgentSessionInfo{
		{ID: "thread-1"}, {ID: "external-1"},
	}
	filtered := filterOwnedSessions(agentSessions, known)
	if len(filtered) != 1 || filtered[0].ID != "thread-1" {
		t.Fatalf("filterOwnedSessions should hide external session, got %v", filtered)
	}
}

// TestLegacyData_PartiallyMigratedData verifies that data saved by a prior code
// version with PastIDTracking=true but without LegacyData persistence is detected
// as legacy if untracked sessions exist (sessions that lost their IDs before
// PastAgentSessionIDs tracking was available).
func TestLegacyData_PartiallyMigratedData(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")

	partialJSON := `{
		"sessions": {
			"s1": {"id":"s1","name":"default","agent_session_id":"","history":null,"created_at":"2026-03-26T22:25:56Z","updated_at":"2026-03-26T22:25:56Z"},
			"s2": {"id":"s2","name":"","agent_session_id":"","history":null,"created_at":"2026-04-18T09:02:57Z","updated_at":"2026-04-18T09:02:57Z"},
			"s3": {"id":"s3","name":"active","agent_session_id":"thread-active","agent_type":"codex","past_agent_session_ids":["thread-old"],"history":null,"created_at":"2026-04-20T21:50:14Z","updated_at":"2026-04-20T21:50:14Z"}
		},
		"active_session": {"user1":"s3"},
		"user_sessions":  {"user1":["s1","s2","s3"]},
		"counter": 3,
		"past_id_tracking": true
	}`
	if err := os.WriteFile(path, []byte(partialJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	sm := NewSessionManager(path)
	known := sm.KnownAgentSessionIDs()

	if known != nil {
		t.Fatalf("partially migrated data should disable filter (return nil), got %v", known)
	}

	agentSessions := []AgentSessionInfo{
		{ID: "thread-active"}, {ID: "thread-old"}, {ID: "other-1"}, {ID: "other-2"},
	}
	filtered := filterOwnedSessions(agentSessions, known)
	if len(filtered) != 4 {
		t.Fatalf("all sessions should be visible with legacy data, got %d", len(filtered))
	}
}

// TestLegacyData_ClearsAfterFirstNewCommand verifies the full migration
// lifecycle: legacy data → disable filter → /new populates PastAgentSessionIDs
// → filter re-enables on next cycle.
func TestLegacyData_ClearsAfterFirstNewCommand(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")

	legacyJSON := `{
		"sessions": {
			"s1": {"id":"s1","name":"","agent_session_id":"thread-old","agent_type":"codex","history":null,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}
		},
		"active_session": {"user1":"s1"},
		"user_sessions": {"user1":["s1"]},
		"counter": 1
	}`
	if err := os.WriteFile(path, []byte(legacyJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	sm := NewSessionManager(path)
	known := sm.KnownAgentSessionIDs()
	if known == nil {
		t.Log("legacy mode: filter disabled (only 1 session, OK)")
	}

	s1 := sm.GetOrCreateActive("user1")
	s1.SetAgentSessionID("", "")
	s2 := sm.NewSession("user1", "new")
	s2.SetAgentSessionID("thread-new", "codex")
	sm.Save()

	sm2 := NewSessionManager(path)
	known2 := sm2.KnownAgentSessionIDs()

	if known2 == nil {
		t.Fatal("after save with new code, known should not be nil")
	}
	if _, ok := known2["thread-old"]; !ok {
		t.Fatal("thread-old should be in known via PastAgentSessionIDs")
	}
	if _, ok := known2["thread-new"]; !ok {
		t.Fatal("thread-new should be in known as current ID")
	}
}
