package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeResultFile(t *testing.T, threadsDir, thread, letter, body string) string {
	t.Helper()
	dir := filepath.Join(threadsDir, thread)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, letter+".result.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestScanResultFiles(t *testing.T) {
	root := t.TempDir()
	threadsDir := filepath.Join(root, "threads")
	writeResultFile(t, threadsDir, "alpha", "L-0100", "---\nID: L-0100\n---\n\n## Conclusion\nfirst answer.\n")
	writeResultFile(t, threadsDir, "alpha", "L-0101", "---\nID: L-0101\nStatus: STUCK\n---\n\n## Blocker\nbudget exhausted.\n")
	// Non-result files must be ignored.
	if err := os.WriteFile(filepath.Join(threadsDir, "alpha", "L-0100.query.md"), []byte("query"), 0o644); err != nil {
		t.Fatal(err)
	}

	files, err := scanResultFiles(threadsDir)
	if err != nil {
		t.Fatalf("scanResultFiles: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 result files, got %d: %+v", len(files), files)
	}
	byLetter := map[string]resultFileInfo{}
	for _, f := range files {
		byLetter[f.Letter] = f
	}
	if byLetter["L-0100"].Thread != "alpha" {
		t.Errorf("L-0100 thread mismatch: %+v", byLetter["L-0100"])
	}
	if byLetter["L-0101"].Thread != "alpha" {
		t.Errorf("L-0101 thread mismatch: %+v", byLetter["L-0101"])
	}
}

