//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/chenhg5/cc-connect/tests/mocks/fake"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// VP-015: E2E daemon full flow — start → events → finalize → persist → PATCH
// ---------------------------------------------------------------------------

// persistedCardSnapshot mirrors core.persistedCardSnapshot for test assertions.
type persistedCardSnapshot struct {
	MessageID string            `json:"message_id"`
	Content   string            `json:"content"`
	State     core.ProgressCardState `json:"state"`
	Finalized bool              `json:"finalized"`
	UpdatedAt time.Time         `json:"updated_at"`
}

func TestE2E_DaemonFullFlow(t *testing.T) {
	// 1. Snapshot goroutine count before starting the daemon.
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	goroutinesBefore := runtime.NumGoroutine()

	// 2. Set up the upstream API mock and the platform adapter.
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

	platform := &httpRecordingPlatform{name: "e2e-http", srv: srv}

	// 3. Prepare the persistent root and start the daemon (Engine).
	root := t.TempDir()
	sessionStorePath := filepath.Join(root, "sessions.json")
	agent := fake.NewFakeAgent("e2e-fake-agent")
	engine := core.NewEngine("e2e-daemon", agent, []core.Platform{platform}, sessionStorePath, core.LangEnglish)
	// Cleanup is performed explicitly at the end of the test so that we can
	// verify goroutine counts after everything has stopped.

	require.NoError(t, engine.Start(), "engine should start cleanly")

	adapter := &messageUpdaterCardAdapter{updater: platform}

	// 4. Inject 5 cards, each with 4 content events plus a finalize.
	const numCards = 5
	const eventsPerCard = 4
	finalContents := make([]string, numCards)
	for i := 0; i < numCards; i++ {
		initialContent := fmt.Sprintf("card-%d initial", i)
		if err := platform.Send(context.Background(), nil, initialContent); err != nil {
			t.Fatalf("platform.Send %d failed: %v", i, err)
		}
		handle := platform.LastReplyCtx()
		require.NotNil(t, handle, "card %d should have a handle", i)

		if err := engine.RegisterCard(handle.id, handle, initialContent, adapter); err != nil {
			t.Fatalf("RegisterCard %d failed: %v", i, err)
		}

		for j := 0; j < eventsPerCard; j++ {
			eventContent := fmt.Sprintf("card-%d event-%d", i, j)
			if err := engine.UpdateCard(handle.id, handle, eventContent, core.ProgressCardStateRunning, adapter); err != nil {
				t.Fatalf("UpdateCard %d/%d failed: %v", i, j, err)
			}
			finalContents[i] = eventContent
		}

		if err := engine.FinalizeCard(handle.id, core.ProgressCardStateCompleted); err != nil {
			t.Fatalf("Finalize %d failed: %v", i, err)
		}
	}

	// 5. Wait for 3 ticker windows (engine default is 100ms) plus a small margin.
	time.Sleep(3*100*time.Millisecond + 50*time.Millisecond)

	// 6. Verify PATCH counts.
	mu.Lock()
	receivedPatches := append([]httpRecordedPatch(nil), patches...)
	receivedIDs := append([]string(nil), createdID...)
	mu.Unlock()

	require.Len(t, receivedIDs, numCards, "one /send per card")

	patchesByID := make(map[string]int)
	seenIDs := make(map[string]bool)
	for _, p := range receivedPatches {
		require.NotEmpty(t, p.MessageID, "PATCH must carry a message_id")
		require.NotEmpty(t, p.Content, "PATCH must carry content")
		patchesByID[p.MessageID]++
		seenIDs[p.MessageID] = true
	}

	for _, id := range receivedIDs {
		assert.True(t, seenIDs[id], "created message_id %q never received a PATCH", id)
		assert.LessOrEqual(t, patchesByID[id], 3, "card %q should receive at most 3 PATCHes", id)
	}

	// 7. Verify persisted snapshots: content matches final content and state is completed.
	persistDir := filepath.Join(root, "cc-connect-progress-cards")
	for i, id := range receivedIDs {
		path := filepath.Join(persistDir, "cc-connect-progress-"+id+".json")
		data, err := os.ReadFile(path)
		require.NoError(t, err, "persisted snapshot for %q should exist", id)

		var snap persistedCardSnapshot
		require.NoError(t, json.Unmarshal(data, &snap), "snapshot for %q should unmarshal", id)

		assert.Equal(t, id, snap.MessageID)
		assert.Equal(t, finalContents[i], snap.Content, "card %q persisted content should match final event", id)
		assert.Equal(t, core.ProgressCardStateCompleted, snap.State, "card %q persisted state should be completed", id)
		assert.True(t, snap.Finalized, "card %q should be marked finalized", id)
	}

	// 8. Stop the upstream server and the engine, then verify no goroutine leak.
	srv.Close()
	require.NoError(t, engine.Stop())

	// Give background goroutines time to wind down.
	time.Sleep(200 * time.Millisecond)
	runtime.GC()
	time.Sleep(100 * time.Millisecond)
	goroutinesAfter := runtime.NumGoroutine()
	// Allow a small tolerance for runtime/test-runner goroutines.
	if goroutinesAfter > goroutinesBefore+3 {
		t.Errorf("possible goroutine leak: before=%d after=%d", goroutinesBefore, goroutinesAfter)
	}

	t.Logf("Daemon full flow: PASS (%d cards, %d PATCHes delivered, %d persisted)", numCards, len(receivedPatches), len(receivedIDs))
}
