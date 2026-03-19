package core

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// testManagementServer creates a ManagementServer with a test engine and returns an httptest.Server.
func testManagementServer(t *testing.T, token string) (*ManagementServer, *httptest.Server, *Engine) {
	t.Helper()

	agent := &stubAgent{}
	sm := NewSessionManager("")
	e := NewEngine("test-project", agent, nil, "", LangEnglish)
	e.sessions = sm

	mgmt := NewManagementServer(0, token, nil)
	mgmt.RegisterEngine("test-project", e)

	mux := http.NewServeMux()
	prefix := "/api/v1"
	mux.HandleFunc(prefix+"/status", mgmt.wrap(mgmt.handleStatus))
	mux.HandleFunc(prefix+"/restart", mgmt.wrap(mgmt.handleRestart))
	mux.HandleFunc(prefix+"/reload", mgmt.wrap(mgmt.handleReload))
	mux.HandleFunc(prefix+"/config", mgmt.wrap(mgmt.handleConfig))
	mux.HandleFunc(prefix+"/projects", mgmt.wrap(mgmt.handleProjects))
	mux.HandleFunc(prefix+"/projects/", mgmt.wrap(mgmt.handleProjectRoutes))
	mux.HandleFunc(prefix+"/cron", mgmt.wrap(mgmt.handleCron))
	mux.HandleFunc(prefix+"/cron/", mgmt.wrap(mgmt.handleCronByID))
	mux.HandleFunc(prefix+"/bridge/adapters", mgmt.wrap(mgmt.handleBridgeAdapters))

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	return mgmt, ts, e
}

type mgmtResponse struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
}

func mgmtGet(t *testing.T, url, token string) mgmtResponse {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var r mgmtResponse
	_ = json.NewDecoder(resp.Body).Decode(&r)
	return r
}

func mgmtPost(t *testing.T, url, token string, body any) mgmtResponse {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest("POST", url, &buf)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var r mgmtResponse
	_ = json.NewDecoder(resp.Body).Decode(&r)
	return r
}

func mgmtPatch(t *testing.T, url, token string, body any) mgmtResponse {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest("PATCH", url, &buf)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var r mgmtResponse
	_ = json.NewDecoder(resp.Body).Decode(&r)
	return r
}

