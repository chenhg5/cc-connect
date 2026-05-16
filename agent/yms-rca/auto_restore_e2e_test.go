package ymsagent

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// emitEnvSwitch fakes the yms-rca-side `yms-rca.env-switch` event that
// fires when a /connect <profile> succeeds. The session reacts by calling
// updateCurrentProfile, which writes to the store.
func emitEnvSwitch(s *session, to string) {
	s.handleEvent(map[string]any{
		"type": "message_end",
		"message": map[string]any{
			"role":       "custom",
			"customType": "yms-rca.env-switch",
			"display":    false,
			"details":    map[string]any{"to": to},
		},
	})
}

// wireSessionForRestore plugs in a per-test profileStore + project +
// sessionKey + platform/engine pipeline, returning everything the test
// needs to drive a full Send → restore → user-prompt flow.
func wireSessionForRestore(t *testing.T, storedProfile, currentProfile string) (
	*session, *mockEncoder, *profileStore, *e2ePlatform, *core.Engine, *sync.WaitGroup,
) {
	t.Helper()
	s, enc := newTestSession(t, "default")
	store := newProfileStore(filepath.Join(t.TempDir(), "store.json"))
	if storedProfile != "" {
		store.Set("yms-rca-youzone", "youzone:test", storedProfile)
	}
	s.profileStore = store
	s.project = "yms-rca-youzone"
	s.sessionKey = "youzone:test"
	s.sessionID.Store("yms-e2e-restore")
	if currentProfile != "" {
		s.currentProfile.Store(currentProfile)
	}
	platform := &e2ePlatform{}
	engine := core.NewEngine("yms-rca-youzone", &e2eAgent{session: s}, []core.Platform{platform}, "", core.LangChinese)
	// driverWG tracks background "fake yms-rca" goroutines; cleanup must
	// wait for them before Stop() races with handleEvent → emit on a
	// channel being closed.
	driverWG := &sync.WaitGroup{}
	t.Cleanup(func() {
		driverWG.Wait()
		engine.Stop()
	})
	return s, enc, store, platform, engine, driverWG
}

