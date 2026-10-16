package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentListSessions_ExcludesSubagentRollouts(t *testing.T) {
	workDir := t.TempDir()
	codexHome := t.TempDir()
	sessionsDir := filepath.Join(codexHome, "sessions", "2026", "08", "03")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("create sessions directory: %v", err)
	}
	workDirJSON, err := json.Marshal(workDir)
	if err != nil {
		t.Fatalf("encode work directory: %v", err)
	}

	writeRollout := func(name, sessionID, source string) {
		t.Helper()
		body := `{"type":"session_meta","payload":{"id":"` + sessionID + `","cwd":` + string(workDirJSON) + `,"source":` + source + `}}` + "\n" +
			`{"type":"response_item","payload":{"role":"user","content":[{"type":"input_text","text":"fix the login bug"}]}}` + "\n"
		if err := os.WriteFile(filepath.Join(sessionsDir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write rollout %s: %v", name, err)
		}
	}

	writeRollout("rollout-top-level.jsonl", "top-level", `"vscode"`)
	writeRollout(
		"rollout-subagent.jsonl",
		"subagent",
		`{"subagent":{"thread_spawn":{"parent_thread_id":"top-level"}}}`,
	)

	agent := &Agent{workDir: workDir, codexHome: codexHome}
	sessions, err := agent.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions() error: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("ListSessions() returned %d sessions, want 1 top-level session", len(sessions))
	}
	if sessions[0].ID != "top-level" {
		t.Fatalf("ListSessions()[0].ID = %q, want %q", sessions[0].ID, "top-level")
	}
}

func TestAgentListSessions_ListAllWorkdirs(t *testing.T) {
	codexHome := t.TempDir()
	workDir := t.TempDir()
	otherWorkDir := t.TempDir()

	writeTestCodexRollout(t, codexHome, "rollout-main.jsonl", "main-session", workDir)
	writeTestCodexRollout(t, codexHome, "rollout-other.jsonl", "other-session", otherWorkDir)

	agent := &Agent{workDir: workDir, codexHome: codexHome, listAllWorkdirs: true}
	sessions, err := agent.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions() error: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("ListSessions() returned %d sessions, want 2", len(sessions))
	}

	got := map[string]bool{}
	for _, session := range sessions {
		got[session.ID] = true
	}
	for _, want := range []string{"main-session", "other-session"} {
		if !got[want] {
			t.Fatalf("ListSessions() missing session %q; got %#v", want, got)
		}
	}
}

func TestListCodexSessions_EmptyWorkDirDoesNotFilterByProcessDir(t *testing.T) {
	codexHome := t.TempDir()
	sessionWorkDir := t.TempDir()
	processWorkDir := t.TempDir()
	writeTestCodexRollout(t, codexHome, "rollout-session.jsonl", "all-workdirs-session", sessionWorkDir)

	originalWorkDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get current working directory: %v", err)
	}
	if err := os.Chdir(processWorkDir); err != nil {
		t.Fatalf("change process working directory: %v", err)
	}
	defer func() {
		if err := os.Chdir(originalWorkDir); err != nil {
			t.Fatalf("restore process working directory: %v", err)
		}
	}()

	sessions, err := listCodexSessions("", codexHome)
	if err != nil {
		t.Fatalf("listCodexSessions() error: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("listCodexSessions() returned %d sessions, want 1", len(sessions))
	}
	if sessions[0].ID != "all-workdirs-session" {
		t.Fatalf("listCodexSessions()[0].ID = %q, want all-workdirs-session", sessions[0].ID)
	}
}

