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

func resumeWorkDirRecord(t *testing.T, kind string, payload any) string {
	t.Helper()
	data, err := json.Marshal(map[string]any{"type": kind, "payload": payload})
	if err != nil {
		t.Fatal(err)
	}
	return string(data) + "\n"
}

func writeResumeWorkDirRollout(t *testing.T, home, name, content string) string {
	t.Helper()
	dir := filepath.Join(home, "sessions", "2026", "10", "26")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCodexResumeWorkDir_SavedDirectoryAndLatestTurnContext(t *testing.T) {
	for _, latestTurn := range []bool{false, true} {
		name := "metadata"
		if latestTurn {
			name = "latest_turn"
		}
		t.Run(name, func(t *testing.T) {
			home, original, first, latest := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
			body := resumeWorkDirRecord(t, "session_meta", map[string]any{"id": "thread-a", "cwd": original})
			want := original
			if latestTurn {
				body += resumeWorkDirRecord(t, "turn_context", map[string]any{"cwd": first})
				body += resumeWorkDirRecord(t, "turn_context", map[string]any{"cwd": latest})
				body += resumeWorkDirRecord(t, "turn_context", map[string]any{"model": "test-model"})
				want = latest
			}
			path := writeResumeWorkDirRollout(t, home, "rollout-thread-a.jsonl", body)
			got, err := codexResumeWorkDir("thread-a", home)
			if err != nil || got != want {
				t.Fatalf("codexResumeWorkDir() = %q, %v; want %q", got, err, want)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != body {
				t.Fatalf("rollout modified: content = %q, error = %v", data, err)
			}
		})
	}
}

func TestCodexResumeWorkDir_ExplicitProfileDoesNotUseOtherProfiles(t *testing.T) {
	explicitHome, environmentHome := t.TempDir(), t.TempDir()
	explicitDir, environmentDir := t.TempDir(), t.TempDir()
	writeResumeWorkDirRollout(t, explicitHome, "rollout-shared-id.jsonl",
		resumeWorkDirRecord(t, "session_meta", map[string]any{"id": "shared-id", "cwd": explicitDir}))
	writeResumeWorkDirRollout(t, environmentHome, "rollout-shared-id.jsonl",
		resumeWorkDirRecord(t, "session_meta", map[string]any{"id": "shared-id", "cwd": environmentDir}))
	writeResumeWorkDirRollout(t, environmentHome, "rollout-environment-only.jsonl",
		resumeWorkDirRecord(t, "session_meta", map[string]any{"id": "environment-only", "cwd": environmentDir}))
	t.Setenv("CODEX_HOME", environmentHome)
	got, err := codexResumeWorkDir("shared-id", explicitHome)
	if err != nil || got != explicitDir {
		t.Fatalf("explicit profile = %q, %v; want %q", got, err, explicitDir)
	}
	got, err = codexResumeWorkDir("shared-id", "")
	if err != nil || got != environmentDir {
		t.Fatalf("environment profile = %q, %v; want %q", got, err, environmentDir)
	}
	if _, err := codexResumeWorkDir("environment-only", explicitHome); err == nil {
		t.Fatal("found a thread that belongs only to another profile")
	}
}

func TestCodexResumeWorkDir_PrefersNewestSplitRollout(t *testing.T) {
	home, olderDir, newerDir := t.TempDir(), t.TempDir(), t.TempDir()
	older := writeResumeWorkDirRollout(t, home, "z-rollout-split-id.jsonl",
		resumeWorkDirRecord(t, "session_meta", map[string]any{"id": "split-id", "cwd": olderDir}))
	newer := writeResumeWorkDirRollout(t, home, "a-rollout-split-id-resumed.jsonl",
		resumeWorkDirRecord(t, "session_meta", map[string]any{"id": "split-id", "cwd": newerDir}))
	oldTime := time.Unix(1700000000, 0)
	for path, modified := range map[string]time.Time{older: oldTime, newer: oldTime.Add(time.Minute)} {
		if err := os.Chtimes(path, modified, modified); err != nil {
			t.Fatal(err)
		}
	}
	got, err := codexResumeWorkDir("split-id", home)
	if err != nil || got != newerDir {
		t.Fatalf("latest segment = %q, %v; want %q", got, err, newerDir)
	}
	if err := os.Remove(newerDir); err != nil {
		t.Fatal(err)
	}
	if _, err := codexResumeWorkDir("split-id", home); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("missing latest directory must not fall back to older segment: %v", err)
	}
}

func TestCodexResumeWorkDir_ListSourceRewriteDoesNotSelectOlderSegment(t *testing.T) {
	home, olderDir, newerDir := t.TempDir(), t.TempDir(), t.TempDir()
	writeRecord := func(timestamp, kind string, payload any) string {
		t.Helper()
		data, err := json.Marshal(map[string]any{"timestamp": timestamp, "type": kind, "payload": payload})
		if err != nil {
			t.Fatal(err)
		}
		return string(data) + "\n"
	}
	olderBody := writeRecord("2026-01-01T00:00:00Z", "session_meta", map[string]any{
		"id": "split-id", "cwd": olderDir, "source": "exec", "originator": "codex_exec",
	})
	newerBody := writeRecord("2026-01-01T00:00:00Z", "session_meta", map[string]any{
		"id": "split-id", "cwd": olderDir, "source": "exec", "originator": "codex_exec",
	}) + writeRecord("2026-01-02T00:00:00.123456789Z", "turn_context", map[string]any{"cwd": newerDir})
	older := writeResumeWorkDirRollout(t, home, "a-rollout-split-id.jsonl", olderBody)
	newer := writeResumeWorkDirRollout(t, home, "b-rollout-split-id-resumed.jsonl", newerBody)
	oldTime := time.Unix(1700000000, 0)
	for path, modified := range map[string]time.Time{older: oldTime, newer: oldTime.Add(time.Minute)} {
		if err := os.Chtimes(path, modified, modified); err != nil {
			t.Fatal(err)
		}
	}
	agent := &Agent{workDir: olderDir, codexHome: home, listAllSessions: true}
	if _, err := agent.ListSessions(context.Background()); err != nil {
		t.Fatal(err)
	}
	olderInfo, err := os.Stat(older)
	if err != nil {
		t.Fatal(err)
	}
	newerInfo, err := os.Stat(newer)
	if err != nil {
		t.Fatal(err)
	}
	if !olderInfo.ModTime().After(newerInfo.ModTime()) {
		t.Fatal("fixture did not reproduce /list rewriting the older rollout")
	}
	got, err := codexResumeWorkDir("split-id", home)
	if err != nil || got != newerDir {
		t.Fatalf("directory after /list source rewrite = %q, %v; want %q", got, err, newerDir)
	}
	if err := os.Remove(newerDir); err != nil {
		t.Fatal(err)
	}
	if _, err := codexResumeWorkDir("split-id", home); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("missing logical latest directory must not select old segment: %v", err)
	}
}

