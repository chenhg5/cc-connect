package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestSessionManager returns a fresh in-memory SessionManager (no persistence).
func newTestSessionManager() *SessionManager {
	return NewSessionManager("")
}

func TestThreadRouter_NoThreadID(t *testing.T) {
	r := NewThreadRouter(3, false)
	sm := newTestSessionManager()

	result := r.Route("slack:C1", "", sm)
	assert.Equal(t, "slack:C1", result.EffectiveKey)
	assert.False(t, result.Forked)
}

func TestThreadRouter_ContextSwitch_IdleBase(t *testing.T) {
	r := NewThreadRouter(3, false)
	sm := newTestSessionManager()

	// Base session exists but is idle (not busy).
	base := sm.GetOrCreateActive("slack:C1")
	_ = base // idle — not locked

	result := r.Route("slack:C1", "thread-ts-1", sm)
	assert.Equal(t, "slack:C1", result.EffectiveKey, "should context-switch to base when idle")
	assert.False(t, result.Forked)
}

func TestThreadRouter_SameThread_RoutesSameKey(t *testing.T) {
	r := NewThreadRouter(3, false)
	sm := newTestSessionManager()

	// First route establishes affinity.
	r1 := r.Route("slack:C1", "ts-1", sm)
	// Second route from same thread must return same key.
	r2 := r.Route("slack:C1", "ts-1", sm)
	assert.Equal(t, r1.EffectiveKey, r2.EffectiveKey)
}

func TestThreadRouter_Fork_WhenBaseBusy(t *testing.T) {
	r := NewThreadRouter(3, false)
	sm := newTestSessionManager()

	// Lock the base session to simulate a mid-turn.
	base := sm.GetOrCreateActive("slack:C1")
	require.True(t, base.TryLock(), "should be able to lock fresh session")
	defer base.Unlock()

	result := r.Route("slack:C1", "ts-new", sm)
	assert.NotEqual(t, "slack:C1", result.EffectiveKey, "should fork when base is busy")
	assert.True(t, result.Forked)
	assert.NotEmpty(t, result.ForkWarning)
}

func TestThreadRouter_Fork_LimitEnforced(t *testing.T) {
	r := NewThreadRouter(2, false) // max 2 concurrent
	sm := newTestSessionManager()

	// Lock the base session.
	base := sm.GetOrCreateActive("slack:C1")
	require.True(t, base.TryLock())
	defer base.Unlock()

	// First new thread forks (midTurn=1 < max=2).
	r1 := r.Route("slack:C1", "ts-thread-1", sm)
	require.True(t, r1.Forked)

	// Lock the forked session so it counts as mid-turn.
	fork1 := sm.GetOrCreateActive(r1.EffectiveKey)
	require.True(t, fork1.TryLock())
	defer fork1.Unlock()

	// Second new thread hits max_concurrent → falls back to base.
	r2 := r.Route("slack:C1", "ts-thread-2", sm)
	assert.Equal(t, "slack:C1", r2.EffectiveKey, "should fall back to base when at max_concurrent")
	assert.False(t, r2.Forked)
}

func TestThreadRouter_ReleaseForked(t *testing.T) {
	r := NewThreadRouter(3, false)
	sm := newTestSessionManager()

	// Lock base, fork a session.
	base := sm.GetOrCreateActive("slack:C1")
	require.True(t, base.TryLock())

	r1 := r.Route("slack:C1", "ts-1", sm)
	require.True(t, r1.Forked)
	forkedKey := r1.EffectiveKey

	// Release the forked session.
	r.ReleaseSession(forkedKey)

	// The thread should now re-route (no existing affinity).
	// Base is still busy, so it would fork again.
	r2 := r.Route("slack:C1", "ts-1", sm)
	assert.True(t, r2.Forked, "should fork again after release when base is still busy")
	base.Unlock()
}

func TestThreadRouter_ReleaseBase_ClearsAllAffinity(t *testing.T) {
	r := NewThreadRouter(3, false)
	sm := newTestSessionManager()

	// Two threads context-switch into the idle base.
	r.Route("slack:C1", "ts-A", sm)
	r.Route("slack:C1", "ts-B", sm)

	// Release the base session.
	r.ReleaseSession("slack:C1")

	// Now lock the base and route ts-A — it should fork (no existing affinity).
	base := sm.GetOrCreateActive("slack:C1")
	require.True(t, base.TryLock())
	defer base.Unlock()

	result := r.Route("slack:C1", "ts-A", sm)
	assert.True(t, result.Forked, "thread affinity should have been cleared on base release")
}

func TestThreadRouter_IndependentChannels(t *testing.T) {
	r := NewThreadRouter(1, false) // max 1 = no forking
	sm := newTestSessionManager()

	// Lock channel C1's base.
	c1base := sm.GetOrCreateActive("slack:C1")
	require.True(t, c1base.TryLock())
	defer c1base.Unlock()

	// Channel C2 is unaffected — its base is idle.
	result := r.Route("slack:C2", "ts-X", sm)
	assert.Equal(t, "slack:C2", result.EffectiveKey, "channels are independent")
	assert.False(t, result.Forked)
}

func TestThreadRouter_ClientMsgIDDedup(t *testing.T) {
	r := NewThreadRouter(3, false)

	assert.False(t, r.IsDuplicateClientMsg(""), "empty id is never a duplicate")
	assert.False(t, r.IsDuplicateClientMsg("abc-123"), "first time is not a duplicate")
	assert.True(t, r.IsDuplicateClientMsg("abc-123"), "second time is a duplicate")
	assert.False(t, r.IsDuplicateClientMsg("def-456"), "different id is not a duplicate")
}

func TestThreadRouter_Isolation_NeverContextSwitches(t *testing.T) {
	r := NewThreadRouter(3, true) // isolation enabled
	sm := newTestSessionManager()

	// Base session is idle.
	base := sm.GetOrCreateActive("slack:C1")
	_ = base // idle — not locked

	// In isolation mode, even with idle base, thread gets its own session.
	r1 := r.Route("slack:C1", "thread-ts-1", sm)
	assert.NotEqual(t, "slack:C1", r1.EffectiveKey, "isolation mode should not context-switch to base")
	assert.True(t, r1.Forked, "should create thread-specific session")
	assert.Empty(t, r1.ForkWarning, "isolation mode should not show fork warning")

	// Second thread also gets its own session, different from first.
	r2 := r.Route("slack:C1", "thread-ts-2", sm)
	assert.NotEqual(t, "slack:C1", r2.EffectiveKey)
	assert.NotEqual(t, r1.EffectiveKey, r2.EffectiveKey, "each thread should have its own session")
	assert.True(t, r2.Forked)
}

func TestThreadRouter_Isolation_SameThreadSameSession(t *testing.T) {
	r := NewThreadRouter(3, true) // isolation enabled
	sm := newTestSessionManager()

	// First message in thread establishes affinity.
	r1 := r.Route("slack:C1", "ts-1", sm)

	// Second message in same thread uses same session (affinity preserved).
	r2 := r.Route("slack:C1", "ts-1", sm)
	assert.Equal(t, r1.EffectiveKey, r2.EffectiveKey, "same thread should use same session")
}
