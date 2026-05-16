package ymsagent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// runInternalPromptAsync calls runInternalPrompt in a goroutine and returns
// a channel for the eventual error. Used to drive event injection in tests.
func runInternalPromptAsync(t *testing.T, s *session, prompt, profile string, timeout time.Duration) <-chan error {
	t.Helper()
	out := make(chan error, 1)
	go func() {
		out <- s.runInternalPrompt(context.Background(), prompt, profile, timeout)
	}()
	// Give the goroutine a tick to flip internalActive and write the frame.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if s.internalActive.Load() {
			return out
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("internalActive did not become true within 200ms")
	return out
}

func TestRunInternalPromptSuppressesText(t *testing.T) {
	s, enc := newTestSession(t, "default")

	done := runInternalPromptAsync(t, s, "/connect pre", "pre", 2*time.Second)

	// Simulate yms-rca turn: text, profile env-switch, then final Result.
	s.emit(core.Event{Type: core.EventText, Content: "Connected to pre"})
	s.emit(core.Event{Type: core.EventText, Content: "*profile: pre*"})
	// Profile must update so verification at end of runInternalPrompt passes.
	s.updateCurrentProfile("pre")
	s.emit(core.Event{Type: core.EventResult, Done: true})

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runInternalPrompt = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runInternalPrompt did not return")
	}

	// No user-visible events should have been emitted.
	events := drainEvents(t, s, 50*time.Millisecond)
	if len(events) != 0 {
		t.Errorf("hidden turn leaked %d events: %+v", len(events), events)
	}

	// Hidden prompt frame should be present with the -restore id.
	frames := enc.framesCopy()
	if len(frames) != 1 {
		t.Fatalf("want 1 frame written, got %d", len(frames))
	}
	if got := frames[0]["message"]; got != "/connect pre" {
		t.Errorf("frame message = %v, want /connect pre", got)
	}
	id, _ := frames[0]["id"].(string)
	if id == "" || !contains(id, "-restore") {
		t.Errorf("expected hidden prompt id to contain '-restore', got %q", id)
	}

	// internalActive should be cleared after return.
	if s.internalActive.Load() {
		t.Error("internalActive still true after return")
	}
}

