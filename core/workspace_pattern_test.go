package core

import (
	"path/filepath"
	"strings"
	"testing"
)

func init() {
	RegisterAgent("stub", func(opts map[string]any) (Agent, error) {
		return &stubAgent{}, nil
	})
}

func TestWorkspacePatternResolvesLetterIDFromDispatchLedger(t *testing.T) {
	root := t.TempDir()
	e := NewEngine("dev-pro", &stubAgent{}, nil, filepath.Join(root, "sessions.json"), LangEnglish)
	e.SetDataDir(root)
	e.SetWorkspacePattern(filepath.Join(root, "worktrees", "letter-{{LETTER_ID}}"))

	if err := e.ensureDispatchStore().upsert(DispatchExpectation{
		Letter:          "L-0158",
		To:              "dev-pro",
		TopicID:         "1091",
		TopicSessionKey: "telegram:-1003917051393:1091:7664413698",
		State:           dispatchStateDispatched,
	}); err != nil {
		t.Fatalf("upsert dispatch expectation: %v", err)
	}

	want := filepath.Join(root, "worktrees", "letter-L-0158")
	if got := e.resolveWorkspacePattern("1091", ""); got != want {
		t.Fatalf("resolveWorkspacePattern() = %q, want %q", got, want)
	}
	if got := e.branchNameForWorkspace(want); got != "letter/L-0158" {
		t.Fatalf("branchNameForWorkspace() = %q, want %q", got, "letter/L-0158")
	}
}

func TestWorkspacePatternLetterFallbackUsesTaskBranch(t *testing.T) {
	root := t.TempDir()
	e := NewEngine("dev-pro", &stubAgent{}, nil, filepath.Join(root, "sessions.json"), LangEnglish)
	e.SetDataDir(root)
	e.SetWorkspacePattern(filepath.Join(root, "worktrees", "letter-{{LETTER_ID}}"))

	want := filepath.Join(root, "worktrees", "letter-L-2222")
	if got := e.resolveWorkspacePattern("2222", ""); got != want {
		t.Fatalf("resolveWorkspacePattern() = %q, want %q", got, want)
	}
	if got := e.branchNameForWorkspace(want); got != "letter/L-2222" {
		t.Fatalf("branchNameForWorkspace() = %q, want %q", got, "letter-2222")
	}
}

func TestWorkspacePatternHelpers(t *testing.T) {
	// Test extractThreadID
	if got := extractThreadID("chatID:123"); got != "123" {
		t.Errorf("extractThreadID(chatID:123) = %q, want %q", got, "123")
	}
	if got := extractThreadID("chatID"); got != "" {
		t.Errorf("extractThreadID(chatID) = %q, want %q", got, "")
	}

	// Test extractThreadIDFromSessionKey
	if got := extractThreadIDFromSessionKey("telegram:chatID:123:userID"); got != "123" {
		t.Errorf("extractThreadIDFromSessionKey(telegram:chatID:123:userID) = %q, want %q", got, "123")
	}
	if got := extractThreadIDFromSessionKey("telegram:chatID:userID"); got != "" {
		t.Errorf("extractThreadIDFromSessionKey(telegram:chatID:userID) = %q, want %q", got, "")
	}

	// Test extractThreadIDFromPath
	pattern := `F:\nexus\worktrees\task-{{THREAD_ID}}`
	if got := extractThreadIDFromPath(pattern, `F:\nexus\worktrees\task-123`); got != "123" {
		t.Errorf("extractThreadIDFromPath(F:\\nexus\\worktrees\\task-123) = %q, want %q", got, "123")
	}
}

func TestAppendRehydrationEnvUsesDispatchLetter(t *testing.T) {
	root := t.TempDir()
	seedArchive(t, root)

	dataDir := filepath.Join(root, "data")
	e := NewEngine("dev-pro", &stubAgent{}, nil, filepath.Join(root, "sessions.json"), LangEnglish)
	e.SetDataDir(dataDir)
	e.SetWorkspacePattern(filepath.Join(root, "worktrees", "letter-{{LETTER_ID}}"))

	if err := e.ensureDispatchStore().upsert(DispatchExpectation{
		Letter:          "L-0251",
		Thread:          "rehydration-mechanism",
		To:              "dev-pro",
		TopicID:         "1091",
		TopicSessionKey: "telegram:-1003917051393:1091:7664413698",
		State:           dispatchStateDispatched,
	}); err != nil {
		t.Fatalf("upsert dispatch expectation: %v", err)
	}

	env := e.appendRehydrationEnv(nil, "telegram:-1003917051393:1091:7664413698", "", "", PersonaClassWrite)
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "CC_REHYDRATION_ACTIVE_LETTER=L-0251") {
		t.Fatalf("missing active letter env:\n%s", joined)
	}
	if !strings.Contains(joined, "CC_REHYDRATION_BUDGET=write-heavy") {
		t.Fatalf("missing write budget env:\n%s", joined)
	}
	if !strings.Contains(joined, "Rehydration Digest") || !strings.Contains(joined, "实现方案 B") {
		t.Fatalf("digest did not include active letter context:\n%s", joined)
	}
}

