// Channel-scoped session-key tests for multi-workspace mode.
//
// makeSessionKey (platform/feishu/feishu.go:4099) encodes an isolated thread as
// "feishu:<chatID>:root:<rootID>" for both group-chat threads and topics, while
// workspace bindings are keyed "feishu:<chatID>:topic:<rootID>". Rebuilding the
// binding key from the session key alone drops the root segment, so every
// consumer of extractWorkspaceChannelKey resolves a bound topic to the global
// agent: /status reports ".", /list is empty, /new leaks an agent process.
//
// These tests pin both the key derivation and the user-visible consequences.

package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// scopedKeyPlatform is a platform stub that declares its session-key layout the
// way a real platform would, via ChannelScopedSessionKey. Core must not parse
// ":root:" itself, so this is the only way the key can be recovered.
type scopedKeyPlatform struct {
	name          string
	threadScoped  bool
	supportedKeys map[string]string // sessionKey -> channelKey
}

func (p *scopedKeyPlatform) Name() string {
	if p.name != "" {
		return p.name
	}
	return "feishu"
}
func (p *scopedKeyPlatform) Start(MessageHandler) error                     { return nil }
func (p *scopedKeyPlatform) Reply(_ context.Context, _ any, _ string) error { return nil }
func (p *scopedKeyPlatform) Send(_ context.Context, _ any, _ string) error  { return nil }
func (p *scopedKeyPlatform) Stop() error                                    { return nil }

func (p *scopedKeyPlatform) ChannelKeyFromSessionKey(sessionKey string) (string, bool) {
	if !p.threadScoped {
		return "", false
	}
	ck, ok := p.supportedKeys[sessionKey]
	return ck, ok
}

// scopedWorkDirAgent reports the work_dir it was constructed with, so a wrong
// agent selection is observable. A stub without GetWorkDir would fall back to
// os.Getwd() and make both the broken and fixed behaviour render as ".".
type scopedWorkDirAgent struct {
	name string
	// workDir is captured at construction. Reading a package-level variable
	// instead would report whichever agent was built last, since
	// getOrCreateWorkspaceAgent caches instances per workspace and does not
	// re-run the factory (core/engine.go:4109).
	workDir string
	// sessionsByWorkDir mirrors real agents, which list the CLI-side sessions
	// belonging to their own working directory (agent/acp/list_sessions.go).
	sessionsByWorkDir map[string][]AgentSessionInfo
}

func (a *scopedWorkDirAgent) Name() string { return a.name }

func (a *scopedWorkDirAgent) StartSession(_ context.Context, _ string) (AgentSession, error) {
	return &stubAgentSession{}, nil
}

func (a *scopedWorkDirAgent) ListSessions(_ context.Context) ([]AgentSessionInfo, error) {
	return a.sessionsByWorkDir[a.workDir], nil
}

func (a *scopedWorkDirAgent) Stop() error { return nil }

func (a *scopedWorkDirAgent) GetWorkDir() string { return a.workDir }

const (
	topicKeyA = "feishu:oc_chat:root:om_root_a"
	topicKeyB = "feishu:oc_chat:root:om_root_b"
)

