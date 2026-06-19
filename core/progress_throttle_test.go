package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type testMessageUpdate struct {
	handle  any
	content string
}

type testMessageUpdater struct {
	mu      sync.Mutex
	updates []testMessageUpdate
	seqErrs []error
}

func (u *testMessageUpdater) UpdateMessage(_ context.Context, handle any, content string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.seqErrs) > 0 {
		err := u.seqErrs[0]
		u.seqErrs = u.seqErrs[1:]
		return err
	}
	u.updates = append(u.updates, testMessageUpdate{handle: handle, content: content})
	return nil
}

func (u *testMessageUpdater) snapshot() []testMessageUpdate {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := make([]testMessageUpdate, len(u.updates))
	copy(out, u.updates)
	return out
}

// callsForHandle returns the number of successful UpdateMessage calls that
// were made with the given handle (replyCtx). It is safe for concurrent use.
func (u *testMessageUpdater) callsForHandle(h any) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	n := 0
	for _, update := range u.updates {
		if update.handle == h {
			n++
		}
	}
	return n
}

// setSeqErrs replaces the queued error sequence with the given slice.
// It is safe for concurrent use.
func (u *testMessageUpdater) setSeqErrs(errs []error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.seqErrs = errs
}

// UpdateCard satisfies the CardUpdater interface by delegating to
// UpdateMessage. This lets the same fake updater be passed to APIs that
// require CardUpdater (e.g. RegisterCard).
func (u *testMessageUpdater) UpdateCard(ctx context.Context, handle any, _ string, content string, _ ProgressCardState) error {
	return u.UpdateMessage(ctx, handle, content)
}

// contractUpdateCall records one invocation of MessageUpdater.UpdateMessage.
type contractUpdateCall struct {
	ctx     context.Context
	handle  any
	content string
}

// contractMessageUpdater is a fake MessageUpdater used by TestMessageUpdater_Contract.
type contractMessageUpdater struct {
	mu            sync.Mutex
	calls         []contractUpdateCall
	timeoutHandle any
	t             *testing.T
}

func (u *contractMessageUpdater) UpdateMessage(ctx context.Context, handle any, content string) error {
	if ctx == nil {
		u.t.Error("UpdateMessage received nil context")
		return errors.New("nil context")
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.calls = append(u.calls, contractUpdateCall{ctx: ctx, handle: handle, content: content})
	if handle == u.timeoutHandle {
		return context.DeadlineExceeded
	}
	return nil
}

func TestRegistryUpdateAndFinalize(t *testing.T) {
	r := NewCardRegistry(t.TempDir())
	defer r.Stop()

	h := "handle-1"

	if err := r.UpdateCard("msg_123", h, `{"state":"running"}`, ProgressCardStateRunning, nil); err != nil {
		t.Fatalf("UpdateCard failed: %v", err)
	}

	card := r.lookup("msg_123")
	if card == nil {
		t.Fatal("card not found")
	}
	if card.content != `{"state":"running"}` {
		t.Errorf("content = %q, want %q", card.content, `{"state":"running"}`)
	}
	if card.state != ProgressCardStateRunning {
		t.Errorf("state = %q, want %q", card.state, ProgressCardStateRunning)
	}
	if card.finalized {
		t.Error("finalized = true, want false")
	}
	if card.handle != h {
		t.Errorf("handle = %v, want %v", card.handle, h)
	}

	if err := r.Finalize("msg_123", ProgressCardStateCompleted); err != nil {
		t.Fatalf("Finalize failed: %v", err)
	}

	card = r.lookup("msg_123")
	if card == nil {
		t.Fatal("card not found after finalize")
	}
	if !card.finalized {
		t.Error("finalized = false, want true")
	}
	if card.state != ProgressCardStateCompleted {
		t.Errorf("state = %q, want %q", card.state, ProgressCardStateCompleted)
	}
	if card.content != `{"state":"running"}` {
		t.Errorf("content = %q, want %q", card.content, `{"state":"running"}`)
	}
}

func TestRegistryFinalizeBlocksUpdates(t *testing.T) {
	r := NewCardRegistry(t.TempDir())
	defer r.Stop()

	if err := r.UpdateCard("msg_123", nil, "initial", ProgressCardStateRunning, nil); err != nil {
		t.Fatalf("UpdateCard failed: %v", err)
	}
	if err := r.Finalize("msg_123", ProgressCardStateCompleted); err != nil {
		t.Fatalf("Finalize failed: %v", err)
	}
	if err := r.UpdateCard("msg_123", nil, "after finalize", ProgressCardStateRunning, nil); err == nil {
		t.Fatal("expected error updating finalized card with non-final state")
	}
}

func TestRegistryFinalizeAllowsFinalStateUpdate(t *testing.T) {
	r := NewCardRegistry(t.TempDir())
	defer r.Stop()

	if err := r.UpdateCard("msg_123", nil, "initial", ProgressCardStateRunning, nil); err != nil {
		t.Fatalf("UpdateCard failed: %v", err)
	}
	if err := r.Finalize("msg_123", ProgressCardStateCompleted); err != nil {
		t.Fatalf("Finalize failed: %v", err)
	}
	if err := r.UpdateCard("msg_123", nil, "failed payload", ProgressCardStateFailed, nil); err != nil {
		t.Fatalf("UpdateCard with final state after finalize should succeed: %v", err)
	}
}

// TestCardState_FinalizeImmutability verifies that once a card is finalized,
// non-final UpdateCard calls are rejected with a clear error and do not mutate
// the registry state. Final-state updates are still permitted and keep the
// card finalized.
func TestCardState_FinalizeImmutability(t *testing.T) {
	r := NewCardRegistry(t.TempDir())
	defer r.Stop()

	msgID := "msg_finalize_immutability"
	initial := "initial content"

	if err := r.UpdateCard(msgID, "handle", initial, ProgressCardStateRunning, nil); err != nil {
		t.Fatalf("UpdateCard failed: %v", err)
	}
	if err := r.Finalize(msgID, ProgressCardStateCompleted); err != nil {
		t.Fatalf("Finalize failed: %v", err)
	}

	// A non-final update after Finalize must be rejected and leave state intact.
	if err := r.UpdateCard(msgID, "handle", "mutated content", ProgressCardStateRunning, nil); err == nil {
		t.Fatal("expected error updating finalized card with non-final state")
	}

	card := r.lookup(msgID)
	if card == nil {
		t.Fatal("card not found")
	}
	if !card.finalized {
		t.Errorf("finalized = false, want true")
	}
	if card.state != ProgressCardStateCompleted {
		t.Errorf("state = %q, want %q", card.state, ProgressCardStateCompleted)
	}
	if card.content != initial {
		t.Errorf("content = %q, want %q", card.content, initial)
	}

	// A final-state update is allowed and preserves finalized=true.
	finalContent := "final content"
	if err := r.UpdateCard(msgID, "handle", finalContent, ProgressCardStateFailed, nil); err != nil {
		t.Fatalf("UpdateCard with final state after finalize should succeed: %v", err)
	}
	card = r.lookup(msgID)
	if card == nil {
		t.Fatal("card not found after final-state update")
	}
	if !card.finalized {
		t.Errorf("finalized = false after final-state update, want true")
	}
	if card.content != finalContent {
		t.Errorf("content = %q, want %q", card.content, finalContent)
	}
}

func TestRegistryConcurrentUpdateFinalize(t *testing.T) {
	r := NewCardRegistry(t.TempDir())
	defer r.Stop()

	if err := r.UpdateCard("msg_123", nil, "seed", ProgressCardStateRunning, nil); err != nil {
		t.Fatalf("seed UpdateCard failed: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = r.UpdateCard("msg_123", nil, "content", ProgressCardStateRunning, nil)
		}()
		go func() {
			defer wg.Done()
			_ = r.Finalize("msg_123", ProgressCardStateCompleted)
		}()
	}
	wg.Wait()

	card := r.lookup("msg_123")
	if card == nil {
		t.Fatal("card not found")
	}
	if card.finalized && card.state != ProgressCardStateCompleted && card.state != ProgressCardStateRunning {
		t.Errorf("unexpected final state: %q", card.state)
	}
}

// TestRegistry_ConcurrentSafety verifies that 1000 rounds of concurrent
// UpdateCard, ticker scanning, and Finalize operations on the card registry
// produce no data races and leave the registry and cardState in a consistent
// state. It is intended to be run with -race.
func TestRegistry_ConcurrentSafety(t *testing.T) {
	r := NewCardRegistry(t.TempDir())
	defer r.Stop()

	updater := &testMessageUpdater{}
	const interval = 50 * time.Millisecond
	r.StartTicker(updater, interval)

	const rounds = 1000
	const numCards = 5

	var wg sync.WaitGroup

	// Ticker scanner goroutine: drive registry ticks concurrently with updates.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			r.tick()
			time.Sleep(interval / 20)
		}
	}()

	// Concurrent UpdateCard goroutines: each card receives many updates.
	for c := 0; c < numCards; c++ {
		msgID := fmt.Sprintf("msg_%d", c)
		handle := fmt.Sprintf("handle_%d", c)
		wg.Add(1)
		go func(mid, h string) {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				content := fmt.Sprintf("content-%s-%d", mid, i)
				_ = r.UpdateCard(mid, h, content, ProgressCardStateRunning, updater)
			}
		}(msgID, handle)
	}

	// Concurrent Finalize goroutines: repeatedly finalize each card.
	for c := 0; c < numCards; c++ {
		msgID := fmt.Sprintf("msg_%d", c)
		wg.Add(1)
		go func(mid string) {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				_ = r.Finalize(mid, ProgressCardStateCompleted)
			}
		}(msgID)
	}

	wg.Wait()

	// Drain any remaining pending work.
	time.Sleep(2 * interval)
	r.tick()

	// Consistency checks: every expected card exists and registry/cardState
	// agree. If a card is finalized, its state must be a final state.
	for c := 0; c < numCards; c++ {
		msgID := fmt.Sprintf("msg_%d", c)
		card := r.lookup(msgID)
		if card == nil {
			t.Fatalf("card %s not found in registry", msgID)
		}
		if card.finalized && !isFinalProgressCardState(card.state) {
			t.Errorf("card %s finalized but state %q is not final", msgID, card.state)
		}
	}

	// The registry map keys must match each card's messageID.
	r.mu.RLock()
	if len(r.cards) != numCards {
		t.Errorf("registry has %d cards, want %d", len(r.cards), numCards)
	}
	for msgID, c := range r.cards {
		c.mu.RLock()
		if c.messageID != msgID {
			t.Errorf("registry key %q maps to card with messageID %q", msgID, c.messageID)
		}
		c.mu.RUnlock()
	}
	r.mu.RUnlock()
}

