package core

import (
	"strings"
	"testing"
	"time"
)

// Regression tests for issue #1604: the display mode is resolved per scope.
//
//	session override (/config mode) > group default > project/global config

func newDisplayScopeEngine() *Engine {
	e := NewEngine("test", &stubAgent{}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangEnglish)
	e.SetDisplayConfig(DisplayCfg{
		Mode:             "full",
		ThinkingMessages: true,
		ToolMessages:     true,
		ThinkingMaxLen:   300,
		ToolMaxLen:       500,
	})
	return e
}

// TestDisplayForSession_GroupDefaultApplies verifies the group-scoped default
// is used for sessions in that group and does not leak to other groups.
func TestDisplayForSession_GroupDefaultApplies(t *testing.T) {
	e := newDisplayScopeEngine()
	e.SetGroupDisplays(map[string]DisplayModeOverride{
		"feishu:oc_a": {Mode: "quiet", ThinkingMessages: false, ToolMessages: false},
	})

	// Session inside the configured group gets the group default.
	got := e.displayForSession("feishu:oc_a:ou_alice")
	if got.Mode != "quiet" {
		t.Fatalf("mode = %q, want quiet", got.Mode)
	}
	if got.ThinkingMessages || got.ToolMessages {
		t.Fatalf("thinking/tool = %v/%v, want false/false", got.ThinkingMessages, got.ToolMessages)
	}

	// Session in another group keeps the global config.
	other := e.displayForSession("feishu:oc_b:ou_alice")
	if other.Mode != "full" {
		t.Fatalf("other group mode = %q, want full", other.Mode)
	}
	if !other.ThinkingMessages || !other.ToolMessages {
		t.Fatalf("other group thinking/tool = %v/%v, want true/true", other.ThinkingMessages, other.ToolMessages)
	}
}

// TestDisplayForSession_GroupDefaultBareChatID verifies the bare chat id is
// accepted as a group id (the form printed by /whoami).
func TestDisplayForSession_GroupDefaultBareChatID(t *testing.T) {
	e := newDisplayScopeEngine()
	e.SetGroupDisplays(map[string]DisplayModeOverride{
		"oc_a": {Mode: "compact", ThinkingMessages: false, ToolMessages: false},
	})

	got := e.displayForSession("feishu:oc_a:ou_alice")
	if got.Mode != "compact" {
		t.Fatalf("mode = %q, want compact", got.Mode)
	}
}

// TestDisplayForSession_GroupDefaultWinsOverGlobal verifies the group default
// overrides the project/global display config.
func TestDisplayForSession_GroupDefaultWinsOverGlobal(t *testing.T) {
	e := newDisplayScopeEngine()
	e.SetDisplayConfig(DisplayCfg{Mode: "full", ThinkingMessages: true, ToolMessages: true})
	e.SetGroupDisplays(map[string]DisplayModeOverride{
		"feishu:oc_a": {Mode: "quiet", ThinkingMessages: false, ToolMessages: false},
	})

	if got := e.displayForSession("feishu:oc_a:ou_alice"); got.Mode != "quiet" {
		t.Fatalf("mode = %q, want quiet", got.Mode)
	}
}

// TestDisplayForSession_SessionOverrideWinsOverGroup verifies an explicit
// session setting takes precedence over the group default.
func TestDisplayForSession_SessionOverrideWinsOverGroup(t *testing.T) {
	e := newDisplayScopeEngine()
	e.SetGroupDisplays(map[string]DisplayModeOverride{
		"feishu:oc_a": {Mode: "quiet", ThinkingMessages: false, ToolMessages: false},
	})

	if err := e.setSessionDisplayMode("feishu:oc_a:ou_alice", "full"); err != nil {
		t.Fatalf("set session mode: %v", err)
	}

	got := e.displayForSession("feishu:oc_a:ou_alice")
	if got.Mode != "full" {
		t.Fatalf("mode = %q, want full", got.Mode)
	}
	if !got.ThinkingMessages || !got.ToolMessages {
		t.Fatalf("thinking/tool = %v/%v, want true/true", got.ThinkingMessages, got.ToolMessages)
	}
}

