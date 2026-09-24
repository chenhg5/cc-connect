package core

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// stubPersistProvider is a ProviderSwitcher with a mutex so handler tests can
// assert on in-memory state without racing the HTTP handler goroutine.
type stubPersistProvider struct {
	stubAgent
	mu        sync.Mutex
	providers []ProviderConfig
	active    string
}

func (s *stubPersistProvider) ListProviders() []ProviderConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ProviderConfig, len(s.providers))
	copy(out, s.providers)
	return out
}
func (s *stubPersistProvider) SetProviders(p []ProviderConfig) {
	s.mu.Lock()
	s.providers = append([]ProviderConfig(nil), p...)
	s.mu.Unlock()
}
func (s *stubPersistProvider) GetActiveProvider() *ProviderConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.providers {
		if s.providers[i].Name == s.active {
			p := s.providers[i]
			return &p
		}
	}
	return nil
}
func (s *stubPersistProvider) SetActiveProvider(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if name == "" {
		s.active = ""
		return true
	}
	for _, p := range s.providers {
		if p.Name == name {
			s.active = name
			return true
		}
	}
	return false
}
func (s *stubPersistProvider) activeName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}
func (s *stubPersistProvider) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.providers)
}

func newMgmtProviderServer(t *testing.T, token string, save, addSave, removeSave func() error) (*ManagementServer, *httptest.Server, *Engine, *stubPersistProvider) {
	t.Helper()
	agent := &stubPersistProvider{
		providers: []ProviderConfig{{Name: "prov-a"}, {Name: "prov-b"}},
	}
	e := NewEngine("test-project", agent, nil, "", LangEnglish)
	e.sessions = NewSessionManager("")
	e.providerSaveFunc = func(string) error { return save() }
	e.providerAddSaveFunc = func(ProviderConfig) error { return addSave() }
	e.providerRemoveSaveFunc = func(string) error { return removeSave() }

	mgmt := NewManagementServer(0, token, nil)
	mgmt.RegisterEngine("test-project", e)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/projects/", mgmt.wrap(mgmt.handleProjectRoutes))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return mgmt, ts, e, agent
}

// Regression: activate previously returned 200 even when persisting the active
// provider to disk failed, so the in-memory state diverged from what was on
// disk without telling the caller. The handler must now return 500 and leave
// the active provider unchanged when save fails.
func TestMgmt_ActivateProvider_PersistFailure(t *testing.T) {
	wantErr := errors.New("disk full")
	_, ts, _, agent := newMgmtProviderServer(t, "tok",
		func() error { return wantErr },
		func() error { return nil },
		func() error { return nil },
	)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/projects/test-project/providers/prov-a/activate", nil)
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post activate: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 on persist failure", resp.StatusCode)
	}
	if got := agent.activeName(); got != "" {
		t.Fatalf("active provider mutated to %q despite persist failure", got)
	}
}

// Regression: DELETE provider must not mutate in-memory state when disk removal
// fails; it must surface a 500 so the user knows the operation didn't take.
func TestMgmt_DeleteProvider_PersistFailure(t *testing.T) {
	wantErr := errors.New("permission denied")
	_, ts, _, agent := newMgmtProviderServer(t, "tok",
		func() error { return nil },
		func() error { return nil },
		func() error { return wantErr },
	)

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/v1/projects/test-project/providers/prov-a", nil)
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 on persist failure", resp.StatusCode)
	}
	if got := agent.count(); got != 2 {
		t.Fatalf("provider count = %d after failed delete, want 2 (unchanged)", got)
	}
}

// Regression: POST provider must surface persistence failures as 500 and must
// not add the provider to in-memory state when disk write fails.
func TestMgmt_AddProvider_PersistFailure(t *testing.T) {
	wantErr := errors.New("config read-only")
	_, ts, _, agent := newMgmtProviderServer(t, "tok",
		func() error { return nil },
		func() error { return wantErr },
		func() error { return nil },
	)

	body := strings.NewReader(`{"name":"prov-c","api_key":"x"}`)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/projects/test-project/providers", body)
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post add: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 on persist failure", resp.StatusCode)
	}
	if got := agent.count(); got != 2 {
		t.Fatalf("provider count = %d after failed add, want 2 (unchanged)", got)
	}
}

// Sanity: when persistence succeeds, the happy paths still do the in-memory
// mutation and return 200.
func TestMgmt_ProviderLifecycle_HappyPath(t *testing.T) {
	_, ts, _, agent := newMgmtProviderServer(t, "tok",
		func() error { return nil },
		func() error { return nil },
		func() error { return nil },
	)

	// activate
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/projects/test-project/providers/prov-a/activate", nil)
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("activate status = %d, want 200", resp.StatusCode)
	}
	if got := agent.activeName(); got != "prov-a" {
		t.Fatalf("active = %q, want prov-a", got)
	}

	// add
	addBody := strings.NewReader(`{"name":"prov-c"}`)
	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/api/v1/projects/test-project/providers", addBody)
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("add status = %d, want 200", resp.StatusCode)
	}
	if got := agent.count(); got != 3 {
		t.Fatalf("count after add = %d, want 3", got)
	}

	// delete
	req, _ = http.NewRequest(http.MethodDelete, ts.URL+"/api/v1/projects/test-project/providers/prov-c", nil)
	req.Header.Set("Authorization", "Bearer tok")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete status = %d, want 200", resp.StatusCode)
	}
	if got := agent.count(); got != 2 {
		t.Fatalf("count after delete = %d, want 2", got)
	}
}
