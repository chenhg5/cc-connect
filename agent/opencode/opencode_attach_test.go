package opencode

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// TestBuildRunArgs_StandaloneHasNoAttach pins the pre-attach command shape:
// with no serverURL configured, buildRunArgs must remain byte-identical to
// the historical standalone behavior (no --attach anywhere).
func TestBuildRunArgs_StandaloneHasNoAttach(t *testing.T) {
	s := &opencodeSession{workDir: "/repo", model: "provider/model", mode: "default"}

	got := s.buildRunArgs("hello", nil, "")
	want := []string{"run", "--format", "json", "--model", "provider/model", "--dir", "/repo", "--thinking"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("standalone args = %#v, want %#v", got, want)
	}
	for _, a := range got {
		if a == "--attach" {
			t.Fatalf("standalone args unexpectedly contain --attach: %#v", got)
		}
	}
}

// TestBuildRunArgs_AttachPrependsServerURL verifies that a configured
// serverURL injects exactly ["--attach", url] right after
// ["run", "--format", "json"], with all existing args preserved after it.
func TestBuildRunArgs_AttachPrependsServerURL(t *testing.T) {
	s := &opencodeSession{
		workDir:   "/repo",
		model:     "provider/model",
		serverURL: "http://127.0.0.1:4096",
	}

	got := s.buildRunArgs("hello", nil, "ses_123")
	want := []string{
		"run", "--format", "json",
		"--attach", "http://127.0.0.1:4096",
		"--session", "ses_123",
		"--model", "provider/model",
		"--dir", "/repo",
		"--thinking",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attach args = %#v, want %#v", got, want)
	}
}

// TestBuildRunArgs_AttachPreservesAllOptions ensures attach mode keeps every
// existing behavior: agent selection, yolo permissions, work_dir, files,
// session resume, and extra cmd args.
func TestBuildRunArgs_AttachPreservesAllOptions(t *testing.T) {
	s := &opencodeSession{
		extraArgs: []string{"--log-level", "ERROR"},
		workDir:   "/repo",
		model:     "m",
		mode:      "yolo",
		agentName: "build",
		serverURL: "http://127.0.0.1:4096",
	}

	got := s.buildRunArgs("hi", []string{"/tmp/a.png"}, "ses_9")
	want := []string{
		"--log-level", "ERROR",
		"run", "--format", "json",
		"--attach", "http://127.0.0.1:4096",
		"--session", "ses_9",
		"--agent", "build",
		"--model", "m",
		"--dir", "/repo",
		"--thinking",
		"--dangerously-skip-permissions",
		"--file", "/tmp/a.png",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attach args = %#v, want %#v", got, want)
	}
}

// TestBuildRunArgs_WorkDirPreservedInBothModes verifies --dir scoping is
// identical in standalone and attach mode: the server interprets --dir on
// its own side when attaching, so per-project work_dir isolation holds.
func TestBuildRunArgs_WorkDirPreservedInBothModes(t *testing.T) {
	for _, dir := range []string{"/repo-a", "/repo-b"} {
		standalone := (&opencodeSession{workDir: dir}).buildRunArgs("hi", nil, "")
		attached := (&opencodeSession{workDir: dir, serverURL: "http://127.0.0.1:4096"}).buildRunArgs("hi", nil, "")

		// attached = standalone with exactly ["--attach", url] inserted at index 3.
		want := append(append([]string{}, standalone[:3]...),
			append([]string{"--attach", "http://127.0.0.1:4096"}, standalone[3:]...)...)
		if !reflect.DeepEqual(attached, want) {
			t.Fatalf("dir %s: attached = %#v, want %#v", dir, attached, want)
		}
		if got := attached[len(attached)-3]; got != "--dir" {
			t.Fatalf("dir %s: missing --dir flag: %#v", dir, attached)
		}
		if got := attached[len(attached)-2]; got != dir {
			t.Fatalf("dir %s: --dir = %q, want %q", dir, got, dir)
		}
	}
}