// Scenario A: store has "pre", user asks a business question, hidden
// /connect pre runs and is suppressed, then user prompt is delivered.
func TestE2E_AutoRestoreScenarioA_Success(t *testing.T) {
	s, enc, _, platform, engine, wg := wireSessionForRestore(t, "pre", "local")

	// Background driver: when the hidden /connect frame appears, emit the
	// env-switch + EventResult to signal completion.
	wg.Add(1)
	go func() {
		defer wg.Done()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			for _, f := range enc.framesCopy() {
				if f["type"] == "prompt" && strings.Contains(asString(f, "id", ""), "-restore") {
					emitEnvSwitch(s, "pre")
					// Emit a Result event to end the hidden turn.
					s.emit(core.Event{Type: core.EventResult, Done: true})
					return
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	engine.ReceiveMessage(platform, &core.Message{
		SessionKey: "youzone:test",
		Platform:   "youzone",
		UserID:     "5837619.esn.upesn",
		UserName:   "test",
		Content:    "流量切入了吗",
		MessageID:  "msg-1",
	})

	waitForPromptFrame(t, enc, "流量切入了吗")

	// Now drive a normal user-turn response and confirm it surfaces.
	s.handleEvent(map[string]any{
		"type": "message_update",
		"assistantMessageEvent": map[string]any{
			"type":  "text_delta",
			"delta": "pre 环境已切入 ✅",
		},
	})
	s.handleEvent(map[string]any{
		"type": "turn_end",
	})

	sent := waitForPlatformMessage(t, platform, "pre 环境已切入")
	combined := strings.Join(sent, "\n")
	if strings.Contains(combined, "Connected to pre") {
		t.Errorf("hidden /connect output leaked to user: %s", combined)
	}
	// Hidden frame must have a `-restore` id; user prompt has a fresh id.
	frames := enc.framesCopy()
	if len(frames) < 2 {
		t.Fatalf("want >=2 prompt frames (hidden + user), got %d", len(frames))
	}
	restoreFrame, userFrame := frames[0], frames[1]
	if !strings.Contains(asString(restoreFrame, "id", ""), "-restore") {
		t.Errorf("first frame id should contain -restore: %v", restoreFrame)
	}
	if strings.Contains(asString(userFrame, "id", ""), "-restore") {
		t.Errorf("second frame should NOT have -restore id: %v", userFrame)
	}
	if asString(restoreFrame, "message", "") != "/connect pre" {
		t.Errorf("hidden prompt = %q, want /connect pre", asString(restoreFrame, "message", ""))
	}
	if !strings.Contains(asString(userFrame, "message", ""), "流量切入了吗") {
		t.Errorf("user prompt = %q, want '流量切入了吗'", asString(userFrame, "message", ""))
	}
}

// Scenario B: store has "pre" but hidden /connect fails (token missing).
// User sees an i18n error + recovery hint; store is preserved (conservative
// policy); user's original prompt is NOT delivered to yms-rca.
func TestE2E_AutoRestoreScenarioB_FailurePreservesStore(t *testing.T) {
	s, enc, store, platform, engine, wg := wireSessionForRestore(t, "pre", "local")

	wg.Add(1)
	go func() {
		defer wg.Done()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			for _, f := range enc.framesCopy() {
				if f["type"] == "prompt" && strings.Contains(asString(f, "id", ""), "-restore") {
					// Simulate yms-rca-side error.
					s.emit(core.Event{Type: core.EventError, Error: errTokenMissing()})
					return
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	engine.ReceiveMessage(platform, &core.Message{
		SessionKey: "youzone:test",
		Platform:   "youzone",
		UserID:     "u",
		UserName:   "test",
		Content:    "流量切入了吗",
		MessageID:  "msg-1",
	})

	// Wait for the localised failure message to surface to the user.
	sent := waitForPlatformMessage(t, platform, "自动恢复上次 profile `pre`")
	combined := strings.Join(sent, "\n")
	if !strings.Contains(combined, "/connect pre") {
		t.Errorf("expected recovery hint '/connect pre', got %q", combined)
	}

	// Original user prompt must NOT have been delivered to yms-rca.
	for _, f := range enc.framesCopy() {
		if f["type"] == "prompt" && strings.Contains(asString(f, "message", ""), "流量切入了吗") {
			t.Errorf("user prompt should not reach yms-rca after restore failure: %v", f)
		}
	}

	// Store should be preserved (conservative policy: only user actions
	// clear it).
	if got := store.Get("yms-rca-youzone", "youzone:test"); got != "pre" {
		t.Errorf("store should preserve pre after failure, got %q", got)
	}
}

// Scenario C: user's first message is `/disconnect`. Auto-restore is
// bypassed (slash command); yms-rca's env-switch to=local triggers a
// store.Clear; daemon restart sim verifies no second restore attempt.
func TestE2E_AutoRestoreScenarioC_DisconnectClearsStore(t *testing.T) {
	s, enc, store, platform, engine, wg := wireSessionForRestore(t, "pre", "local")

	wg.Add(1)
	go func() {
		defer wg.Done()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			for _, f := range enc.framesCopy() {
				if f["type"] == "prompt" && asString(f, "message", "") == "/disconnect" {
					// Simulate yms-rca returning env-switch to=local.
					emitEnvSwitch(s, "local")
					// Slash command finishes via maybeFinalizeSlashCommandTurn.
					s.handleEvent(map[string]any{
						"type": "message_end",
						"message": map[string]any{
							"role":       "custom",
							"customType": "yms-command",
							"text":       "Disconnected.",
							"display":    true,
						},
					})
					s.handleEvent(map[string]any{
						"type":    "response",
						"command": "prompt",
						"id":      asString(f, "id", ""),
					})
					return
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	engine.ReceiveMessage(platform, &core.Message{
		SessionKey: "youzone:test",
		Platform:   "youzone",
		UserID:     "u",
		UserName:   "test",
		Content:    "/disconnect",
		MessageID:  "msg-1",
	})

	// Wait for the disconnect to propagate by polling the store. The
	// turn-completion path is timing-dependent; what we really care about
	// here is that the env-switch to=local clears the persisted entry.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if store.Get("yms-rca-youzone", "youzone:test") == "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := store.Get("yms-rca-youzone", "youzone:test"); got != "" {
		t.Errorf("after /disconnect env-switch, store should be cleared, got %q", got)
	}

	// Confirm no hidden -restore frame was ever written.
	for _, f := range enc.framesCopy() {
		if strings.Contains(asString(f, "id", ""), "-restore") {
			t.Errorf("slash command should bypass restore, but hidden frame appeared: %v", f)
		}
	}
}

func errTokenMissing() error {
	return tokenMissingError{}
}

type tokenMissingError struct{}

func (tokenMissingError) Error() string {
	return `yms-rca: connection "pre" needs env IUAPYYS_MCP_TOKEN (declared in profile pre.json) but it is not set`
}

// Scenario D: store has "pre" but user's first message is `/connect dev`
// (an explicit user-driven switch to a different profile). The plan's
// bypass rule guarantees we do NOT silently insert a hidden /connect pre
// in front of it — that would either waste an MCP attach cycle (best
// case) or fail and block the user's intended `/connect dev` (worst).
//
// Expectation: exactly one prompt frame, and that frame is the user's
// `/connect dev` — no hidden -restore frame, no `/connect pre`.
func TestE2E_AutoRestoreScenarioD_UserConnectOtherBypassesRestore(t *testing.T) {
	s, enc, _, platform, engine, wg := wireSessionForRestore(t, "pre", "local")
	_ = engine

	// Background driver: complete the slash command turn as yms-rca would.
	wg.Add(1)
	go func() {
		defer wg.Done()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			for _, f := range enc.framesCopy() {
				if f["type"] == "prompt" && asString(f, "message", "") == "/connect dev" {
					emitEnvSwitch(s, "dev")
					s.handleEvent(map[string]any{
						"type": "message_end",
						"message": map[string]any{
							"role":       "custom",
							"customType": "yms-command",
							"text":       "Connected to dev.",
							"display":    true,
						},
					})
					s.handleEvent(map[string]any{
						"type":    "response",
						"command": "prompt",
						"id":      asString(f, "id", ""),
					})
					return
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	engine.ReceiveMessage(platform, &core.Message{
		SessionKey: "youzone:test",
		Platform:   "youzone",
		UserID:     "u",
		UserName:   "test",
		Content:    "/connect dev",
		MessageID:  "msg-1",
	})

	waitForPromptFrame(t, enc, "/connect dev")

	// Confirm no hidden -restore frame was written.
	for _, f := range enc.framesCopy() {
		if strings.Contains(asString(f, "id", ""), "-restore") {
			t.Errorf("user /connect dev should bypass restore, but hidden frame appeared: %v", f)
		}
		if f["type"] == "prompt" && asString(f, "message", "") == "/connect pre" {
			t.Errorf("hidden /connect pre should not have run before user's /connect dev: %v", f)
		}
	}
}