func TestWorkspacePatternRouting(t *testing.T) {
	agent := &stubAgent{}
	p := &stubPlatformEngine{n: "telegram"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	e.SetWorkspacePattern(`F:\nexus\worktrees\task-{{THREAD_ID}}`)

	msg := &Message{
		SessionKey: "telegram:-1003917051393:123:7664413698",
		ChannelKey: "-1003917051393:123",
		Platform:   "telegram",
	}

	_, _, _, effectiveDir, err := e.commandContextWithWorkspace(p, msg)
	if err != nil {
		t.Fatalf("unexpected error in commandContextWithWorkspace: %v", err)
	}

	wantDir := `F:\nexus\worktrees\task-123`
	if effectiveDir != wantDir {
		t.Errorf("effectiveDir = %q, want %q", effectiveDir, wantDir)
	}
}

func TestIsThreadWorktreeBranch(t *testing.T) {
	cases := []struct {
		branch string
		want   bool
	}{
		{"letter-824", true},
		{"letter/L-0158", true},
		{"task-824", true},
		{"feature/foo", false},
	}
	for _, tc := range cases {
		if got := isThreadWorktreeBranch(tc.branch); got != tc.want {
			t.Fatalf("isThreadWorktreeBranch(%q) = %v, want %v", tc.branch, got, tc.want)
		}
	}
}

// Regression test for L-0320: manual dispatch (no ledger entry) should extract
// the letter ID from the message content (e.g. "处理 L-0313") instead of
// fabricating L-<topicID> (e.g. L-2793).
func TestResolveWorkspacePattern_ManualDispatchUsesMessageHint(t *testing.T) {
	root := t.TempDir()
	e := NewEngine("dev-swift", &stubAgent{}, nil, filepath.Join(root, "sessions.json"), LangEnglish)
	e.SetDataDir(root)
	e.SetWorkspacePattern(filepath.Join(root, "worktrees", "letter-{{LETTER_ID}}"))

	// No ledger entry — simulates manual dispatch (@bot 处理 L-0313)
	// with Telegram topic ID 2793.
	want := filepath.Join(root, "worktrees", "letter-L-0313")
	got := e.resolveWorkspacePattern("2793", "处理 L-0313")
	if got != want {
		t.Fatalf("resolveWorkspacePattern(manual dispatch) = %q, want %q", got, want)
	}
	if branch := e.branchNameForWorkspace(want); branch != "letter/L-0313" {
		t.Fatalf("branchNameForWorkspace() = %q, want %q", branch, "letter/L-0313")
	}

	// Without message hint, falls back to L-<topicID> (existing behavior)
	wantFallback := filepath.Join(root, "worktrees", "letter-L-2793")
	gotFallback := e.resolveWorkspacePattern("2793", "")
	if gotFallback != wantFallback {
		t.Fatalf("resolveWorkspacePattern(no hint) = %q, want %q", gotFallback, wantFallback)
	}
}

func TestWorkspacePatternRouting_DispatchTopicIsolation(t *testing.T) {
	root := t.TempDir()

	// Create a dummy agent that implements GetWorkDir() string
	dummyWorkDir := filepath.Join(root, "my_workdir")
	agent := &dummyAgentWithWorkDir{
		stubAgent: stubAgent{},
		workDir:   dummyWorkDir,
	}

	p := &stubPlatformEngine{n: "telegram"}
	e := NewEngine("reviewer-seat", agent, []Platform{p}, filepath.Join(root, "sessions.json"), LangEnglish)
	e.SetDataDir(root)
	e.SetDispatchTopicIsolation(true)

	// Verify that multiWorkspace is enabled and workspacePool is initialized
	if !e.multiWorkspace {
		t.Fatalf("expected multiWorkspace to be true")
	}
	if e.workspacePool == nil {
		t.Fatalf("expected workspacePool to be initialized")
	}

	// We simulate a message in threadID "2793" with content "处理 L-0323"
	msg := &Message{
		SessionKey: "telegram:-1003917051393:2793:7664413698",
		ChannelKey: "-1003917051393:2793",
		Platform:   "telegram",
		Content:    "处理 L-0323",
	}

	// Make sure the agent type is registered in the pool
	RegisterAgent("reviewer-seat-agent", func(opts map[string]any) (Agent, error) {
		return agent, nil
	})
	// Change dummy name to match
	agent.name = "reviewer-seat-agent"

	// Resolve the command context
	wsAgent, wsSessions, interactiveKey, effectiveDir, err := e.commandContextWithWorkspace(p, msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify that the workspace is L-0323 (derived from message hint)
	if interactiveKey != "L-0323:telegram:-1003917051393:2793:7664413698" {
		t.Errorf("unexpected interactiveKey: %q", interactiveKey)
	}

	// The effective directory should NOT be the virtual workspace "L-0323"
	// but the agent's workdir because "L-0323" is not an absolute path.
	if effectiveDir != dummyWorkDir {
		t.Errorf("effectiveDir = %q, want %q", effectiveDir, dummyWorkDir)
	}

	if wsAgent == nil || wsSessions == nil {
		t.Fatalf("expected non-nil wsAgent and wsSessions")
	}
}

type dummyAgentWithWorkDir struct {
	stubAgent
	workDir string
	name    string
}

func (a *dummyAgentWithWorkDir) Name() string {
	if a.name != "" {
		return a.name
	}
	return "dummy-agent-with-workdir"
}

func (a *dummyAgentWithWorkDir) GetWorkDir() string {
	return a.workDir
}
