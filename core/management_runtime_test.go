package core

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
)

type mgmtRuntimeAgentSession struct {
	stubAgentSession
	alive atomic.Bool
}

func (s *mgmtRuntimeAgentSession) Alive() bool { return s.alive.Load() }

// Check both API representations, including presence of the nullable field.
func assertMgmtSessionRuntime(t *testing.T, baseURL, id string, busy bool, alive *bool) {
	t.Helper()
	list := mgmtGet(t, baseURL, "tok")
	if !list.OK {
		t.Fatalf("list sessions: %s", list.Error)
	}
	var data struct {
		Sessions []map[string]json.RawMessage `json:"sessions"`
	}
	if err := json.Unmarshal(list.Data, &data); err != nil {
		t.Fatal(err)
	}
	var summary map[string]json.RawMessage
	for _, item := range data.Sessions {
		var itemID string
		if err := json.Unmarshal(item["id"], &itemID); err != nil {
			t.Fatal(err)
		}
		if itemID == id {
			summary = item
		}
	}
	if summary == nil {
		t.Fatalf("session %s missing from list", id)
	}
	detail := mgmtGet(t, baseURL+"/"+id, "tok")
	if !detail.OK {
		t.Fatalf("session detail: %s", detail.Error)
	}
	var item map[string]json.RawMessage
	if err := json.Unmarshal(detail.Data, &item); err != nil {
		t.Fatal(err)
	}
	for name, fields := range map[string]map[string]json.RawMessage{"list": summary, "detail": item} {
		if _, exists := fields["agent_alive"]; exists {
			t.Errorf("%s still exposes the ambiguous agent_alive field", name)
		}
		wantBusy, _ := json.Marshal(busy)
		wantAlive, _ := json.Marshal(alive)
		if string(fields["busy"]) != string(wantBusy) {
			t.Errorf("%s busy = %s, want %s", name, fields["busy"], wantBusy)
		}
		if string(fields["agent_session_alive"]) != string(wantAlive) {
			t.Errorf("%s agent_session_alive = %s, want %s", name, fields["agent_session_alive"], wantAlive)
		}
	}
}

func TestMgmt_SessionRuntimeLifecycle(t *testing.T) {
	_, ts, e := testManagementServer(t, "tok")
	key := "test:chat:user"
	s := e.sessions.GetOrCreateActive(key)
	url := ts.URL + "/api/v1/projects/test-project/sessions"
	assertMgmtSessionRuntime(t, url, s.ID, false, nil)

	// Exercise the real engine association, including process reuse.
	p := &stubPlatformEngine{n: "test"}
	state := e.getOrCreateInteractiveStateWith(key, p, nil, s, e.sessions, nil, "")
	if state == nil {
		t.Fatal("failed to start agent session")
	}
	if reused := e.getOrCreateInteractiveStateWith(key, p, nil, s, e.sessions, nil, ""); reused != state {
		t.Fatal("expected the existing session process to be reused")
	}
	agent := &mgmtRuntimeAgentSession{}
	agent.alive.Store(true)
	e.interactiveMu.Lock()
	state.mu.Lock()
	state.agentSession = agent
	state.mu.Unlock()
	e.interactiveMu.Unlock()
	alive := true
	assertMgmtSessionRuntime(t, url, s.ID, false, &alive)

	gen, ok := s.TryLock()
	if !ok {
		t.Fatal("failed to acquire session")
	}
	assertMgmtSessionRuntime(t, url, s.ID, true, &alive)
	s.Unlock(gen)
	assertMgmtSessionRuntime(t, url, s.ID, false, &alive)

	agent.alive.Store(false)
	alive = false
	assertMgmtSessionRuntime(t, url, s.ID, false, &alive)
	e.cleanupInteractiveState(key)
	assertMgmtSessionRuntime(t, url, s.ID, false, nil)
}

