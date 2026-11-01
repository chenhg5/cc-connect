package droid

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestParseTokenUsageSettings(t *testing.T) {
	data := []byte(`{
  "model": "custom:gpt",
  "tokenUsage": {
    "inputTokens": 100,
    "outputTokens": 20,
    "cacheCreationTokens": 3,
    "cacheReadTokens": 40,
    "thinkingTokens": 8
  }
}`)
	model, summary := parseTokenUsageSettings(data)
	if model != "custom:gpt" {
		t.Fatalf("model = %q, want custom:gpt", model)
	}
	want := "input 100, output 20, thinking 8, cache_read 40, cache_create 3"
	if summary != want {
		t.Fatalf("summary = %q, want %q", summary, want)
	}
}

func TestDroidUsageProcess(t *testing.T) {
	if os.Getenv("CC_CONNECT_DROID_USAGE_HELPER") != "1" {
		return
	}
	marker := os.Getenv("CC_CONNECT_DROID_USAGE_MARKER")
	if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := os.WriteFile(marker, []byte("usage started a conversation"), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	fmt.Println(`{"type":"result","content":"Input tokens: 999"}`)
	os.Exit(0)
}

func newUsageTestAgent(t *testing.T, home, workDir string) *Agent {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	agent, err := New(map[string]any{
		"cmd":      []string{binary, "-test.run=^TestDroidUsageProcess$", "--"},
		"work_dir": workDir,
		"env": map[string]any{
			"HOME": home, "USERPROFILE": home,
			"CC_CONNECT_DROID_USAGE_HELPER": "1",
			"CC_CONNECT_DROID_USAGE_MARKER": filepath.Join(home, ".factory", "usage-probe.marker"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return agent.(*Agent)
}

func writeUsageTestSession(t *testing.T, home, workDir, id string, input int) string {
	t.Helper()
	sessionDir := filepath.Join(home, ".factory", "sessions", droidSessionDirKeys(workDir)[0])
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	header, err := json.Marshal(map[string]string{"type": "session_start", "id": id, "cwd": workDir})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessionDir, id+".jsonl")
	if err := os.WriteFile(path, append(header, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	settings := fmt.Sprintf(`{"model":"custom:isolated","tokenUsage":{"inputTokens":%d,"outputTokens":20,"thinkingTokens":8,"cacheReadTokens":40,"cacheCreationTokens":3,"factoryCredits":123},"apiKey":"secret@example.com"}`, input)
	if err := os.WriteFile(strings.TrimSuffix(path, ".jsonl")+".settings.json", []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func resumeUsageTestSession(t *testing.T, a *Agent, id string) core.AgentSession {
	t.Helper()
	session, err := a.StartSession(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func requireUsageTestInput(t *testing.T, reporter core.UsageReporter, input int) {
	t.Helper()
	report, err := reporter.GetUsage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("custom:isolated | input %d, output 20, thinking 8, cache_read 40, cache_create 3", input)
	if report.Plan != want {
		t.Fatalf("usage plan = %q, want %q", report.Plan, want)
	}
	if report.Email != "" || len(report.Buckets) != 0 {
		t.Fatalf("session token usage must not invent account identity or quotas: %#v", report)
	}
}

func TestGetUsageUsesResumedConversationNotNewerProbe(t *testing.T) {
	home, workDir := t.TempDir(), t.TempDir()
	a := newUsageTestAgent(t, home, workDir)
	realPath := writeUsageTestSession(t, home, workDir, "real-conversation", 100)
	probePath := writeUsageTestSession(t, home, workDir, "old-status-probe", 999)
	probe, err := os.OpenFile(probePath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := probe.WriteString("{\"type\":\"message\",\"message\":{\"role\":\"user\",\"content\":\"/status\"}}\n"); err != nil {
		t.Fatal(err)
	}
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(realPath, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(probePath, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	resumeUsageTestSession(t, a, "real-conversation")
	requireUsageTestInput(t, a, 100)
}

func TestGetUsageUnavailableDoesNotStartCLI(t *testing.T) {
	for _, resumeID := range []string{"", "missing-settings"} {
		t.Run(resumeID, func(t *testing.T) {
			home, workDir := t.TempDir(), t.TempDir()
			a := newUsageTestAgent(t, home, workDir)
			if resumeID != "" {
				resumeUsageTestSession(t, a, resumeID)
			}
			for range 2 {
				report, usageErr := a.GetUsage(context.Background())
				if _, err := os.Stat(filepath.Join(home, ".factory", "usage-probe.marker")); !os.IsNotExist(err) {
					t.Fatalf("usage lookup must not invoke CLI: %v", err)
				}
				if usageErr == nil || report != nil {
					t.Fatalf("missing settings returned report=%#v, error=%v", report, usageErr)
				}
			}
			if _, err := os.Stat(filepath.Join(home, ".factory")); !os.IsNotExist(err) {
				t.Fatalf("usage lookup must not create Factory files: %v", err)
			}
			entries, err := os.ReadDir(workDir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("usage lookup changed workspace: entries=%v, error=%v", entries, err)
			}
		})
	}
}

func TestGetUsageMissingSettingsDoesNotStartCLI(t *testing.T) {
	home, workDir := t.TempDir(), t.TempDir()
	a := newUsageTestAgent(t, home, workDir)
	path := writeUsageTestSession(t, home, workDir, "conversation", 100)
	settingsPath := strings.TrimSuffix(path, ".jsonl") + ".settings.json"
	if err := os.Remove(settingsPath); err != nil {
		t.Fatal(err)
	}
	resumeUsageTestSession(t, a, "conversation")
	if report, err := a.GetUsage(context.Background()); err == nil || report != nil {
		t.Fatalf("missing settings returned report=%#v, error=%v", report, err)
	}
	if _, err := os.Stat(settingsPath); !os.IsNotExist(err) {
		t.Fatalf("usage lookup must not create settings: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".factory", "usage-probe.marker")); !os.IsNotExist(err) {
		t.Fatalf("usage lookup must not invoke CLI: %v", err)
	}
}

func TestGetUsageDoesNotGuessSessionAfterRestart(t *testing.T) {
	home, workDir := t.TempDir(), t.TempDir()
	writeUsageTestSession(t, home, workDir, "historical", 100)
	a := newUsageTestAgent(t, home, workDir)
	if _, err := a.GetUsage(context.Background()); err == nil {
		t.Fatal("historical session must not be selected before an explicit resume")
	}
	resumeUsageTestSession(t, a, "historical")
	requireUsageTestInput(t, a, 100)
	resumeUsageTestSession(t, a, core.ContinueSession)
	if _, err := a.GetUsage(context.Background()); err == nil {
		t.Fatal("continue sentinel must not guess a persisted conversation")
	}
}

func TestGetUsageIsolatesWorkspaceAndHome(t *testing.T) {
	home, otherHome := t.TempDir(), t.TempDir()
	workDir, otherWorkDir := t.TempDir(), t.TempDir()
	writeUsageTestSession(t, home, workDir, "conversation", 100)
	writeUsageTestSession(t, home, otherWorkDir, "conversation", 200)
	writeUsageTestSession(t, otherHome, workDir, "conversation", 300)
	a := newUsageTestAgent(t, home, workDir)
	original := resumeUsageTestSession(t, a, "conversation")
	requireUsageTestInput(t, a, 100)
	originalReporter, ok := original.(core.UsageReporter)
	if !ok {
		t.Fatal("session must provide conversation-specific usage")
	}
	a.SetWorkDir(otherWorkDir)
	if _, err := a.GetUsage(context.Background()); err == nil {
		t.Fatal("changing workspace must clear session ownership")
	}
	resumeUsageTestSession(t, a, "conversation")
	requireUsageTestInput(t, a, 200)
	a.SetWorkDir(workDir)
	resumeUsageTestSession(t, a, "conversation")
	a.SetSessionEnv([]string{"HOME=" + otherHome, "USERPROFILE=" + otherHome})
	if _, err := a.GetUsage(context.Background()); err == nil {
		t.Fatal("changing home must clear session ownership")
	}
	resumeUsageTestSession(t, a, "conversation")
	requireUsageTestInput(t, a, 300)
	requireUsageTestInput(t, originalReporter, 100)
}

func TestGetUsageLearnsStreamIdentityAndRejectsOldSessionCallbacks(t *testing.T) {
	home, workDir := t.TempDir(), t.TempDir()
	writeUsageTestSession(t, home, workDir, "session-one", 100)
	writeUsageTestSession(t, home, workDir, "new-conversation", 200)
	a := newUsageTestAgent(t, home, workDir)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	a.cmd = binary
	a.cliExtraArgs = []string{"-test.run=^TestDroidSessionProcess$", "--"}
	a.SetSessionEnv([]string{"CC_CONNECT_DROID_HELPER=1", "CC_CONNECT_DROID_SCENARIO=stream"})
	oldSession := resumeUsageTestSession(t, a, "")
	if err := oldSession.Send("hello", "message-one", nil, nil); err != nil {
		t.Fatal(err)
	}
	if event := droidTerminalEvent(t, oldSession); event.Type != core.EventResult {
		t.Fatalf("terminal event = %#v", event)
	}
	requireUsageTestInput(t, a, 100)
	oldReporter, ok := oldSession.(core.UsageReporter)
	if !ok {
		t.Fatal("session must provide conversation-specific usage")
	}
	resumeUsageTestSession(t, a, "new-conversation")
	if err := oldSession.Send("another message", "message-two", nil, nil); err != nil {
		t.Fatal(err)
	}
	if event := droidTerminalEvent(t, oldSession); event.Type != core.EventResult {
		t.Fatalf("terminal event = %#v", event)
	}
	requireUsageTestInput(t, a, 200)
	requireUsageTestInput(t, oldReporter, 100)
}