// TestNormalizeServerURL covers config parsing: absent/empty stays
// standalone, whitespace is trimmed, and malformed values fail fast.
func TestNormalizeServerURL(t *testing.T) {
	cases := []struct {
		name    string
		raw     any
		want    string
		wantErr bool
	}{
		{"absent", nil, "", false},
		{"empty", "", "", false},
		{"blank", "   ", "", false},
		{"non-string-int", 42, "", true},
		{"non-string-bool", true, "", true},
		{"http", "http://127.0.0.1:4096", "http://127.0.0.1:4096", false},
		{"https", "https://opencode.internal:4096", "https://opencode.internal:4096", false},
		{"uppercase-scheme", "HTTP://127.0.0.1:4096", "HTTP://127.0.0.1:4096", false},
		{"trims-spaces", "  http://127.0.0.1:4096  ", "http://127.0.0.1:4096", false},
		{"with-path", "http://127.0.0.1:4096/some/path", "http://127.0.0.1:4096/some/path", false},
		{"bare-host", "127.0.0.1:4096", "", true},
		{"missing-host", "http://", "", true},
		{"missing-host-slashes", "http:///path", "", true},
		{"wrong-scheme", "ws://127.0.0.1:4096", "", true},
		{"ftp-scheme", "ftp://127.0.0.1:4096", "", true},
		{"garbage", "not a url", "", true},
		{"userinfo", "http://user:s3cret@127.0.0.1:4096", "", true},
		{"userinfo-no-password", "http://user@127.0.0.1:4096", "", true},
		{"userinfo-wrong-scheme", "ftp://user:pw@127.0.0.1:4096", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeServerURL(tc.raw)
			if tc.wantErr && err == nil {
				t.Fatalf("normalizeServerURL(%v) err = nil, want error", tc.raw)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("normalizeServerURL(%v) err = %v, want nil", tc.raw, err)
			}
			if got != tc.want {
				t.Fatalf("normalizeServerURL(%v) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

// TestNormalizeServerURL_NeverEchoesSecrets is a regression test: rejection
// errors for secret-bearing URLs must not echo the credential back, since
// errors can surface on messaging platforms.
func TestNormalizeServerURL_NeverEchoesSecrets(t *testing.T) {
	secrets := []any{
		"http://admin:s3cret-pw@127.0.0.1:4096",
		"http://admin:s3cret-pw@127.0.0.1:4096/?token=abc#frag",
		"ftp://admin:s3cret-pw@127.0.0.1:4096",
	}
	for _, raw := range secrets {
		_, err := normalizeServerURL(raw)
		if err == nil {
			t.Fatalf("normalizeServerURL(%v) err = nil, want rejection", raw)
		}
		if strings.Contains(err.Error(), "s3cret-pw") || strings.Contains(err.Error(), "admin:") {
			t.Fatalf("rejection error leaks credentials: %q", err.Error())
		}
	}
}

// TestSanitizeServerURLForLog ensures logs never expose credentials, query
// secrets, or fragments — even for misconfigured URLs. Unparseable values
// become a fixed placeholder instead of being echoed.
func TestSanitizeServerURLForLog(t *testing.T) {
	if got := sanitizeServerURLForLog("http://127.0.0.1:4096"); got != "http://127.0.0.1:4096" {
		t.Fatalf("plain URL rewritten: %q", got)
	}
	got := sanitizeServerURLForLog("http://user:s3cret@127.0.0.1:4096")
	if strings.Contains(got, "s3cret") || strings.Contains(got, "user") {
		t.Fatalf("credentials leaked in sanitized URL: %q", got)
	}
	if !strings.Contains(got, "127.0.0.1:4096") {
		t.Fatalf("host lost in sanitized URL: %q", got)
	}
	got = sanitizeServerURLForLog("http://127.0.0.1:4096/?token=s3cret#frag")
	if strings.Contains(got, "s3cret") || strings.Contains(got, "frag") {
		t.Fatalf("query/fragment leaked in sanitized URL: %q", got)
	}
	if got := sanitizeServerURLForLog("://bad-url"); got != "[invalid server_url]" {
		t.Fatalf("unparseable URL = %q, want placeholder", got)
	}
	if got := sanitizeServerURLForLog("not a url"); got != "[invalid server_url]" {
		t.Fatalf("schemaless value = %q, want placeholder", got)
	}
}

// TestAttachErrMsg verifies standalone errors pass through untouched while
// attach errors are prefixed with the sanitized server URL (no silent
// fallback: the backend failure stays visible and attributable).
func TestAttachErrMsg(t *testing.T) {
	if got := attachErrMsg("", "boom"); got != "boom" {
		t.Fatalf("standalone err = %q, want passthrough", got)
	}
	got := attachErrMsg("http://127.0.0.1:4096", "boom")
	if !strings.Contains(got, "http://127.0.0.1:4096") || !strings.Contains(got, "boom") {
		t.Fatalf("attach err = %q, want server + message", got)
	}
	got = attachErrMsg("http://user:pw@host:4096", "boom")
	if strings.Contains(got, "pw") {
		t.Fatalf("attach err leaks credentials: %q", got)
	}
}

// TestAttach_ConversationIsolation verifies the key attach invariant: reusing
// the same backend (same serverURL) does NOT merge conversations. Session
// identity still comes from chatID via --session, exactly as in standalone.
func TestAttach_ConversationIsolation(t *testing.T) {
	const server = "http://127.0.0.1:4096"

	argsFor := func(chatID string) []string {
		return (&opencodeSession{workDir: "/repo", serverURL: server}).buildRunArgs("hi", nil, chatID)
	}

	// Same conversation, repeated messages: same --session (semantics
	// preserved from standalone).
	if a, b := argsFor("ses_A"), argsFor("ses_A"); !reflect.DeepEqual(a, b) {
		t.Fatalf("same conversation diverged:\n%#v\n%#v", a, b)
	}

	// Two unrelated conversations: different --session despite shared backend.
	a, b := argsFor("ses_A"), argsFor("ses_B")
	var sessionOf = func(args []string) string {
		for i, v := range args {
			if v == "--session" && i+1 < len(args) {
				return args[i+1]
			}
		}
		return ""
	}
	if sessionOf(a) != "ses_A" || sessionOf(b) != "ses_B" {
		t.Fatalf("session mapping broken: %#v vs %#v", a, b)
	}

	// Fresh conversation (no chatID yet): no --session, same as standalone.
	if got := argsFor(""); sessionOf(got) != "" {
		t.Fatalf("fresh conversation should have no --session: %#v", got)
	}
}

// TestWorkspaceAgentOptions_PreservesServerURL is a regression test for the
// multi-workspace propagation gap: server_url must survive the per-workspace
// agent copy in core.Engine.getOrCreateWorkspaceAgent, otherwise workspaces
// would silently split between attached and standalone backends.
func TestWorkspaceAgentOptions_PreservesServerURL(t *testing.T) {
	a := &Agent{mode: "default", serverURL: "http://127.0.0.1:4096"}
	opts := a.WorkspaceAgentOptions()
	if opts["server_url"] != "http://127.0.0.1:4096" {
		t.Fatalf("server_url = %v, want http://127.0.0.1:4096", opts["server_url"])
	}

	plain := (&Agent{mode: "default"}).WorkspaceAgentOptions()
	if _, ok := plain["server_url"]; ok {
		t.Fatalf("standalone opts unexpectedly contain server_url: %v", plain)
	}
}

// TestHandleText_KeepsShortFragments is a regression test for silent
// "(空响应)" on short replies: text fragments of any non-zero length must be
// delivered (the engine joins them). Dropping e.g. single-character chunks
// would turn a real "OK" answer into an empty turn.
func TestHandleText_KeepsShortFragments(t *testing.T) {
	s, err := newOpencodeSession(context.Background(), "opencode", nil, t.TempDir(), "", "default", "", "", core.ContinueSession, nil)
	if err != nil {
		t.Fatalf("newOpencodeSession: %v", err)
	}
	defer func() { _ = s.Close() }()

	for _, chunk := range []string{"O", "K"} {
		s.handleText(map[string]any{
			"type": "text",
			"part": map[string]any{"type": "text", "text": chunk, "sessionID": "ses_1"},
		})
	}
	var got []string
	for range 2 {
		select {
		case ev := <-s.Events():
			if ev.Type != core.EventText {
				t.Fatalf("event type = %v, want EventText", ev.Type)
			}
			got = append(got, ev.Content)
		default:
			t.Fatalf("missing EventText, got %q so far", got)
		}
	}
	if strings.Join(got, "") != "OK" {
		t.Fatalf("fragments = %q, want OK", got)
	}
}

// TestCleanExitEvent_VacuousTurnReturnsError covers the intermittent
// `run --attach` dropped-stream race (backend records parts the client never
// prints, or vice versa; exit 0 either way): a clean exit with zero text and
// zero tool events must surface an explicit error asking for a resend — never
// a silent empty result. No automatic retry: the unseen turn may have
// executed tools server-side.
func TestCleanExitEvent_VacuousTurnReturnsError(t *testing.T) {
	s, err := newOpencodeSession(context.Background(), "opencode", nil, t.TempDir(), "", "default", "", "http://127.0.0.1:4096", core.ContinueSession, nil)
	if err != nil {
		t.Fatalf("newOpencodeSession: %v", err)
	}
	defer func() { _ = s.Close() }()

	evt := s.cleanExitEvent()
	if evt == nil {
		t.Fatal("cleanExitEvent = nil for vacuous turn, want explicit error")
	}
	if evt.Type != core.EventError {
		t.Fatalf("event type = %v, want EventError", evt.Type)
	}
	if evt.Error == nil || !strings.Contains(evt.Error.Error(), "resend") {
		t.Fatalf("error = %v, want resend guidance", evt.Error)
	}
}

// TestCleanExitEvent_WithOutputProceedsNormally ensures ordinary turns
// (text seen, or tools seen) still take the fallback EventResult path.
func TestCleanExitEvent_WithOutputProceedsNormally(t *testing.T) {
	newSess := func(t *testing.T) *opencodeSession {
		s, err := newOpencodeSession(context.Background(), "opencode", nil, t.TempDir(), "", "default", "", "", core.ContinueSession, nil)
		if err != nil {
			t.Fatalf("newOpencodeSession: %v", err)
		}
		return s
	}

	s := newSess(t)
	defer func() { _ = s.Close() }()
	s.handleText(map[string]any{
		"part": map[string]any{"type": "text", "text": "OK", "sessionID": "ses_1"},
	})
	<-s.Events() // drain
	if evt := s.cleanExitEvent(); evt != nil {
		t.Fatalf("cleanExitEvent = %v after text, want nil (normal result path)", evt)
	}

	s2 := newSess(t)
	defer func() { _ = s2.Close() }()
	s2.handleToolUse(map[string]any{
		"part": map[string]any{"type": "tool_use", "tool": "bash",
			"state": map[string]any{"status": "pending", "input": "ls"}},
	})
	<-s2.Events() // drain
	if evt := s2.cleanExitEvent(); evt != nil {
		t.Fatalf("cleanExitEvent = %v after tool use, want nil (normal result path)", evt)
	}
}

// TestAttach_ConversationIsolationViaSessionManager is the integration-level
// companion to TestAttach_ConversationIsolation: it drives the real
// core.SessionManager conversation mapping (the same mapping the engine uses
// for every frontend — GetOrCreateActive keyed by conversation identity,
// AgentSessionID persisted per conversation and passed as the resume ID to
// StartSession, mirroring engine.go) and verifies that two conversations
// sharing one attached backend still resolve to distinct OpenCode sessions.
// No platform code is involved; userKeys are opaque conversation identities.
func TestAttach_ConversationIsolationViaSessionManager(t *testing.T) {
	sm := core.NewSessionManager(filepath.Join(t.TempDir(), "state.json"))

	convA := sm.GetOrCreateActive("conv-A")
	convB := sm.GetOrCreateActive("conv-B")
	if convA == convB {
		t.Fatal("distinct conversations mapped to the same core session")
	}

	// Simulate one completed turn per conversation, as the engine does when
	// it writes EventResult session IDs back via SetAgentSessionID.
	convA.SetAgentSessionID("ses_agent_A", "opencode")
	convB.SetAgentSessionID("ses_agent_B", "opencode")

	// Same mapping must survive a manager reload (persistence path).
	sm.Save()
	reloaded := core.NewSessionManager(sm.StorePath())
	if got := reloaded.GetOrCreateActive("conv-A").GetAgentSessionID(); got != "ses_agent_A" {
		t.Fatalf("reloaded conv-A AgentSessionID = %q, want ses_agent_A", got)
	}
	if got := reloaded.GetOrCreateActive("conv-B").GetAgentSessionID(); got != "ses_agent_B" {
		t.Fatalf("reloaded conv-B AgentSessionID = %q, want ses_agent_B", got)
	}

	serverURL, err := normalizeServerURL("http://127.0.0.1:4096")
	if err != nil {
		t.Fatalf("normalizeServerURL: %v", err)
	}
	a := &Agent{cmd: "opencode", workDir: t.TempDir(), serverURL: serverURL}

	ctx := context.Background()
	sessA, err := a.StartSession(ctx, reloaded.GetOrCreateActive("conv-A").GetAgentSessionID())
	if err != nil {
		t.Fatalf("StartSession conv-A: %v", err)
	}
	defer func() { _ = sessA.Close() }()
	sessB, err := a.StartSession(ctx, reloaded.GetOrCreateActive("conv-B").GetAgentSessionID())
	if err != nil {
		t.Fatalf("StartSession conv-B: %v", err)
	}
	defer func() { _ = sessB.Close() }()

	if sessA.CurrentSessionID() != "ses_agent_A" {
		t.Fatalf("conv-A agent session = %q, want ses_agent_A", sessA.CurrentSessionID())
	}
	if sessB.CurrentSessionID() != "ses_agent_B" {
		t.Fatalf("conv-B agent session = %q, want ses_agent_B", sessB.CurrentSessionID())
	}
	if sessA.CurrentSessionID() == sessB.CurrentSessionID() {
		t.Fatal("backend reuse merged two conversations into one agent session")
	}
}
