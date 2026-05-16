package ymsagent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// defaultAutoRestoreTimeout caps how long a hidden /connect <profile> turn
// may take before runInternalPrompt gives up. 30s is generous enough for
// MCP attach + token negotiation but tight enough that a stuck subprocess
// surfaces as a user-visible error rather than a silent hang.
const defaultAutoRestoreTimeout = 30 * time.Second

// runInternalPrompt drives a hidden yms-rca turn — e.g. an auto-restore
// `/connect <profile>` — without surfacing its events to the user.
//
// Lifecycle:
//
//  1. Set internalActive=true and install a result channel.
//  2. Reset all turn-level latches so the hidden turn starts clean.
//  3. Write the hidden prompt frame with a `-restore` suffixed id so a
//     stale response/prompt ack from the prior turn can't influence state.
//  4. Wait for: EventResult (success), EventError (failure), permission
//     request (auto-deny + failure), ctx cancel, or timeout.
//  5. Clear internalActive and the channel.
//  6. Reset turn-level latches again so the upcoming user turn is fresh.
//  7. On success, verify currentProfileName matches the requested profile.
//
// The caller must hold session.busy=true across this call AND the user
// prompt that follows — runInternalPrompt does not toggle busy.
func (s *session) runInternalPrompt(ctx context.Context, prompt, expectProfile string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = defaultAutoRestoreTimeout
	}

	done := make(chan error, 1)
	s.internalMu.Lock()
	s.internalDone = done
	s.internalMu.Unlock()
	s.internalActive.Store(true)

	// Reset turn-level latches so handlers don't read stale state from
	// the prior turn.
	s.resetTurnLatches(prompt)

	id := fmt.Sprintf("cc-%d-restore", atomic.AddUint64(&s.seq, 1))
	s.currentPromptID.Store(id)

	frame := map[string]any{
		"type":    "prompt",
		"id":      id,
		"message": prompt,
	}

	if err := s.writeFrame(frame); err != nil {
		s.endInternalTurn()
		return fmt.Errorf("yms-rca: write hidden prompt: %w", err)
	}

	var result error
	select {
	case result = <-done:
	case <-ctx.Done():
		result = ctx.Err()
	case <-time.After(timeout):
		result = fmt.Errorf("yms-rca: auto-restore timeout after %s", timeout)
	}

	s.endInternalTurn()

	if result != nil {
		return result
	}

	// Verify the hidden /connect actually switched the profile. yms-rca
	// emits an EventResult even when the connection failed silently
	// (e.g. profile not found), so we double-check via the env-switch
	// side effect.
	if got := s.currentProfileName(); got != expectProfile {
		return fmt.Errorf("yms-rca: auto-restore did not switch to %q (current: %q)", expectProfile, got)
	}
	return nil
}

// endInternalTurn flips the active flag off, clears the result channel,
// and resets latches so the upcoming user turn starts fresh.
func (s *session) endInternalTurn() {
	s.internalActive.Store(false)
	s.internalMu.Lock()
	s.internalDone = nil
	s.internalMu.Unlock()
	// Caller will write the user prompt next; reset latches now so the
	// stale state from the hidden turn doesn't leak into it. The Send
	// path also resets, but that happens BEFORE Send decides to run a
	// hidden turn — so we need to reset here, between turns.
	s.resetTurnLatches("")
}

// signalInternalDone delivers a hidden-turn result. Safe to call from any
// goroutine; only the first signal per turn is delivered.
func (s *session) signalInternalDone(err error) {
	s.internalMu.Lock()
	ch := s.internalDone
	s.internalMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- err:
	default:
		// already signalled — first wins
	}
}

// handleInternalEvent decides what to do with an event observed during
// an active hidden turn. EventText / EventThinking / EventToolUse /
// EventToolResult are dropped; EventError / EventResult / EventPermission
// Request all terminate the hidden turn.
//
// EventPermissionRequest is special: auto-restore cannot prompt the user,
// so we must (a) write extension_ui_response confirmed:false back to the
// yms-rca subprocess so it unblocks, and (b) end the turn with an error.
func (s *session) handleInternalEvent(evt core.Event) {
	switch evt.Type {
	case core.EventResult:
		s.signalInternalDone(nil)
	case core.EventError:
		err := evt.Error
		if err == nil {
			err = errors.New("yms-rca: auto-restore failed (no detail)")
		}
		s.signalInternalDone(err)
	case core.EventPermissionRequest:
		if evt.RequestID != "" {
			// nil emit — we don't want any user-visible reason text leaking
			// during a hidden turn.
			s.resolvePendingConfirm(evt.RequestID, false, "", nil)
		}
		s.signalInternalDone(errors.New("yms-rca: auto-restore cannot prompt user for permission"))
	default:
		// EventText, EventThinking, EventToolUse, EventToolResult — dropped.
		slog.Debug("yms-rca: suppressing event during hidden turn", "type", evt.Type)
	}
}