// TestConfigModeIsSessionScoped verifies `/config mode` only changes the
// current session: the global config and sibling sessions stay untouched.
func TestConfigModeIsSessionScoped(t *testing.T) {
	e := newDisplayScopeEngine()
	// A displaySaveFunc would persist the mode to the config file — the
	// session-scoped mode must never call it.
	saved := false
	e.SetDisplaySaveFunc(func(mode *string, thinkingMessages *bool, thinkingMaxLen, toolMaxLen *int, toolMessages *bool) error {
		saved = true
		return nil
	})

	target := "feishu:oc_a:ou_alice"
	sibling := "feishu:oc_a:ou_bob"

	items := e.configItems(target)
	var mode *configItem
	for i := range items {
		if items[i].key == "mode" {
			mode = &items[i]
			break
		}
	}
	if mode == nil {
		t.Fatal("expected a mode config item")
	}
	if err := mode.setFunc("quiet"); err != nil {
		t.Fatalf("set mode: %v", err)
	}
	if saved {
		t.Fatal("session-scoped mode must not be written back to the config file")
	}

	// The global display config is untouched.
	if e.display.Mode != "full" {
		t.Fatalf("global mode = %q, want full", e.display.Mode)
	}
	if !e.display.ThinkingMessages || !e.display.ToolMessages {
		t.Fatalf("global thinking/tool = %v/%v, want true/true", e.display.ThinkingMessages, e.display.ToolMessages)
	}

	// The sibling session in the same group is untouched.
	sib := e.displayForSession(sibling)
	if sib.Mode != "full" {
		t.Fatalf("sibling mode = %q, want full", sib.Mode)
	}

	// The target session uses the new mode.
	if got := e.displayForSession(target); got.Mode != "quiet" {
		t.Fatalf("target mode = %q, want quiet", got.Mode)
	}

	// `/config get mode` reports the session-scoped value.
	if got := mode.getFunc(); got != "quiet" {
		t.Fatalf("config get mode = %q, want quiet", got)
	}
}

// TestConfigModeRejectsInvalidValue verifies invalid modes are rejected and
// leave the previous value in place.
func TestConfigModeRejectsInvalidValue(t *testing.T) {
	e := newDisplayScopeEngine()
	target := "feishu:oc_a:ou_alice"
	if err := e.setSessionDisplayMode(target, "quiet"); err != nil {
		t.Fatalf("set session mode: %v", err)
	}
	if err := e.setSessionDisplayMode(target, "bogus"); err == nil {
		t.Fatal("expected an error for an invalid mode")
	}
	if got := e.displayForSession(target); got.Mode != "quiet" {
		t.Fatalf("mode = %q, want quiet (unchanged)", got.Mode)
	}
}

// TestSessionDisplayOverrideDroppedWhenSessionEnds verifies the temporary mode
// does not outlive the session it was set on: after the session ends, the
// group default applies again.
func TestSessionDisplayOverrideDroppedWhenSessionEnds(t *testing.T) {
	e := newDisplayScopeEngine()
	e.SetGroupDisplays(map[string]DisplayModeOverride{
		"feishu:oc_a": {Mode: "quiet", ThinkingMessages: false, ToolMessages: false},
	})

	target := "feishu:oc_a:ou_alice"
	if err := e.setSessionDisplayMode(target, "full"); err != nil {
		t.Fatalf("set session mode: %v", err)
	}
	if got := e.displayForSession(target); got.Mode != "full" {
		t.Fatalf("mode before session end = %q, want full", got.Mode)
	}

	// Session ends (e.g. /new or /cancel): the temporary mode is dropped.
	e.clearSessionDisplayOverride(target)

	got := e.displayForSession(target)
	if got.Mode != "quiet" {
		t.Fatalf("mode after session end = %q, want quiet (group default)", got.Mode)
	}
	if got.ThinkingMessages || got.ToolMessages {
		t.Fatalf("thinking/tool after session end = %v/%v, want false/false", got.ThinkingMessages, got.ToolMessages)
	}
}