func TestCodexResumeWorkDir_EqualRecordedTimesUseStablePathOrder(t *testing.T) {
	home, firstDir, lastDir := t.TempDir(), t.TempDir(), t.TempDir()
	for name, cwd := range map[string]string{"a-rollout-tied.jsonl": firstDir, "z-rollout-tied.jsonl": lastDir} {
		body, err := json.Marshal(map[string]any{
			"timestamp": "2026-01-01T00:00:00Z", "type": "session_meta",
			"payload": map[string]any{"id": "tied", "cwd": cwd},
		})
		if err != nil {
			t.Fatal(err)
		}
		writeResumeWorkDirRollout(t, home, name, string(body)+"\n")
	}
	got, err := codexResumeWorkDir("tied", home)
	if err != nil || got != lastDir {
		t.Fatalf("equal timestamp selection = %q, %v; want %q", got, err, lastDir)
	}
}

func TestCodexResumeWorkDir_ValidatesFirstMetadataIdentity(t *testing.T) {
	home, ownDir, copiedDir := t.TempDir(), t.TempDir(), t.TempDir()
	ownMeta := resumeWorkDirRecord(t, "session_meta", map[string]any{"id": "wanted-id", "cwd": ownDir})
	parentMeta := resumeWorkDirRecord(t, "session_meta", map[string]any{"id": "parent-id", "cwd": copiedDir})
	writeResumeWorkDirRollout(t, home, "rollout-wanted-id.jsonl", ownMeta+parentMeta)
	// A child filename can contain the parent's ID, but copied parent metadata
	// later in that file must not make it a candidate for the parent session.
	child := writeResumeWorkDirRollout(t, home, "rollout-wanted-id-child.jsonl", parentMeta+ownMeta)
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(child, future, future); err != nil {
		t.Fatal(err)
	}
	got, err := codexResumeWorkDir("wanted-id", home)
	if err != nil || got != ownDir {
		t.Fatalf("first metadata identity = %q, %v; want %q", got, err, ownDir)
	}
	if _, err := codexResumeWorkDir("wanted", home); err == nil {
		t.Fatal("accepted an ID prefix as the exact metadata identity")
	}
}

func TestCodexResumeWorkDir_RejectsUnavailableMetadataOrDirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "ordinary-file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, body, wantError string
	}{
		{"missing metadata", resumeWorkDirRecord(t, "response_item", map[string]any{}), "metadata is missing"},
		{"missing cwd", resumeWorkDirRecord(t, "session_meta", map[string]any{"id": "broken"}), "absolute working directory"},
		{"relative cwd", resumeWorkDirRecord(t, "session_meta", map[string]any{"id": "broken", "cwd": "relative/project"}), "absolute working directory"},
		{"deleted cwd", resumeWorkDirRecord(t, "session_meta", map[string]any{"id": "broken", "cwd": filepath.Join(t.TempDir(), "deleted")}), "unavailable"},
		{"file cwd", resumeWorkDirRecord(t, "session_meta", map[string]any{"id": "broken", "cwd": file}), "not a directory"},
		{"corrupt metadata", "{broken json}\n", "decode rollout record"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			writeResumeWorkDirRollout(t, home, "rollout-broken.jsonl", tc.body)
			_, err := codexResumeWorkDir("broken", home)
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("error = %v; want %q", err, tc.wantError)
			}
		})
	}
}