func TestMgmt_SessionRuntimeDoesNotFollowSelectedSession(t *testing.T) {
	_, ts, e := testManagementServer(t, "tok")
	key := "test:chat:user"
	old := e.sessions.GetOrCreateActive(key)
	state := e.getOrCreateInteractiveStateWith(key, &stubPlatformEngine{n: "test"}, nil, old, e.sessions, nil, "")
	if state == nil {
		t.Fatal("failed to start agent session")
	}
	defer e.cleanupInteractiveState(key)
	gen, ok := old.TryLock()
	if !ok {
		t.Fatal("failed to acquire session")
	}
	defer old.Unlock(gen)
	current := e.sessions.NewSession(key, "new")
	url := ts.URL + "/api/v1/projects/test-project/sessions"
	alive := true
	// Both sessions share a routing key; only the old one owns the process.
	assertMgmtSessionRuntime(t, url, old.ID, true, &alive)
	assertMgmtSessionRuntime(t, url, current.ID, false, nil)

	// Switching selection alone must not transfer process ownership either.
	if _, err := e.sessions.SwitchSession(key, old.ID); err != nil {
		t.Fatal(err)
	}
	assertMgmtSessionRuntime(t, url, old.ID, true, &alive)
	assertMgmtSessionRuntime(t, url, current.ID, false, nil)
	if _, err := e.sessions.SwitchSession(key, current.ID); err != nil {
		t.Fatal(err)
	}
	if replacement := e.getOrCreateInteractiveStateWith(key, &stubPlatformEngine{n: "test"}, nil, current, e.sessions, nil, ""); replacement == nil || replacement == state {
		t.Fatal("expected a replacement process for the new session")
	}
	assertMgmtSessionRuntime(t, url, old.ID, true, nil)
	assertMgmtSessionRuntime(t, url, current.ID, false, &alive)
}

func TestMgmt_SessionRuntimeStartingAndUnassociatedStates(t *testing.T) {
	_, ts, e := testManagementServer(t, "tok")
	s := e.sessions.GetOrCreateActive("test:chat:user")
	gen, ok := s.TryLock()
	if !ok {
		t.Fatal("failed to acquire session")
	}
	defer s.Unlock(gen)
	url := ts.URL + "/api/v1/projects/test-project/sessions"
	// The busy lock can be held before the process has been created.
	e.interactiveStates["test:chat:user"] = &interactiveState{busySession: s}
	assertMgmtSessionRuntime(t, url, s.ID, true, nil)
	// A routing-key entry alone is not proof of association with this session.
	e.interactiveStates["test:chat:user"] = &interactiveState{agentSession: &stubAgentSession{}}
	assertMgmtSessionRuntime(t, url, s.ID, true, nil)
}

func TestMgmt_SessionRuntimeConcurrentUpdates(t *testing.T) {
	_, ts, e := testManagementServer(t, "tok")
	key := "test:chat:user"
	s := e.sessions.GetOrCreateActive(key)
	other := e.sessions.NewSession(key, "other")
	agent := &mgmtRuntimeAgentSession{}
	agent.alive.Store(true)
	state := &interactiveState{busySession: s, agentSession: agent}
	e.interactiveStates[key] = state
	var wg sync.WaitGroup
	done := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-done:
				return
			default:
			}
			gen, ok := s.TryLock()
			if ok {
				s.Unlock(gen)
			}
			agent.alive.Store(i%2 == 0)
			e.interactiveMu.Lock()
			state.mu.Lock()
			if i%2 == 0 {
				state.busySession = other
				state.agentSession = nil
				delete(e.interactiveStates, key)
			} else {
				state.busySession = s
				state.agentSession = agent
				e.interactiveStates[key] = state
			}
			state.mu.Unlock()
			e.interactiveMu.Unlock()
		}
	}()
	defer func() {
		close(done)
		wg.Wait()
	}()
	for i := 0; i < 30; i++ {
		for _, path := range []string{"", "/" + s.ID} {
			r := mgmtGet(t, ts.URL+"/api/v1/projects/test-project/sessions"+path, "tok")
			if !r.OK {
				t.Fatalf("runtime read failed: %s", r.Error)
			}
		}
	}
}
