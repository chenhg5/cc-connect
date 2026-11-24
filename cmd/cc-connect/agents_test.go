package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestParseAgentsArgs(t *testing.T) {
	opts, err := parseAgentsArgs([]string{"list", "--project", "backend", "--json"})
	if err != nil {
		t.Fatalf("parseAgentsArgs() error = %v", err)
	}
	if !opts.list || opts.project != "backend" || !opts.jsonOutput {
		t.Fatalf("parseAgentsArgs() = %+v, want list/backend/json", opts)
	}
}

func TestMergeAgentSessionsDeduplicatesAndSorts(t *testing.T) {
	older := time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)
	got := mergeAgentSessions([]agentSessionRecord{
		{Project: "first", AgentSessionInfo: core.AgentSessionInfo{ID: "same", Summary: "old", WorkDir: "/old", ModifiedAt: older}},
		{Project: "second", AgentSessionInfo: core.AgentSessionInfo{ID: "same", Summary: "new", WorkDir: "/new", ModifiedAt: newer}},
		{Project: "first", AgentSessionInfo: core.AgentSessionInfo{ID: "other", Summary: "other", WorkDir: "/other", ModifiedAt: older}},
	})
	if len(got) != 2 {
		t.Fatalf("mergeAgentSessions() returned %d records, want 2: %+v", len(got), got)
	}
	if got[0].ID != "same" || got[0].Summary != "new" || got[0].WorkDir != "/new" {
		t.Fatalf("first merged record = %+v, want newest duplicate", got[0])
	}
}

func TestRenderAgentSessionsJSON(t *testing.T) {
	records := []agentSessionRecord{{
		Project: "backend",
		AgentSessionInfo: core.AgentSessionInfo{
			ID:           "session-1",
			Summary:      "fix tests",
			WorkDir:      "/workspace/backend",
			MessageCount: 4,
			ModifiedAt:   time.Date(2026, 11, 13, 19, 0, 0, 0, time.UTC),
		},
	}}
	out, err := renderAgentSessionsJSON(records)
	if err != nil {
		t.Fatalf("renderAgentSessionsJSON() error = %v", err)
	}
	var decoded []map[string]any
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("rendered output is not JSON: %v\n%s", err, out)
	}
	if decoded[0]["id"] != "session-1" || decoded[0]["work_dir"] != "/workspace/backend" {
		t.Fatalf("decoded JSON = %#v, want stable session fields", decoded[0])
	}
}

func TestRenderAgentSessionsTableIncludesWorkDir(t *testing.T) {
	records := []agentSessionRecord{{AgentSessionInfo: core.AgentSessionInfo{
		ID: "session-1", Summary: "fix tests", WorkDir: "/workspace/backend",
		MessageCount: 4, ModifiedAt: time.Date(2026, 11, 13, 19, 0, 0, 0, time.UTC),
	}}}
	out := renderAgentSessionsTable(records)
	for _, want := range []string{"session-1", "fix tests", "/workspace/backend", "Messages", "Last Activity"} {
		if !strings.Contains(out, want) {
			t.Fatalf("table output missing %q:\n%s", want, out)
		}
	}
}