func TestScanResultFilesMissingThreadsDir(t *testing.T) {
	files, err := scanResultFiles(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("expected nil error for missing threads dir, got %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("expected no files, got %+v", files)
	}
}

func TestExtractResultSummary(t *testing.T) {
	root := t.TempDir()
	donePath := writeResultFile(t, root, "alpha", "L-0100", "---\nID: L-0100\n---\n\n## Conclusion\nfirst answer.\n\n## Options for Boss\n...\n")
	if got := extractResultSummary(donePath); got != "first answer." {
		t.Errorf("DONE summary = %q, want %q", got, "first answer.")
	}

	stuckPath := writeResultFile(t, root, "alpha", "L-0101", "---\nID: L-0101\nStatus: STUCK\n---\n\n## Partial Work\nsome\n\n## Blocker\nbudget exhausted.\n")
	if got := extractResultSummary(stuckPath); got != "budget exhausted." {
		t.Errorf("STUCK summary = %q, want %q", got, "budget exhausted.")
	}
}

func TestScanNewResultFilesDedupesAndSkipsDispatchCovered(t *testing.T) {
	now := time.Now()
	files := []resultFileInfo{
		{Letter: "L-0100", Thread: "alpha", Path: "L-0100.result.md", ModTime: now},
		{Letter: "L-0101", Thread: "alpha", Path: "L-0101.result.md", ModTime: now},
	}
	ledger := notifyLedger{Notified: map[string]string{}}

	// L-0100 was dispatched: the dispatch watcher owns its notification.
	fresh := scanNewResultFiles(files, &ledger, map[string]bool{"L-0100": true})
	if len(fresh) != 1 || fresh[0].Letter != "L-0101" {
		t.Fatalf("expected only L-0101 fresh, got %+v", fresh)
	}
	// Covered letter must still be recorded so it never re-triggers.
	if _, ok := ledger.Notified["L-0100"]; !ok {
		t.Error("dispatch-covered letter not recorded in ledger")
	}

	// Second scan with unchanged mtimes: nothing new.
	fresh = scanNewResultFiles(files, &ledger, nil)
	if len(fresh) != 0 {
		t.Fatalf("expected no fresh files on rescan, got %+v", fresh)
	}

	// A pursuit-mode edit bumps mtime: must re-fire even though the letter
	// was seen before (L-0429 requires "created or changed").
	files[1].ModTime = now.Add(1 * time.Hour)
	fresh = scanNewResultFiles(files, &ledger, nil)
	if len(fresh) != 1 || fresh[0].Letter != "L-0101" {
		t.Fatalf("expected L-0101 to re-fire after modification, got %+v", fresh)
	}
}

func TestCheckNewResultsEndToEnd(t *testing.T) {
	root := t.TempDir()
	threadsDir := filepath.Join(root, "threads")
	indexPath := filepath.Join(root, "INDEX.md")
	if err := os.WriteFile(indexPath, []byte("# Archive INDEX\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	e := NewEngine("secretary-seat", &stubAgent{}, nil, "", LangEnglish)
	e.dataDir = root
	e.configureNotify(NotifyConfig{
		Enabled:   true,
		IndexPath: indexPath,
	})

	// First scan seeds an already-existing result without notifying.
	writeResultFile(t, threadsDir, "alpha", "L-0100", "---\nID: L-0100\n---\n\n## Conclusion\npre-existing.\n")
	e.checkNewResults()
	ledger, err := e.notifyStore.load()
	if err != nil {
		t.Fatalf("load ledger: %v", err)
	}
	if !ledger.Seeded {
		t.Fatal("expected ledger to be seeded after first scan")
	}
	if _, seen := ledger.Notified["L-0100"]; !seen {
		t.Fatal("pre-existing result must be recorded during seed")
	}

	// A new result written after seeding must be picked up on the next scan
	// with no dependency on INDEX.md ever being touched (L-0429).
	writeResultFile(t, threadsDir, "alpha", "L-0101", "---\nID: L-0101\n---\n\n## Conclusion\nbrand new.\n")
	e.checkNewResults()
	ledger, _ = e.notifyStore.load()
	if _, seen := ledger.Notified["L-0101"]; !seen {
		t.Fatal("new result was not recorded as notified")
	}
}

func TestNotifyStoreRoundTrip(t *testing.T) {
	store := newNotifyStore(t.TempDir())
	ledger, err := store.load()
	if err != nil {
		t.Fatalf("load empty: %v", err)
	}
	if ledger.Seeded {
		t.Fatal("fresh ledger must not be seeded")
	}
	ledger.Seeded = true
	ledger.Notified["L-0042"] = "2026-07-07T00:00:00Z"
	if err := store.save(ledger); err != nil {
		t.Fatalf("save: %v", err)
	}
	back, err := store.load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !back.Seeded || back.Notified["L-0042"] == "" {
		t.Fatalf("round trip lost data: %+v", back)
	}
}

func TestDispatchStoreLetters(t *testing.T) {
	var s *dispatchStore
	if got := s.letters(); len(got) != 0 {
		t.Fatalf("nil store must return empty set, got %v", got)
	}
	s = newDispatchStore(t.TempDir())
	if err := s.upsert(DispatchExpectation{Letter: "L-0200", Thread: "x", To: "dev-pro", State: dispatchStateDispatched}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got := s.letters()
	if !got["L-0200"] {
		t.Fatalf("expected L-0200 in letters set, got %v", got)
	}
}

func TestPsToastEscape(t *testing.T) {
	if got := psToastEscape("Boss's letter"); got != "Boss''s letter" {
		t.Errorf("escape failed: %q", got)
	}
	if got := psToastEscape("no quotes"); got != "no quotes" {
		t.Errorf("no-op case failed: %q", got)
	}
}

func TestNotifyStoreReceiptPersistsAndIsIdempotent(t *testing.T) {
	store := newNotifyStore(t.TempDir())
	if err := store.recordArrival(indexResultRow{Letter: "L-0430", Thread: "cc-connect-maintenance", Summary: "ready"}); err != nil {
		t.Fatalf("recordArrival: %v", err)
	}
	first, changed, err := store.acknowledge("L-0430", "jay")
	if err != nil || !changed {
		t.Fatalf("first acknowledge = (%+v, %v, %v), want changed receipt", first, changed, err)
	}
	if first.AcknowledgedBy != "jay" || first.AcknowledgedAt == "" {
		t.Fatalf("first receipt = %+v", first)
	}
	second, changed, err := store.acknowledge("L-0430", "other")
	if err != nil || changed {
		t.Fatalf("second acknowledge = (%+v, %v, %v), want idempotent", second, changed, err)
	}
	if second.AcknowledgedBy != "jay" || second.AcknowledgedAt != first.AcknowledgedAt {
		t.Fatalf("idempotent receipt = %+v, want %+v", second, first)
	}
}

func TestNotifyLetterArrivedSendsShortPlatformMessageWithoutAgentTurn(t *testing.T) {
	p := &stubPlatformEngine{n: "telegram"}
	e := NewEngine("secretary-seat", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.configureNotify(NotifyConfig{
		TelegramEnabled: true,
		ToastEnabled:    false,
		Platform:        "telegram",
		SessionKey:      "telegram:123:123",
	})

	e.notifyLetterArrived(indexResultRow{
		Letter:  "L-0430",
		Thread:  "cc-connect-maintenance",
		Summary: "notification context is decoupled.",
	})

	sent := p.getSent()
	if len(sent) != 1 {
		t.Fatalf("sent = %#v, want one direct notification", sent)
	}
	if got, want := sent[0], "📬 L-0430 到货"; got != want {
		t.Fatalf("notification = %q, want %q", got, want)
	}
	if strings.Contains(sent[0], "[LETTER_ARRIVED]") {
		t.Fatalf("notification must not use agent-injected marker: %q", sent[0])
	}
}