func newScopedEngine(t *testing.T) (*Engine, *stubPlatformEngine, *scopedKeyPlatform, string) {
	t.Helper()
	baseDir := t.TempDir()
	sharedRaw := filepath.Join(baseDir, "repo-shared")
	otherRaw := filepath.Join(baseDir, "repo-other")
	for _, dir := range []string{sharedRaw, otherRaw} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Normalize after the directories exist. On macOS /var is a symlink to
	// /private/var; EvalSymlinks fails on a missing path and would leave the
	// unresolved form, which then disagrees with the engine's later resolve.
	shared := normalizeWorkspacePath(sharedRaw)
	other := normalizeWorkspacePath(otherRaw)

	sessionsByWorkDir := map[string][]AgentSessionInfo{
		shared: {
			{ID: "sess-a1", Summary: "a1", MessageCount: 3},
			{ID: "sess-b1", Summary: "b1", MessageCount: 4},
		},
		other: {{ID: "sess-other", Summary: "other", MessageCount: 1}},
	}

	const agentName = "channel-scope-agent"
	RegisterAgent(agentName, func(opts map[string]any) (Agent, error) {
		workDir, _ := opts["work_dir"].(string)
		return &scopedWorkDirAgent{name: agentName, workDir: workDir, sessionsByWorkDir: sessionsByWorkDir}, nil
	})

	p := &scopedKeyPlatform{
		name:         "feishu",
		threadScoped: true,
		supportedKeys: map[string]string{
			topicKeyA: "oc_chat:topic:om_root_a",
			topicKeyB: "oc_chat:topic:om_root_b",
		},
	}
	reply := &stubPlatformEngine{n: "feishu"}
	e := NewEngine(
		"test",
		&scopedWorkDirAgent{name: agentName, sessionsByWorkDir: sessionsByWorkDir},
		[]Platform{p},
		filepath.Join(t.TempDir(), "sessions.json"),
		LangEnglish,
	)
	e.SetMultiWorkspace(baseDir, filepath.Join(t.TempDir(), "bindings.json"))
	e.SetProjectStateStore(NewProjectStateStore(filepath.Join(t.TempDir(), "projects", "test.state.json")))
	return e, reply, p, shared
}

func bindScopedTopic(e *Engine, rootID, workspace string) {
	e.workspaceBindings.Bind(
		"project:test",
		workspaceChannelKey("feishu", "oc_chat:topic:"+rootID),
		"",
		workspace,
	)
}

// A platform that declares its key layout must win over core's generic parsing,
// otherwise every bound topic collapses onto the chat-level key.
func TestExtractWorkspaceChannelKey_PlatformScopedTopic(t *testing.T) {
	e, _, _, _ := newScopedEngine(t)

	gotA := e.extractWorkspaceChannelKey(topicKeyA)
	if want := "feishu:oc_chat:topic:om_root_a"; gotA != want {
		t.Fatalf("channelKey(topic A) = %q, want %q", gotA, want)
	}
	gotB := e.extractWorkspaceChannelKey(topicKeyB)
	if want := "feishu:oc_chat:topic:om_root_b"; gotB != want {
		t.Fatalf("channelKey(topic B) = %q, want %q", gotB, want)
	}
	if gotA == gotB {
		t.Fatalf("two topics resolved to the same key %q; they must stay separate", gotA)
	}
}

// Keys the platform does not claim (short forms, other platforms) must keep
// resolving exactly as before, so non-topic behaviour is untouched.
func TestExtractWorkspaceChannelKey_UnclaimedKeysUnchanged(t *testing.T) {
	e, _, _, _ := newScopedEngine(t)

	for _, tc := range []struct{ name, sessionKey, want string }{
		{"two-segment chat", "feishu:oc_chat", "feishu:oc_chat"},
		{"three-segment per-user", "feishu:oc_chat:ou_user", "feishu:oc_chat"},
		{"unregistered platform", "telegram:oc_chat:root:om_x", "telegram:oc_chat"},
		{"single-char tag platform", "dingtalk:g:cid:uid", "dingtalk:cid"},
	} {
		if got := e.extractWorkspaceChannelKey(tc.sessionKey); got != tc.want {
			t.Errorf("%s: channelKey(%q) = %q, want %q", tc.name, tc.sessionKey, got, tc.want)
		}
	}
}

// Two topics bound to different directories must resolve to different agents.
func TestSessionContextForKey_TwoTopicsIsolated(t *testing.T) {
	e, _, _, shared := newScopedEngine(t)
	other := normalizeWorkspacePath(filepath.Join(filepath.Dir(shared), "repo-other"))
	bindScopedTopic(e, "om_root_a", shared)
	bindScopedTopic(e, "om_root_b", other)

	agentA, _ := e.sessionContextForKey(topicKeyA)
	agentB, _ := e.sessionContextForKey(topicKeyB)

	if agentA == agentB {
		t.Fatal("both topics resolved to the same agent; directory isolation is lost")
	}
	if got := agentA.(interface{ GetWorkDir() string }).GetWorkDir(); got != shared {
		t.Fatalf("topic A workDir = %q, want %q", got, shared)
	}
	if got := agentB.(interface{ GetWorkDir() string }).GetWorkDir(); got != other {
		t.Fatalf("topic B workDir = %q, want %q", got, other)
	}
}

