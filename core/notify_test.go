package core

import (
	"strings"
	"testing"
)

const notifyTestIndex = `# Archive INDEX
| ID | Type | Thread | Parent | Summary | Date |
|---|---|---|---|---|---|
| L-0100 | QUERY | alpha | ROOT | first question | 07-01 |
| L-0100 | RESULT | alpha | ROOT | first answer | 07-01 |
| L-0101 | QUERY | alpha | L-0100 | second question | 07-02 |
| L-0101 | RESULT | alpha | L-0100 | STUCK: budget exhausted, partial work on branch | 07-02 |
| L-0102 | QUERY | beta | ROOT | third question | 07-03 |
| L-0100 | CLOSED | alpha | ROOT | boss closed it | 07-03 |
malformed line without pipes
| not-a-letter | RESULT | gamma | ROOT | should be skipped | 07-03 |
`

func TestParseIndexResultRows(t *testing.T) {
	rows := parseIndexResultRows(notifyTestIndex)
	if len(rows) != 2 {
		t.Fatalf("expected 2 RESULT rows, got %d: %+v", len(rows), rows)
	}
	if rows[0].Letter != "L-0100" || rows[0].Thread != "alpha" || rows[0].Summary != "first answer" {
		t.Errorf("row 0 mismatch: %+v", rows[0])
	}
	if rows[1].Letter != "L-0101" || !strings.HasPrefix(rows[1].Summary, "STUCK:") {
		t.Errorf("row 1 mismatch (STUCK rows must be included): %+v", rows[1])
	}
}

func TestScanNewResultsDedupesAndSkipsDispatchCovered(t *testing.T) {
	rows := parseIndexResultRows(notifyTestIndex)
	ledger := notifyLedger{Notified: map[string]string{}}

	// L-0100 was dispatched: the dispatch watcher owns its notification.
	fresh := scanNewResults(rows, &ledger, map[string]bool{"L-0100": true})
	if len(fresh) != 1 || fresh[0].Letter != "L-0101" {
		t.Fatalf("expected only L-0101 fresh, got %+v", fresh)
	}
	// Covered letter must still be recorded so it never re-triggers.
	if _, ok := ledger.Notified["L-0100"]; !ok {
		t.Error("dispatch-covered letter not recorded in ledger")
	}

	// Second scan: nothing new.
	fresh = scanNewResults(rows, &ledger, nil)
	if len(fresh) != 0 {
		t.Fatalf("expected no fresh rows on rescan, got %+v", fresh)
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
