package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
