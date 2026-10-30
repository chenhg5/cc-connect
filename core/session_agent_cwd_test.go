package core

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSessionAgentCwd covers the AgentCwd accessors and JSON contract used by
// resume validation: set/get, empty-does-not-clobber, and omitempty so old
// records (no agent_cwd) load with an empty value.
func TestSessionAgentCwd(t *testing.T) {
	s := &Session{}
	if got := s.GetAgentCwd(); got != "" {
		t.Fatalf("new session cwd = %q, want empty", got)
	}

	const cwd = "/workspace/.sessions/x/owner-repo"
	s.SetAgentCwd(cwd)
	if got := s.GetAgentCwd(); got != cwd {
		t.Fatalf("GetAgentCwd() = %q, want %q", got, cwd)
	}

	// Empty must not clobber a previously captured cwd.
	s.SetAgentCwd("")
	if got := s.GetAgentCwd(); got != cwd {
		t.Fatalf("empty SetAgentCwd clobbered cwd: got %q", got)
	}

	// Round-trips via the agent_cwd field.
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"agent_cwd":"`+cwd+`"`) {
		t.Fatalf("marshaled session missing agent_cwd: %s", b)
	}

	// omitempty: an empty cwd is not serialized, and a record without the field
	// loads as empty (backward compatible with pre-fix session records).
	empty, err := json.Marshal(&Session{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(empty), "agent_cwd") {
		t.Fatalf("empty session serialized agent_cwd (omitempty broken): %s", empty)
	}
	var loaded Session
	if err := json.Unmarshal([]byte(`{"id":"x"}`), &loaded); err != nil {
		t.Fatal(err)
	}
	if loaded.GetAgentCwd() != "" {
		t.Fatalf("record without agent_cwd loaded cwd = %q, want empty", loaded.GetAgentCwd())
	}
}