// TestMessageUpdater_Contract verifies VP-013: the card registry passes a
// non-nil context with a working Done channel to MessageUpdater.UpdateMessage,
// a timeout error from one card does not block updates for other cards, and the
// handle/content delivered to UpdateMessage match the values stored in the
// registry exactly.
func TestMessageUpdater_Contract(t *testing.T) {
	r := NewCardRegistry(t.TempDir())
	defer r.Stop()

	updater := &contractMessageUpdater{timeoutHandle: "slow", t: t}
	var _ MessageUpdater = updater

	r.StartTicker(updater, 50*time.Millisecond)

	const slowHandle = "slow"
	const slowContent = "slow-card-content"
	const fastHandle = "fast"
	const fastContent = "fast-card-content"

	if err := r.UpdateCard("msg_slow", slowHandle, slowContent, ProgressCardStateRunning, nil); err != nil {
		t.Fatalf("UpdateCard slow failed: %v", err)
	}
	if err := r.UpdateCard("msg_fast", fastHandle, fastContent, ProgressCardStateRunning, nil); err != nil {
		t.Fatalf("UpdateCard fast failed: %v", err)
	}

	// Wait long enough for the ticker to process both cards. The slow card
	// returns DeadlineExceeded immediately, so it must not delay the fast card.
	time.Sleep(200 * time.Millisecond)

	updater.mu.Lock()
	calls := make([]contractUpdateCall, len(updater.calls))
	copy(calls, updater.calls)
	updater.mu.Unlock()

	if len(calls) == 0 {
		t.Fatal("UpdateMessage was never called")
	}

	var sawSlow, sawFast bool
	for _, c := range calls {
		if c.ctx == nil {
			t.Fatalf("UpdateMessage received nil context for handle %v", c.handle)
		}
		if c.ctx.Done() == nil {
			t.Fatalf("UpdateMessage received context with nil Done channel for handle %v", c.handle)
		}
		if _, ok := c.ctx.Deadline(); !ok {
			t.Errorf("UpdateMessage received context without deadline for handle %v", c.handle)
		}

		// Verify the context supports cancellation by deriving a child and
		// cancelling it: the child's Done channel must close.
		child, cancel := context.WithCancel(c.ctx)
		done := make(chan struct{})
		go func() {
			<-child.Done()
			close(done)
		}()
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Errorf("derived context Done channel did not close for handle %v", c.handle)
		}

		switch c.handle {
		case slowHandle:
			sawSlow = true
			if c.content != slowContent {
				t.Errorf("slow handle content = %q, want %q", c.content, slowContent)
			}
		case fastHandle:
			sawFast = true
			if c.content != fastContent {
				t.Errorf("fast handle content = %q, want %q", c.content, fastContent)
			}
		default:
			t.Errorf("unexpected UpdateMessage handle %v content %q", c.handle, c.content)
		}
	}

	if !sawSlow {
		t.Errorf("UpdateMessage was not called for slow handle %q", slowHandle)
	}
	if !sawFast {
		t.Errorf("UpdateMessage was not called for fast handle %q", fastHandle)
	}
}

func TestRegistryUpdateRejectsEmptyMessageID(t *testing.T) {
	r := NewCardRegistry(t.TempDir())
	defer r.Stop()

	if err := r.UpdateCard("", nil, "x", ProgressCardStateRunning, nil); err == nil {
		t.Fatal("expected error for empty messageID")
	}
}

func TestRegistryFinalizeRejectsUnregistered(t *testing.T) {
	r := NewCardRegistry(t.TempDir())
	defer r.Stop()

	if err := r.Finalize("msg_999", ProgressCardStateCompleted); err == nil {
		t.Fatal("expected error finalizing unregistered card")
	}
}

func TestPersistCardAtomicWrite(t *testing.T) {
	tmp := t.TempDir()
	r := NewCardRegistry(tmp)
	defer r.Stop()

	r.UpdateCard("msg_123", "handle", `{"state":"running"}`, ProgressCardStateRunning, nil)

	files, err := filepath.Glob(filepath.Join(tmp, "cc-connect-progress-*.json"))
	if err != nil {
		t.Fatalf("glob failed: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 persisted file, got %d", len(files))
	}

	info, err := os.Stat(files[0])
	if err != nil {
		t.Fatalf("stat failed: %v", err)
	}
	if info.Mode()&077 != 0 {
		t.Fatalf("file mode %o has group/other bits set", info.Mode())
	}

	tmpFiles, err := filepath.Glob(filepath.Join(tmp, ".tmp-*"))
	if err != nil {
		t.Fatalf("glob temp files failed: %v", err)
	}
	if len(tmpFiles) != 0 {
		t.Fatalf("expected no leftover temp files, got %d", len(tmpFiles))
	}

	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	var snap persistedCardSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if snap.MessageID != "msg_123" {
		t.Fatalf("message_id = %q, want msg_123", snap.MessageID)
	}
	if snap.Content != `{"state":"running"}` {
		t.Fatalf("content = %q, want {\"state\":\"running\"}", snap.Content)
	}
	if snap.State != ProgressCardStateRunning {
		t.Fatalf("state = %q, want running", snap.State)
	}
}

func TestLoadPersistedCardsMtimeFilter(t *testing.T) {
	tmp := t.TempDir()
	oldPath := filepath.Join(tmp, "cc-connect-progress-old.json")
	oldSnap := persistedCardSnapshot{
		MessageID: "old",
		Content:   "stale",
		State:     ProgressCardStateCompleted,
		UpdatedAt: time.Now().UTC().Add(-25 * time.Hour),
	}
	data, _ := json.Marshal(oldSnap)
	if err := os.WriteFile(oldPath, data, 0o600); err != nil {
		t.Fatalf("write old file failed: %v", err)
	}
	oldTime := time.Now().Add(-25 * time.Hour)
	if err := os.Chtimes(oldPath, oldTime, oldTime); err != nil {
		t.Fatalf("chtimes failed: %v", err)
	}

	r := NewCardRegistry(tmp)
	defer r.Stop()
	loaded := r.LoadPersistedCards()
	if len(loaded) != 0 {
		t.Fatalf("expected 0 loaded cards, got %d", len(loaded))
	}
}

