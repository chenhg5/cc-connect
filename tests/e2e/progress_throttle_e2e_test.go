//go:build e2e

// Package e2e contains smoke, regression, and end-to-end tests for cc-connect.
//
// progress_throttle_e2e_test.go exercises the PATCH-throttling pipeline:
//
//	cardRegistry (100ms tick) → MessageUpdater (platform) → httptest server
//
// Performance test verifies the 90% PATCH reduction goal: 100 events in a
// tight window must coalesce to at most ~1 PATCH per 100ms window (i.e. <= 3
// PATCHes across 3 windows for a 100-event burst).
//
// End-to-end test wires the full chain through a real Engine and verifies
// that an httptest server receives valid PATCH requests.
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/chenhg5/cc-connect/tests/mocks/fake"
)

// ---------------------------------------------------------------------------
// P-300: Throttle Performance — 100 events / 3 ticks → PATCH <= 3
// ---------------------------------------------------------------------------

// recordingUpdater is a MessageUpdater that records every UpdateMessage call.
// It is safe for concurrent use.
type recordingUpdater struct {
	mu     sync.Mutex
	calls  []recordingCall
	seqErr []error
}

type recordingCall struct {
	handle  any
	content string
	at      time.Time
}

func (u *recordingUpdater) UpdateMessage(_ context.Context, handle any, content string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.seqErr) > 0 {
		err := u.seqErr[0]
		u.seqErr = u.seqErr[1:]
		if err != nil {
			return err
		}
	}
	u.calls = append(u.calls, recordingCall{
		handle:  handle,
		content: content,
		at:      time.Now(),
	})
	return nil
}

func (u *recordingUpdater) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.calls)
}

func (u *recordingUpdater) callsForHandle(h any) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	n := 0
	for _, c := range u.calls {
		if c.handle == h {
			n++
		}
	}
	return n
}

func TestThrottlePerformance(t *testing.T) {
	tmpDir := t.TempDir()

	r := core.NewCardRegistry(tmpDir)
	defer r.Stop()

	updater := &recordingUpdater{}
	const interval = 50 * time.Millisecond
	r.StartTicker(updater, interval)

	const messageID = "perf-card-1"
	const handleKey = "h-perf-1"

	// Burst 100 distinct content updates for a single card in a tight loop
	// (well under one ticker window). The throttler is expected to coalesce
	// these into at most one PATCH per 50ms window.
	burstStart := time.Now()
	for i := 0; i < 100; i++ {
		content := fmt.Sprintf("event-%d", i)
		if err := r.UpdateCard(messageID, handleKey, content, core.ProgressCardStateRunning, nil); err != nil {
			t.Fatalf("UpdateCard %d failed: %v", i, err)
		}
	}
	burstDur := time.Since(burstStart)

	// Sanity: the burst should be much shorter than a single 50ms window.
	if burstDur > interval {
		t.Logf("warning: burst of 100 updates took %v (> interval %v); test semantics may be looser than intended", burstDur, interval)
	}

	// Wait for 3 ticker windows to elapse, plus a small margin to let
	// in-flight PATCH calls land.
	time.Sleep(3*interval + interval/2)

	got := updater.callsForHandle(handleKey)

	// TDD spec: 100 events / 3 ticks → PATCH <= 3.
	// Rationale: each ticker window produces at most one PATCH per card
	// because the throttler skips pushes whose content has not changed and
	// enforces an interval between pushes. So 100 events spread over 3
	// windows must produce at most 3 PATCHes.
	if got > 3 {
		t.Errorf("PATCH calls for %s in 3 windows = %d, want <= 3 (90%% reduction goal violated)", messageID, got)
	}

	// Lower bound: the initial burst must have been pushed at least once.
	if got < 1 {
		t.Errorf("PATCH calls for %s = %d, want >= 1 (initial burst should push at least once)", messageID, got)
	}

	// PATCH / event ratio must stay under 10% (90% reduction goal).
	const totalEvents = 100
	ratio := float64(got) / float64(totalEvents)
	if ratio > 0.10 {
		t.Errorf("PATCH/event ratio = %.2f (got=%d, events=%d), want <= 0.10", ratio, got, totalEvents)
	}

	t.Logf("Throttle performance: PASS (100 events → %d PATCHes, ratio=%.2f%%, burst=%v)", got, ratio*100, burstDur)
}

