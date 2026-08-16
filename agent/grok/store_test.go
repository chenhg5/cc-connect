package grok

import (
	"bytes"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveGrokHomePrecedence(t *testing.T) {
	fallbackHome := t.TempDir()
	t.Setenv("HOME", fallbackHome)
	t.Setenv("GROK_HOME", filepath.Join(fallbackHome, "from-process"))

	extraHome := filepath.Join(fallbackHome, "from-extra")
	lastHome := filepath.Join(fallbackHome, "from-last-extra")
	got := resolveGrokHome([]string{
		"GROK_HOME=" + extraHome,
		"UNRELATED=1",
		"GROK_HOME=" + lastHome,
	}, fallbackHome)
	if got != lastHome {
		t.Fatalf("resolveGrokHome() = %q, want last injected value %q", got, lastHome)
	}

	if got := resolveGrokHome(nil, fallbackHome); got != filepath.Join(fallbackHome, "from-process") {
		t.Fatalf("resolveGrokHome(nil) = %q, want process value", got)
	}

	t.Setenv("GROK_HOME", "")
	if got := resolveGrokHome(nil, fallbackHome); got != filepath.Join(fallbackHome, ".grok") {
		t.Fatalf("resolveGrokHome(nil) = %q, want default under HOME", got)
	}
}

func TestResolveGrokHomeUsesEffectiveEnvironmentAndWorkDir(t *testing.T) {
	processHome := t.TempDir()
	optionsHome := filepath.Join(t.TempDir(), "options-home")
	workDir := filepath.Join(t.TempDir(), "workspace")
	mustMkdirAll(t, workDir)
	t.Setenv("HOME", processHome)
	t.Setenv("GROK_HOME", filepath.Join(processHome, "process-grok-home"))

	t.Run("empty override clears inherited GROK_HOME", func(t *testing.T) {
		got := resolveGrokHome([]string{
			"HOME=" + optionsHome,
			"GROK_HOME=",
		}, workDir)
		want := filepath.Join(optionsHome, ".grok")
		if got != want {
			t.Fatalf("resolveGrokHome() = %q, want %q", got, want)
		}
	})

	t.Run("relative GROK_HOME is relative to work_dir", func(t *testing.T) {
		got := resolveGrokHome([]string{
			"HOME=" + optionsHome,
			"GROK_HOME=.state/grok",
		}, workDir)
		want := filepath.Join(workDir, ".state", "grok")
		if got != want {
			t.Fatalf("resolveGrokHome() = %q, want %q", got, want)
		}
	})

	t.Run("empty HOME falls back to the system user home", func(t *testing.T) {
		got := resolveGrokHome([]string{
			"HOME=",
			"GROK_HOME=",
		}, workDir)
		want := filepath.Join(processHome, ".grok")
		if got != want {
			t.Fatalf("resolveGrokHome() = %q, want %q", got, want)
		}
	})
}

func TestGrokModelContextWindow(t *testing.T) {
	grokHome := t.TempDir()
	payload := `{"models":{"grok-4.5":{"info":{"context_window":500000}},"grok-4.6":{"info":{"context_window":500000}}}}`
	if err := os.WriteFile(filepath.Join(grokHome, "models_cache.json"), []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := grokModelContextWindow([]string{"GROK_HOME=" + grokHome}, t.TempDir(), "grok-4.5"); got != 500000 {
		t.Fatalf("grokModelContextWindow() = %d, want 500000", got)
	}
	// Headless modelUsage keys use the "-build" suffix; cache uses bare ids.
	if got := grokModelContextWindow([]string{"GROK_HOME=" + grokHome}, t.TempDir(), "grok-4.6-build"); got != 500000 {
		t.Fatalf("grokModelContextWindow(grok-4.6-build) = %d, want 500000", got)
	}
	if got := grokModelContextWindow([]string{"GROK_HOME=" + grokHome}, t.TempDir(), "missing"); got != 0 {
		t.Fatalf("missing model context window = %d, want 0", got)
	}
}

func TestGrokModelReasoningEffortsUsesAdvertisedOptionIDs(t *testing.T) {
	workDir := t.TempDir()
	grokHome := filepath.Join(t.TempDir(), ".grok")
	require.NoError(t, os.MkdirAll(grokHome, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(grokHome, "models_cache.json"), []byte(`{
		"models": {
			"grok-custom": {"info": {"reasoning_efforts": [
				{"id":"deep"}, {"id":"xhigh"}, {"id":"DEEP"}, {"id":"--unsafe"}
			]}}
		}
	}`), 0o600))

	got := grokModelReasoningEfforts([]string{"GROK_HOME=" + grokHome}, workDir, "grok-custom")
	assert.Equal(t, []string{"deep", "xhigh"}, got)
	assert.Equal(t, got, grokModelReasoningEfforts([]string{"GROK_HOME=" + grokHome}, workDir, ""),
		"a single cached model should be the local default fallback")
}

func TestGrokStoreStrictWorkspaceIsolation(t *testing.T) {
	grokHome := filepath.Join(t.TempDir(), "grok-home")
	workspaceA := filepath.Join(t.TempDir(), "workspace-a")
	workspaceB := filepath.Join(t.TempDir(), "workspace-b")
	mustMkdirAll(t, workspaceA)
	mustMkdirAll(t, workspaceB)

	sessionA := writeGrokSummaryFixture(t, grokHome, url.PathEscape(workspaceA), "session-a", workspaceA, "A", "2026-08-03T01:00:00Z")
	writeGrokSummaryFixture(t, grokHome, url.PathEscape(workspaceB), "session-b", workspaceB, "B", "2026-08-03T02:00:00Z")

	sessions, err := listGrokSessions(grokHome, workspaceA)
	if err != nil {
		t.Fatalf("listGrokSessions() error = %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != "session-a" {
		t.Fatalf("listGrokSessions() = %+v, want only session-a", sessions)
	}
	if got := findGrokSessionDir(grokHome, workspaceA, "session-a"); got != sessionA {
		t.Fatalf("findGrokSessionDir(session-a) = %q, want %q", got, sessionA)
	}
	if got := findGrokSessionDir(grokHome, workspaceA, "session-b"); got != "" {
		t.Fatalf("cross-workspace session leaked: %q", got)
	}
}

func TestGrokStoreCanonicalizesMacOSSymlink(t *testing.T) {
	grokHome := filepath.Join(t.TempDir(), "grok-home")
	realWorkspace := filepath.Join(t.TempDir(), "real-workspace")
	mustMkdirAll(t, realWorkspace)
	alias := filepath.Join(t.TempDir(), "workspace-link")
	if err := os.Symlink(realWorkspace, alias); err != nil {
		t.Fatalf("create workspace symlink: %v", err)
	}
	writeGrokSummaryFixture(t, grokHome, url.PathEscape(alias), "session-symlink", alias, "symlink", "2026-08-03T03:00:00Z")

	sessions, err := listGrokSessions(grokHome, realWorkspace)
	if err != nil {
		t.Fatalf("listGrokSessions() error = %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != "session-symlink" {
		t.Fatalf("listGrokSessions() = %+v, want symlink-backed session", sessions)
	}
}

func TestGrokStoreMatchesCaseVariantsOnlyWhenFilesystemDoes(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS default volumes are case-insensitive; other platforms retain native semantics")
	}

	grokHome := filepath.Join(t.TempDir(), "grok-home")
	parent := t.TempDir()
	workspace := filepath.Join(parent, "CaseSensitiveWorkspace")
	mustMkdirAll(t, workspace)
	caseVariant := filepath.Join(parent, "casesensitiveworkspace")
	variantInfo, err := os.Stat(caseVariant)
	if err != nil {
		t.Skip("test volume is case-sensitive")
	}
	workspaceInfo, err := os.Stat(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(workspaceInfo, variantInfo) {
		t.Skip("test volume does not resolve case variants to the same directory")
	}

	writeGrokSummaryFixture(t, grokHome, "case-variant", "session-case", caseVariant, "case", "2026-08-03T03:30:00Z")
	sessions, err := listGrokSessions(grokHome, workspace)
	if err != nil {
		t.Fatalf("listGrokSessions() error = %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != "session-case" {
		t.Fatalf("listGrokSessions() = %+v, want case-variant session", sessions)
	}
}

func TestGrokStoreFindsLongSlugUsingCWDMarker(t *testing.T) {
	grokHome := filepath.Join(t.TempDir(), "grok-home")
	workspace := filepath.Join(t.TempDir(), strings.Repeat("long-workspace-", 12), "nested")
	mustMkdirAll(t, workspace)

	group := strings.Repeat("percent-encoded-prefix-", 8) + "-7eac7d8f"
	groupDir := filepath.Join(grokHome, "sessions", group)
	mustMkdirAll(t, groupDir)
	if err := os.WriteFile(filepath.Join(groupDir, ".cwd"), []byte(workspace+"\n"), 0o600); err != nil {
		t.Fatalf("write .cwd: %v", err)
	}
	wantDir := writeGrokSummaryFixture(t, grokHome, group, "session-long", "", "long cwd", "2026-08-03T04:00:00Z")

	if got := findGrokSessionDir(grokHome, workspace, "session-long"); got != wantDir {
		t.Fatalf("findGrokSessionDir() = %q, want %q", got, wantDir)
	}
}

func TestGetGrokSessionHistoryFiltersMergesLimitsAndReadsLargeLines(t *testing.T) {
	grokHome := filepath.Join(t.TempDir(), "grok-home")
	workspace := filepath.Join(t.TempDir(), "workspace")
	mustMkdirAll(t, workspace)
	sessionDir := writeGrokSummaryFixture(t, grokHome, url.PathEscape(workspace), "session-history", workspace, "history", "2026-08-03T05:00:00Z")

	large := strings.Repeat("大", 70*1024)
	lines := [][]byte{
		marshalGrokUpdate(t, 1785725690, "user_message_chunk", "hello"),
		marshalGrokUpdate(t, 1785725690, "user_message_chunk", " world"),
		marshalGrokUpdate(t, 1785725691, "agent_thought_chunk", "PRIVATE_THOUGHT"),
		marshalGrokUpdate(t, 1785725691, "tool_call", "TOKEN_MUST_NOT_LEAK"),
		marshalGrokUpdate(t, 1785725692, "agent_message_chunk", large),
		marshalGrokUpdate(t, 1785725692, "agent_message_chunk", "tail"),
		[]byte("{partially-written-json"),
		marshalGrokUpdate(t, 1785725693, "user_message_chunk", "last"),
	}
	content := bytes.Join(lines, []byte("\n"))
	content = append(content, '\n')
	if err := os.WriteFile(filepath.Join(sessionDir, "updates.jsonl"), content, 0o600); err != nil {
		t.Fatalf("write updates.jsonl: %v", err)
	}

	all, err := getGrokSessionHistory(grokHome, workspace, "session-history", 0)
	if err != nil {
		t.Fatalf("getGrokSessionHistory() error = %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("history length = %d, want 3: %+v", len(all), all)
	}
	if all[0].Role != "user" || all[0].Content != "hello world" {
		t.Fatalf("merged user entry = %+v", all[0])
	}
	if all[1].Role != "assistant" || all[1].Content != large+"tail" {
		t.Fatalf("large merged assistant entry has role=%q len=%d", all[1].Role, len(all[1].Content))
	}
	if all[1].Timestamp.Unix() != 1785725692 {
		t.Fatalf("assistant timestamp = %v, want unix 1785725692", all[1].Timestamp)
	}
	for _, entry := range all {
		if strings.Contains(entry.Content, "PRIVATE_THOUGHT") || strings.Contains(entry.Content, "TOKEN_MUST_NOT_LEAK") {
			t.Fatalf("private/tool content leaked into history")
		}
	}

	limited, err := getGrokSessionHistory(grokHome, workspace, "session-history", 2)
	if err != nil {
		t.Fatalf("getGrokSessionHistory(limit=2) error = %v", err)
	}
	if len(limited) != 2 || limited[0].Role != "assistant" || limited[1].Content != "last" {
		t.Fatalf("limited history = %+v, want final two merged entries", limited)
	}
}

func writeGrokSummaryFixture(t *testing.T, grokHome, group, sessionID, cwd, summary, updatedAt string) string {
	t.Helper()
	sessionDir := filepath.Join(grokHome, "sessions", group, sessionID)
	mustMkdirAll(t, sessionDir)
	payload := map[string]any{
		"info": map[string]any{
			"id":  sessionID,
			"cwd": cwd,
		},
		"session_summary":   summary,
		"num_chat_messages": 4,
		"updated_at":        updatedAt,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal summary fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "summary.json"), data, 0o600); err != nil {
		t.Fatalf("write summary fixture: %v", err)
	}
	return sessionDir
}

func marshalGrokUpdate(t *testing.T, timestamp int64, updateType, text string) []byte {
	t.Helper()
	payload := map[string]any{
		"timestamp": timestamp,
		"params": map[string]any{
			"update": map[string]any{
				"sessionUpdate": updateType,
				"content": map[string]any{
					"type": "text",
					"text": text,
				},
			},
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal update fixture: %v", err)
	}
	return data
}

func mustMkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
}
