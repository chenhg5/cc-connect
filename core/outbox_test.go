package core

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRetryOutboxCleanupKeepsDispatchedCardUntilDeleteSucceeds(t *testing.T) {
	p := &receiptActionPlatform{stubPlatformEngine: stubPlatformEngine{n: "telegram"}, deleteErr: errors.New("telegram unavailable")}
	e := NewEngine("secretary-seat", &stubAgent{}, nil, "", LangEnglish)
	e.platforms = []Platform{p}
	e.outboxConfig = OutboxConfig{Platform: "telegram"}
	e.outboxRecords = map[string]outboxRecord{
		"L-0100": {Dispatched: true, Card: &MessageLocator{Platform: "telegram", ChatID: 1, ThreadID: 2, MessageID: 3}},
	}

	e.retryOutboxCleanup()
	if !e.outboxRecords["L-0100"].Dispatched {
		t.Fatal("failed delete must retain the dispatched card for retry")
	}

	p.deleteErr = nil
	e.retryOutboxCleanup()
	if _, ok := e.outboxRecords["L-0100"]; ok {
		t.Fatal("successful retry must remove the dispatched card record")
	}
}

func TestMarkOutboxDispatchedMarksCardWhenDeleteFails(t *testing.T) {
	p := &receiptActionPlatform{stubPlatformEngine: stubPlatformEngine{n: "telegram"}, deleteErr: errors.New("telegram unavailable")}
	e := NewEngine("secretary-seat", &stubAgent{}, nil, "", LangEnglish)
	e.outboxRecords = map[string]outboxRecord{
		"L-0100": {Card: &MessageLocator{Platform: "telegram", ChatID: 1, ThreadID: 2, MessageID: 3}},
	}

	e.markOutboxDispatched(p, "L-0100", "callback-card")
	record, ok := e.outboxRecords["L-0100"]
	if !ok || !record.Dispatched {
		t.Fatal("failed delete must preserve a dispatched cleanup record")
	}
	if !strings.Contains(p.updatedContent, "已分发") || len(p.updatedButtons) != 0 {
		t.Fatalf("fallback card = content:%q buttons:%#v", p.updatedContent, p.updatedButtons)
	}
}

func writeQueryFile(t *testing.T, threadsDir, thread, letter, body string) string {
	t.Helper()
	dir := filepath.Join(threadsDir, thread)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, letter+".query.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestScanOutboxQueriesRequiresRegisteredUndispatchedQuery(t *testing.T) {
	root := t.TempDir()
	threads := filepath.Join(root, "threads")
	index := filepath.Join(root, "INDEX.md")
	writeQueryFile(t, threads, "alpha", "L-0100", "---\nID: L-0100\nThread: alpha\nType: QUERY\nTo: dev-pro\nRoute: heavy\nDate: 2026-07-18\n---\n\n## Query\nShip it\n")
	writeQueryFile(t, threads, "alpha", "L-0101", "---\nID: L-0101\nThread: alpha\nType: QUERY\nTo: dev-pro\nRoute: heavy\nDate: 2026-07-18\n---\n")
	if err := os.WriteFile(index, []byte("| L-0100 | QUERY | alpha | ROOT | queued | 2026-07-18 |\n| L-0101 | QUERY | alpha | ROOT | queued | 2026-07-18 |\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := scanOutboxQueries(threads, index, map[string]bool{"L-0101": true})
	if err != nil || len(got) != 1 || got[0].Letter != "L-0100" {
		t.Fatalf("outbox = %#v, %v", got, err)
	}
}

func TestFormatOutboxCardShowsMetadataAndReadOnlyButtons(t *testing.T) {
	content, buttons := formatOutboxCard(NewI18n(LangEnglish), outboxRecord{Thread: "alpha", To: "dev-pro", Route: "heavy", QueryPath: "F:\\archive\\L-0100.query.md", Generation: "g1", Summary: "Ship it"}, "L-0100", "", 0, 0)
	for _, want := range []string{"📤 L-0100", "To: dev-pro", "Route: heavy", "Ship it"} {
		if !strings.Contains(content, want) {
			t.Fatalf("missing %q in %q", want, content)
		}
	}
	if len(buttons) != 1 || len(buttons[0]) != 3 || buttons[0][0].Data != "cmd:/outbox page L-0100 g1 0" || buttons[0][1].Data != "cmd:/outbox manual L-0100 g1" || buttons[0][2].Data != "cmd:/outbox secretary L-0100 g1" {
		t.Fatalf("buttons = %#v", buttons)
	}
}

func TestOutboxManualStatePersistsAcrossRestart(t *testing.T) {
	root := t.TempDir()
	e := NewEngine("secretary-seat", &stubAgent{}, nil, "", LangEnglish)
	e.dataDir = root
	e.outboxManual = map[string]bool{"L-0100": true}
	if err := e.saveOutboxManual(); err != nil {
		t.Fatal(err)
	}
	restarted := NewEngine("secretary-seat", &stubAgent{}, nil, "", LangEnglish)
	restarted.dataDir = root
	if !restarted.loadOutboxManual()["L-0100"] {
		t.Fatal("manual outbox state was not persisted")
	}
}

func TestOutboxFirstScanIsQuietBaseline(t *testing.T) {
	root := t.TempDir()
	threads := filepath.Join(root, "threads")
	index := filepath.Join(root, "INDEX.md")
	if err := os.WriteFile(index, []byte("| L-0100 | QUERY | alpha | ROOT | queued | 2026-07-18 |\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeQueryFile(t, threads, "alpha", "L-0100", "---\nID: L-0100\nThread: alpha\nType: QUERY\nTo: dev-pro\nRoute: heavy\nDate: 2026-07-18\n---\n\n## Query\nold\n")
	e := NewEngine("secretary-seat", &stubAgent{}, nil, "", LangEnglish)
	e.dataDir = root
	e.outboxConfig = OutboxConfig{Enabled: true, IndexPath: index}
	e.outboxRecords = map[string]outboxRecord{}
	e.outboxManual = map[string]bool{}
	e.checkOutbox()
	if !e.outboxSeeded || len(e.outboxRecords) != 1 {
		t.Fatalf("baseline = seeded:%v records:%#v", e.outboxSeeded, e.outboxRecords)
	}
}