// TestDisplayForSession_NoGroupDefaultFallsBackToGlobal verifies that a group
// without a configured default keeps the previous global behaviour.
func TestDisplayForSession_NoGroupDefaultFallsBackToGlobal(t *testing.T) {
	e := newDisplayScopeEngine()
	e.SetDisplayConfig(DisplayCfg{Mode: "compact", ThinkingMessages: false, ToolMessages: false})
	e.SetGroupDisplays(map[string]DisplayModeOverride{
		"feishu:oc_a": {Mode: "quiet", ThinkingMessages: false, ToolMessages: false},
	})

	got := e.displayForSession("feishu:oc_b:ou_alice")
	if got.Mode != "compact" {
		t.Fatalf("mode = %q, want compact (global fallback)", got.Mode)
	}
	if got.ThinkingMessages || got.ToolMessages {
		t.Fatalf("thinking/tool = %v/%v, want false/false", got.ThinkingMessages, got.ToolMessages)
	}
}

// TestDisplayForSession_EmptyGroupMapFallsBackToGlobal verifies the engine
// behaves exactly as before when no group is configured at all.
func TestDisplayForSession_EmptyGroupMapFallsBackToGlobal(t *testing.T) {
	e := newDisplayScopeEngine()
	e.SetGroupDisplays(nil)

	got := e.displayForSession("feishu:oc_a:ou_alice")
	if got.Mode != "full" {
		t.Fatalf("mode = %q, want full", got.Mode)
	}
	if !got.ThinkingMessages || !got.ToolMessages {
		t.Fatalf("thinking/tool = %v/%v, want true/true", got.ThinkingMessages, got.ToolMessages)
	}
}

// TestDisplayGroupKey verifies the group id derived from a session key.
func TestDisplayGroupKey(t *testing.T) {
	cases := []struct {
		sessionKey string
		want       string
	}{
		{"feishu:oc_a:ou_alice", "feishu:oc_a"},
		{"feishu:oc_a", "feishu:oc_a"},
		{"telegram:-100123:ou_alice", "telegram:-100123"},
	}
	for _, tc := range cases {
		if got := displayGroupKey(tc.sessionKey); got != tc.want {
			t.Errorf("displayGroupKey(%q) = %q, want %q", tc.sessionKey, got, tc.want)
		}
	}
}

// TestDisplaySessionKey_UsesRawKeyAcrossWorkspaces verifies that the display
// scope is resolved from the raw platform session key even when the
// interactive key carries a workspace prefix (Issue #1604, QA P2-2).
//
// Two workspaces may share one raw platform session key while each of them
// runs its own interactive session. The normalization must return the raw key
// for both, so neither workspace resolves against the other's key.
func TestDisplaySessionKey_UsesRawKeyAcrossWorkspaces(t *testing.T) {
	const rawKey = "feishu:oc_a:ou_alice"

	for _, ws := range []string{"ws-a", "ws-b"} {
		interactiveKey := ws + ":" + rawKey
		state := &interactiveState{rawSessionKey: rawKey}

		if got := displaySessionKey(state, interactiveKey); got != rawKey {
			t.Errorf("%s: displaySessionKey = %q, want %q", ws, got, rawKey)
		}
	}

	// A state that does not carry a raw key falls back to the given key.
	interactiveKey := "ws-a:" + rawKey
	if got := displaySessionKey(&interactiveState{}, interactiveKey); got != interactiveKey {
		t.Errorf("fallback key = %q, want %q", got, interactiveKey)
	}
}