func TestRunInternalPromptCapturesError(t *testing.T) {
	s, _ := newTestSession(t, "default")

	done := runInternalPromptAsync(t, s, "/connect pre", "pre", 2*time.Second)

	wantErr := errors.New(`connection "pre" needs env IUAPYYS_MCP_TOKEN`)
	s.emit(core.Event{Type: core.EventError, Error: wantErr})

	select {
	case err := <-done:
		if err == nil || !contains(err.Error(), "IUAPYYS_MCP_TOKEN") {
			t.Fatalf("runInternalPrompt err = %v, want IUAPYYS_MCP_TOKEN", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runInternalPrompt did not return")
	}
}

func TestRunInternalPromptTimesOut(t *testing.T) {
	s, _ := newTestSession(t, "default")

	done := runInternalPromptAsync(t, s, "/connect pre", "pre", 50*time.Millisecond)
	// Don't emit anything; let timeout fire.

	select {
	case err := <-done:
		if err == nil || !contains(err.Error(), "timeout") {
			t.Fatalf("runInternalPrompt err = %v, want timeout", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("runInternalPrompt did not time out")
	}
}

func TestRunInternalPromptAutoDeniesPermissionRequest(t *testing.T) {
	s, enc := newTestSession(t, "default")
	// Register a pending so resolvePendingConfirm has something to ack.
	s.registerPending("req-7", "MCP attach approval?")

	done := runInternalPromptAsync(t, s, "/connect pre", "pre", time.Second)

	s.emit(core.Event{Type: core.EventPermissionRequest, RequestID: "req-7", ToolName: "approve"})

	select {
	case err := <-done:
		if err == nil || !contains(err.Error(), "permission") {
			t.Fatalf("runInternalPrompt err = %v, want permission-related error", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runInternalPrompt did not return after permission request")
	}

	// Confirm an extension_ui_response was written with confirmed:false so
	// yms-rca subprocess doesn't hang.
	frames := enc.framesCopy()
	found := false
	for _, f := range frames {
		if f["type"] == "extension_ui_response" && f["id"] == "req-7" {
			if confirmed, ok := f["confirmed"].(bool); ok && !confirmed {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("expected extension_ui_response confirmed:false frame, got: %+v", frames)
	}
}

func TestRunInternalPromptResetsLatchesAfterCompletion(t *testing.T) {
	s, _ := newTestSession(t, "default")
	// Pre-set some latches to make sure they get reset.
	s.turnResultEmitted.Store(true)
	s.promptAcked.Store(true)
	s.assistantMessageEnded.Store(true)

	done := runInternalPromptAsync(t, s, "/connect pre", "pre", time.Second)
	s.updateCurrentProfile("pre")
	s.emit(core.Event{Type: core.EventResult, Done: true})

	<-done

	// After hidden turn, latches should be cleared so the upcoming user
	// turn starts fresh.
	if s.turnResultEmitted.Load() {
		t.Error("turnResultEmitted not reset after hidden turn")
	}
	if s.promptAcked.Load() {
		t.Error("promptAcked not reset after hidden turn")
	}
	if s.assistantMessageEnded.Load() {
		t.Error("assistantMessageEnded not reset after hidden turn")
	}
}

func TestRunInternalPromptVerifiesProfileApplied(t *testing.T) {
	s, _ := newTestSession(t, "default")

	done := runInternalPromptAsync(t, s, "/connect pre", "pre", time.Second)
	// Result fires WITHOUT updateCurrentProfile, so the verify-after-success
	// check must fail.
	s.emit(core.Event{Type: core.EventResult, Done: true})

	select {
	case err := <-done:
		if err == nil || !contains(err.Error(), "did not switch") {
			t.Fatalf("expected verify-failed error, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runInternalPrompt did not return")
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

// ── maybeRestoreProfileBeforePrompt ────────────────────────────────

func TestMaybeRestoreSkipsWhenNoPersistedProfile(t *testing.T) {
	s, _ := newTestSessionWithStore(t, "p", "k")
	// store empty
	if err := s.maybeRestoreProfileBeforePrompt(context.Background(), "流量切入了吗"); err != nil {
		t.Errorf("with empty store want nil, got %v", err)
	}
	// second call returns nil (restoreAttempted latched)
	if err := s.maybeRestoreProfileBeforePrompt(context.Background(), "again"); err != nil {
		t.Errorf("second call want nil, got %v", err)
	}
}

func TestMaybeRestoreSkipsWhenSlashCommand(t *testing.T) {
	s, store := newTestSessionWithStore(t, "p", "k")
	store.Set("p", "k", "pre")

	for _, prompt := range []string{"/connect dev", "/disconnect", "/status", "  /help  "} {
		s.restoreAttempted.Store(false) // reset between cases
		err := s.maybeRestoreProfileBeforePrompt(context.Background(), prompt)
		if err != nil {
			t.Errorf("slash command %q should bypass restore, got err %v", prompt, err)
		}
		// no hidden frame should have been written
		if s.internalActive.Load() {
			t.Errorf("internalActive set for slash command %q", prompt)
		}
	}
}

func TestMaybeRestoreSkipsWhenAlreadyConnected(t *testing.T) {
	s, store := newTestSessionWithStore(t, "p", "k")
	store.Set("p", "k", "pre")
	s.currentProfile.Store("pre") // already connected in same-process recycle case

	if err := s.maybeRestoreProfileBeforePrompt(context.Background(), "流量切入了吗"); err != nil {
		t.Errorf("currentProfile=pre should skip restore, got %v", err)
	}
}

func TestMaybeRestoreRunsOnceOnly(t *testing.T) {
	s, store := newTestSessionWithStore(t, "p", "k")
	store.Set("p", "k", "pre")
	s.restoreAttempted.Store(true) // simulate "already attempted"

	if err := s.maybeRestoreProfileBeforePrompt(context.Background(), "流量切入了吗"); err != nil {
		t.Errorf("second-time call should be no-op, got %v", err)
	}
}

func TestMaybeRestoreInvokesHiddenConnect(t *testing.T) {
	s, store := newTestSessionWithStore(t, "p", "k")
	store.Set("p", "k", "pre")

	// Drive the hidden turn from another goroutine: when internalActive
	// flips on, simulate yms-rca completing the /connect successfully.
	go func() {
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			if s.internalActive.Load() {
				s.updateCurrentProfile("pre")
				s.emit(core.Event{Type: core.EventResult, Done: true})
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()

	err := s.maybeRestoreProfileBeforePrompt(context.Background(), "流量切入了吗")
	if err != nil {
		t.Fatalf("maybeRestore returned %v, want nil", err)
	}
	if got := s.currentProfileName(); got != "pre" {
		t.Errorf("after restore currentProfileName = %q, want pre", got)
	}
}

func TestMaybeRestoreFailureEmitsLocalisedText(t *testing.T) {
	cases := []struct {
		name       string
		userPrompt string
		extraEnv   []string
		wantSub    string
	}{
		{"zh", "流量切入了吗", nil, "自动恢复上次 profile `pre` 失败"},
		{"en", "did traffic cut in?", nil, "Auto-restore of last profile `pre` failed"},
		{"ja", "プレ環境のトラフィック切替", nil, "前回の profile `pre`"},
		{"es", "¿se conectó la pre?", nil, "restauración automática del último profile `pre`"},
		// zh-TW unreachable via DetectLanguage; opt-in via CC_LANG override.
		// Prompt is intentionally non-Chinese so DetectLanguage would say
		// English — only CC_LANG can promote to zh-TW.
		{"zh-TW via CC_LANG", "test", []string{"CC_LANG=zh-TW"}, "自動恢復上次 profile `pre` 失敗"},
		// POSIX LANG fallback also honored.
		{"ja via LANG", "test", []string{"LANG=ja_JP.UTF-8"}, "前回の profile `pre`"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, store := newTestSessionWithStore(t, "p", "k")
			store.Set("p", "k", "pre")
			s.extraEnv = tc.extraEnv

			// Drive a failure from another goroutine.
			go func() {
				deadline := time.Now().Add(time.Second)
				for time.Now().Before(deadline) {
					if s.internalActive.Load() {
						s.emit(core.Event{Type: core.EventError, Error: errors.New("token missing")})
						return
					}
					time.Sleep(2 * time.Millisecond)
				}
			}()

			err := s.maybeRestoreProfileBeforePrompt(context.Background(), tc.userPrompt)
			if err == nil {
				t.Fatalf("want error, got nil")
			}
			// The user-facing localised text is embedded in the error.
			// Engine wraps it with MsgError when delivering to the platform.
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error message = %q, want substring %q", err.Error(), tc.wantSub)
			}
			if !strings.Contains(err.Error(), "/connect pre") {
				t.Errorf("expected '/connect pre' recovery hint, got %q", err.Error())
			}
		})
	}
}

func TestMaybeRestoreRejectsInvalidStoredProfile(t *testing.T) {
	s, store := newTestSessionWithStore(t, "p", "k")
	// Bypass the Set guard by writing directly to the in-memory map. This
	// simulates a hand-edited store file or a legacy entry surviving load.
	store.mu.Lock()
	store.data.Projects = map[string]map[string]profileEntry{
		"p": {"k": {Profile: "bad name", UpdatedAt: "now"}},
	}
	store.mu.Unlock()

	err := s.maybeRestoreProfileBeforePrompt(context.Background(), "流量切入了吗")
	if err != nil {
		t.Errorf("invalid profile should be silently skipped (warn + clear), got %v", err)
	}
	// The invalid entry should now be cleared from the store.
	if got := store.Get("p", "k"); got != "" {
		t.Errorf("invalid entry should be cleared, store has %q", got)
	}
}

// ── Noise-event suppression during hidden turn ────────────────────────

// emitAndCheckDropped emits an event during an active internal turn and
// asserts that the user-facing events channel sees nothing. The hidden
// turn is left open so the test driver can keep emitting; cleanup ends it.
func TestRunInternalPromptSuppressesNoiseEventTypes(t *testing.T) {
	cases := []core.Event{
		{Type: core.EventText, Content: "Connected to pre"},
		{Type: core.EventThinking, Content: "thinking..."},
		{Type: core.EventToolUse, ToolName: "mcp_attach", ToolInput: "{}"},
		{Type: core.EventToolResult, ToolName: "mcp_attach", ToolResult: "ok"},
	}
	for _, evt := range cases {
		t.Run(string(evt.Type), func(t *testing.T) {
			s, _ := newTestSession(t, "default")

			done := runInternalPromptAsync(t, s, "/connect pre", "pre", 2*time.Second)

			s.emit(evt)

			// Confirm nothing leaked to user channel.
			leaked := drainEvents(t, s, 30*time.Millisecond)
			if len(leaked) != 0 {
				t.Errorf("event type %s leaked: %+v", evt.Type, leaked)
			}

			// End the hidden turn cleanly so the goroutine returns.
			s.updateCurrentProfile("pre")
			s.emit(core.Event{Type: core.EventResult, Done: true})
			<-done
		})
	}
}

// ── Send() integration: busy releasing on restore failure ────────────

// TestSendReleasesBusyOnRestoreFailure pins the highest-impact contract:
// when auto-restore fails inside Send(), the busy flag MUST be cleared,
// otherwise every subsequent Send would be rejected with "previous turn
// still running" and the session hangs until subprocess restart.
func TestSendReleasesBusyOnRestoreFailure(t *testing.T) {
	s, store := newTestSessionWithStore(t, "p", "k")
	store.Set("p", "k", "pre")

	// Drive an immediate failure from the hidden turn.
	go func() {
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			if s.internalActive.Load() {
				s.emit(core.Event{Type: core.EventError, Error: errors.New("token missing")})
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()

	err := s.Send("流量切入了吗", nil, nil)
	if err == nil {
		t.Fatalf("Send should fail when restore fails, got nil")
	}
	if !strings.Contains(err.Error(), "自动恢复") && !strings.Contains(err.Error(), "Auto-restore") {
		t.Errorf("error should be the localised auto-restore message, got %q", err.Error())
	}

	// CRITICAL: busy must be released so the next Send can proceed.
	if s.busy.Load() {
		t.Fatal("busy still true after restore failure — would hang next Send")
	}

	// Also confirm: no user prompt frame was written to yms-rca. Only the
	// hidden /connect frame should have appeared.
	// (Note: writeFrame routes through mockEncoder in tests.)
	// We can't see frames from here (enc isn't returned by newTestSessionWithStore),
	// but the busy assertion above is the user-visible contract.
}

// TestSendAfterRestoreFailureCanRetry exercises the recovery flow that
// motivates the busy-release contract: user gets the failure message,
// fixes the env, and re-issues the prompt. The second Send must NOT be
// rejected.
func TestSendAfterRestoreFailureCanRetry(t *testing.T) {
	s, store := newTestSessionWithStore(t, "p", "k")
	store.Set("p", "k", "pre")

	go func() {
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			if s.internalActive.Load() {
				s.emit(core.Event{Type: core.EventError, Error: errors.New("first attempt fails")})
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	if err := s.Send("first prompt", nil, nil); err == nil {
		t.Fatalf("first Send should fail")
	}

	// restoreAttempted is now true, so the second Send bypasses restore
	// and writes the user prompt directly. busy must allow it through.
	if err := s.Send("second prompt after fixup", nil, nil); err != nil {
		t.Fatalf("second Send rejected: %v — busy was not released?", err)
	}
}

// ── Same-process recycle: snapshot profile bypass ────────────────────

// TestSameProcessRecycleSkipsRestore verifies that when newSession's
// snapshot carries a non-local profile (the same-process session recycle
// path), the very first Send bypasses auto-restore. This is the safety
// claim in the plan's §"短路条件的安全性".
func TestSameProcessRecycleSkipsRestore(t *testing.T) {
	s, store, _, enc := newSessionWithEncoder(t)
	store.Set("p", "k", "pre")
	s.profileStore = store
	s.project = "p"
	s.sessionKey = "k"
	// Simulate the recycle path: newSession set currentProfile to "pre"
	// from snapshot before the first Send.
	s.currentProfile.Store("pre")

	if err := s.Send("普通问题", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Only the user prompt frame — no hidden /connect.
	frames := enc.framesCopy()
	if len(frames) != 1 {
		t.Fatalf("want 1 frame (user prompt only), got %d: %+v", len(frames), frames)
	}
	if strings.Contains(asString(frames[0], "id", ""), "-restore") {
		t.Errorf("recycle should skip hidden /connect, but found -restore frame: %v", frames[0])
	}
	if !strings.Contains(asString(frames[0], "message", ""), "普通问题") {
		t.Errorf("expected user prompt to be written directly, got %v", frames[0])
	}
}

// newSessionWithEncoder is like newTestSessionWithStore but also returns
// the mockEncoder so callers can assert frames written.
func newSessionWithEncoder(t *testing.T) (*session, *profileStore, string, *mockEncoder) {
	t.Helper()
	s, enc := newTestSession(t, "default")
	dir := t.TempDir()
	path := dir + "/store.json"
	store := newProfileStore(path)
	return s, store, path, enc
}

// ── Hidden turn ctx cancellation ──────────────────────────────────────

func TestRunInternalPromptHonorsContextCancellation(t *testing.T) {
	s, _ := newTestSession(t, "default")
	ctx, cancel := context.WithCancel(context.Background())

	out := make(chan error, 1)
	go func() { out <- s.runInternalPrompt(ctx, "/connect pre", "pre", 5*time.Second) }()

	// Wait for internal turn to start.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) && !s.internalActive.Load() {
		time.Sleep(2 * time.Millisecond)
	}
	cancel()

	select {
	case err := <-out:
		if err == nil {
			t.Fatal("want non-nil error from cancelled ctx")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("want context.Canceled, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runInternalPrompt did not return after ctx cancel")
	}
}

// ── Double signal safety ───────────────────────────────────────────────

func TestSignalInternalDoneDoesNotDeadlockOnDoubleSignal(t *testing.T) {
	s, _ := newTestSession(t, "default")
	done := make(chan error, 1)
	s.internalDone = done
	s.signalInternalDone(nil)
	// Second call must not block (select-default in implementation).
	doneCh := make(chan struct{})
	go func() {
		s.signalInternalDone(errors.New("ignored"))
		close(doneCh)
	}()
	select {
	case <-doneCh:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("second signalInternalDone blocked")
	}
	// First signal wins.
	if err := <-done; err != nil {
		t.Errorf("first signal should win (nil), got %v", err)
	}
}

