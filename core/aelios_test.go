package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// setupAeliosTest creates a temporary data dir and a ManagementServer
// with aelios routes wired up. Returns the mux handler and temp dir path.
func setupAeliosTest(t *testing.T) (http.Handler, string) {
	t.Helper()
	dir := t.TempDir()
	aeliosDir := filepath.Join(dir, ".cc-connect", "aelios")
	os.MkdirAll(aeliosDir, 0o755)

	// Point HOME so AeliosDataDir() returns our temp path.
	t.Setenv("HOME", dir)

	mux := http.NewServeMux()
	ms := NewManagementServer(0, "", nil)
	ms.registerAeliosRoutes(mux, "/api/v1")
	return mux, aeliosDir
}

func doReq(handler http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func decodeResp(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp
}

// ── Tests ─────────────────────────────────────────────────────

func TestAeliosTimelineAppendAndList(t *testing.T) {
	handler, _ := setupAeliosTest(t)

	// Create two timeline entries.
	for _, typ := range []string{"chat_summary", "agent_task"} {
		w := doReq(handler, http.MethodPost, "/api/v1/aelios/timeline", map[string]any{
			"type":    typ,
			"content": "test " + typ,
			"date":    "2026-05-06",
		})
		if w.Code != http.StatusOK {
			t.Fatalf("POST timeline %s: status %d, body %s", typ, w.Code, w.Body.String())
		}
		resp := decodeResp(t, w)
		data := resp["data"].(map[string]any)
		if !strings.HasPrefix(data["id"].(string), "tl_") {
			t.Errorf("expected id prefix tl_, got %q", data["id"])
		}
		if data["created_at"] == nil || data["created_at"] == "" {
			t.Error("expected created_at to be set")
		}
	}

	// List all.
	w := doReq(handler, http.MethodGet, "/api/v1/aelios/timeline", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET timeline: status %d", w.Code)
	}
	resp := decodeResp(t, w)
	data := resp["data"].(map[string]any)
	entries := data["entries"].([]any)
	if len(entries) != 2 {
		t.Errorf("expected 2 entries, got %d", len(entries))
	}
}

func TestAeliosTimelineInvalidType(t *testing.T) {
	handler, _ := setupAeliosTest(t)

	w := doReq(handler, http.MethodPost, "/api/v1/aelios/timeline", map[string]any{
		"type":    "bogus",
		"content": "test",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	resp := decodeResp(t, w)
	if resp["ok"] != false {
		t.Error("expected ok=false")
	}
	errMsg := resp["error"].(string)
	if !strings.Contains(errMsg, "invalid type") {
		t.Errorf("error should mention invalid type, got %q", errMsg)
	}
}

func TestAeliosTimelineEmptyContent(t *testing.T) {
	handler, _ := setupAeliosTest(t)

	w := doReq(handler, http.MethodPost, "/api/v1/aelios/timeline", map[string]any{
		"type":    "chat_summary",
		"content": "",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestAeliosSavedDelete(t *testing.T) {
	handler, _ := setupAeliosTest(t)

	// Create a saved entry.
	w := doReq(handler, http.MethodPost, "/api/v1/aelios/saved", map[string]any{
		"type":    "link",
		"content": "https://example.com",
		"source":  "test",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("POST saved: %d %s", w.Code, w.Body.String())
	}
	resp := decodeResp(t, w)
	id := resp["data"].(map[string]any)["id"].(string)

	// Delete it.
	w = doReq(handler, http.MethodDelete, "/api/v1/aelios/saved/"+id, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE saved: %d %s", w.Code, w.Body.String())
	}

	// Verify gone.
	w = doReq(handler, http.MethodGet, "/api/v1/aelios/saved", nil)
	resp = decodeResp(t, w)
	entries := resp["data"].(map[string]any)["entries"].([]any)
	if len(entries) != 0 {
		t.Errorf("expected 0 entries after delete, got %d", len(entries))
	}

	// Delete again → 404.
	w = doReq(handler, http.MethodDelete, "/api/v1/aelios/saved/"+id, nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 on re-delete, got %d", w.Code)
	}
}

func TestAeliosSavedInvalidType(t *testing.T) {
	handler, _ := setupAeliosTest(t)

	w := doReq(handler, http.MethodPost, "/api/v1/aelios/saved", map[string]any{
		"type":    "image",
		"content": "test",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestAeliosDiaryDateFilter(t *testing.T) {
	handler, _ := setupAeliosTest(t)

	// Create entries on two different dates.
	for _, date := range []string{"2026-05-06", "2026-05-07", "2026-05-06"} {
		w := doReq(handler, http.MethodPost, "/api/v1/aelios/diary", map[string]any{
			"type":    "work",
			"content": "entry for " + date,
			"date":    date,
			"time":    "10:00",
		})
		if w.Code != http.StatusOK {
			t.Fatalf("POST diary: %d %s", w.Code, w.Body.String())
		}
	}

	// Filter by 2026-05-06 → expect 2.
	w := doReq(handler, http.MethodGet, "/api/v1/aelios/diary?date=2026-05-06", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET diary: %d", w.Code)
	}
	resp := decodeResp(t, w)
	entries := resp["data"].(map[string]any)["entries"].([]any)
	if len(entries) != 2 {
		t.Errorf("expected 2 entries for 2026-05-06, got %d", len(entries))
	}

	// Filter by 2026-05-07 → expect 1.
	w = doReq(handler, http.MethodGet, "/api/v1/aelios/diary?date=2026-05-07", nil)
	resp = decodeResp(t, w)
	entries = resp["data"].(map[string]any)["entries"].([]any)
	if len(entries) != 1 {
		t.Errorf("expected 1 entry for 2026-05-07, got %d", len(entries))
	}
}

func TestAeliosDiaryInvalidDate(t *testing.T) {
	handler, _ := setupAeliosTest(t)

	w := doReq(handler, http.MethodPost, "/api/v1/aelios/diary", map[string]any{
		"type":    "manual",
		"content": "test",
		"date":    "not-a-date",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}

	// GET with bad date filter.
	w = doReq(handler, http.MethodGet, "/api/v1/aelios/diary?date=20260506", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 on bad date filter, got %d", w.Code)
	}
}

func TestAeliosDiaryMissingDate(t *testing.T) {
	handler, _ := setupAeliosTest(t)

	w := doReq(handler, http.MethodPost, "/api/v1/aelios/diary", map[string]any{
		"type":    "life",
		"content": "no date",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestAeliosStatus(t *testing.T) {
	handler, _ := setupAeliosTest(t)

	w := doReq(handler, http.MethodGet, "/api/v1/aelios/status", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET status: %d", w.Code)
	}
	resp := decodeResp(t, w)
	data := resp["data"].(map[string]any)
	if data["cc_connect"] != "online" {
		t.Errorf("expected cc_connect=online, got %v", data["cc_connect"])
	}
	if data["storage"] != "jsonl" {
		t.Errorf("expected storage=jsonl, got %v", data["storage"])
	}
}

func TestAeliosTimelineDateFilter(t *testing.T) {
	handler, _ := setupAeliosTest(t)

	dates := []string{"2026-05-06", "2026-05-07", "2026-05-06"}
	for _, date := range dates {
		w := doReq(handler, http.MethodPost, "/api/v1/aelios/timeline", map[string]any{
			"type":    "chat_summary",
			"content": "entry " + date,
			"date":    date,
		})
		if w.Code != http.StatusOK {
			t.Fatalf("POST: %d", w.Code)
		}
	}

	w := doReq(handler, http.MethodGet, "/api/v1/aelios/timeline?date=2026-05-07", nil)
	resp := decodeResp(t, w)
	entries := resp["data"].(map[string]any)["entries"].([]any)
	if len(entries) != 1 {
		t.Errorf("expected 1 entry for 2026-05-07, got %d", len(entries))
	}
}

func TestAeliosConcurrentAppend(t *testing.T) {
	handler, _ := setupAeliosTest(t)

	const goroutines = 20
	var wg sync.WaitGroup
	errCh := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			w := doReq(handler, http.MethodPost, "/api/v1/aelios/timeline", map[string]any{
				"type":    "chat_summary",
				"content": fmt.Sprintf("concurrent entry %d", idx),
			})
			if w.Code != http.StatusOK {
				errCh <- fmt.Errorf("goroutine %d: status %d, body %s", idx, w.Code, w.Body.String())
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}

	// All entries must be present.
	w := doReq(handler, http.MethodGet, "/api/v1/aelios/timeline", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET timeline: %d", w.Code)
	}
	resp := decodeResp(t, w)
	entries := resp["data"].(map[string]any)["entries"].([]any)
	if len(entries) != goroutines {
		t.Errorf("expected %d entries, got %d (data loss detected)", goroutines, len(entries))
	}

	// Every ID must be unique.
	seen := make(map[string]bool, goroutines)
	for _, e := range entries {
		id := e.(map[string]any)["id"].(string)
		if seen[id] {
			t.Errorf("duplicate id: %s", id)
		}
		seen[id] = true
	}
}

func TestAeliosStoreSamePointer(t *testing.T) {
	dir := t.TempDir()
	aeliosDir := filepath.Join(dir, ".cc-connect", "aelios")
	os.MkdirAll(aeliosDir, 0o755)
	t.Setenv("HOME", dir)

	ms := NewManagementServer(0, "", nil)

	s1, err := ms.getAeliosStore("timeline")
	if err != nil {
		t.Fatalf("first getAeliosStore: %v", err)
	}
	s2, err := ms.getAeliosStore("timeline")
	if err != nil {
		t.Fatalf("second getAeliosStore: %v", err)
	}
	if s1 != s2 {
		t.Error("getAeliosStore returned different pointers for the same collection")
	}

	// Different collection → different pointer.
	s3, err := ms.getAeliosStore("saved")
	if err != nil {
		t.Fatalf("getAeliosStore(saved): %v", err)
	}
	if s1 == s3 {
		t.Error("getAeliosStore returned same pointer for different collections")
	}
}
