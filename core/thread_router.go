package core

import (
	"fmt"
	"sync"
)

const defaultMaxConcurrent = 3

// threadKey uniquely identifies a thread within a base session key space.
// It combines the base session key (e.g. "slack:C123") with the platform
// thread ID (e.g. Slack thread_ts) to allow independent routing per channel.
func threadKey(baseKey, threadID string) string {
	return baseKey + "|" + threadID
}

// forkedInfo records the provenance of a forked session key so it can be
// cleaned up when the session expires.
type forkedInfo struct {
	baseKey  string
	threadID string
}

// ThreadRouter implements thread-aware session routing for a project binding.
//
// Design: context-switch first, fork on contention.
//
//	Message arrives → check thread affinity
//	  → Thread has existing session? Route to it.
//	  → No affinity yet + base session idle? Context-switch: map thread → base, use base.
//	  → No affinity yet + base session busy + below max_concurrent? Fork new session.
//	  → At max_concurrent? Fall back to base (message will be queued).
//
// A single ThreadRouter handles all channels/users for a project binding.
// max_concurrent is enforced per base session key (per channel) so that
// unrelated channels do not compete for the same concurrency budget.
// Forked sessions are locked to their originating thread; only the base session
// participates in context-switching between threads.
type ThreadRouter struct {
	mu sync.Mutex

	// threadToKey maps threadKey(baseKey, threadID) → effective session key.
	threadToKey map[string]string

	// forkedKeys maps effective session key → forkedInfo (base key + thread ID).
	// Only contains entries for forked (non-base) sessions.
	forkedKeys map[string]forkedInfo

	maxConcurrent int

	// clientMsgDedup prevents double-processing of Slack "also send to channel"
	// events, which fire two events sharing the same client_msg_id.
	clientMsgDedup MessageDedup
}

// NewThreadRouter creates a ThreadRouter for a project binding.
// maxConcurrent is the ceiling on simultaneous mid-turn sessions per channel
// (base session key); it defaults to 3 when <= 0.
func NewThreadRouter(maxConcurrent int) *ThreadRouter {
	if maxConcurrent <= 0 {
		maxConcurrent = defaultMaxConcurrent
	}
	return &ThreadRouter{
		threadToKey:   make(map[string]string),
		forkedKeys:    make(map[string]forkedInfo),
		maxConcurrent: maxConcurrent,
	}
}

// IsDuplicateClientMsg reports whether clientMsgID was already seen within the
// dedup TTL window. Empty clientMsgID is never considered a duplicate.
func (r *ThreadRouter) IsDuplicateClientMsg(clientMsgID string) bool {
	return r.clientMsgDedup.IsDuplicate(clientMsgID)
}

// RouteResult carries the outcome of a Route call.
type RouteResult struct {
	// EffectiveKey is the session key the message should be processed under.
	EffectiveKey string
	// Forked is true when a brand-new parallel session was allocated for this thread.
	// The caller must ensure the session is created via sessions.GetOrCreateActive.
	Forked bool
	// ForkWarning is a user-visible message sent to the thread when Forked is true.
	ForkWarning string
}

// Route resolves the effective session key for an incoming message.
//
//   - baseKey is msg.SessionKey (e.g. "slack:C123" or "slack:C123:U456").
//   - threadID is msg.ThreadID (Slack thread_ts or message ts for top-level messages).
//   - sessions is consulted to check whether the base session is mid-turn.
//
// When threadID is empty the base key is returned unchanged (backward-compatible
// with platforms that do not set ThreadID).
func (r *ThreadRouter) Route(baseKey, threadID string, sessions *SessionManager) RouteResult {
	if threadID == "" {
		return RouteResult{EffectiveKey: baseKey}
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	tk := threadKey(baseKey, threadID)

	// 1. Existing affinity — route to the already-assigned session.
	if existing, ok := r.threadToKey[tk]; ok {
		return RouteResult{EffectiveKey: existing}
	}

	// 2. No affinity yet.  Check whether the base session is currently mid-turn.
	if base := sessions.PeekActive(baseKey); base == nil || !base.IsBusy() {
		// Base session is idle (or not yet created): context-switch into it.
		r.threadToKey[tk] = baseKey
		return RouteResult{EffectiveKey: baseKey}
	}

	// 3. Base is busy.  Count how many sessions for this base key are mid-turn.
	midTurn := 1 // base session counts as one
	for fk, fi := range r.forkedKeys {
		if fi.baseKey != baseKey {
			continue
		}
		if s := sessions.PeekActive(fk); s != nil && s.IsBusy() {
			midTurn++
		}
	}

	if midTurn < r.maxConcurrent {
		// Fork: allocate a new session key for this thread.
		forkedKey := fmt.Sprintf("%s:t:%s", baseKey, shortThreadID(threadID))
		r.threadToKey[tk] = forkedKey
		r.forkedKeys[forkedKey] = forkedInfo{baseKey: baseKey, threadID: threadID}
		return RouteResult{
			EffectiveKey: forkedKey,
			Forked:       true,
			ForkWarning: "⚠️ A new parallel session was started for this thread. " +
				"Parallel sessions may have divergent context. " +
				"If you experience unexpected behaviour, keep conversations to a single thread.",
		}
	}

	// 4. At max_concurrent: fall back to base (message will be queued there).
	r.threadToKey[tk] = baseKey
	return RouteResult{EffectiveKey: baseKey}
}

// ReleaseSession removes routing state for effectiveSessionKey when its session
// is cleaned up (idle timeout, explicit reset, etc.).
// After this call, the next message on the owning thread will re-route as a new
// thread — it will context-switch into the base session if idle, or fork again.
func (r *ThreadRouter) ReleaseSession(effectiveSessionKey string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if fi, ok := r.forkedKeys[effectiveSessionKey]; ok {
		// Forked session: remove its specific mapping.
		delete(r.forkedKeys, effectiveSessionKey)
		delete(r.threadToKey, threadKey(fi.baseKey, fi.threadID))
		return
	}

	// Base session cleanup: release all threads that context-switched into it.
	// We identify which base key this is by finding all threadToKey entries
	// that point to effectiveSessionKey (the base key).
	for tk, mapped := range r.threadToKey {
		if mapped == effectiveSessionKey {
			delete(r.threadToKey, tk)
		}
	}
}

// shortThreadID returns the last 12 characters of a thread ID string (e.g.
// a Slack thread_ts like "1234567890.123456" → "567890.123456"), producing a
// concise suffix that is unique enough for a session key while staying readable
// in logs.
func shortThreadID(s string) string {
	if len(s) > 12 {
		return s[len(s)-12:]
	}
	return s
}