func TestSanitizeMessageIDBlocksTraversal(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"msg_123", "msg_123"},
		{"../etc/passwd", ""},
		{"..\\windows\\system32", ""},
		{"foo/bar", ""},
		{"foo\\bar", ""},
		{"..", ""},
		{".", ""},
		{"msg\x00id", ""},
		{"msg\x1fid", ""},
		{"  msg_123  ", "msg_123"},
		{"", ""},
	}
	for _, tc := range cases {
		got := sanitizeMessageID(tc.in)
		if got != tc.want {
			t.Errorf("sanitizeMessageID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSecurity_PathTraversal(t *testing.T) {
	tmp := t.TempDir()
	r := NewCardRegistry(tmp)
	defer r.Stop()

	updater := &cardUpdaterFromMessageUpdater{updater: &testMessageUpdater{}}

	unsafeIDs := []string{
		"../../../etc/passwd",
		`..\windows\system32`,
		"foo/bar",
		`foo\bar`,
		"..",
		".",
		"msg\x00id",
		"msg\x1fid",
		"  ../escape  ",
	}

	for _, id := range unsafeIDs {
		if err := r.RegisterCard(id, "handle", "content", updater); err == nil {
			t.Errorf("RegisterCard(%q) expected error, got nil", id)
		}
		if err := r.UpdateCard(id, "handle", "content", ProgressCardStateRunning, updater); err == nil {
			t.Errorf("UpdateCard(%q) expected error, got nil", id)
		}
		if err := r.Finalize(id, ProgressCardStateCompleted); err == nil {
			t.Errorf("Finalize(%q) expected error, got nil", id)
		}
		if got := sanitizeMessageID(id); got != "" {
			t.Errorf("sanitizeMessageID(%q) = %q; want empty", id, got)
		}
	}

	// A safe ID with surrounding whitespace must be accepted and persisted inside tmp.
	safeID := "  safe_msg  "
	if err := r.RegisterCard(safeID, "handle", "content", updater); err != nil {
		t.Fatalf("RegisterCard(%q) unexpected error: %v", safeID, err)
	}

	matches, err := filepath.Glob(filepath.Join(tmp, "cc-connect-progress-*.json"))
	if err != nil {
		t.Fatalf("glob failed: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected 1 persisted file, got %d", len(matches))
	}

	clean := filepath.Clean(matches[0])
	tmpClean := filepath.Clean(tmp)
	if !strings.HasPrefix(clean, tmpClean+string(os.PathSeparator)) {
		t.Errorf("persisted file %q is not under tmp dir %q", clean, tmpClean)
	}
}

func TestLoadPersistedCardsLoadsRecentCards(t *testing.T) {
	tmp := t.TempDir()
	freshPath := filepath.Join(tmp, "cc-connect-progress-fresh.json")
	freshSnap := persistedCardSnapshot{
		MessageID: "fresh",
		Content:   `{"state":"running"}`,
		State:     ProgressCardStateRunning,
		UpdatedAt: time.Now().UTC(),
	}
	data, _ := json.Marshal(freshSnap)
	if err := os.WriteFile(freshPath, data, 0o600); err != nil {
		t.Fatalf("write fresh file failed: %v", err)
	}

	r := NewCardRegistry(tmp)
	defer r.Stop()
	loaded := r.LoadPersistedCards()
	if len(loaded) != 1 {
		t.Fatalf("expected 1 loaded card, got %d", len(loaded))
	}
	if loaded[0].messageID != "fresh" {
		t.Fatalf("message_id = %q, want fresh", loaded[0].messageID)
	}
	if loaded[0].state != ProgressCardStateRunning {
		t.Fatalf("state = %q, want running", loaded[0].state)
	}
}

func TestUpdateCardRejectsUnsafeMessageID(t *testing.T) {
	tmp := t.TempDir()
	r := NewCardRegistry(tmp)
	defer r.Stop()

	err := r.UpdateCard("../escape", nil, "x", ProgressCardStateRunning, nil)
	if err == nil {
		t.Fatal("expected error for unsafe messageID")
	}

	files, err := filepath.Glob(filepath.Join(tmp, "cc-connect-progress-*.json"))
	if err != nil {
		t.Fatalf("glob failed: %v", err)
	}
	for _, f := range files {
		if strings.Contains(filepath.Base(f), "escape") {
			t.Fatalf("unsafe messageID was persisted to %s", f)
		}
	}
}

func TestTickerSkipsUnchangedContent(t *testing.T) {
	r := NewCardRegistry(t.TempDir())
	defer r.Stop()

	updater := &testMessageUpdater{}
	r.StartTicker(updater, 50*time.Millisecond)

	if err := r.UpdateCard("msg_1", "handle-1", "content-a", ProgressCardStateRunning, nil); err != nil {
		t.Fatalf("UpdateCard failed: %v", err)
	}

	r.tick()
	if n := len(updater.snapshot()); n != 1 {
		t.Fatalf("after first tick, updates = %d, want 1", n)
	}

	r.tick()
	if n := len(updater.snapshot()); n != 1 {
		t.Fatalf("after second tick with unchanged content, updates = %d, want 1", n)
	}
}

func TestTickerThrottlesPerWindow(t *testing.T) {
	r := NewCardRegistry(t.TempDir())
	defer r.Stop()

	updater := &testMessageUpdater{}
	interval := 200 * time.Millisecond
	r.StartTicker(updater, interval)

	if err := r.UpdateCard("msg_1", "handle-1", "content-a", ProgressCardStateRunning, nil); err != nil {
		t.Fatalf("UpdateCard failed: %v", err)
	}
	r.tick()
	if n := len(updater.snapshot()); n != 1 {
		t.Fatalf("first update = %d, want 1", n)
	}

	if err := r.UpdateCard("msg_1", "handle-1", "content-b", ProgressCardStateRunning, nil); err != nil {
		t.Fatalf("UpdateCard failed: %v", err)
	}
	r.tick()
	if n := len(updater.snapshot()); n != 1 {
		t.Fatalf("update within throttle window = %d, want 1", n)
	}

	time.Sleep(interval)
	r.tick()
	if n := len(updater.snapshot()); n != 2 {
		t.Fatalf("after throttle window, updates = %d, want 2", n)
	}
}

// TestThrottle_MultipleCardsSameWindow verifies that N cards with different
// messageIDs can all receive their first PATCH inside a single ticker window
// without interfering with each other. Each card's update must be delivered to
// its own handle with its own content.
func TestThrottle_MultipleCardsSameWindow(t *testing.T) {
	r := NewCardRegistry(t.TempDir())
	defer r.Stop()

	updater := &testMessageUpdater{}
	const interval = 200 * time.Millisecond
	r.StartTicker(updater, interval)

	const n = 3
	type cardSpec struct {
		msgID   string
		handle  string
		content string
	}
	cards := make([]cardSpec, n)
	for i := 0; i < n; i++ {
		cards[i] = cardSpec{
			msgID:   fmt.Sprintf("msg_%d", i),
			handle:  fmt.Sprintf("handle_%d", i),
			content: fmt.Sprintf("content_%d", i),
		}
		if err := r.UpdateCard(cards[i].msgID, cards[i].handle, cards[i].content, ProgressCardStateRunning, nil); err != nil {
			t.Fatalf("UpdateCard %d failed: %v", i, err)
		}
	}

	// All cards are new (lastPushTime is zero), so a single tick inside the
	// throttle window must flush every card exactly once.
	r.tick()

	snapshot := updater.snapshot()
	if len(snapshot) != n {
		t.Fatalf("PATCH calls = %d, want %d", len(snapshot), n)
	}

	seen := make(map[string]bool)
	for _, update := range snapshot {
		var owner *cardSpec
		for i := range cards {
			if update.handle == cards[i].handle {
				owner = &cards[i]
				break
			}
		}
		if owner == nil {
			t.Errorf("unexpected PATCH to handle %v with content %q", update.handle, update.content)
			continue
		}
		if update.content != owner.content {
			t.Errorf("handle %v got content %q, want %q", update.handle, update.content, owner.content)
		}
		if seen[owner.handle] {
			t.Errorf("handle %v received more than one PATCH", update.handle)
		}
		seen[owner.handle] = true
	}

	if len(seen) != n {
		t.Errorf("distinct handles patched = %d, want %d", len(seen), n)
	}
}

// TestThrottle_SingleCardHundredEvents verifies the 5-minute throttle window
// behavior for a single card. The test uses a short interval as an accelerated
// stand-in for the production 5-minute window.
//
//   - 100 progress events with at least one content change must result in at
//     most one PATCH call within a single throttle window.
//   - 100 progress events whose content never changes (the card was already
//     pushed with that content) must result in zero PATCH calls.
func TestThrottle_SingleCardHundredEvents(t *testing.T) {
	t.Run("changing_content", func(t *testing.T) {
		r := NewCardRegistry(t.TempDir())
		// Disable disk persistence for this burst test: atomic writes would
		// dominate wall time and cause the 100 updates to span multiple
		// throttle windows.
		r.dir = ""
		defer r.Stop()

		updater := &testMessageUpdater{}
		const interval = 50 * time.Millisecond
		r.StartTicker(updater, interval)

		const msgID = "msg_1"
		const handle = "handle_1"
		for i := 0; i < 100; i++ {
			content := fmt.Sprintf("content-%d", i)
			if err := r.UpdateCard(msgID, handle, content, ProgressCardStateRunning, nil); err != nil {
				t.Fatalf("UpdateCard %d failed: %v", i, err)
			}
		}

		// Stay inside a single throttle window after the burst.
		time.Sleep(interval / 2)

		if got := updater.callsForHandle(handle); got > 1 {
			t.Errorf("PATCH calls for changing content = %d, want <= 1", got)
		}
	})

	t.Run("identical_content", func(t *testing.T) {
		r := NewCardRegistry(t.TempDir())
		defer r.Stop()

		updater := &testMessageUpdater{}
		const interval = 50 * time.Millisecond
		r.StartTicker(updater, interval)

		const msgID = "msg_2"
		const handle = "handle_2"
		const stable = "stable-content"

		// The card already exists on the platform with this content.
		if err := r.RegisterCard(msgID, handle, stable, updater); err != nil {
			t.Fatalf("RegisterCard failed: %v", err)
		}

		for i := 0; i < 100; i++ {
			if err := r.UpdateCard(msgID, handle, stable, ProgressCardStateRunning, updater); err != nil {
				t.Fatalf("UpdateCard %d failed: %v", i, err)
			}
		}

		// Allow the ticker to fire multiple times; identical content must not
		// trigger any PATCH.
		time.Sleep(2 * interval)

		if got := updater.callsForHandle(handle); got != 0 {
			t.Errorf("PATCH calls for identical content = %d, want 0", got)
		}
	})
}

// TestPerf_HundredEventsPerWindow verifies VP-016: with a 50ms ticker interval,
// 100 rapid UpdateCard calls on a single card must be coalesced into at most one
// PATCH per throttle window. After waiting for 3 full ticker windows the total
// PATCH count must be <= 3, and every PATCH content must equal the last event's
// content (i.e. the window's latest state, not an intermediate one).
func TestPerf_HundredEventsPerWindow(t *testing.T) {
	r := NewCardRegistry(t.TempDir())
	r.dir = "" // disable atomic file persistence so the burst stays inside one window
	defer r.Stop()

	const interval = 50 * time.Millisecond
	updater := &testMessageUpdater{}
	r.StartTicker(updater, interval)

	const msgID = "msg_perf"
	const handle = "handle_perf"
	var lastContent string
	for i := 0; i < 100; i++ {
		lastContent = fmt.Sprintf("content-%d", i)
		if err := r.UpdateCard(msgID, handle, lastContent, ProgressCardStateRunning, nil); err != nil {
			t.Fatalf("UpdateCard %d failed: %v", i, err)
		}
	}

	// Wait for 3 full ticker windows so the ticker has multiple chances to flush.
	time.Sleep(3*interval + interval/2)

	snapshot := updater.snapshot()
	if len(snapshot) > 3 {
		t.Errorf("PATCH calls after 3 windows = %d, want <= 3", len(snapshot))
	}

	// Every observed PATCH must carry the final window content.
	for i, update := range snapshot {
		if update.handle != handle {
			t.Errorf("PATCH %d handle = %v, want %v", i, update.handle, handle)
		}
		if update.content != lastContent {
			t.Errorf("PATCH %d content = %q, want %q", i, update.content, lastContent)
		}
	}
}

// TestPerf_PatchEventRatio verifies VP-017: 1000+ rapid UpdateCard events spread
// across multiple cards and multiple throttle windows must coalesce so that the
// total number of PATCH calls is at most 10% of the number of events.
func TestPerf_PatchEventRatio(t *testing.T) {
	r := NewCardRegistry("")
	defer r.Stop()

	const interval = 50 * time.Millisecond
	updater := &testMessageUpdater{}
	r.StartTicker(updater, interval)

	const (
		numCards      = 10
		eventsPerCard = 100
	)
	const totalEvents = numCards * eventsPerCard

	lastContent := make([]string, numCards)
	for c := 0; c < numCards; c++ {
		msgID := fmt.Sprintf("msg_ratio_%d", c)
		handle := fmt.Sprintf("handle_ratio_%d", c)
		for i := 0; i < eventsPerCard; i++ {
			lastContent[c] = fmt.Sprintf("card-%d-event-%d", c, i)
			if err := r.UpdateCard(msgID, handle, lastContent[c], ProgressCardStateRunning, nil); err != nil {
				t.Fatalf("UpdateCard card=%d event=%d failed: %v", c, i, err)
			}
		}
	}

	// Wait long enough for several ticker windows to drain pending updates.
	time.Sleep(5*interval + interval/2)

	snapshot := updater.snapshot()
	patches := len(snapshot)
	ratio := float64(patches) / float64(totalEvents)

	if ratio > 0.10 {
		t.Errorf("PATCH/event ratio = %.4f (PATCHes=%d, events=%d), want <= 0.10", ratio, patches, totalEvents)
	}

	// Each card must converge to its final content.
	for c := 0; c < numCards; c++ {
		handle := fmt.Sprintf("handle_ratio_%d", c)
		var finalContent string
		for i := len(snapshot) - 1; i >= 0; i-- {
			if snapshot[i].handle == handle {
				finalContent = snapshot[i].content
				break
			}
		}
		if finalContent != lastContent[c] {
			t.Errorf("card %d final PATCH content = %q, want %q", c, finalContent, lastContent[c])
		}
	}

	t.Logf("PATCH/event ratio: %.4f (%d PATCHes, %d events)", ratio, patches, totalEvents)
}

func TestTickerHandlesPatchErrors(t *testing.T) {
	r := NewCardRegistry(t.TempDir())
	defer r.Stop()

	retriable := errors.New("gateway timeout")
	updater := &testMessageUpdater{seqErrs: []error{retriable}}
	r.StartTicker(updater, 50*time.Millisecond)

	if err := r.UpdateCard("msg_1", "handle-1", "content-a", ProgressCardStateRunning, nil); err != nil {
		t.Fatalf("UpdateCard failed: %v", err)
	}
	r.tick()
	if n := len(updater.snapshot()); n != 0 {
		t.Fatalf("after retriable error, successes = %d, want 0", n)
	}

	r.tick()
	if n := len(updater.snapshot()); n != 1 {
		t.Fatalf("after retry, successes = %d, want 1", n)
	}

	// Non-retriable error should mark the card as pushed and stop retrying.
	updater.seqErrs = []error{errors.New("bad request")}
	if err := r.UpdateCard("msg_1", "handle-1", "content-b", ProgressCardStateRunning, nil); err != nil {
		t.Fatalf("UpdateCard failed: %v", err)
	}
	r.tick()
	if n := len(updater.snapshot()); n != 1 {
		t.Fatalf("after non-retriable error, successes = %d, want 1", n)
	}
	r.tick()
	if n := len(updater.snapshot()); n != 1 {
		t.Fatalf("non-retriable error should not be retried, successes = %d, want 1", n)
	}
}

// TestTickerBatching verifies that:
//   - 3 cards each updated 100 times in a tight loop result in <= 1 PATCH per
//     window per card across 3 ticker windows (i.e. <= 3 total PATCHes per
//     card in 3 windows)
//   - updates to different cards are flushed independently — there is no
//     cross-card coupling
func TestTickerBatching(t *testing.T) {
	r := NewCardRegistry(t.TempDir())
	r.dir = "" // disable atomic file persistence — it would dominate wall time for 300 burst updates

	const interval = 50 * time.Millisecond
	updater := &testMessageUpdater{}
	r.StartTicker(updater, interval)

	type cardSpec struct {
		msgID  string
		handle string
	}
	cards := []cardSpec{
		{"m1", "h1"},
		{"m2", "h2"},
		{"m3", "h3"},
	}

	// Each card is burst-updated 100 times (different content each time).
	// The expectation is: at most one PATCH per window per card, so across
	// 3 windows we should see at most 3 PATCHes per card.
	for _, c := range cards {
		for i := 0; i < 100; i++ {
			content := fmt.Sprintf("%s-v%d", c.msgID, i)
			if err := r.UpdateCard(c.msgID, c.handle, content, ProgressCardStateRunning, nil); err != nil {
				t.Fatalf("UpdateCard %s failed: %v", c.msgID, err)
			}
		}
	}

	time.Sleep(3 * interval)
	r.StopTicker()

	for _, c := range cards {
		got := updater.callsForHandle(c.handle)
		if got > 3 {
			t.Errorf("PATCH calls for %s in 3 windows = %d, want <= 3 (one per window)", c.msgID, got)
		}
		if got < 1 {
			t.Errorf("PATCH calls for %s = %d, want >= 1 (initial burst should push at least once)", c.msgID, got)
		}
	}

	// Cross-card isolation: each card should have been PATCHed via its own
	// handle. If updates from one card bled into another's PATCH, the
	// per-handle count would not match what we expect.
	snapshot := updater.snapshot()
	seenHandles := make(map[any]bool)
	for _, update := range snapshot {
		seenHandles[update.handle] = true
	}
	for _, c := range cards {
		if !seenHandles[c.handle] {
			t.Errorf("card %s (handle=%q) never received a PATCH — cross-card isolation broken", c.msgID, c.handle)
		}
	}

	// And no card should have content from another card's last update —
	// that would indicate writes interleaved across cards during a batch.
	for _, update := range snapshot {
		var owner string
		for _, c := range cards {
			if update.handle == c.handle {
				owner = c.msgID
				break
			}
		}
		if owner == "" {
			t.Errorf("PATCH delivered to unknown handle %v (content=%q)", update.handle, update.content)
			continue
		}
		if !strings.HasPrefix(update.content, owner+"-") {
			t.Errorf("handle %v (card %s) received content %q which belongs to a different card", update.handle, owner, update.content)
		}
	}
}

// TestTickerContentDiff verifies that content that has not changed does not
// generate any PATCH calls. Two scenarios are covered:
//  1. RegisterCard marks the card as already-pushed; subsequent ticks produce
//     no PATCHes because the card never becomes dirty.
//  2. A successful PATCH followed by a burst of UpdateCard calls with the
//     same content produces no additional PATCHes — the throttler recognizes
//     "content already pushed".
func TestTickerContentDiff(t *testing.T) {
	r := NewCardRegistry(t.TempDir())

	const interval = 50 * time.Millisecond
	updater := &testMessageUpdater{}
	r.StartTicker(updater, interval)

	// Scenario 1: RegisterCard signals "already pushed" — ticker must not push.
	if err := r.RegisterCard("m1", "h1", "v1", updater); err != nil {
		t.Fatalf("RegisterCard failed: %v", err)
	}

	time.Sleep(5 * interval)

	if got := updater.callsForHandle("h1"); got != 0 {
		t.Errorf("PATCH calls for m1 (RegisterCard, no updates) = %d, want 0", got)
	}

	r.StopTicker()

	// Scenario 2: drive a successful PATCH, then issue many UpdateCard calls
	// with the same content. The throttler must recognize that the content
	// was already pushed and skip subsequent ticks entirely.
	r2 := NewCardRegistry(t.TempDir())
	defer r2.Stop()
	updater2 := &testMessageUpdater{}
	r2.StartTicker(updater2, interval)

	if err := r2.UpdateCard("m2", "h2", "stable", ProgressCardStateRunning, nil); err != nil {
		t.Fatalf("UpdateCard failed: %v", err)
	}

	// Wait for the first tick to drain the initial PATCH.
	time.Sleep(2 * interval)
	if got := updater2.callsForHandle("h2"); got != 1 {
		t.Fatalf("after initial PATCH, calls = %d, want 1", got)
	}

	// Now burst 50 identical updates — content does not change, so no PATCH.
	for i := 0; i < 50; i++ {
		if err := r2.UpdateCard("m2", "h2", "stable", ProgressCardStateRunning, nil); err != nil {
			t.Fatalf("UpdateCard %d failed: %v", i, err)
		}
	}

	time.Sleep(3 * interval)

	if got := updater2.callsForHandle("h2"); got != 1 {
		t.Errorf("PATCH calls for m2 with unchanged content = %d, want 1 (only the initial push)", got)
	}
}

// TestThrottle_ContentDiffSkipsUnchanged verifies that the ticker skips PATCH
// calls when the card content has not changed, and pushes exactly one PATCH
// when the content changes in the next window.
func TestThrottle_ContentDiffSkipsUnchanged(t *testing.T) {
	r := NewCardRegistry(t.TempDir())
	defer r.Stop()

	updater := &testMessageUpdater{}
	const interval = 50 * time.Millisecond
	r.StartTicker(updater, interval)

	const msgID = "msg_1"
	const handle = "handle_1"
	const stable = "stable-content"

	// Register the card as already pushed to the platform with stable content.
	if err := r.RegisterCard(msgID, handle, stable, updater); err != nil {
		t.Fatalf("RegisterCard failed: %v", err)
	}

	// Burst identical content updates inside a single throttle window.
	for i := 0; i < 20; i++ {
		if err := r.UpdateCard(msgID, handle, stable, ProgressCardStateRunning, updater); err != nil {
			t.Fatalf("UpdateCard %d failed: %v", i, err)
		}
	}

	time.Sleep(2 * interval)
	if got := updater.callsForHandle(handle); got != 0 {
		t.Errorf("PATCH calls with unchanged content = %d, want 0", got)
	}

	// Change content; the next window must deliver exactly one PATCH.
	if err := r.UpdateCard(msgID, handle, "changed-content", ProgressCardStateRunning, updater); err != nil {
		t.Fatalf("UpdateCard changed-content failed: %v", err)
	}

	time.Sleep(2 * interval)
	if got := updater.callsForHandle(handle); got != 1 {
		t.Errorf("PATCH calls after content change = %d, want 1", got)
	}
}

// TestTickerPatchErrors verifies the ticker's behavior when PATCH calls fail
// with HTTP 429 (Too Many Requests) and 5xx (server error) responses:
//
//   - the registry must not panic
//   - the dirty state must NOT be lost (so the next tick will retry)
//   - once the upstream recovers, the PATCH must succeed and the dirty state
//     must be cleared
//
// All of the codes "429", "500", "502", "503", "504" are classified as
// retriable by isRetriablePatchError — the test exercises all of them.
func TestTickerPatchErrors(t *testing.T) {
	r := NewCardRegistry(t.TempDir())
	defer r.Stop()

	retriableErrs := []error{
		errors.New("HTTP 429 too many requests"),
		errors.New("HTTP 500 internal server error"),
		errors.New("HTTP 502 bad gateway"),
		errors.New("HTTP 503 service unavailable"),
		errors.New("HTTP 504 gateway timeout"),
	}

	updater := &testMessageUpdater{seqErrs: append([]error{}, retriableErrs...)}
	const interval = 50 * time.Millisecond
	r.StartTicker(updater, interval)

	if err := r.UpdateCard("m1", "h1", "v1", ProgressCardStateRunning, nil); err != nil {
		t.Fatalf("UpdateCard failed: %v", err)
	}

	// Tick once per retriable error. None of these should panic, and none
	// should mark the card as pushed (lastPushedContent must stay empty,
	// pending must stay true so the dirty state is preserved).
	for i, wantErr := range retriableErrs {
		r.tick()

		// After a retriable failure the snapshot must remain empty.
		if got := len(updater.snapshot()); got != 0 {
			t.Fatalf("after retriable error %d (%v), successful PATCHes = %d, want 0", i, wantErr, got)
		}

		// The card is still registered, content is still "v1", and it
		// must NOT be marked as finalized or pushed.
		card := r.lookup("m1")
		if card == nil {
			t.Fatalf("after error %d, card disappeared (panic suspected)", i)
		}
		if card.content != "v1" {
			t.Fatalf("after retriable error %d, card.content = %q, want %q (dirty state must not be lost)", i, card.content, "v1")
		}
		if card.finalized {
			t.Fatalf("after retriable error %d, card marked finalized (should only happen on success)", i)
		}
	}

	// All 5 errors exhausted but the registry is still healthy and dirty.
	if got := len(updater.snapshot()); got != 0 {
		t.Fatalf("after all retriable errors, successful PATCHes = %d, want 0", got)
	}
	if card := r.lookup("m1"); card == nil || card.content != "v1" {
		t.Fatalf("dirty state lost after retriable errors: %+v", card)
	}

	// Now simulate the upstream recovering: clear the error queue and tick
	// again. The PATCH must succeed and the dirty state must be cleared.
	updater.setSeqErrs(nil)
	r.tick()

	snapshot := updater.snapshot()
	if len(snapshot) != 1 {
		t.Fatalf("after recovery tick, successful PATCHes = %d, want 1", len(snapshot))
	}
	if snapshot[0].content != "v1" {
		t.Errorf("recovered PATCH content = %q, want %q", snapshot[0].content, "v1")
	}
	if snapshot[0].handle != "h1" {
		t.Errorf("recovered PATCH handle = %v, want %q", snapshot[0].handle, "h1")
	}

	// Subsequent ticks must NOT re-PATCH — the dirty state was cleared.
	r.tick()
	r.tick()
	if got := len(updater.snapshot()); got != 1 {
		t.Errorf("after recovery, extra ticks generated %d new PATCHes, want 0", got-1)
	}

	// And the registry remains usable: a new update must still flush.
	if err := r.UpdateCard("m1", "h1", "v2", ProgressCardStateRunning, nil); err != nil {
		t.Fatalf("UpdateCard v2 failed: %v", err)
	}
	time.Sleep(2 * interval)
	if got := len(updater.snapshot()); got != 2 {
		t.Errorf("after new update v2, total PATCHes = %d, want 2 (recovery + v2)", got)
	}
}

// TestAtomicWriteNoResidual verifies that AtomicWriteFile does not leave
// behind partial temp files after successful or failed writes. It exercises
// a high-volume burst of writes plus a forced-failure path.
func TestAtomicWriteNoResidual(t *testing.T) {
	tmp := t.TempDir()

	r := NewCardRegistry(tmp)
	defer r.Stop()

	// Burst: 50 distinct cards, each overwritten 20 times. This guarantees
	// lots of temp-file churn (one .tmp-* per atomic write).
	const cards = 50
	const overwrites = 20
	for i := 0; i < cards; i++ {
		msgID := fmt.Sprintf("burst-%d", i)
		for j := 0; j < overwrites; j++ {
			content := fmt.Sprintf("v-%d-%d", i, j)
			if err := r.UpdateCard(msgID, "h", content, ProgressCardStateRunning, nil); err != nil {
				t.Fatalf("UpdateCard %s v%d failed: %v", msgID, j, err)
			}
		}
	}

	// Only the final, fully-named snapshot files should exist.
	final, err := filepath.Glob(filepath.Join(tmp, "cc-connect-progress-*.json"))
	if err != nil {
		t.Fatalf("glob final files: %v", err)
	}
	if len(final) != cards {
		t.Fatalf("final file count = %d, want %d", len(final), cards)
	}

	// And absolutely no residual temp files.
	tmpFiles, err := filepath.Glob(filepath.Join(tmp, ".tmp-*"))
	if err != nil {
		t.Fatalf("glob tmp files: %v", err)
	}
	if len(tmpFiles) != 0 {
		var names []string
		for _, f := range tmpFiles {
			names = append(names, filepath.Base(f))
		}
		t.Fatalf("found %d residual temp file(s): %v", len(tmpFiles), names)
	}

	// Forced-failure path: chmod the dir to read-only so AtomicWriteFile
	// fails. The registry must not panic, and no residual .tmp-* must be
	// left behind from the failed attempt.
	if err := os.Chmod(tmp, 0o500); err != nil {
		t.Fatalf("chmod 0500: %v", err)
	}
	defer func() {
		_ = os.Chmod(tmp, 0o755) // restore for t.TempDir cleanup
	}()

	// UpdateCard calls persistCard synchronously; with the dir read-only,
	// this should fail internally without panicking.
	_ = r.UpdateCard("after-fail", "h", "x", ProgressCardStateRunning, nil)

	tmpFiles, err = filepath.Glob(filepath.Join(tmp, ".tmp-*"))
	if err != nil {
		t.Fatalf("glob tmp files post-fail: %v", err)
	}
	if len(tmpFiles) != 0 {
		var names []string
		for _, f := range tmpFiles {
			names = append(names, filepath.Base(f))
		}
		t.Fatalf("forced-failure path left %d residual temp file(s): %v", len(tmpFiles), names)
	}
}

// TestAtomicWrite_NoPartialFiles verifies the atomic-write contract expected
// by VP-006: after persistCard writes large content, the target JSON must be
// fully parsable; no .tmp-* residual files remain; concurrent writes to the
// same messageID never produce a corrupted/partial file; and any observed file
// is either complete valid JSON or absent.
func TestAtomicWrite_NoPartialFiles(t *testing.T) {
	tmp := t.TempDir()
	r := NewCardRegistry(tmp)
	defer r.Stop()

	msgID := "msg-large-atomic"
	handle := "h-large"

	// Large content: 1 MiB of non-trivial JSON-escaped text.
	large := strings.Repeat("ɑtomic-write-consistency-check ", 1<<15) // ~1 MiB
	if err := r.UpdateCard(msgID, handle, large, ProgressCardStateRunning, nil); err != nil {
		t.Fatalf("UpdateCard large content failed: %v", err)
	}

	path := filepath.Join(tmp, "cc-connect-progress-"+msgID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading persisted large file: %v", err)
	}
	var snap persistedCardSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("unmarshaling persisted large file: %v", err)
	}
	if snap.Content != large {
		t.Fatalf("persisted large content mismatch: got %d bytes, want %d", len(snap.Content), len(large))
	}

	if err := assertNoTmpFiles(t, tmp); err != nil {
		t.Fatalf("residual tmp files after large write: %v", err)
	}

	// Concurrent hammer: many goroutines repeatedly update the same messageID
	// while readers concurrently observe the persisted file. Every observation
	// must be either a complete, valid JSON snapshot or os.ErrNotExist.
	const writers = 16
	const writesPerWriter = 100
	const readers = 8
	const readsPerReader = 200

	var wg sync.WaitGroup
	stop := make(chan struct{})

	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < writesPerWriter; j++ {
				content := fmt.Sprintf("writer-%d-iter-%d-%s", id, j, large[:64])
				_ = r.UpdateCard(msgID, handle, content, ProgressCardStateRunning, nil)
			}
		}(i)
	}

	var readErrs sync.Map
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < readsPerReader; j++ {
				select {
				case <-stop:
					return
				default:
				}
				raw, err := os.ReadFile(path)
				if err != nil {
					if os.IsNotExist(err) {
						continue
					}
					readErrs.Store(fmt.Sprintf("reader-%d-iter-%d", id, j), err)
					continue
				}
				var s persistedCardSnapshot
				if err := json.Unmarshal(raw, &s); err != nil {
					readErrs.Store(fmt.Sprintf("reader-%d-iter-%d", id, j), fmt.Errorf("partial/corrupt JSON: %w", err))
				}
			}
		}(i)
	}

	wg.Wait()
	close(stop)

	var badReads []string
	readErrs.Range(func(key, value any) bool {
		badReads = append(badReads, fmt.Sprintf("%s: %v", key, value))
		return true
	})
	if len(badReads) > 0 {
		t.Fatalf("observed non-atomic reads (%d): %s", len(badReads), strings.Join(badReads, "; "))
	}

	if err := assertNoTmpFiles(t, tmp); err != nil {
		t.Fatalf("residual tmp files after concurrent writes: %v", err)
	}

	// Final file must be valid JSON.
	final, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading final file: %v", err)
	}
	if err := json.Unmarshal(final, &snap); err != nil {
		t.Fatalf("unmarshaling final file: %v", err)
	}
}