// /dir inside a topic writes a per-interactive-key override rather than a new
// binding, so the resolved agent must follow that override.
func TestSessionContextForKey_AppliesDirOverride(t *testing.T) {
	e, _, _, shared := newScopedEngine(t)
	bindScopedTopic(e, "om_root_a", shared)

	override := normalizeWorkspacePath(filepath.Join(filepath.Dir(shared), "repo-alpha"))
	if err := os.MkdirAll(override, 0o755); err != nil {
		t.Fatal(err)
	}
	interactiveKey := shared + ":" + topicKeyA
	e.projectState.SetWorkspaceDirOverride(interactiveKey, override)
	e.projectState.Save()

	agent, _ := e.sessionContextForKey(topicKeyA)
	got := agent.(interface{ GetWorkDir() string }).GetWorkDir()
	if got != override {
		t.Fatalf("workDir after /dir = %q, want the override %q", got, override)
	}
}

// The interactive state key must carry the workspace prefix, otherwise /new
// cleans up a key that never existed and the old agent process leaks.
func TestInteractiveKeyForSessionKey_CarriesWorkspacePrefix(t *testing.T) {
	e, _, _, shared := newScopedEngine(t)
	bindScopedTopic(e, "om_root_a", shared)

	got := e.interactiveKeyForSessionKey(topicKeyA)
	if want := shared + ":" + topicKeyA; got != want {
		t.Fatalf("interactiveKey = %q, want %q", got, want)
	}
}

// Within one topic: /new creates a second session and /switch returns to the
// first, with history intact (cmdSwitch deliberately never clears history).
func TestSessionCommands_NewAndSwitchRoundTrip(t *testing.T) {
	e, p, _, shared := newScopedEngine(t)
	bindScopedTopic(e, "om_root_a", shared)

	_, sessions := e.sessionContextForKey(topicKeyA)
	if sessions == nil {
		t.Fatal("expected a session manager")
	}

	first := sessions.GetOrCreateActive(topicKeyA)
	first.SetAgentSessionID("sess-a1", "channel-scope-agent")
	first.AddHistory("user", "A1 第一句")
	first.AddHistory("assistant", "A1 回答")
	sessions.Save()

	// /new archives the current session and starts a fresh one.
	sessions.NewSession(topicKeyA, "a2")
	second := sessions.GetOrCreateActive(topicKeyA)
	second.SetAgentSessionID("sess-a2", "channel-scope-agent")
	second.AddHistory("user", "A2 第一句")
	sessions.Save()

	if got := second.GetHistory(10); len(got) == 0 || !strings.Contains(got[0].Content, "A2") {
		t.Fatalf("active session after /new = %q, want A2 content", got)
	}

	// /switch back to A1 must restore the original history, not an empty one.
	switched := sessions.SwitchToAgentSession(topicKeyA, "sess-a1", "channel-scope-agent", "a1")
	if switched == nil {
		t.Fatal("SwitchToAgentSession returned nil")
	}
	back := sessions.GetOrCreateActive(topicKeyA)
	if back.GetAgentSessionID() != "sess-a1" {
		t.Fatalf("after /switch active session = %q, want sess-a1", back.GetAgentSessionID())
	}
	hist := back.GetHistory(10)
	if len(hist) == 0 || !strings.Contains(hist[0].Content, "A1") {
		t.Fatalf("history after /switch round-trip = %v, want A1 content preserved", hist)
	}
	_ = p
}

