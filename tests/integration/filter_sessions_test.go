//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// skipUnlessBinaryAvailable skips if the agent binary is not in PATH.
// Unlike skipUnlessAgentReady, it does NOT require API keys, since these
// tests only exercise ListSessions (file reading), not StartSession.
func skipUnlessBinaryAvailable(t *testing.T, agentType string) {
	t.Helper()
	bin, err := findAgentBin(agentType)
	if err != nil {
		t.Skipf("skip %s: %v", agentType, err)
	}
	if _, err := exec.LookPath(bin); err != nil {
		t.Skipf("skip %s: binary %q not in PATH", agentType, bin)
	}
}

// writeCodexSessionFixture creates a realistic Codex JSONL session file.
func writeCodexSessionFixture(t *testing.T, sessionsDir, threadID, workDir, userPrompt string) {
	t.Helper()
	dir := filepath.Join(sessionsDir, threadID[:8])
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	meta := map[string]any{
		"type":      "session_meta",
		"timestamp": time.Now().Format(time.RFC3339Nano),
		"payload": map[string]any{
			"id":         threadID,
			"cwd":        workDir,
			"source":     "cli",
			"originator": "codex_cli_rs",
		},
	}
	userMsg := map[string]any{
		"type":      "response_item",
		"timestamp": time.Now().Format(time.RFC3339Nano),
		"payload": map[string]any{
			"role": "user",
			"content": []map[string]any{
				{"type": "input_text", "text": userPrompt},
			},
		},
	}
	assistantMsg := map[string]any{
		"type":      "response_item",
		"timestamp": time.Now().Format(time.RFC3339Nano),
		"payload": map[string]any{
			"role": "assistant",
			"content": []map[string]any{
				{"type": "output_text", "text": "Done."},
			},
		},
	}

	f, err := os.Create(filepath.Join(dir, threadID+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	for _, entry := range []any{meta, userMsg, assistantMsg} {
		if err := enc.Encode(entry); err != nil {
			t.Fatal(err)
		}
	}
}

// writeClaudeCodeSessionFixture creates a realistic Claude Code JSONL session file.
func writeClaudeCodeSessionFixture(t *testing.T, projectDir, sessionID, userPrompt string) {
	t.Helper()
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	userEntry := map[string]any{
		"type":      "user",
		"timestamp": time.Now().Format(time.RFC3339Nano),
		"message":   map[string]any{"content": userPrompt},
	}
	assistantEntry := map[string]any{
		"type":      "assistant",
		"timestamp": time.Now().Format(time.RFC3339Nano),
		"message":   map[string]any{"content": "OK, done."},
	}

	f, err := os.Create(filepath.Join(projectDir, sessionID+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	for _, entry := range []any{userEntry, assistantEntry} {
		if err := enc.Encode(entry); err != nil {
			t.Fatal(err)
		}
	}
}

// setupFilterSessionTest creates a real agent with fixture session files and
// wires it into a real Engine. Some sessions are tracked by cc-connect (via
// SessionManager), others are "external" (exist on disk but not tracked).
// This tests the full pipeline: real agent adapter → ListSessions → Engine filtering.
func setupFilterSessionTest(t *testing.T, agentType string, filterEnabled bool) (
	engine *core.Engine, platform *mockPlatform, userKey string, trackedIDs, externalIDs []string,
) {
	t.Helper()

	workDir := t.TempDir()
	sessPath := filepath.Join(workDir, "cc-sessions.json")

	trackedIDs = []string{
		"019d0001-aaaa-7000-8000-000000000001",
		"019d0002-bbbb-7000-8000-000000000002",
		"019d0003-cccc-7000-8000-000000000003",
	}
	externalIDs = []string{
		"019dff01-dddd-7000-8000-000000000011",
		"019dff02-eeee-7000-8000-000000000012",
	}

	opts := map[string]any{"work_dir": workDir}
	allIDs := append(append([]string{}, trackedIDs...), externalIDs...)

	switch agentType {
	case "codex":
		codexHome := filepath.Join(workDir, ".codex-test")
		opts["codex_home"] = codexHome
		sessionsDir := filepath.Join(codexHome, "sessions")
		for i, id := range allIDs {
			writeCodexSessionFixture(t, sessionsDir, id, workDir, fmt.Sprintf("Prompt for session %d", i+1))
			time.Sleep(10 * time.Millisecond) // ensure different mod times
		}

	case "claudecode":
		homeDir, _ := os.UserHomeDir()
		absWorkDir, _ := filepath.Abs(workDir)
		projectKey := strings.ReplaceAll(absWorkDir, string(filepath.Separator), "-")
		projectDir := filepath.Join(homeDir, ".claude", "projects", projectKey)
		t.Cleanup(func() { os.RemoveAll(projectDir) })
		for i, id := range allIDs {
			writeClaudeCodeSessionFixture(t, projectDir, id, fmt.Sprintf("Prompt for session %d", i+1))
			time.Sleep(10 * time.Millisecond)
		}
	}

	agent, err := core.CreateAgent(agentType, opts)
	if err != nil {
		t.Skipf("skip: cannot create %s agent: %v", agentType, err)
	}

	listed, err := agent.ListSessions(nil)
	if err != nil {
		t.Fatalf("agent.ListSessions failed: %v", err)
	}
	if len(listed) < len(allIDs) {
		t.Fatalf("agent.ListSessions returned %d sessions, want >= %d (fixture broken)", len(listed), len(allIDs))
	}

	mp := &mockPlatform{}
	e := core.NewEngine("test", agent, []core.Platform{mp}, sessPath, core.LangEnglish)
	e.SetFilterExternalSessions(filterEnabled)

	userKey = "mock:test-filter:user1"
	for _, id := range trackedIDs {
		s := e.GetSessions().NewSession(userKey, "")
		s.SetAgentSessionID(id, agentType)
	}
	e.GetSessions().Save()

	return e, mp, userKey, trackedIDs, externalIDs
}

// ---------------------------------------------------------------------------
// Codex: real agent adapter + Engine filter integration
// ---------------------------------------------------------------------------

func TestRealCodex_FilterDisabled_ListShowsAll(t *testing.T) {
	skipUnlessBinaryAvailable(t, "codex")
	e, mp, userKey, _, _ := setupFilterSessionTest(t, "codex", false)
	defer e.Stop()

	mp.clear()
	e.ReceiveMessage(mp, &core.Message{
		SessionKey: userKey, Platform: "mock", UserID: "user1", Content: "/list", ReplyCtx: "ctx",
	})

	msgs, ok := waitForMessages(mp, 1, 5*time.Second)
	if !ok {
		t.Fatal("timeout waiting for /list reply")
	}
	reply := joinMsgContent(msgs)

	// All 5 sessions (3 tracked + 2 external) should be visible
	count := strings.Count(reply, "msgs")
	if count != 5 {
		t.Errorf("filter OFF: /list should show 5 sessions, got %d\n%s", count, reply)
	}
}

func TestRealCodex_FilterEnabled_ListHidesExternal(t *testing.T) {
	skipUnlessBinaryAvailable(t, "codex")
	e, mp, userKey, _, _ := setupFilterSessionTest(t, "codex", true)
	defer e.Stop()

	mp.clear()
	e.ReceiveMessage(mp, &core.Message{
		SessionKey: userKey, Platform: "mock", UserID: "user1", Content: "/list", ReplyCtx: "ctx",
	})

	msgs, ok := waitForMessages(mp, 1, 5*time.Second)
	if !ok {
		t.Fatal("timeout waiting for /list reply")
	}
	reply := joinMsgContent(msgs)

	// Only 3 tracked sessions should be visible
	count := strings.Count(reply, "msgs")
	if count != 3 {
		t.Errorf("filter ON: /list should show 3 tracked sessions, got %d\n%s", count, reply)
	}
	// External sessions (session 4, session 5) should not appear
	if strings.Contains(reply, "session 4") || strings.Contains(reply, "session 5") {
		t.Errorf("filter ON: /list should NOT show external sessions\n%s", reply)
	}
}

func TestRealCodex_FilterEnabled_SwitchExternal_Rejected(t *testing.T) {
	skipUnlessBinaryAvailable(t, "codex")
	e, mp, userKey, _, externalIDs := setupFilterSessionTest(t, "codex", true)
	defer e.Stop()

	mp.clear()
	e.ReceiveMessage(mp, &core.Message{
		SessionKey: userKey, Platform: "mock", UserID: "user1",
		Content: "/switch " + externalIDs[0][:8], ReplyCtx: "ctx",
	})

	msgs, ok := waitForMessages(mp, 1, 5*time.Second)
	if !ok {
		t.Fatal("timeout waiting for /switch reply")
	}
	reply := joinMsgContent(msgs)
	if strings.Contains(reply, externalIDs[0]) && !strings.Contains(strings.ToLower(reply), "no") {
		t.Errorf("filter ON: /switch to external session should fail:\n%s", reply)
	}
}

func TestRealCodex_FilterDisabled_SwitchExternal_Allowed(t *testing.T) {
	skipUnlessBinaryAvailable(t, "codex")
	e, mp, userKey, _, externalIDs := setupFilterSessionTest(t, "codex", false)
	defer e.Stop()

	mp.clear()
	e.ReceiveMessage(mp, &core.Message{
		SessionKey: userKey, Platform: "mock", UserID: "user1",
		Content: "/switch " + externalIDs[0][:8], ReplyCtx: "ctx",
	})

	msgs, ok := waitForMessages(mp, 1, 5*time.Second)
	if !ok {
		t.Fatal("timeout waiting for /switch reply")
	}
	reply := joinMsgContent(msgs)
	if strings.Contains(strings.ToLower(reply), "no match") || strings.Contains(strings.ToLower(reply), "not found") {
		t.Errorf("filter OFF: /switch to external session should succeed:\n%s", reply)
	}
}

func TestRealCodex_FilterEnabled_DeleteExternal_Rejected(t *testing.T) {
	skipUnlessBinaryAvailable(t, "codex")
	e, mp, userKey, _, externalIDs := setupFilterSessionTest(t, "codex", true)
	defer e.Stop()

	mp.clear()
	e.ReceiveMessage(mp, &core.Message{
		SessionKey: userKey, Platform: "mock", UserID: "user1",
		Content: "/delete " + externalIDs[0][:8], ReplyCtx: "ctx",
	})

	msgs, ok := waitForMessages(mp, 1, 5*time.Second)
	if !ok {
		t.Fatal("timeout waiting for /delete reply")
	}
	reply := joinMsgContent(msgs)
	lowerReply := strings.ToLower(reply)
	// The delete should be rejected — either "no session matching" or "not found"
	if !strings.Contains(lowerReply, "no session") && !strings.Contains(lowerReply, "not found") && !strings.Contains(lowerReply, "no match") {
		t.Errorf("filter ON: /delete external session should be rejected, got:\n%s", reply)
	}
}

// ---------------------------------------------------------------------------
// Claude Code: real agent adapter + Engine filter integration
// ---------------------------------------------------------------------------

func TestRealClaudeCode_FilterDisabled_ListShowsAll(t *testing.T) {
	skipUnlessBinaryAvailable(t, "claudecode")
	e, mp, userKey, _, _ := setupFilterSessionTest(t, "claudecode", false)
	defer e.Stop()

	mp.clear()
	e.ReceiveMessage(mp, &core.Message{
		SessionKey: userKey, Platform: "mock", UserID: "user1", Content: "/list", ReplyCtx: "ctx",
	})

	msgs, ok := waitForMessages(mp, 1, 5*time.Second)
	if !ok {
		t.Fatal("timeout waiting for /list reply")
	}
	reply := joinMsgContent(msgs)

	count := strings.Count(reply, "msgs")
	if count != 5 {
		t.Errorf("filter OFF: /list should show 5 sessions, got %d\n%s", count, reply)
	}
}

func TestRealClaudeCode_FilterEnabled_ListHidesExternal(t *testing.T) {
	skipUnlessBinaryAvailable(t, "claudecode")
	e, mp, userKey, _, _ := setupFilterSessionTest(t, "claudecode", true)
	defer e.Stop()

	mp.clear()
	e.ReceiveMessage(mp, &core.Message{
		SessionKey: userKey, Platform: "mock", UserID: "user1", Content: "/list", ReplyCtx: "ctx",
	})

	msgs, ok := waitForMessages(mp, 1, 5*time.Second)
	if !ok {
		t.Fatal("timeout waiting for /list reply")
	}
	reply := joinMsgContent(msgs)

	count := strings.Count(reply, "msgs")
	if count != 3 {
		t.Errorf("filter ON: /list should show 3 tracked sessions, got %d\n%s", count, reply)
	}
	if strings.Contains(reply, "session 4") || strings.Contains(reply, "session 5") {
		t.Errorf("filter ON: /list should NOT show external sessions\n%s", reply)
	}
}

func TestRealClaudeCode_FilterEnabled_SwitchExternal_Rejected(t *testing.T) {
	skipUnlessBinaryAvailable(t, "claudecode")
	e, mp, userKey, _, externalIDs := setupFilterSessionTest(t, "claudecode", true)
	defer e.Stop()

	mp.clear()
	e.ReceiveMessage(mp, &core.Message{
		SessionKey: userKey, Platform: "mock", UserID: "user1",
		Content: "/switch " + externalIDs[0][:8], ReplyCtx: "ctx",
	})

	msgs, ok := waitForMessages(mp, 1, 5*time.Second)
	if !ok {
		t.Fatal("timeout waiting for /switch reply")
	}
	reply := joinMsgContent(msgs)
	if strings.Contains(reply, externalIDs[0]) && !strings.Contains(strings.ToLower(reply), "no") {
		t.Errorf("filter ON: /switch to external session should fail:\n%s", reply)
	}
}

// ---------------------------------------------------------------------------
// Dynamic toggle: switch filter at runtime
// ---------------------------------------------------------------------------

func TestRealCodex_DynamicFilterToggle(t *testing.T) {
	skipUnlessBinaryAvailable(t, "codex")
	e, mp, userKey, _, _ := setupFilterSessionTest(t, "codex", false)
	defer e.Stop()
	msg := &core.Message{SessionKey: userKey, Platform: "mock", UserID: "user1", Content: "/list", ReplyCtx: "ctx"}

	// Phase 1: filter OFF → 5 sessions
	mp.clear()
	e.ReceiveMessage(mp, msg)
	msgs, _ := waitForMessages(mp, 1, 5*time.Second)
	reply1 := joinMsgContent(msgs)
	count1 := strings.Count(reply1, "msgs")
	if count1 != 5 {
		t.Fatalf("before toggle: expected 5 sessions, got %d\n%s", count1, reply1)
	}

	// Phase 2: filter ON → 3 sessions
	e.SetFilterExternalSessions(true)
	mp.clear()
	e.ReceiveMessage(mp, msg)
	msgs, _ = waitForMessages(mp, 1, 5*time.Second)
	reply2 := joinMsgContent(msgs)
	count2 := strings.Count(reply2, "msgs")
	if count2 != 3 {
		t.Fatalf("after enabling filter: expected 3 sessions, got %d\n%s", count2, reply2)
	}

	// Phase 3: filter OFF → 5 sessions again
	e.SetFilterExternalSessions(false)
	mp.clear()
	e.ReceiveMessage(mp, msg)
	msgs, _ = waitForMessages(mp, 1, 5*time.Second)
	reply3 := joinMsgContent(msgs)
	count3 := strings.Count(reply3, "msgs")
	if count3 != 5 {
		t.Fatalf("after disabling filter: expected 5 sessions, got %d\n%s", count3, reply3)
	}
}

// Full E2E tests (real agent conversations, /list /new /switch /delete etc.)
// have been moved to e2e_session_test.go which uses config.test.toml for
// full isolation from production config.