// TestDisplayForSession_MultiWorkspaceResolvesRawScope verifies that a
// workspace-prefixed interactive key still resolves to the scope of the raw
// platform session key: the group default, or the session override set by
// `/config mode` (Issue #1604, QA P2-2).
func TestDisplayForSession_MultiWorkspaceResolvesRawScope(t *testing.T) {
	const rawKey = "feishu:oc_a:ou_alice"
	e := newDisplayScopeEngine()
	e.SetGroupDisplays(map[string]DisplayModeOverride{
		"feishu:oc_a": {Mode: "quiet", ThinkingMessages: false, ToolMessages: false},
	})

	for _, ws := range []string{"ws-a", "ws-b"} {
		interactiveKey := ws + ":" + rawKey
		state := &interactiveState{rawSessionKey: rawKey}

		got := e.displayForSession(displaySessionKey(state, interactiveKey))
		if got.Mode != "quiet" {
			t.Errorf("%s: mode = %q, want quiet (group default)", ws, got.Mode)
		}
		if got.ThinkingMessages || got.ToolMessages {
			t.Errorf("%s: thinking/tool = %v/%v, want false/false", ws, got.ThinkingMessages, got.ToolMessages)
		}
	}

	// A session override on the raw key wins over the group default.
	if err := e.setSessionDisplayMode(rawKey, "compact"); err != nil {
		t.Fatalf("set session mode: %v", err)
	}
	state := &interactiveState{rawSessionKey: rawKey}
	if got := e.displayForSession(displaySessionKey(state, "ws-a:"+rawKey)); got.Mode != "compact" {
		t.Errorf("mode = %q, want compact (session override)", got.Mode)
	}

	// The workspace-prefixed key alone matches no group scope: it falls back
	// to the global config. This is what makes the normalization load-bearing.
	if got := e.displayForSession("ws-a:" + rawKey); got.Mode != "full" {
		t.Errorf("workspace-prefixed mode = %q, want full (global fallback)", got.Mode)
	}
}

// TestProcessInteractiveEvents_MultiWorkspaceAppliesGroupDefault verifies that
// the event loop applies the display scope of the raw platform session key
// when the interactive key is workspace-prefixed (Issue #1604, QA P2-2).
//
// This is the regression guard for the normalization in the event loop: if the
// raw key were not used, the group default would be skipped silently and the
// session would fall back to the global display mode.
func TestProcessInteractiveEvents_MultiWorkspaceAppliesGroupDefault(t *testing.T) {
	const (
		rawKey    = "feishu:oc_a:ou_alice"
		probeText = "group scope probe"
	)

	run := func(t *testing.T, rawSessionKey string) []string {
		t.Helper()
		p := &stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
		e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
		e.SetDisplayConfig(DisplayCfg{
			Mode:             "full",
			ThinkingMessages: true,
			ToolMessages:     true,
			ThinkingMaxLen:   300,
			ToolMaxLen:       500,
		})
		e.SetGroupDisplays(map[string]DisplayModeOverride{
			"feishu:oc_a": {Mode: "quiet", ThinkingMessages: false, ToolMessages: false},
		})

		interactiveKey := "ws-a:" + rawKey
		session := e.sessions.GetOrCreateActive(interactiveKey)
		agentSession := newControllableSession("s1")
		state := &interactiveState{
			agentSession:  agentSession,
			platform:      p,
			replyCtx:      "ctx-1",
			rawSessionKey: rawSessionKey,
		}
		e.interactiveStates[interactiveKey] = state

		agentSession.events <- Event{Type: EventThinking, Content: probeText}
		agentSession.events <- Event{Type: EventResult, Content: "done", Done: true}
		e.processInteractiveEvents(state, session, e.sessions, interactiveKey, "m1", time.Now(), nil, nil, nil, 0)
		return p.getSent()
	}

	// Control: without the raw key the group default is not reachable, so the
	// global mode applies and the thinking message is sent. This keeps the
	// assertion below from being vacuous.
	if control := run(t, ""); !sentContainsText(control, probeText) {
		t.Fatalf("control run sent %#v, want the thinking message", control)
	}

	// Regression: with the raw key the group default applies and the thinking
	// message is suppressed.
	if got := run(t, rawKey); sentContainsText(got, probeText) {
		t.Fatalf("thinking message leaked under the group default: %#v", got)
	}
}

// sentContainsText reports whether one of the sent messages contains want.
func sentContainsText(sent []string, want string) bool {
	for _, msg := range sent {
		if strings.Contains(msg, want) {
			return true
		}
	}
	return false
}