func assertNoTmpFiles(t *testing.T, dir string) error {
	t.Helper()
	tmpFiles, err := filepath.Glob(filepath.Join(dir, ".tmp-*"))
	if err != nil {
		return err
	}
	if len(tmpFiles) != 0 {
		var names []string
		for _, f := range tmpFiles {
			names = append(names, filepath.Base(f))
		}
		return fmt.Errorf("found %d residual tmp file(s): %v", len(tmpFiles), names)
	}
	return nil
}

// TestMidWindowKillRecovery simulates a process kill mid-window: r1 is
// started, pushes v1, then a second update is made but r1 is killed BEFORE
// it can push v2. A fresh registry r2 must load the persisted card, start
// its ticker, and push v2 on the next tick.
//
// Also verifies that persisted files use mode 0600 and stay inside the
// configured directory (no path-traversal escape).
func TestRestartRecovery_MidWindowKill(t *testing.T) {
	tmp := t.TempDir()
	interval := 100 * time.Millisecond

	// Phase 1 — drive a couple of updates and stop r1 before the throttle
	// window opens for the second update.
	r1 := NewCardRegistry(tmp)
	updater1 := &testMessageUpdater{}
	r1.StartTicker(updater1, interval)

	if err := r1.UpdateCard("m1", "h1", "v1", ProgressCardStateRunning, nil); err != nil {
		t.Fatalf("UpdateCard v1 failed: %v", err)
	}
	// Wait for v1 to actually be pushed so we know the state is consistent.
	time.Sleep(2 * interval)
	if n := updater1.callsForHandle("h1"); n != 1 {
		t.Fatalf("r1 pushed v1: %d PATCHes, want 1", n)
	}

	// Stage v2 — this writes to disk via persistCard and marks pending=true,
	// but we kill r1 before the next tick window elapses, so v2 is never
	// pushed to the platform.
	if err := r1.UpdateCard("m1", "h1", "v2", ProgressCardStateRunning, nil); err != nil {
		t.Fatalf("UpdateCard v2 failed: %v", err)
	}
	r1.StopTicker() // "kill"

	if n := updater1.callsForHandle("h1"); n != 1 {
		t.Fatalf("after kill, r1 PATCHes = %d, want 1 (v2 must NOT have been pushed)", n)
	}

	// Sanity: v2 was persisted to disk.
	path := filepath.Join(tmp, "cc-connect-progress-m1.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("persisted file missing: %v", err)
	}
	var snap persistedCardSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("unmarshal persisted snapshot: %v", err)
	}
	if snap.Content != "v2" {
		t.Fatalf("persisted content = %q, want %q (v2 must be on disk before kill)", snap.Content, "v2")
	}

	// Phase 2 — fresh registry, fresh updater: r2 must load m1 from disk
	// and push v2 on its first tick.
	r2 := NewCardRegistry(tmp)
	updater2 := &testMessageUpdater{}
	r2.StartTicker(updater2, interval)

	loaded := r2.LoadPersistedCards()
	if len(loaded) != 1 {
		t.Fatalf("r2 loaded cards = %d, want 1", len(loaded))
	}
	if loaded[0].messageID != "m1" {
		t.Fatalf("loaded card messageID = %q, want m1", loaded[0].messageID)
	}
	if loaded[0].content != "v2" {
		t.Fatalf("loaded card content = %q, want v2", loaded[0].content)
	}

	// Drive a tick and verify the dirty state is flushed.
	r2.tick()

	snapshot := updater2.snapshot()
	if len(snapshot) != 1 {
		t.Fatalf("r2 pushed PATCHes = %d, want 1 (the recovered v2)", len(snapshot))
	}
	if snapshot[0].content != "v2" {
		t.Errorf("r2 PATCH content = %q, want %q", snapshot[0].content, "v2")
	}

	r2.StopTicker()

	// TDD spec: persisted file mode must be 0600 (owner r/w only).
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat persisted file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("persisted file mode = %o, want 0600", got)
	}

	// TDD spec: no file was created outside the configured directory.
	escapePattern := filepath.Join(filepath.Dir(tmp), "cc-connect-progress-*")
	escape, err := filepath.Glob(escapePattern)
	if err != nil {
		t.Fatalf("glob escape pattern: %v", err)
	}
	for _, f := range escape {
		if !strings.HasPrefix(f, tmp+string(os.PathSeparator)) {
			t.Errorf("persisted file escaped sandbox: %s", f)
		}
	}
}