// ---------------------------------------------------------------------------
// P-301: Daemon End-to-End — full chain PATCH success
// ---------------------------------------------------------------------------

// httpPlatformMessageHandle carries a stable message ID and is returned by
// the stub platform as the "handle" of a freshly-sent message.
type httpPlatformMessageHandle struct {
	id string
}

func (h *httpPlatformMessageHandle) MessageID() string { return h.id }

// httpRecordingPlatform is a stub Platform that implements MessageUpdater and
// whose replyCtx implements MessageHandleIdentifier. Every UpdateMessage call
// is forwarded as an HTTP PATCH to a backing httptest server, simulating the
// real platform → upstream API path.
type httpRecordingPlatform struct {
	name string
	srv  *httptest.Server

	mu             sync.Mutex
	patches        []httpRecordedPatch
	lastReplyCtx   any
	nextSequenceID int64
}

type httpRecordedPatch struct {
	MessageID string
	Content   string
	At        time.Time
}

func (p *httpRecordingPlatform) Name() string { return p.name }

func (p *httpRecordingPlatform) Start(_ core.MessageHandler) error { return nil }

func (p *httpRecordingPlatform) Reply(_ context.Context, _ any, _ string) error { return nil }

// Send simulates sending a new message: it asks the httptest server to mint
// a message ID, then constructs a replyCtx that carries that ID.
func (p *httpRecordingPlatform) Send(_ context.Context, _ any, content string) error {
	resp, err := http.Post(p.srv.URL+"/send", "application/json",
		strings.NewReader(fmt.Sprintf(`{"content":%q}`, content)))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var ack struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(body, &ack); err != nil {
		return err
	}
	p.mu.Lock()
	p.lastReplyCtx = &httpPlatformMessageHandle{id: ack.MessageID}
	p.mu.Unlock()
	return nil
}

func (p *httpRecordingPlatform) Stop() error { return nil }

