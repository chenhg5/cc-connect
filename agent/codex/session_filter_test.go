package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFilterRollout(t *testing.T, home, workDir, id string, prompts ...string) {
	writeFilterRolloutPart(t, home, workDir, id, id, prompts...)
}

func writeFilterRolloutPart(t *testing.T, home, workDir, id, part string, prompts ...string) string {
	t.Helper()
	dir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var lines []string
	meta, err := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{"id": id, "cwd": workDir, "source": "cli"}})
	if err != nil {
		t.Fatal(err)
	}
	lines = append(lines, string(meta))
	for _, prompt := range prompts {
		entry, err := json.Marshal(map[string]any{"type": "response_item", "payload": map[string]any{"role": "user", "content": []map[string]string{{"type": "input_text", "text": prompt}}}})
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(entry))
	}
	path := filepath.Join(dir, "rollout-"+part+"-"+id+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCodexInternalFilterUsesFirstRolloutSegment(t *testing.T) {
	home, workDir := t.TempDir(), t.TempDir()
	old := writeFilterRolloutPart(t, home, workDir, "split-internal", "first", "You are the final diagnosis layer for the task")
	writeFilterRolloutPart(t, home, workDir, "split-internal", "second", "Ordinary follow-up")
	writeFilterRolloutPart(t, home, workDir, "split-normal", "first", "Ordinary first prompt")
	writeFilterRolloutPart(t, home, workDir, "split-normal", "second", "You are the decision layer for a task")
	prior := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, prior, prior); err != nil {
		t.Fatal(err)
	}
	firstNormal := filepath.Join(home, "sessions", "rollout-first-split-normal.jsonl")
	if err := os.Chtimes(firstNormal, prior, prior); err != nil {
		t.Fatal(err)
	}
	agent := &Agent{codexHome: home, workDir: workDir}
	sessions, err := agent.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ID != "split-normal" {
		t.Fatalf("split rollout visibility = %+v; want split-normal only", sessions)
	}
}

func TestCodexInternalSessionFilter(t *testing.T) {
	home, workDir := t.TempDir(), t.TempDir()
	writeFilterRollout(t, home, workDir, "internal", "# AGENTS.md instructions", "You are the final diagnosis layer for an internal system", "Please continue")
	writeFilterRollout(t, home, workDir, "normal", "Please help", "You are the decision layer for an internal system", "Another real request")
	agent := &Agent{codexHome: home, workDir: workDir}
	sessions, err := agent.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ID != "normal" {
		t.Fatalf("visible sessions = %+v; want normal only", sessions)
	}
	history, err := agent.GetSessionHistory(context.Background(), "normal", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range history {
		if strings.HasPrefix(entry.Content, "You are the decision layer") {
			t.Fatalf("internal prompt leaked into history: %+v", history)
		}
	}
}

func TestCodexCustomSessionFilterPrefixes(t *testing.T) {
	home, workDir := t.TempDir(), t.TempDir()
	writeFilterRollout(t, home, workDir, "custom-internal", "Private workflow: sync state")
	writeFilterRollout(t, home, workDir, "ordinary", "Please review Private workflow: sync state")
	a, err := New(map[string]any{
		"cmd": "go", "work_dir": workDir, "codex_home": home,
		"session_filter": map[string]any{"ignore_summary_prefixes": []any{" Private workflow:", 17, " "}},
	})
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := a.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ID != "ordinary" {
		t.Fatalf("custom prefix filtering = %+v; want ordinary only", sessions)
	}
	entries, err := a.(*Agent).GetSessionHistory(context.Background(), "custom-internal", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("custom internal prompt leaked into history: %+v", entries)
	}
}

func TestCodexDefaultInternalPromptPrefixes(t *testing.T) {
	for _, prefix := range defaultInternalPromptPrefixes {
		if isUserPrompt(" \n" + prefix + " for a task") {
			t.Errorf("internal prefix %q was treated as a user prompt", prefix)
		}
	}
	if !isUserPrompt("Please review: You are the final diagnosis layer") {
		t.Fatal("non-prefix mention was filtered")
	}
	if !isUserPrompt("you are the final diagnosis layer for me") {
		t.Fatal("case-mismatched prefix was filtered")
	}
}