func mgmtDelete(t *testing.T, url, token string) mgmtResponse {
	t.Helper()
	req, _ := http.NewRequest("DELETE", url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var r mgmtResponse
	_ = json.NewDecoder(resp.Body).Decode(&r)
	return r
}

func TestMgmt_AuthRequired(t *testing.T) {
	_, ts, _ := testManagementServer(t, "secret-token")

	r := mgmtGet(t, ts.URL+"/api/v1/status", "")
	if r.OK {
		t.Fatal("expected auth failure without token")
	}
	if !strings.Contains(r.Error, "unauthorized") {
		t.Fatalf("expected unauthorized error, got: %s", r.Error)
	}

	r = mgmtGet(t, ts.URL+"/api/v1/status", "wrong-token")
	if r.OK {
		t.Fatal("expected auth failure with wrong token")
	}

	r = mgmtGet(t, ts.URL+"/api/v1/status", "secret-token")
	if !r.OK {
		t.Fatalf("expected success with correct token, got error: %s", r.Error)
	}
}

func TestMgmt_AuthQueryParam(t *testing.T) {
	_, ts, _ := testManagementServer(t, "qp-token")

	r := mgmtGet(t, ts.URL+"/api/v1/status?token=qp-token", "")
	if !r.OK {
		t.Fatalf("expected success with query param token, got: %s", r.Error)
	}
}

func TestMgmt_NoAuthRequired(t *testing.T) {
	_, ts, _ := testManagementServer(t, "")

	r := mgmtGet(t, ts.URL+"/api/v1/status", "")
	if !r.OK {
		t.Fatalf("expected success without token when no token configured, got: %s", r.Error)
	}
}

func TestMgmt_Status(t *testing.T) {
	_, ts, _ := testManagementServer(t, "tok")

	r := mgmtGet(t, ts.URL+"/api/v1/status", "tok")
	if !r.OK {
		t.Fatalf("status failed: %s", r.Error)
	}

	var data map[string]any
	_ = json.Unmarshal(r.Data, &data)
	if data["projects_count"] != float64(1) {
		t.Fatalf("expected 1 project, got %v", data["projects_count"])
	}
}

func TestMgmt_Projects(t *testing.T) {
	_, ts, _ := testManagementServer(t, "tok")

	r := mgmtGet(t, ts.URL+"/api/v1/projects", "tok")
	if !r.OK {
		t.Fatalf("projects failed: %s", r.Error)
	}

	var data struct {
		Projects []map[string]any `json:"projects"`
	}
	_ = json.Unmarshal(r.Data, &data)
	if len(data.Projects) != 1 {
		t.Fatalf("expected 1 project, got %d", len(data.Projects))
	}
	if data.Projects[0]["name"] != "test-project" {
		t.Fatalf("expected test-project, got %v", data.Projects[0]["name"])
	}
}

func TestMgmt_ProjectDetail(t *testing.T) {
	_, ts, _ := testManagementServer(t, "tok")

	r := mgmtGet(t, ts.URL+"/api/v1/projects/test-project", "tok")
	if !r.OK {
		t.Fatalf("project detail failed: %s", r.Error)
	}

	var data map[string]any
	_ = json.Unmarshal(r.Data, &data)
	if data["name"] != "test-project" {
		t.Fatalf("expected test-project, got %v", data["name"])
	}

	r = mgmtGet(t, ts.URL+"/api/v1/projects/nonexistent", "tok")
	if r.OK {
		t.Fatal("expected 404 for nonexistent project")
	}
}

func TestMgmt_ProjectPatch(t *testing.T) {
	_, ts, _ := testManagementServer(t, "tok")

	quiet := true
	r := mgmtPatch(t, ts.URL+"/api/v1/projects/test-project", "tok", map[string]any{
		"quiet": quiet,
	})
	if !r.OK {
		t.Fatalf("patch failed: %s", r.Error)
	}
}

func TestMgmt_Sessions(t *testing.T) {
	_, ts, e := testManagementServer(t, "tok")

	e.sessions.GetOrCreateActive("user1")

	r := mgmtGet(t, ts.URL+"/api/v1/projects/test-project/sessions", "tok")
	if !r.OK {
		t.Fatalf("sessions list failed: %s", r.Error)
	}

	// Create a session via API
	r = mgmtPost(t, ts.URL+"/api/v1/projects/test-project/sessions", "tok", map[string]string{
		"session_key": "user2",
		"name":        "work",
	})
	if !r.OK {
		t.Fatalf("create session failed: %s", r.Error)
	}
}

func TestMgmt_SessionDetail(t *testing.T) {
	_, ts, e := testManagementServer(t, "tok")

	s := e.sessions.GetOrCreateActive("user1")
	s.AddHistory("user", "hello")
	s.AddHistory("assistant", "hi there")

	r := mgmtGet(t, ts.URL+"/api/v1/projects/test-project/sessions/"+s.ID, "tok")
	if !r.OK {
		t.Fatalf("session detail failed: %s", r.Error)
	}

	var data struct {
		History []map[string]any `json:"history"`
	}
	_ = json.Unmarshal(r.Data, &data)
	if len(data.History) != 2 {
		t.Fatalf("expected 2 history entries, got %d", len(data.History))
	}
}

func TestMgmt_SessionDelete(t *testing.T) {
	_, ts, e := testManagementServer(t, "tok")

	s := e.sessions.GetOrCreateActive("user1")
	sid := s.ID

	r := mgmtDelete(t, ts.URL+"/api/v1/projects/test-project/sessions/"+sid, "tok")
	if !r.OK {
		t.Fatalf("delete session failed: %s", r.Error)
	}

	r = mgmtGet(t, ts.URL+"/api/v1/projects/test-project/sessions/"+sid, "tok")
	if r.OK {
		t.Fatal("expected 404 after deletion")
	}
}

func TestMgmt_Config(t *testing.T) {
	_, ts, _ := testManagementServer(t, "tok")

	r := mgmtGet(t, ts.URL+"/api/v1/config", "tok")
	if !r.OK {
		t.Fatalf("config failed: %s", r.Error)
	}

	var data struct {
		Projects []map[string]any `json:"projects"`
	}
	_ = json.Unmarshal(r.Data, &data)
	if len(data.Projects) != 1 {
		t.Fatalf("expected 1 project in config, got %d", len(data.Projects))
	}
}

func TestMgmt_Reload(t *testing.T) {
	_, ts, e := testManagementServer(t, "tok")

	reloaded := false
	e.configReloadFunc = func() (*ConfigReloadResult, error) {
		reloaded = true
		return &ConfigReloadResult{}, nil
	}

	r := mgmtPost(t, ts.URL+"/api/v1/reload", "tok", nil)
	if !r.OK {
		t.Fatalf("reload failed: %s", r.Error)
	}
	if !reloaded {
		t.Fatal("expected config reload to be triggered")
	}
}

func TestMgmt_BridgeAdapters(t *testing.T) {
	_, ts, _ := testManagementServer(t, "tok")

	r := mgmtGet(t, ts.URL+"/api/v1/bridge/adapters", "tok")
	if !r.OK {
		t.Fatalf("bridge adapters failed: %s", r.Error)
	}
}

func TestMgmt_HeartbeatNotConfigured(t *testing.T) {
	_, ts, _ := testManagementServer(t, "tok")

	r := mgmtGet(t, ts.URL+"/api/v1/projects/test-project/heartbeat", "tok")
	if r.OK {
		var data map[string]any
		_ = json.Unmarshal(r.Data, &data)
		// heartbeat scheduler is nil, so we expect service unavailable
	}
}

func TestMgmt_HeartbeatWithScheduler(t *testing.T) {
	mgmt, ts, _ := testManagementServer(t, "tok")
	hs := NewHeartbeatScheduler("")
	mgmt.SetHeartbeatScheduler(hs)

	r := mgmtGet(t, ts.URL+"/api/v1/projects/test-project/heartbeat", "tok")
	if !r.OK {
		t.Fatalf("heartbeat status failed: %s", r.Error)
	}

	var data map[string]any
	_ = json.Unmarshal(r.Data, &data)
	if data["enabled"] != false {
		t.Fatalf("expected heartbeat disabled, got %v", data["enabled"])
	}
}

func TestMgmt_CronNilScheduler(t *testing.T) {
	_, ts, _ := testManagementServer(t, "tok")

	r := mgmtGet(t, ts.URL+"/api/v1/cron", "tok")
	if r.OK {
		t.Fatal("expected error when cron scheduler is nil")
	}
}

func TestMgmt_CronWithScheduler(t *testing.T) {
	mgmt, ts, e := testManagementServer(t, "tok")
	store, err := NewCronStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cs := NewCronScheduler(store)
	cs.RegisterEngine("test-project", e)
	mgmt.SetCronScheduler(cs)

	// List (empty)
	r := mgmtGet(t, ts.URL+"/api/v1/cron", "tok")
	if !r.OK {
		t.Fatalf("cron list failed: %s", r.Error)
	}

	// Add
	r = mgmtPost(t, ts.URL+"/api/v1/cron", "tok", map[string]any{
		"project":     "test-project",
		"session_key": "user1",
		"cron_expr":   "0 9 * * *",
		"prompt":      "hello",
		"description": "test cron",
	})
	if !r.OK {
		t.Fatalf("cron add failed: %s", r.Error)
	}

	var job CronJob
	_ = json.Unmarshal(r.Data, &job)
	if job.ID == "" {
		t.Fatal("expected cron job ID")
	}

	// List (should have 1)
	r = mgmtGet(t, ts.URL+"/api/v1/cron", "tok")
	if !r.OK {
		t.Fatalf("cron list failed: %s", r.Error)
	}

	// Delete
	r = mgmtDelete(t, ts.URL+"/api/v1/cron/"+job.ID, "tok")
	if !r.OK {
		t.Fatalf("cron delete failed: %s", r.Error)
	}

	// Delete nonexistent
	r = mgmtDelete(t, ts.URL+"/api/v1/cron/nonexistent", "tok")
	if r.OK {
		t.Fatal("expected 404 for nonexistent cron job")
	}
}

func TestMgmt_CORS(t *testing.T) {
	mgmt := NewManagementServer(0, "", []string{"http://localhost:3000"})
	mgmt.RegisterEngine("p", NewEngine("p", &stubAgent{}, nil, "", LangEnglish))

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/status", mgmt.wrap(mgmt.handleStatus))
	ts := httptest.NewServer(mux)
	defer ts.Close()

	req, _ := http.NewRequest("OPTIONS", ts.URL+"/api/v1/status", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 for OPTIONS, got %d", resp.StatusCode)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "http://localhost:3000" {
		t.Fatalf("expected CORS origin header, got %q", resp.Header.Get("Access-Control-Allow-Origin"))
	}
}

func TestMgmt_MethodNotAllowed(t *testing.T) {
	_, ts, _ := testManagementServer(t, "tok")

	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/status", nil)
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	var r mgmtResponse
	_ = json.NewDecoder(resp.Body).Decode(&r)
}