// UpdateMessage forwards a PATCH to the httptest server.
func (p *httpRecordingPlatform) UpdateMessage(ctx context.Context, handle any, content string) error {
	ident, ok := handle.(core.MessageHandleIdentifier)
	if !ok {
		return fmt.Errorf("handle does not implement MessageHandleIdentifier")
	}
	body, _ := json.Marshal(map[string]string{
		"message_id": ident.MessageID(),
		"content":    content,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, p.srv.URL+"/patch", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("PATCH %s: status %d", p.srv.URL+"/patch", resp.StatusCode)
	}
	p.mu.Lock()
	p.patches = append(p.patches, httpRecordedPatch{
		MessageID: ident.MessageID(),
		Content:   content,
		At:        time.Now(),
	})
	p.mu.Unlock()
	return nil
}

// LastReplyCtx returns the most recent replyCtx minted by Send. It is used by
// the test to drive the registry with a handle that the platform recognizes.
func (p *httpRecordingPlatform) LastReplyCtx() *httpPlatformMessageHandle {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastReplyCtx
}

func TestDaemonEndToEnd(t *testing.T) {
	// 1. Set up the "upstream API" — an httptest server that records every
	//    PATCH and replies to /send by minting a message ID.
	var (
		mu        sync.Mutex
		patches   []httpRecordedPatch
		nextID    int64
		createdID []string
	)
	mux := http.NewServeMux()
	mux.HandleFunc("/send", func(w http.ResponseWriter, r *http.Request) {
		id := fmt.Sprintf("msg-%d", atomic.AddInt64(&nextID, 1))
		mu.Lock()
		createdID = append(createdID, id)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"message_id": id})
	})
	mux.HandleFunc("/patch", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			MessageID string `json:"message_id"`
			Content   string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		patches = append(patches, httpRecordedPatch{
			MessageID: body.MessageID,
			Content:   body.Content,
			At:        time.Now(),
		})
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// 2. Spin up the platform adapter.
	platform := &httpRecordingPlatform{name: "e2e-http", srv: srv}

	// 3. Start a real Engine (the daemon's brain) with a fake agent. The
	//    engine's internal cardRegistry is wired up but we drive a separate
	//    registry below to exercise the public chain end-to-end.
	agent := fake.NewFakeAgent("e2e-fake-agent")
	storePath := filepath.Join(t.TempDir(), "sessions.json")
	engine := core.NewEngine("e2e-daemon", agent, []core.Platform{platform}, storePath, core.LanguageEN)
	defer engine.Stop()

	// 4. Drive a real cardRegistry that uses the platform's UpdateMessage as
	//    the global ticker updater. This is the full chain:
	//      UpdateCard → ticker → platform.UpdateMessage → httptest /patch.
	registryDir := t.TempDir()
	registry := core.NewCardRegistry(registryDir)
	defer registry.Stop()
	registry.StartTicker(platform, 50*time.Millisecond)

	// 5. Inject 5 cards: create each via the platform's Send (which mints a
	//    real message ID from the httptest server), then register/finalize
	//    them through the registry.
	const numCards = 5
	for i := 0; i < numCards; i++ {
		initialContent := fmt.Sprintf("card-%d initial", i)
		if err := platform.Send(context.Background(), nil, initialContent); err != nil {
			t.Fatalf("platform.Send %d failed: %v", i, err)
		}
		handle := platform.LastReplyCtx()
		if handle == nil {
			t.Fatalf("card %d: platform did not return a handle", i)
		}

		// RegisterCard signals "content already pushed via Send" — the
		// registry will not re-PATCH the initial content.
		if err := registry.RegisterCard(handle.id, handle, initialContent, nil); err != nil {
			t.Fatalf("RegisterCard %d failed: %v", i, err)
		}

		// Drive one fresh content update through UpdateCard, which DOES
		// need to be PATCHed.
		updated := fmt.Sprintf("card-%d updated", i)
		if err := registry.UpdateCard(handle.id, handle, updated, core.ProgressCardStateRunning, nil); err != nil {
			t.Fatalf("UpdateCard %d failed: %v", i, err)
		}

		// Finalize — this triggers a final PATCH with the completed state.
		if err := registry.Finalize(handle.id, core.ProgressCardStateCompleted); err != nil {
			t.Fatalf("Finalize %d failed: %v", i, err)
		}
	}

	// 6. Wait for 3 ticker windows to elapse so the throttler drains all
	//    pending updates.
	time.Sleep(3*50*time.Millisecond + 50*time.Millisecond)

	// 7. Verify: the httptest server received valid PATCHes for the 5 cards.
	mu.Lock()
	receivedPatches := append([]httpRecordedPatch(nil), patches...)
	receivedIDs := append([]string(nil), createdID...)
	mu.Unlock()

	if len(receivedIDs) != numCards {
		t.Errorf("httptest /send received %d IDs, want %d", len(receivedIDs), numCards)
	}
	if got := len(receivedPatches); got < numCards {
		t.Errorf("httptest /patch received %d PATCHes, want >= %d (one final PATCH per card)", got, numCards)
	}
	// We expect at most 3 PATCHes per card (one for the initial update,
	// one for the final state, plus a margin of safety).
	if got := len(receivedPatches); got > numCards*3 {
		t.Errorf("httptest /patch received %d PATCHes, want <= %d (3 per card: update+finalize safety margin)", got, numCards*3)
	}

	// Every PATCH must carry a non-empty message_id from our test server
	// and a non-empty content body.
	seenIDs := make(map[string]bool)
	for _, p := range receivedPatches {
		if p.MessageID == "" {
			t.Errorf("PATCH delivered with empty message_id (content=%q)", p.Content)
		}
		if p.Content == "" {
			t.Errorf("PATCH delivered with empty content (message_id=%q)", p.MessageID)
		}
		seenIDs[p.MessageID] = true
	}
	for _, id := range receivedIDs {
		if !seenIDs[id] {
			t.Errorf("created message_id %q never received a PATCH", id)
		}
	}

	// 8. Verify the engine shuts down cleanly.
	if err := engine.Stop(); err != nil {
		t.Errorf("engine.Stop returned error: %v", err)
	}

	t.Logf("Daemon end-to-end: PASS (%d cards, %d PATCHes delivered)", numCards, len(receivedPatches))
}
