package main

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestParseSendArgsBindsCodexThreadWithoutOverridingExplicitOrLegacySession(t *testing.T) {
	t.Setenv("CODEX_THREAD_ID", "bound-thread")
	t.Setenv("CC_CONNECT_SESSION_ENV", "")
	t.Setenv("CC_PROJECT", "")
	t.Setenv("CC_SESSION_KEY", "")
	req, _, err := parseSendArgs([]string{"-m", "hello"})
	if err != nil || req.AgentSessionID != "bound-thread" || req.Project != "" || req.SessionKey != "" {
		t.Fatalf("%+v %v", req, err)
	}
	req, _, err = parseSendArgs([]string{"--project", "other", "--session", "explicit", "-m", "hello"})
	if err != nil || req.AgentSessionID != "" || req.SessionKey != "explicit" || req.Project != "other" {
		t.Fatal(req, err)
	}
	t.Setenv("CC_CONNECT_SESSION_ENV", "1")
	t.Setenv("CC_PROJECT", "legacy")
	t.Setenv("CC_SESSION_KEY", "legacy-chat")
	req, _, err = parseSendArgs([]string{"-m", "hello"})
	if err != nil || req.AgentSessionID != "" || req.SessionKey != "legacy-chat" || req.Project != "legacy" {
		t.Fatal(req, err)
	}
}

func TestAgentToolCommandsSendThreadMetadataOnlyToLocalAPI(t *testing.T) {
	t.Setenv("CODEX_THREAD_ID", "bound-thread")
	t.Setenv("CC_CONNECT_SESSION_ENV", "")
	t.Setenv("CC_PROJECT", "")
	t.Setenv("CC_SESSION_KEY", "")
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "run"), 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(dir, "run", "api.sock"))
	if err != nil {
		t.Fatal(err)
	}
	requests := make(chan map[string]any, 4)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		requests <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"test-job","response":"test relay response","fire_at":"2099-01-01T00:00:00Z"}`))
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	runCronAdd([]string{"--data-dir", dir, "--cron", "0 0 * * *", "--prompt", "cron task"})
	runTimerAdd([]string{"--data-dir", dir, "--delay", "24h", "--prompt", "timer task"})
	runRelaySend([]string{"--data-dir", dir, "--to", "target", "--message", "relay task"})
	for i := 0; i < 3; i++ {
		body := <-requests
		if body["agent_session_id"] != "bound-thread" || body["session_key"] != "" {
			t.Fatal(body)
		}
	}
}

func TestManagedToolIgnoresInheritedLegacyRouting(t *testing.T) {
	t.Setenv("CODEX_THREAD_ID", "actual-thread")
	t.Setenv("CC_CONNECT_SESSION_ENV", "")
	t.Setenv("CC_PROJECT", "stale-project")
	t.Setenv("CC_SESSION_KEY", "stale-chat")
	t.Setenv("CC_DATA_DIR", "/stale-data")
	req, _, err := parseSendArgs([]string{"-m", "hello"})
	if err != nil || req.AgentSessionID != "actual-thread" || req.Project != "" || req.SessionKey != "" {
		t.Fatalf("inherited routing leaked: %+v %v", req, err)
	}
	if agentToolLegacyEnv("CC_DATA_DIR") != "" {
		t.Fatal("daemon inherited data_dir")
	}
	req, _, err = parseSendArgs([]string{"--project", "explicit", "--session", "explicit-chat", "-m", "hello"})
	if err != nil || req.Project != "explicit" || req.SessionKey != "explicit-chat" || req.AgentSessionID != "" {
		t.Fatal(req, err)
	}
}

func TestAgentToolLegacyEnvironmentWithoutCodexThread(t *testing.T) {
	t.Setenv("CODEX_THREAD_ID", "")
	t.Setenv("CC_CONNECT_SESSION_ENV", "")
	t.Setenv("CC_PROJECT", "legacy")
	t.Setenv("CC_SESSION_KEY", "legacy-chat")
	t.Setenv("CC_DATA_DIR", "/legacy-data")
	req, _, err := parseSendArgs([]string{"-m", "hello"})
	if err != nil || req.Project != "legacy" || req.SessionKey != "legacy-chat" || req.AgentSessionID != "" {
		t.Fatal(req, err)
	}
	if got := resolveSocketPath(""); got != "/legacy-data/run/api.sock" {
		t.Fatal(got)
	}
}
