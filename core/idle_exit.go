package core

import (
	"log/slog"
	"time"
)

// SetIdleExit enables automatic termination of idle interactive agent
// subprocesses (idle_exit_mins). After d of inactivity the agent subprocess
// is closed to release its memory; the conversation itself is preserved and
// the next incoming message transparently resumes it via the saved agent
// session ID. A zero or negative duration leaves idle exit disabled.
func (e *Engine) SetIdleExit(d time.Duration) {
	if d <= 0 {
		return
	}
	e.idleExit = d
	go e.runInteractiveIdleReaper()
}

func (e *Engine) runInteractiveIdleReaper() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-e.ctx.Done():
			return
		case <-ticker.C:
			e.reapIdleInteractiveSessions()
		}
	}
}

// reapIdleInteractiveSessions closes agent subprocesses of interactive
// sessions that have been inactive longer than idleExit. Sessions with an
// in-flight turn (foreground or unsolicited), a pending permission request,
// or queued messages are never reaped.
func (e *Engine) reapIdleInteractiveSessions() {
	if e.idleExit <= 0 {
		return
	}
	cutoff := time.Now().Add(-e.idleExit)

	type target struct {
		key   string
		state *interactiveState
	}
	var targets []target

	e.interactiveMu.Lock()
	for key, state := range e.interactiveStates {
		state.mu.Lock()
		alive := state.agentSession != nil && state.agentSession.Alive()
		if state.lastActivityAt.IsZero() {
			// State predates activity tracking (or was created without a
			// turn): start the idle clock now instead of reaping immediately.
			state.lastActivityAt = time.Now()
		}
		idle := !state.isBusyLocked() && state.lastActivityAt.Before(cutoff)
		state.mu.Unlock()
		if alive && idle {
			targets = append(targets, target{key: key, state: state})
		}
	}
	e.interactiveMu.Unlock()

	for _, t := range targets {
		// Re-check right before closing: a turn may have started while an
		// earlier target was being closed (Close can block for seconds).
		t.state.mu.Lock()
		busy := t.state.isBusyLocked()
		t.state.mu.Unlock()
		if busy {
			continue
		}
		slog.Info("idle exit: closing idle agent subprocess, next message resumes the conversation",
			"session_key", t.key, "idle_timeout", e.idleExit)
		e.cleanupInteractiveState(t.key, t.state)
	}
}

// isBusyLocked reports whether the session has an in-flight turn, a pending
// permission request, or queued messages. Callers must hold st.mu.
func (st *interactiveState) isBusyLocked() bool {
	return st.activeTurns > 0 || st.pending != nil || len(st.pendingMessages) > 0
}

// beginTurn marks the start of a foreground or unsolicited agent turn for
// idle-exit tracking.
func (st *interactiveState) beginTurn() {
	st.mu.Lock()
	st.activeTurns++
	st.lastActivityAt = time.Now()
	st.mu.Unlock()
}

// endTurn marks the end of a turn started with beginTurn.
func (st *interactiveState) endTurn() {
	st.mu.Lock()
	if st.activeTurns > 0 {
		st.activeTurns--
	}
	st.lastActivityAt = time.Now()
	st.mu.Unlock()
}