// TestRestartRecovery_MtimeFilter verifies that LoadPersistedCards loads a card
// whose persisted JSON file was modified within the last 24 hours, and ignores
// a card whose file is older than 24 hours.
func TestRestartRecovery_MtimeFilter(t *testing.T) {
	tmp := t.TempDir()

	// recent file: mtime = now-1h, should be loaded
	recentPath := filepath.Join(tmp, "cc-connect-progress-recent.json")
	recentSnap := persistedCardSnapshot{
		MessageID: "recent",
		Content:   `{"state":"running"}`,
		State:     ProgressCardStateRunning,
		UpdatedAt: time.Now().UTC().Add(-time.Hour),
	}
	data, _ := json.Marshal(recentSnap)
	if err := os.WriteFile(recentPath, data, 0o600); err != nil {
		t.Fatalf("write recent file failed: %v", err)
	}
	recentMtime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(recentPath, recentMtime, recentMtime); err != nil {
		t.Fatalf("chtimes recent failed: %v", err)
	}

	// stale file: mtime = now-25h, should be ignored
	stalePath := filepath.Join(tmp, "cc-connect-progress-stale.json")
	staleSnap := persistedCardSnapshot{
		MessageID: "stale",
		Content:   "stale",
		State:     ProgressCardStateCompleted,
		UpdatedAt: time.Now().UTC().Add(-25 * time.Hour),
	}
	data, _ = json.Marshal(staleSnap)
	if err := os.WriteFile(stalePath, data, 0o600); err != nil {
		t.Fatalf("write stale file failed: %v", err)
	}
	staleMtime := time.Now().Add(-25 * time.Hour)
	if err := os.Chtimes(stalePath, staleMtime, staleMtime); err != nil {
		t.Fatalf("chtimes stale failed: %v", err)
	}

	r := NewCardRegistry(tmp)
	defer r.Stop()

	loaded := r.LoadPersistedCards()
	if len(loaded) != 1 {
		t.Fatalf("expected 1 loaded card, got %d", len(loaded))
	}
	if loaded[0].messageID != "recent" {
		t.Fatalf("loaded message_id = %q, want recent", loaded[0].messageID)
	}

	if r.lookup("stale") != nil {
		t.Fatal("stale card should not be in registry")
	}
}

