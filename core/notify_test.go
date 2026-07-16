package core

import (
	"os"
	"path/filepath"
	"reflect"
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

func TestExtractOpenPointsUsesExactHeadingsOnly(t *testing.T) {
	body := "## Conclusion\nready\n\n## Open Points\n- ship it\n- test it\n\n## Notes\ntext: open points are elsewhere\n"
	if got := extractOpenPoints(body); !reflect.DeepEqual(got, []string{"ship it", "test it"}) {
		t.Fatalf("open points = %#v", got)
	}
	legacy := "## Open Questions\n- legacy item\n"
	if got := extractOpenPoints(legacy); !reflect.DeepEqual(got, []string{"legacy item"}) {
		t.Fatalf("legacy open points = %#v", got)
	}
}

func TestDiffResultSectionsReturnsCurrentChangedTextOnly(t *testing.T) {
	previous := "## Conclusion\nold\n\n## Open Points\n- keep\n"
	current := "## Conclusion\nnew\n\n## Open Points\n- keep\n- decide\n"
	got := diffResultSections(previous, current)
	want := []receiptSection{{Heading: "Conclusion", Body: "new"}, {Heading: "Open Points", Body: "- keep\n- decide"}}
	if !reflect.DeepEqual(got.Sections, want) {
		t.Fatalf("update = %#v, want %#v", got, want)
	}
}

func TestNotifyStoreUpdateDiffBaseKeepsOnlyPreviousGeneration(t *testing.T) {
	store := newNotifyStore(filepath.Join(t.TempDir(), "data"))
	if got, err := store.updateDiffBase("L-0430", []byte("## Conclusion\nfirst\n")); err != nil || len(got.Sections) != 0 {
		t.Fatalf("first base = %#v, %v", got, err)
	}
	got, err := store.updateDiffBase("L-0430", []byte("## Conclusion\nsecond\n"))
	if err != nil || !reflect.DeepEqual(got.Sections, []receiptSection{{Heading: "Conclusion", Body: "second"}}) {
		t.Fatalf("second base = %#v, %v", got, err)
	}
	data, err := os.ReadFile(store.diffBasePath("L-0430"))
	if err != nil || string(data) != "## Conclusion\nsecond\n" {
		t.Fatalf("rolling base = %q, %v", data, err)
	}
}

func TestNotifyStorePersistsOpenPointsAndUpdateForNewGeneration(t *testing.T) {
	store := newNotifyStore(filepath.Join(t.TempDir(), "data"))
	row := indexResultRow{Letter: "L-0430", Thread: "alpha", Generation: "g1", OpenPoints: []string{"decide"}, Update: receiptUpdate{Sections: []receiptSection{{Heading: "Conclusion", Body: "new"}}}}
	if _, err := store.recordArrivalTransition(row); err != nil {
		t.Fatal(err)
	}
	record, err := store.receipt("L-0430")
	if err != nil || !reflect.DeepEqual(record.OpenPoints, row.OpenPoints) || !reflect.DeepEqual(record.Update, row.Update) {
		t.Fatalf("receipt = %#v, %v", record, err)
	}
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
	if got := extractResultStatus(donePath); got != "" {
		t.Errorf("missing status = %q, want empty", got)
	}

	stuckPath := writeResultFile(t, root, "alpha", "L-0101", "---\nID: L-0101\nStatus: STUCK\n---\n\n## Partial Work\nsome\n\n## Blocker\nbudget exhausted.\n")
	if got := extractResultSummary(stuckPath); got != "budget exhausted." {
		t.Errorf("STUCK summary = %q, want %q", got, "budget exhausted.")
	}
	if got := extractResultStatus(stuckPath); got != "STUCK" {
		t.Errorf("STUCK status = %q, want STUCK", got)
	}

	bodyStatusPath := writeResultFile(t, root, "alpha", "L-0102", "ID: L-0102\nStatus: DONE\n---\n\n## Conclusion\nready\n\nStatus: STUCK\n")
	if got := extractResultStatus(bodyStatusPath); got != "DONE" {
		t.Errorf("header status = %q, want DONE", got)
	}
	noHeaderStatusPath := writeResultFile(t, root, "alpha", "L-0103", "ID: L-0103\n---\n\n## Conclusion\nready\n\nStatus: STUCK\n")
	if got := extractResultStatus(noHeaderStatusPath); got != "" {
		t.Errorf("body status = %q, want empty", got)
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

func TestCheckNewResultsStoresParsedOpenPointsAndGenerationUpdate(t *testing.T) {
	root := t.TempDir()
	threadsDir := filepath.Join(root, "threads")
	indexPath := filepath.Join(root, "INDEX.md")
	if err := os.WriteFile(indexPath, []byte("# Archive INDEX\n"), 0o644); err != nil { t.Fatal(err) }
	e := NewEngine("secretary-seat", &stubAgent{}, nil, "", LangEnglish)
	e.dataDir = root
	e.configureNotify(NotifyConfig{Enabled: true, IndexPath: indexPath})
	path := writeResultFile(t, threadsDir, "alpha", "L-0430", "## Conclusion\nfirst\n\n## Open Points\n- decide\n")
	e.checkNewResults()
	if err := os.WriteFile(path, []byte("## Conclusion\nsecond\n\n## Open Points\n- decide\n- ship\n"), 0o644); err != nil { t.Fatal(err) }
	next := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, next, next); err != nil { t.Fatal(err) }
	e.checkNewResults()
	record, err := e.notifyStore.receipt("L-0430")
	if err != nil { t.Fatal(err) }
	if !reflect.DeepEqual(record.OpenPoints, []string{"decide", "ship"}) {
		t.Fatalf("open points = %#v", record.OpenPoints)
	}
	if !reflect.DeepEqual(record.Update.Sections, []receiptSection{{Heading: "Conclusion", Body: "second"}, {Heading: "Open Points", Body: "- decide\n- ship"}}) {
		t.Fatalf("update = %#v", record.Update)
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
	root := t.TempDir()
	resultPath := writeResultFile(t, root, "cc-connect-maintenance", "L-0430", "ID: L-0430\nStatus: DONE\n---\n\nbody\n")
	store := newNotifyStore(filepath.Join(root, "data"))
	if err := store.recordArrival(indexResultRow{
		Letter: "L-0430", Thread: "cc-connect-maintenance", Summary: "ready",
		Path: resultPath, Status: "DONE",
	}); err != nil {
		t.Fatalf("recordArrival: %v", err)
	}
	first, changed, err := store.acknowledge("L-0430", "jay")
	if err != nil || !changed {
		t.Fatalf("first acknowledge = (%+v, %v, %v), want changed receipt", first, changed, err)
	}
	if first.AcknowledgedBy != "jay" || first.AcknowledgedAt == "" || first.ResultPath == "" || first.Status != "DONE" {
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

func TestNotifyStoreKeepsOriginalResultPathAtArrival(t *testing.T) {
	root := t.TempDir()
	resultPath := writeResultFile(t, root, "alpha", "L-0431", "ID: L-0431\nStatus: DONE\n---\n\n## Conclusion\noriginal\n")
	store := newNotifyStore(filepath.Join(root, "data"))
	if err := store.recordArrival(indexResultRow{Letter: "L-0431", Thread: "alpha", Path: resultPath, Status: "DONE"}); err != nil {
		t.Fatalf("record arrival: %v", err)
	}
	ledger, err := store.load()
	if err != nil {
		t.Fatal(err)
	}
	record := ledger.Receipts["L-0431"]
	if got, want := record.ResultPath, resultPath; got != want {
		t.Fatalf("result path = %q, want %q", got, want)
	}
}

func TestNotifyStoreReceiptGenerationReplacesPendingAndReopensAcknowledged(t *testing.T) {
	root := t.TempDir()
	resultPath := writeResultFile(t, root, "alpha", "L-0430", "body")
	store := newNotifyStore(filepath.Join(root, "data"))
	first := indexResultRow{Letter: "L-0430", Thread: "alpha", Path: resultPath, Summary: "first", Status: "DONE", Generation: "2026-07-16T20:00:00Z"}
	if _, err := store.recordArrivalTransition(first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.Summary, second.Generation = "second", "2026-07-16T20:01:00Z"
	arrival, err := store.recordArrivalTransition(second)
	if err != nil || !arrival.Replaced || arrival.Receipt.Summary != "second" || arrival.Receipt.AcknowledgedAt != "" {
		t.Fatalf("pending replacement = %+v, %v", arrival, err)
	}
	if _, changed, err := store.acknowledge("L-0430", "jay"); err != nil || !changed {
		t.Fatalf("acknowledge = (%v, %v)", changed, err)
	}
	third := second
	third.Summary, third.Generation = "third", "2026-07-16T20:02:00Z"
	arrival, err = store.recordArrivalTransition(third)
	if err != nil || !arrival.Replaced || arrival.Receipt.AcknowledgedAt != "" || arrival.Receipt.Summary != "third" {
		t.Fatalf("acknowledged re-entry = %+v, %v", arrival, err)
	}
}

func TestNotifyStorePreservesFullReceiptSummaryWithoutCreatingSnapshot(t *testing.T) {
	root := t.TempDir()
	body := "ID: L-0430\nStatus: DONE\n---\n\nimmutable body\n"
	resultPath := writeResultFile(t, root, "alpha", "L-0430", body)
	store := newNotifyStore(filepath.Join(root, "data"))
	longSummary := strings.Repeat("legacy summary ", 40)
	if err := store.save(notifyLedger{Receipts: map[string]receiptRecord{
		"L-0430": {Thread: "alpha", ResultPath: resultPath, Summary: longSummary, Status: "DONE", ArrivedAt: "2026-07-16T16:15:01Z"},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.recordArrival(indexResultRow{Letter: "L-0430", Thread: "alpha", Path: resultPath, Summary: longSummary, Status: "DONE"}); err != nil {
		t.Fatal(err)
	}
	record, err := store.receipt("L-0430")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := record.ResultPath, resultPath; got != want {
		t.Fatalf("legacy result path = %q, want %q", got, want)
	}
	if got, want := record.Summary, longSummary; got != want {
		t.Fatalf("summary = %q, want full %q", got, want)
	}
}

func TestReceiptEnvelopeGivesAgentOriginalResultPath(t *testing.T) {
	got := formatReceiptEnvelope("L-0430", receiptRecord{
		Thread:     "cc-connect-maintenance",
		Status:     "DONE",
		ResultPath: "F:\\nexus\\docs\\archive\\threads\\cc-connect-maintenance\\L-0430.result.md",
	})
	want := "[RECEIPT L-0430]\n原信文件：F:\\nexus\\docs\\archive\\threads\\cc-connect-maintenance\\L-0430.result.md\n线程：cc-connect-maintenance\n状态：DONE\n\n请直接读取上述 RESULT 原信，并按正常译信流程处理。"
	if got != want {
		t.Errorf("receipt envelope = %q, want %q", got, want)
	}
}

func TestReceiptInboxCardPaginatesOriginalResultWithoutHash(t *testing.T) {
	record := receiptRecord{
		Thread: "alpha", Status: "DONE", Summary: "ready", ArrivedAt: "2026-07-16T16:20:00Z",
		ResultPath: "F:\\nexus\\docs\\archive\\threads\\alpha\\L-0431.result.md",
	}
	content, buttons := formatReceiptInboxCard(NewI18n(LangEnglish), "L-0431", record, "first page\nsecond page", 0, 2)
	if !strings.Contains(content, "📬 L-0431") || !strings.Contains(content, "Thread: alpha") || !strings.Contains(content, "Result path: F:\\nexus\\docs\\archive\\threads\\alpha\\L-0431.result.md") || strings.Contains(content, "SHA-256") || !strings.Contains(content, "Page 1/2") {
		t.Fatalf("inbox card content = %q", content)
	}
	if got := buttons[0][0].Text; got != "Next →" {
		t.Fatalf("next button = %q", got)
	}
	if got := buttons[0][0].Data; got != "cmd:/receipt page L-0431 2026-07-16T16:20:00Z 1" {
		t.Fatalf("next button = %q", got)
	}
	if got := buttons[len(buttons)-1][0].Data; got != "cmd:/receipt collapse L-0431 2026-07-16T16:20:00Z" {
		t.Fatalf("collapse button = %q", got)
	}
	if got := buttons[len(buttons)-1][1].Data; got != "cmd:/receipt receive L-0431 2026-07-16T16:20:00Z" {
		t.Fatalf("receive button = %q", got)
	}
	if got := buttons[len(buttons)-1][2].Data; got != "cmd:/receipt primary L-0431 2026-07-16T16:20:00Z" {
		t.Fatalf("primary button = %q", got)
	}

	_, buttons = formatReceiptInboxCard(NewI18n(LangEnglish), "L-0431", record, "", 0, 0)
	if got := buttons[0][0].Text; got != "View original" {
		t.Fatalf("view-original button = %q", got)
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

func TestNotifyLetterArrivedDoesNotAdvertiseReceiptWithoutStore(t *testing.T) {
	p := &stubInlineButtonPlatform{stubPlatformEngine: stubPlatformEngine{n: "telegram"}}
	e := NewEngine("secretary-seat", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.configureNotify(NotifyConfig{
		TelegramEnabled: true,
		ToastEnabled:    false,
		Platform:        "telegram",
		SessionKey:      "telegram:123:123",
	})

	e.notifyLetterArrived(indexResultRow{Letter: "L-0430", Thread: "cc-connect-maintenance"})

	if len(p.buttonRows) != 0 {
		t.Fatalf("receipt button advertised without store: %#v", p.buttonRows)
	}
	if got, want := p.getSent(), []string{"📬 L-0430 到货"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("plain notification = %#v, want %#v", got, want)
	}
}

func TestNotifyLetterArrivedUpdatesPendingCardForNewGeneration(t *testing.T) {
	root := t.TempDir()
	resultPath := writeResultFile(t, root, "alpha", "L-0430", "body")
	p := &receiptActionPlatform{stubPlatformEngine: stubPlatformEngine{n: "telegram"}}
	e := NewEngine("secretary-seat", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.notifyStore = newNotifyStore(filepath.Join(root, "data"))
	e.notifyConfig = NotifyConfig{TelegramEnabled: true, Platform: "telegram", SessionKey: "telegram:123:123"}
	first := indexResultRow{Letter: "L-0430", Thread: "alpha", Path: resultPath, Status: "DONE", Summary: "first", Generation: "2026-07-16T20:00:00Z"}
	e.notifyLetterArrived(first)
	second := first
	second.Summary, second.Generation = "second", "2026-07-16T20:01:00Z"
	e.notifyLetterArrived(second)
	if p.receiptCardsSent != 1 || p.receiptCardsUpdated != 1 {
		t.Fatalf("card lifecycle = send %d update %d, want 1/1", p.receiptCardsSent, p.receiptCardsUpdated)
	}
	if !strings.Contains(p.updatedContent, "second") {
		t.Fatalf("updated card = %q", p.updatedContent)
	}
}