// TestProcessInteractiveEvents_QuietOverridesGroupDefault verifies that
// `/quiet` in a session with a group default is not shadowed by that default
// (Issue #2097, P2-1).
//
// The group default hides the thinking messages. After `/quiet full` the
// session must show them again, which is only possible when the command
// reaches the session scope: the global config is still "full" here, so a
// command that only wrote the global would leave the group default in place
// and the thinking message would stay hidden.
func TestProcessInteractiveEvents_QuietOverridesGroupDefault(t *testing.T) {
	const (
		rawKey    = "feishu:oc_a:ou_alice"
		probeText = "quiet scope probe"
	)

	run := func(t *testing.T, quietArgs []string) []string {
		t.Helper()
		p := &stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
		e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
		e.SetDisplayConfig(DisplayCfg{
			Mode:             "full",
			ThinkingMessages: true,
			ToolMessages:     true,
			ThinkingMaxLen:   300,
			ToolMaxLen:       500,
		})
		e.SetGroupDisplays(map[string]DisplayModeOverride{
			"feishu:oc_a": {Mode: "quiet", ThinkingMessages: false, ToolMessages: false},
		})

		if quietArgs != nil {
			e.cmdQuiet(p, &Message{SessionKey: rawKey, ReplyCtx: "ctx-1"}, quietArgs)
		}

		interactiveKey := "ws-a:" + rawKey
		session := e.sessions.GetOrCreateActive(interactiveKey)
		agentSession := newControllableSession("s1")
		state := &interactiveState{
			agentSession:  agentSession,
			platform:      p,
			replyCtx:      "ctx-1",
			rawSessionKey: rawKey,
		}
		e.interactiveStates[interactiveKey] = state

		agentSession.events <- Event{Type: EventThinking, Content: probeText}
		agentSession.events <- Event{Type: EventResult, Content: "done", Done: true}
		e.processInteractiveEvents(state, session, e.sessions, interactiveKey, "m1", time.Now(), nil, nil, nil, 0)
		return p.getSent()
	}

	// Control: without `/quiet` the group default applies and the thinking
	// message is suppressed. This keeps the assertion below from being
	// vacuous.
	if control := run(t, nil); sentContainsText(control, probeText) {
		t.Fatalf("control run sent %#v, want no thinking message", control)
	}

	// Regression: `/quiet full` in the session wins over the group default, so
	// the thinking message is shown again.
	if got := run(t, []string{"full"}); !sentContainsText(got, probeText) {
		t.Fatalf("thinking message still suppressed after /quiet full: %#v", got)
	}
}

// TestCmdQuiet_CyclesFromEffectiveSessionMode verifies that `/quiet` cycles
// from the mode that is in effect for the session and not from the global one
// (Issue #2097, P2-1).
//
// The group default is "full" while the global mode is "quiet". The effective
// mode is the group default, so the cycle lands on "quiet"; reading the
// global mode instead would produce "compact" and the reply would describe a
// mode the session never uses.
func TestCmdQuiet_CyclesFromEffectiveSessionMode(t *testing.T) {
	e := newDisplayScopeEngine()
	e.SetDisplayConfig(DisplayCfg{Mode: "quiet", ThinkingMessages: false, ToolMessages: false})
	e.SetGroupDisplays(map[string]DisplayModeOverride{
		"feishu:oc_a": {Mode: "full", ThinkingMessages: true, ToolMessages: true},
	})

	target := "feishu:oc_a:ou_alice"
	if got := e.displayForSession(target).Mode; got != "full" {
		t.Fatalf("effective mode = %q, want full (group default)", got)
	}

	p := &stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	e.cmdQuiet(p, &Message{SessionKey: target, ReplyCtx: "ctx-1"}, nil)

	if got := e.displayForSession(target).Mode; got != "quiet" {
		t.Fatalf("mode after /quiet = %q, want quiet", got)
	}
}

