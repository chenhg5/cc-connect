package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"testing"
)

// threadRecorder answers every JSON-RPC request written to stdin with a
// thread id and keeps the request params, so ensureThread can run without
// a real codex app-server.
type threadRecorder struct {
	mu      sync.Mutex
	s       *appServerSession
	methods []string
	params  []map[string]any
}

func (w *threadRecorder) Write(p []byte) (int, error) {
	for _, line := range bytes.Split(bytes.TrimSpace(p), []byte("\n")) {
		var req struct {
			ID     int64          `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.Unmarshal(line, &req); err != nil {
			return 0, err
		}
		w.mu.Lock()
		w.methods = append(w.methods, req.Method)
		w.params = append(w.params, req.Params)
		w.mu.Unlock()
		go w.s.handleResponse(rpcResponseEnvelope{ID: float64(req.ID), Result: json.RawMessage(`{"thread":{"id":"thread-1"}}`)})
	}
	return len(p), nil
}

func (w *threadRecorder) Close() error { return nil }

func runEnsureThread(t *testing.T, mode, profile, resumeID string) (string, map[string]any) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &appServerSession{ctx: ctx, cancel: cancel, mode: mode, permissionProfile: profile}
	rec := &threadRecorder{s: s}
	s.stdin = rec
	if err := s.ensureThread(resumeID); err != nil {
		t.Fatalf("ensureThread: %v", err)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.methods) != 1 {
		t.Fatalf("requests = %v, want exactly one", rec.methods)
	}
	return rec.methods[0], rec.params[0]
}

func TestEnsureThread_PermissionProfileReplacesSandbox(t *testing.T) {
	for _, tc := range []struct{ resumeID, method string }{
		{"", "thread/start"},
		{"thread-old", "thread/resume"},
	} {
		method, params := runEnsureThread(t, "full-auto", "jingyu", tc.resumeID)
		if method != tc.method {
			t.Fatalf("method = %q, want %q", method, tc.method)
		}
		if _, ok := params["sandbox"]; ok {
			t.Fatalf("%s: sandbox = %v, want absent when permission_profile is set", method, params["sandbox"])
		}
		cfg, _ := params["config"].(map[string]any)
		if cfg["default_permissions"] != "jingyu" {
			t.Fatalf("%s: config = %v, want default_permissions=jingyu", method, params["config"])
		}
		if params["approvalPolicy"] != "never" {
			t.Fatalf("%s: approvalPolicy = %v, want never (unchanged)", method, params["approvalPolicy"])
		}
	}
}

func TestEnsureThread_NoPermissionProfileKeepsSandbox(t *testing.T) {
	for _, tc := range []struct{ resumeID, method string }{
		{"", "thread/start"},
		{"thread-old", "thread/resume"},
	} {
		method, params := runEnsureThread(t, "full-auto", "", tc.resumeID)
		if method != tc.method {
			t.Fatalf("method = %q, want %q", method, tc.method)
		}
		if params["sandbox"] != "workspace-write" {
			t.Fatalf("%s: sandbox = %v, want workspace-write", method, params["sandbox"])
		}
		if _, ok := params["config"]; ok {
			t.Fatalf("%s: config = %v, want absent", method, params["config"])
		}
		if params["approvalPolicy"] != "never" {
			t.Fatalf("%s: approvalPolicy = %v, want never", method, params["approvalPolicy"])
		}
	}
}

func TestNew_ParsesPermissionProfile(t *testing.T) {
	ag, err := New(map[string]any{"backend": "app_server", "permission_profile": " jingyu "})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a := ag.(*Agent)
	if a.permissionProfile != "jingyu" {
		t.Fatalf("permissionProfile = %q, want jingyu", a.permissionProfile)
	}
	if got := a.WorkspaceAgentOptions()["permission_profile"]; got != "jingyu" {
		t.Fatalf("WorkspaceAgentOptions permission_profile = %v, want jingyu", got)
	}
}