// TestMtimeFilter is a dedicated, table-driven test for the 24h mtime filter.
// It verifies boundary cases around the 24-hour cutoff.
func TestMtimeFilter(t *testing.T) {
	tmp := t.TempDir()

	cases := []struct {
		name string
		age  time.Duration
		load bool
	}{
		{"just-written", 1 * time.Millisecond, true},
		{"6h-old", 6 * time.Hour, true},
		{"12h-old", 12 * time.Hour, true},
		{"23h59m-old (just inside)", 23*time.Hour + 59*time.Minute, true},
		{"24h01m-old (just outside)", 24*time.Hour + 1*time.Minute, false},
		{"25h-old", 25 * time.Hour, false},
		{"72h-old", 72 * time.Hour, false},
	}

	for i, tc := range cases {
		path := filepath.Join(tmp, fmt.Sprintf("cc-connect-progress-case-%d.json", i))
		snap := persistedCardSnapshot{
			MessageID: fmt.Sprintf("case-%d", i),
			Content:   "x",
			State:     ProgressCardStateRunning,
			UpdatedAt: time.Now().UTC().Add(-tc.age),
		}
		data, _ := json.Marshal(snap)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write %s: %v", tc.name, err)
		}
		oldTime := time.Now().Add(-tc.age)
		if err := os.Chtimes(path, oldTime, oldTime); err != nil {
			t.Fatalf("chtimes %s: %v", tc.name, err)
		}
	}

	r := NewCardRegistry(tmp)
	defer r.Stop()

	loaded := r.LoadPersistedCards()
	loadedIDs := make(map[string]bool, len(loaded))
	for _, c := range loaded {
		loadedIDs[c.messageID] = true
	}

	for i, tc := range cases {
		id := fmt.Sprintf("case-%d", i)
		got := loadedIDs[id]
		if got != tc.load {
			t.Errorf("case %q (age=%v): loaded=%v, want %v", tc.name, tc.age, got, tc.load)
		}
	}
}

// TestFilePermission0600 verifies that every persisted card snapshot file is
// written with mode 0600 (owner read/write only). It covers initial writes,
// multiple cards, and overwrites.
func TestFilePermission0600(t *testing.T) {
	tmp := t.TempDir()
	r := NewCardRegistry(tmp)
	defer r.Stop()

	ids := []string{"perm-a", "perm-b", "perm-c", "perm-d"}
	for _, id := range ids {
		if err := r.UpdateCard(id, "h", "v1", ProgressCardStateRunning, nil); err != nil {
			t.Fatalf("UpdateCard %s: %v", id, err)
		}
	}

	for _, id := range ids {
		path := filepath.Join(tmp, "cc-connect-progress-"+id+".json")
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		perm := info.Mode().Perm()
		if perm != 0o600 {
			t.Errorf("file %s mode = %o, want 0600", filepath.Base(path), perm)
		}
	}

	// Overwrite one of them — the mode must remain 0600 (atomic write
	// must not change permissions).
	if err := r.UpdateCard("perm-a", "h", "v2", ProgressCardStateRunning, nil); err != nil {
		t.Fatalf("overwrite perm-a: %v", err)
	}
	path := filepath.Join(tmp, "cc-connect-progress-perm-a.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after overwrite: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("after overwrite, mode = %o, want 0600", got)
	}
}