func TestAgentListSessions_ExcludesSubagentRolloutWithCopiedParentMeta(t *testing.T) {
	workDir := t.TempDir()
	codexHome := t.TempDir()
	sessionsDir := filepath.Join(codexHome, "sessions", "2026", "08", "04")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("create sessions directory: %v", err)
	}

	workDirJSON, err := json.Marshal(workDir)
	if err != nil {
		t.Fatalf("encode work directory: %v", err)
	}
	parentMeta := `{"type":"session_meta","payload":{"id":"parent","cwd":` + string(workDirJSON) + `,"source":"vscode"}}`
	parentRollout := parentMeta + "\n" +
		`{"type":"response_item","payload":{"role":"user","content":[{"type":"input_text","text":"top-level prompt"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(sessionsDir, "rollout-parent.jsonl"), []byte(parentRollout), 0o644); err != nil {
		t.Fatalf("write parent rollout: %v", err)
	}

	childMeta := `{"type":"session_meta","payload":{"id":"child","cwd":` + string(workDirJSON) + `,"source":{"subagent":{"thread_spawn":{"parent_thread_id":"parent"}}}}}`
	childRollout := childMeta + "\n" + parentMeta + "\n" +
		`{"type":"response_item","payload":{"role":"user","content":[{"type":"input_text","text":"copied parent prompt"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(sessionsDir, "rollout-child.jsonl"), []byte(childRollout), 0o644); err != nil {
		t.Fatalf("write child rollout: %v", err)
	}

	agent := &Agent{workDir: workDir, codexHome: codexHome}
	sessions, err := agent.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions() error: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("ListSessions() returned %d sessions, want only the parent session", len(sessions))
	}
	if sessions[0].ID != "parent" {
		t.Fatalf("ListSessions()[0].ID = %q, want parent", sessions[0].ID)
	}
}

func TestAgentListSessions_UsesSessionIndexThreadName(t *testing.T) {
	workDir := t.TempDir()
	codexHome := t.TempDir()
	sessionsDir := filepath.Join(codexHome, "sessions", "2026", "08", "04")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("create sessions directory: %v", err)
	}

	workDirJSON, err := json.Marshal(workDir)
	if err != nil {
		t.Fatalf("encode work directory: %v", err)
	}
	const sessionID = "019fc636-3567-76e3-a4d6-b223545f7e71"
	rollout := `{"type":"session_meta","payload":{"id":"` + sessionID + `","cwd":` + string(workDirJSON) + `,"source":"vscode"}}` + "\n" +
		`{"type":"response_item","payload":{"role":"user","content":[{"type":"input_text","text":"这是很长的具体需求正文，不应该覆盖 Codex 生成的会话名称"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(sessionsDir, "rollout-session.jsonl"), []byte(rollout), 0o644); err != nil {
		t.Fatalf("write rollout: %v", err)
	}

	indexEntry := `{"id":"` + sessionID + `","thread_name":"设计简易基础管理模块","updated_at":"2026-08-03T06:01:25Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(codexHome, "session_index.jsonl"), []byte(indexEntry), 0o644); err != nil {
		t.Fatalf("write session index: %v", err)
	}

	agent := &Agent{workDir: workDir, codexHome: codexHome}
	sessions, err := agent.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions() error: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("ListSessions() returned %d sessions, want 1", len(sessions))
	}
	if sessions[0].Summary != "设计简易基础管理模块" {
		t.Fatalf("ListSessions()[0].Summary = %q, want Codex thread name", sessions[0].Summary)
	}
}

func TestAgentListSessions_LongThreadNameTruncated(t *testing.T) {
	workDir := t.TempDir()
	codexHome := t.TempDir()
	sessionsDir := filepath.Join(codexHome, "sessions", "2026", "08", "15")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("create sessions directory: %v", err)
	}

	workDirJSON, err := json.Marshal(workDir)
	if err != nil {
		t.Fatalf("encode work directory: %v", err)
	}
	const sessionID = "019fc636-3567-76e3-a4d6-b223545f7e72"
	rollout := `{"type":"session_meta","payload":{"id":"` + sessionID + `","cwd":` + string(workDirJSON) + `,"source":"vscode"}}` + "\n"
	if err := os.WriteFile(filepath.Join(sessionsDir, "rollout-session.jsonl"), []byte(rollout), 0o644); err != nil {
		t.Fatalf("write rollout: %v", err)
	}

	longTitle := strings.Repeat("会", 61)
	indexEntry := `{"id":"` + sessionID + `","thread_name":"` + longTitle + `","updated_at":"2026-08-15T00:00:00Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(codexHome, "session_index.jsonl"), []byte(indexEntry), 0o644); err != nil {
		t.Fatalf("write session index: %v", err)
	}

	agent := &Agent{workDir: workDir, codexHome: codexHome}
	sessions, err := agent.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions() error: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("ListSessions() returned %d sessions, want 1", len(sessions))
	}
	want := strings.Repeat("会", 60) + "..."
	if sessions[0].Summary != want {
		t.Fatalf("ListSessions()[0].Summary = %q, want %q", sessions[0].Summary, want)
	}
}

func writeTestCodexRollout(t *testing.T, codexHome, name, sessionID, workDir string) {
	t.Helper()

	sessionsDir := filepath.Join(codexHome, "sessions", "2026", "08", "31")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("create sessions directory: %v", err)
	}

	workDirJSON, err := json.Marshal(workDir)
	if err != nil {
		t.Fatalf("encode work directory: %v", err)
	}

	body := `{"type":"session_meta","payload":{"id":"` + sessionID + `","cwd":` + string(workDirJSON) + `,"source":"vscode"}}` + "\n" +
		`{"type":"response_item","payload":{"role":"user","content":[{"type":"input_text","text":"test prompt"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(sessionsDir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write rollout %s: %v", name, err)
	}
}
