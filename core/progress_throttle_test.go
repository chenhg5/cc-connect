package core

import (
	"context"
	"encoding/json"
	"errors"
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