// TestPathTraversalBlocked verifies that the registry refuses to persist or
// load any card whose messageID contains path traversal patterns. It also
// verifies that no file is created outside the configured directory.
func TestPathTraversalBlocked(t *testing.T) {
	tmp := t.TempDir()

	attacks := []struct {
		name     string
		id       string
		rejected bool
	}{
		{"dotdot-slash", "../escape", true},
		{"dotdot-backslash", `..\escape`, true},
		{"dotdot-only", "..", true},
		{"single-dot", ".", true},
		{"subdir-slash", "subdir/file", true},
		{"subdir-backslash", `subdir\file`, true},
		{"abs-slash", "/etc/passwd", true},
		{"tilde", "~/.ssh/authorized_keys", true},
		{"dotdot-inside", "msg..id", true},
		{"null-byte", "msg\x00id", true},
		{"control-char", "msg\x1fid", true},
	}

	r := NewCardRegistry(tmp)
	defer r.Stop()

	for _, tc := range attacks {
		err := r.UpdateCard(tc.id, nil, "evil", ProgressCardStateRunning, nil)
		if tc.rejected {
			if err == nil {
				t.Errorf("attack %q (%s): UpdateCard accepted unsafe ID", tc.id, tc.name)
			}
			continue
		}
		if err != nil {
			t.Errorf("attack %q (%s): UpdateCard rejected safe ID: %v", tc.id, tc.name, err)
		}
	}

	// Ensure no escaped files were created anywhere outside tmp.
	parent := filepath.Dir(tmp)
	matches, err := filepath.Glob(filepath.Join(parent, "cc-connect-progress-*.json"))
	if err != nil {
		t.Fatalf("glob parent: %v", err)
	}
	for _, m := range matches {
		if !strings.HasPrefix(m, tmp+string(os.PathSeparator)) {
			t.Errorf("file escaped sandbox: %s", m)
		}
	}

	// And ensure no file inside tmp contains any of the attack patterns in
	// its basename.
	inside, err := filepath.Glob(filepath.Join(tmp, "cc-connect-progress-*.json"))
	if err != nil {
		t.Fatalf("glob inside: %v", err)
	}
	for _, f := range inside {
		base := filepath.Base(f)
		for _, tc := range attacks {
			if !tc.rejected {
				continue
			}
			// Compare against the sanitized form (sanitizeMessageID trims,
			// so the basename cannot legally contain the raw attack — but
			// it must not be created at all).
			clean := sanitizeMessageID(tc.id)
			if clean != "" && strings.Contains(base, clean) {
				t.Errorf("unsafe messageID %q (sanitized=%q) appears in persisted file %s", tc.id, clean, f)
			}
		}
	}

	// SanitizeMessageID directly: every rejected ID must sanitize to "".
	for _, tc := range attacks {
		if !tc.rejected {
			continue
		}
		if got := sanitizeMessageID(tc.id); got != "" {
			t.Errorf("sanitizeMessageID(%q) = %q, want \"\"", tc.id, got)
		}
	}
}

// TestPersist_FailureNonBlocking verifies VP-011: when JSON persistence fails
// (e.g. the persist directory is read-only), the registry logs the error but
// keeps running: in-memory card state continues to update, the ticker still
// PATCHes the latest content from memory, and the process does not panic or
// hang.
func TestPersist_FailureNonBlocking(t *testing.T) {
	tmp := t.TempDir()
	r := NewCardRegistry(tmp)
	defer r.Stop()

	var logBuf bytes.Buffer
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(oldLogger)

	updater := &testMessageUpdater{}
	const interval = 50 * time.Millisecond
	r.StartTicker(updater, interval)

	const msgID = "m1"
	const handle = "h1"

	if err := r.RegisterCard(msgID, handle, "v1", updater); err != nil {
		t.Fatalf("RegisterCard failed: %v", err)
	}

	if err := os.Chmod(tmp, 0o500); err != nil {
		t.Fatalf("chmod 0500: %v", err)
	}
	defer func() {
		_ = os.Chmod(tmp, 0o755)
	}()

	if err := r.UpdateCard(msgID, handle, "v2", ProgressCardStateRunning, updater); err != nil {
		t.Fatalf("UpdateCard v2 failed: %v", err)
	}

	card := r.lookup(msgID)
	if card == nil {
		t.Fatal("card not found after update")
	}
	if card.content != "v2" {
		t.Errorf("in-memory content = %q, want %q", card.content, "v2")
	}

	time.Sleep(3 * interval)
	if got := updater.callsForHandle(handle); got < 1 {
		t.Fatalf("ticker did not PATCH after persist failure: calls=%d", got)
	}
	foundV2 := false
	for _, u := range updater.snapshot() {
		if u.content == "v2" {
			foundV2 = true
			break
		}
	}
	if !foundV2 {
		t.Errorf("ticker PATCH did not include latest memory content; got %+v", updater.snapshot())
	}

	if err := r.UpdateCard(msgID, handle, "v3", ProgressCardStateRunning, updater); err != nil {
		t.Fatalf("UpdateCard v3 failed: %v", err)
	}
	time.Sleep(3 * interval)
	card = r.lookup(msgID)
	if card == nil || card.content != "v3" {
		t.Errorf("in-memory content after second update = %q, want %q", card.content, "v3")
	}

	logOut := logBuf.String()
	if !strings.Contains(logOut, "card registry: atomic write failed") {
		t.Errorf("expected persistence error log, got %q", logOut)
	}

	_ = os.Chmod(tmp, 0o755)
	if err := assertNoTmpFiles(t, tmp); err != nil {
		t.Errorf("residual tmp files after persist failure: %v", err)
	}
}

// TestFinalize_ThrottledToNextTick verifies that Finalize does not bypass the
// throttle window. The card is registered, a content update is pushed, and then
// Finalize is called mid-window. No PATCH should occur within the current
// window, the next ticker tick must push exactly once, and the persisted JSON
// snapshot must contain finalized=true.
func TestFinalize_ThrottledToNextTick(t *testing.T) {
	tmp := t.TempDir()
	r := NewCardRegistry(tmp)
	defer r.Stop()

	updater := &testMessageUpdater{}
	const interval = 100 * time.Millisecond
	r.StartTicker(updater, interval)
	// Stop the auto goroutine; we drive ticks manually to keep the test
	// deterministic while still using the configured throttle interval.
	r.StopTicker()

	const msgID = "msg_1"
	const handle = "handle_1"

	// Register an already-pushed card and then make a content change.
	if err := r.RegisterCard(msgID, handle, "running-content", updater); err != nil {
		t.Fatalf("RegisterCard failed: %v", err)
	}
	if err := r.UpdateCard(msgID, handle, "updated-content", ProgressCardStateRunning, updater); err != nil {
		t.Fatalf("UpdateCard failed: %v", err)
	}

	// First tick: pushes the updated content.
	r.tick()
	if got := updater.callsForHandle(handle); got != 1 {
		t.Fatalf("after initial tick, PATCH calls = %d, want 1", got)
	}

	// Finalize mid-window: must not trigger an immediate PATCH.
	if err := r.Finalize(msgID, ProgressCardStateCompleted); err != nil {
		t.Fatalf("Finalize failed: %v", err)
	}
	r.tick()
	if got := updater.callsForHandle(handle); got != 1 {
		t.Errorf("after Finalize within throttle window, PATCH calls = %d, want 1", got)
	}

	// Wait for the throttle window to elapse, then tick again.
	time.Sleep(interval + 20*time.Millisecond)
	r.tick()
	if got := updater.callsForHandle(handle); got != 2 {
		t.Errorf("after next tick, PATCH calls = %d, want 2", got)
	}

	// Verify the persisted JSON snapshot contains finalized=true.
	path := filepath.Join(tmp, "cc-connect-progress-"+msgID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read persisted snapshot: %v", err)
	}
	var snap persistedCardSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("unmarshal persisted snapshot: %v", err)
	}
	if !snap.Finalized {
		t.Errorf("persisted finalized = %v, want true", snap.Finalized)
	}
}

// pushState exposes the internal last-pushed state and pending flag for tests.
func (r *cardRegistry) pushState(messageID string) (lastContent string, lastTime time.Time, pending bool) {
	r.mu.RLock()
	c, ok := r.cards[messageID]
	r.mu.RUnlock()
	if !ok {
		return "", time.Time{}, false
	}

	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastPushedContent, c.lastPushTime, c.pending
}

