package codex

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestStartSessionUsesCodexRouterLease(t *testing.T) {
	tmp := t.TempDir()
	fakeBin := filepath.Join(tmp, "codex")
	if err := os.WriteFile(fakeBin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))

	leaseRequested := false
	releaseRequested := false
	leasedHome := filepath.Join(tmp, "leased-home")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/leases":
			leaseRequested = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":            true,
				"lease_id":      "lease-test",
				"codex_home":    leasedHome,
				"account_alias": "test-account",
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/leases/lease-test/release":
			releaseRequested = true
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	agent, err := New(map[string]any{
		"work_dir":                 tmp,
		"codex_router_url":         srv.URL,
		"codex_router_purpose":     "chat",
		"codex_router_ttl_seconds": float64(300),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	session, err := agent.StartSession(context.Background(), "")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if !leaseRequested {
		t.Fatal("expected router lease request")
	}
	cs, ok := session.(*codexRouterSession)
	if !ok {
		t.Fatalf("expected codexRouterSession, got %T", session)
	}
	inner, ok := cs.AgentSession.(*codexSession)
	if !ok {
		t.Fatalf("expected codexSession, got %T", cs.AgentSession)
	}
	if got := getenvFromList(inner.extraEnv, "CODEX_HOME"); got != leasedHome {
		t.Fatalf("CODEX_HOME = %q, want %q", got, leasedHome)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !releaseRequested {
		t.Fatal("expected router release request")
	}
}