// resetTurnLatches resets all turn-level state that emit handlers consult.
// Called at the start of each turn (user or hidden). When prompt is "" the
// slash-command latch defaults to false.
func (s *session) resetTurnLatches(prompt string) {
	s.turnResultEmitted.Store(false)
	s.promptAcked.Store(false)
	s.slashCommandEnded.Store(false)
	s.assistantMessageEnded.Store(false)
	s.awaitingPostToolSummary.Store(false)
	s.postToolTextEmitted.Store(false)
	s.currentPromptSlashCommand.Store(prompt != "" && isSlashCommandPrompt(prompt))
	s.turnTextEmitted.Store(false)
	s.resetAssistantText()
	s.clearPendingTurnResult()
}

// maybeRestoreProfileBeforePrompt checks the persisted profile for this
// (project, session_key) and — if non-local and currently disconnected —
// runs a hidden `/connect <profile>` so the user's first message after a
// daemon restart lands on the right MCP profile.
//
// Bypasses (no restore attempted, no error returned):
//
//   - already attempted once this session (restoreAttempted latch).
//   - user prompt is itself a slash command — /connect, /disconnect,
//     /status, /help etc.: the user's explicit intent wins; we don't
//     want to insert an MCP attach/detach cycle in front of it.
//   - session already has a non-local currentProfile (same-process
//     recycle scenario where snapshot carried the previous profile).
//   - no project / session_key (programmatic test path; no relay).
//   - no profileStore wired.
//   - store has no entry, or entry is "local".
//
// If the stored profile name fails character-set validation, we clear
// the entry and skip — depth-in-defense against hand-edited store files.
//
// On success returns nil and the session's currentProfileName matches the
// stored profile. On failure returns a wrapped error; the caller is
// expected to surface it to the user and NOT send the original prompt.
func (s *session) maybeRestoreProfileBeforePrompt(ctx context.Context, prompt string) error {
	if !s.restoreAttempted.CompareAndSwap(false, true) {
		return nil
	}
	// User's slash command always wins. ParseConnectTarget is a stricter
	// subset of isSlashCommandPrompt and is checked implicitly here.
	if isSlashCommandPrompt(prompt) {
		return nil
	}
	// Same-process recycle: cc-connect already knows we're on a non-local
	// profile, so the subprocess is freshly /connect'd as part of
	// newSession recycle handling.
	if cur := s.currentProfileName(); cur != "" && cur != "local" {
		return nil
	}
	if s.profileStore == nil || s.project == "" || s.sessionKey == "" {
		return nil
	}
	profile := s.profileStore.Get(s.project, s.sessionKey)
	if profile == "" || profile == "local" {
		return nil
	}
	if !isValidProfileName(profile) {
		slog.Warn("yms-rca: stored profile name invalid, clearing entry",
			"project", s.project, "profile", profile)
		s.profileStore.Clear(s.project, s.sessionKey)
		return nil
	}
	// Pre-flight env-var check so a stale token in the store surfaces
	// a clear error without spending a yms-rca turn.
	if s.cfg != nil {
		if err := s.cfg.validateConnectionTokenEnv(profile); err != nil {
			return fmt.Errorf("yms-rca: auto-restore profile %q failed: %w", profile, err)
		}
	}
	if err := s.runInternalPrompt(ctx, "/connect "+profile, profile, defaultAutoRestoreTimeout); err != nil {
		// Return the localised user-facing message as the Go error itself.
		// Engine wraps Send() errors with i18n MsgError ("❌ 错误: %s") and
		// delivers a single message to the platform, then aborts the turn —
		// any EventText pushed to s.events at this point is discarded by
		// engine.go's pendingSend handler, so we put everything the user
		// needs into the error string.
		return errors.New(renderAutoRestoreFailure(s.extraEnv, prompt, profile, err))
	}
	return nil
}

var _ = sync.Once{} // retained for future refactors