// TestCmdQuiet_GroupDefaultScopeLeavesGlobalUntouched verifies that `/quiet`
// in a session with a group default only changes that session: the global
// config and the config file stay untouched, so the command does not leak
// into the other groups (Issue #2097, P2-1).
func TestCmdQuiet_GroupDefaultScopeLeavesGlobalUntouched(t *testing.T) {
	e := newDisplayScopeEngine()
	saved := false
	e.SetDisplaySaveFunc(func(mode *string, thinkingMessages *bool, thinkingMaxLen, toolMaxLen *int, toolMessages *bool) error {
		saved = true
		return nil
	})
	e.SetGroupDisplays(map[string]DisplayModeOverride{
		"feishu:oc_a": {Mode: "quiet", ThinkingMessages: false, ToolMessages: false},
	})

	target := "feishu:oc_a:ou_alice"
	p := &stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	e.cmdQuiet(p, &Message{SessionKey: target, ReplyCtx: "ctx-1"}, []string{"compact"})

	if saved {
		t.Fatal("the session-scoped mode must not be written back to the config file")
	}
	if e.display.Mode != "full" {
		t.Fatalf("global mode = %q, want full (unchanged)", e.display.Mode)
	}
	got := e.displayForSession(target)
	if got.Mode != "compact" {
		t.Fatalf("session mode = %q, want compact", got.Mode)
	}
	if got.ThinkingMessages || got.ToolMessages {
		t.Fatalf("session thinking/tool = %v/%v, want false/false", got.ThinkingMessages, got.ToolMessages)
	}
}

// TestCmdQuiet_GlobalScopeWithoutGroupDefault verifies that a session without
// a group default keeps the historical behavior of `/quiet`: the global config
// is updated and written back to the config file.
func TestCmdQuiet_GlobalScopeWithoutGroupDefault(t *testing.T) {
	e := newDisplayScopeEngine()
	saved := false
	e.SetDisplaySaveFunc(func(mode *string, thinkingMessages *bool, thinkingMaxLen, toolMaxLen *int, toolMessages *bool) error {
		saved = true
		if mode == nil || *mode != "quiet" {
			t.Errorf("persisted mode = %v, want quiet", mode)
		}
		return nil
	})

	target := "feishu:oc_a:ou_alice"
	p := &stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	e.cmdQuiet(p, &Message{SessionKey: target, ReplyCtx: "ctx-1"}, nil)

	if !saved {
		t.Fatal("expected the global mode to be written back to the config file")
	}
	if e.display.Mode != "quiet" {
		t.Fatalf("global mode = %q, want quiet", e.display.Mode)
	}
	if e.display.ThinkingMessages || e.display.ToolMessages {
		t.Fatalf("global thinking/tool = %v/%v, want false/false", e.display.ThinkingMessages, e.display.ToolMessages)
	}
	// No session override is created: the session keeps following the global
	// config, exactly as before.
	if _, ok := e.sessionDisplayOverride(target); ok {
		t.Fatal("expected no session override in the global scope")
	}
}

// TestCmdQuiet_SessionOverrideScopeKeepsOtherGroupsUntouched verifies that
// `/quiet` in a session that already carries a session override updates that
// override instead of the global config (Issue #2097, P2-1). Otherwise the
// command would be shadowed by the very override it is supposed to change.
func TestCmdQuiet_SessionOverrideScopeKeepsOtherGroupsUntouched(t *testing.T) {
	e := newDisplayScopeEngine()
	saved := false
	e.SetDisplaySaveFunc(func(mode *string, thinkingMessages *bool, thinkingMaxLen, toolMaxLen *int, toolMessages *bool) error {
		saved = true
		return nil
	})

	target := "feishu:oc_a:ou_alice"
	if err := e.setSessionDisplayMode(target, "quiet"); err != nil {
		t.Fatalf("set session mode: %v", err)
	}

	p := &stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	e.cmdQuiet(p, &Message{SessionKey: target, ReplyCtx: "ctx-1"}, nil)

	if saved {
		t.Fatal("the session-scoped mode must not be written back to the config file")
	}
	if e.display.Mode != "full" {
		t.Fatalf("global mode = %q, want full (unchanged)", e.display.Mode)
	}
	// The session override decides the cycle: "quiet" cycles to "compact".
	if got := e.displayForSession(target).Mode; got != "compact" {
		t.Fatalf("session mode = %q, want compact", got)
	}
}