// TestThrottle_PatchFailureRetry verifies VP-010: when PATCH fails with a
// retriable error (429/5xx/timeout), the registry logs the error, leaves
// last_pushed_content and last_pushed_at unchanged, keeps the dirty state, and
// retries on the next ticker tick without backoff or poisoning.
func TestThrottle_PatchFailureRetry(t *testing.T) {
	retriableErrs := []error{
		errors.New("HTTP 429 too many requests"),
		errors.New("HTTP 500 internal server error"),
		errors.New("HTTP 502 bad gateway"),
		errors.New("HTTP 503 service unavailable"),
		errors.New("HTTP 504 gateway timeout"),
		errors.New("upstream timeout"),
	}

	for i, wantErr := range retriableErrs {
		t.Run(fmt.Sprintf("case_%d", i), func(t *testing.T) {
			r := NewCardRegistry(t.TempDir())
			defer r.Stop()

			var logBuf bytes.Buffer
			oldLogger := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
			defer slog.SetDefault(oldLogger)

			updater := &testMessageUpdater{}
			const interval = 50 * time.Millisecond
			r.StartTicker(updater, interval)

			const msgID = "m1"
			const handle = "h1"
			const content = "v1"

			updater.setSeqErrs([]error{wantErr})
			if err := r.UpdateCard(msgID, handle, content, ProgressCardStateRunning, nil); err != nil {
				t.Fatalf("UpdateCard failed: %v", err)
			}

			beforeContent, beforeTime, _ := r.pushState(msgID)
			r.tick()
			afterContent, afterTime, afterPending := r.pushState(msgID)

			if got := len(updater.snapshot()); got != 0 {
				t.Fatalf("after retriable error, successes = %d, want 0", got)
			}
			if afterContent != beforeContent {
				t.Errorf("lastPushedContent changed from %q to %q", beforeContent, afterContent)
			}
			if !afterTime.Equal(beforeTime) {
				t.Errorf("lastPushTime changed from %v to %v", beforeTime, afterTime)
			}
			if !afterPending {
				t.Errorf("pending=false, want true (dirty state must be preserved)")
			}

			logOut := logBuf.String()
			if !strings.Contains(logOut, "card registry: patch failed") {
				t.Errorf("expected log entry for patch failure, got %q", logOut)
			}
			if !strings.Contains(logOut, "level=ERROR") {
				t.Errorf("expected error-level log, got %q", logOut)
			}

			// Next tick retries and succeeds.
			updater.setSeqErrs(nil)
			r.tick()
			if got := updater.callsForHandle(handle); got != 1 {
				t.Errorf("retry successes = %d, want 1", got)
			}
			snap := updater.snapshot()
			if len(snap) != 1 || snap[0].content != content || snap[0].handle != handle {
				t.Errorf("retry snapshot = %+v, want [{handle:%v content:%q}]", snap, handle, content)
			}

			// Dirty state is cleared only after a successful push.
			finalContent, _, finalPending := r.pushState(msgID)
			if finalContent != content {
				t.Errorf("lastPushedContent after success = %q, want %q", finalContent, content)
			}
			if finalPending {
				t.Errorf("pending=true after success, want false")
			}
		})
	}
}

// TestPerf_MemoryStable verifies VP-018: issuing 100 UpdateCard events on a
// single card does not cause abnormal memory growth. The registry keeps only
// the latest state per messageID, so HeapAlloc growth must stay below 10MB and
// the registry must contain exactly one card.
func TestPerf_MemoryStable(t *testing.T) {
	r := NewCardRegistry("")
	defer r.Stop()

	const msgID = "msg-memory-stable"
	const handle = "handle-memory-stable"

	// Establish a baseline with the card registered and one update.
	if err := r.UpdateCard(msgID, handle, "baseline", ProgressCardStateRunning, nil); err != nil {
		t.Fatalf("UpdateCard baseline failed: %v", err)
	}

	runtime.GC()
	var baseline runtime.MemStats
	runtime.ReadMemStats(&baseline)

	for i := 0; i < 100; i++ {
		content := fmt.Sprintf("event-%d-%s", i, strings.Repeat("x", 256))
		if err := r.UpdateCard(msgID, handle, content, ProgressCardStateRunning, nil); err != nil {
			t.Fatalf("UpdateCard %d failed: %v", i, err)
		}
	}

	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	growth := int64(after.HeapAlloc) - int64(baseline.HeapAlloc)
	if growth > 10<<20 {
		t.Errorf("HeapAlloc growth = %d bytes (%.2f MB), want < 10 MB", growth, float64(growth)/(1<<20))
	}

	r.mu.RLock()
	cardCount := len(r.cards)
	r.mu.RUnlock()
	if cardCount != 1 {
		t.Errorf("registry card count = %d, want 1", cardCount)
	}

	t.Logf("HeapAlloc growth: %d bytes (%.2f MB)", growth, float64(growth)/(1<<20))
}

// loopCount returns the number of cardRegistry.loop goroutines currently in
// the process stack dump. It tolerates loops leaked by unrelated tests.
func loopCount() int {
	buf := make([]byte, 2<<20)
	n := runtime.Stack(buf, true)
	str := string(buf[:n])
	count := 0
	for {
		i := strings.Index(str, "(*cardRegistry).loop")
		if i < 0 {
			break
		}
		count++
		str = str[i+len("(*cardRegistry).loop"):]
	}
	return count
}

// TestTicker_NoGoroutineLeak verifies VP-022: starting the ticker creates
// exactly one background goroutine, and stopping it leaves no residual
// cardRegistry.loop goroutine. The check uses both runtime.NumGoroutine and a
// full runtime.Stack snapshot so any stuck goroutine is caught.
func TestTicker_NoGoroutineLeak(t *testing.T) {
	r := NewCardRegistry(t.TempDir())
	defer r.Stop()

	countLoops := func() int {
		return loopCount()
	}

	waitFor := func(cond func() bool, msg string) {
		for i := 0; i < 100; i++ {
			if cond() {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		buf := make([]byte, 2<<20)
		n := runtime.Stack(buf, true)
		t.Logf("goroutine stack at timeout:\n%s", string(buf[:n]))
		t.Fatalf("timeout waiting: %s", msg)
	}

	// Stop the ticker created by NewCardRegistry and wait for this registry's
	// loop goroutine to exit. Other test loops may be present, so we measure
	// relative to a baseline instead of requiring zero cardRegistry.loop goroutines.
	r.StopTicker()
	time.Sleep(100 * time.Millisecond)
	loopBase := countLoops()

	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	base := runtime.NumGoroutine()

	updater := &testMessageUpdater{}
	r.StartTicker(updater, 50*time.Millisecond)
	waitFor(func() bool { return countLoops() == loopBase+1 }, "loop goroutine to start")

	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	afterStart := runtime.NumGoroutine()
	if afterStart != base+1 {
		t.Fatalf("after StartTicker NumGoroutine = %d, want %d (base=%d)", afterStart, base+1, base)
	}

	r.StopTicker()
	waitFor(func() bool { return countLoops() == loopBase }, "loop goroutine to stop")

	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	afterStop := runtime.NumGoroutine()
	if afterStop != base {
		t.Fatalf("after StopTicker NumGoroutine = %d, want %d (base=%d)", afterStop, base, base)
	}
}

// TestSecurity_LargeContentGuard verifies VP-021: a single card's content can be
// serialized and deserialized at >1 MiB without panic or OOM, and a configurable
// upper bound rejects oversized input with a clear error instead of silently
// accepting it.
func TestSecurity_LargeContentGuard(t *testing.T) {
	t.Run("one_mebibyte_serializes_without_panic", func(t *testing.T) {
		tmp := t.TempDir()
		r := NewCardRegistry(tmp)
		defer r.Stop()

		// Build a 1 MiB+ payload of non-trivial (multi-byte, JSON-escaped) text
		// to stress the JSON encoder/decoder.
		const wantLen = 1 << 20 // 1 MiB
		chunk := strings.Repeat("α-large-content-guard-check ", 1<<16)
		large := chunk[:wantLen]
		if len(large) != wantLen {
			t.Fatalf("setup: large payload length = %d, want %d", len(large), wantLen)
		}

		if err := r.UpdateCard("msg-large", "handle", large, ProgressCardStateRunning, nil); err != nil {
			t.Fatalf("UpdateCard 1 MiB content failed: %v", err)
		}

		path := filepath.Join(tmp, "cc-connect-progress-msg-large.json")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading persisted 1 MiB file: %v", err)
		}

		var snap persistedCardSnapshot
		if err := json.Unmarshal(data, &snap); err != nil {
			t.Fatalf("unmarshaling 1 MiB persisted file: %v", err)
		}
		if snap.Content != large {
			t.Fatalf("persisted 1 MiB content mismatch: got %d bytes, want %d", len(snap.Content), len(large))
		}
	})

	t.Run("configurable_limit_rejects_oversized_content", func(t *testing.T) {
		tmp := t.TempDir()
		r := NewCardRegistry(tmp)
		defer r.Stop()

		const limit = 1 << 20 // 1 MiB
		r.SetMaxContentBytes(limit)

		over := strings.Repeat("x", limit+1)
		err := r.UpdateCard("msg-over", "handle", over, ProgressCardStateRunning, nil)
		if err == nil {
			t.Fatal("expected error for content exceeding configured limit, got nil")
		}
		msg := err.Error()
		if !strings.Contains(msg, "content exceeds maximum allowed size") {
			t.Errorf("error message = %q, want it to contain 'content exceeds maximum allowed size'", msg)
		}
		if !strings.Contains(msg, fmt.Sprintf("%d", len(over))) {
			t.Errorf("error message = %q, want it to mention the actual size %d", msg, len(over))
		}
		if !strings.Contains(msg, fmt.Sprintf("%d", limit)) {
			t.Errorf("error message = %q, want it to mention the limit %d", msg, limit)
		}

		// The card must not have been created in memory.
		if r.lookup("msg-over") != nil {
			t.Error("oversized content should not create a registry entry")
		}

		// Content exactly at the limit is accepted.
		exact := strings.Repeat("y", limit)
		if err := r.UpdateCard("msg-exact", "handle", exact, ProgressCardStateRunning, nil); err != nil {
			t.Fatalf("content exactly at limit should be accepted: %v", err)
		}
	})

	t.Run("register_card_respects_same_limit", func(t *testing.T) {
		tmp := t.TempDir()
		r := NewCardRegistry(tmp)
		defer r.Stop()

		const limit = 1024
		r.SetMaxContentBytes(limit)

		updater := &testMessageUpdater{}
		over := strings.Repeat("z", limit+1)
		err := r.RegisterCard("msg-reg-over", "handle", over, updater)
		if err == nil {
			t.Fatal("expected RegisterCard to reject oversized content")
		}
		if !strings.Contains(err.Error(), "content exceeds maximum allowed size") {
			t.Errorf("RegisterCard error = %q, want 'content exceeds maximum allowed size'", err.Error())
		}
	})
}