// Two topics sharing one directory must not see each other's sessions.
func TestSessionCommands_TwoTopicsListOwnSessions(t *testing.T) {
	e, _, _, shared := newScopedEngine(t)
	bindScopedTopic(e, "om_root_a", shared)
	bindScopedTopic(e, "om_root_b", shared)

	_, sessionsA := e.sessionContextForKey(topicKeyA)
	_, sessionsB := e.sessionContextForKey(topicKeyB)

	sessionsA.GetOrCreateActive(topicKeyA).SetAgentSessionID("sess-a1", "channel-scope-agent")
	sessionsB.GetOrCreateActive(topicKeyB).SetAgentSessionID("sess-b1", "channel-scope-agent")
	sessionsA.Save()
	sessionsB.Save()

	agentA, _ := e.sessionContextForKey(topicKeyA)
	agentB, _ := e.sessionContextForKey(topicKeyB)

	allA, err := agentA.ListSessions(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	filteredA := e.applySessionFilter(allA, sessionsA, topicKeyA)

	names := make([]string, 0, len(filteredA))
	for _, s := range filteredA {
		names = append(names, s.Summary)
	}
	if len(names) != 1 || names[0] != "a1" {
		t.Fatalf("topic A /list = %v, want only a1 (b1 belongs to topic B)", names)
	}

	allB, err := agentB.ListSessions(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	filteredB := e.applySessionFilter(allB, sessionsB, topicKeyB)
	if len(filteredB) != 1 || filteredB[0].Summary != "b1" {
		t.Fatalf("topic B /list = %v, want only b1", filteredB)
	}
}

// /switch must not reach a session owned by another topic.
func TestSessionCommands_SwitchCannotCrossTopics(t *testing.T) {
	e, _, _, shared := newScopedEngine(t)
	bindScopedTopic(e, "om_root_a", shared)
	bindScopedTopic(e, "om_root_b", shared)

	_, sessionsA := e.sessionContextForKey(topicKeyA)
	_, sessionsB := e.sessionContextForKey(topicKeyB)
	sessionsB.GetOrCreateActive(topicKeyB).SetAgentSessionID("sess-b1", "channel-scope-agent")
	sessionsB.Save()

	agentA, _ := e.sessionContextForKey(topicKeyA)
	all, err := agentA.ListSessions(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	candidates := e.applySessionFilter(all, sessionsA, topicKeyA)
	for _, s := range candidates {
		if s.ID == "sess-b1" {
			t.Fatal("topic A can switch into topic B's session")
		}
	}
	if matched := e.matchSession(candidates, sessionsA, "b1"); matched != nil {
		t.Fatalf("matchSession found topic B's session from topic A: %+v", matched)
	}
}

// Non-multi-workspace engines must keep every existing behaviour.
func TestApplySessionFilter_NonMultiWorkspaceUnchanged(t *testing.T) {
	baseDir := t.TempDir()
	sessionsByWorkDir := map[string][]AgentSessionInfo{
		"/global": {{ID: "sess-global", Summary: "global-session", MessageCount: 2}},
	}
	const agentName = "channel-scope-global-agent"
	RegisterAgent(agentName, func(_ map[string]any) (Agent, error) {
		return &scopedWorkDirAgent{name: agentName, sessionsByWorkDir: sessionsByWorkDir}, nil
	})

	e := NewEngine(
		"test",
		&scopedWorkDirAgent{name: agentName, sessionsByWorkDir: sessionsByWorkDir},
		[]Platform{&stubPlatformEngine{n: "feishu"}},
		filepath.Join(t.TempDir(), "sessions.json"),
		LangEnglish,
	)
	_ = baseDir

	sessionKey := "feishu:oc_chat:root:om_root_a"
	sessions := e.sessions
	sessions.GetOrCreateActive(sessionKey).SetAgentSessionID("sess-global", agentName)
	sessions.Save()

	all := []AgentSessionInfo{{ID: "sess-global", Summary: "global-session"}}

	// The topic filter must be inert without multi-workspace mode.
	got := e.applySessionFilter(all, sessions, sessionKey)
	if len(got) != 1 || got[0].ID != "sess-global" {
		t.Fatalf("non-multi-workspace filter = %v, want the session passed through unchanged", got)
	}

	// Key resolution must also fall back to core's generic parsing.
	if key := e.extractWorkspaceChannelKey(sessionKey); key != "feishu:oc_chat" {
		t.Fatalf("non-multi-workspace channelKey = %q, want %q", key, "feishu:oc_chat")
	}
	if e.multiWorkspace {
		t.Fatal("engine should not be in multi-workspace mode")
	}
}
