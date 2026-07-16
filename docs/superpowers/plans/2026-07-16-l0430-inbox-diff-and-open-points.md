# L-0430 Inbox Diff and Open Points Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make RESULT Inbox cards show declared Open Points and a conditional, mobile-readable view of the current text in sections changed since the prior RESULT generation.

**Architecture:** Extend the existing `core/notify.go` watcher and `notify_ledger.json` receipt record with parsed Open Points and a current-generation `receiptUpdate`. Keep one previous RESULT body per letter under the ignored data directory solely to compute section changes; it is overwritten on every generation and is never a source for agents. Reuse `formatReceiptInboxCard`, `ReceiptCardManager`, and `/receipt` callback routing to render compact changes inline or page long changes on the same Telegram message.

**Tech Stack:** Go standard library, existing cc-connect core notify ledger, existing Telegram inline-button platform adapter.

## Global Constraints

- The archive `result.md` is the authoritative original; no snapshot hash or immutable-copy protocol is introduced.
- The diff cache retains at most one predecessor per L-ID and is not passed to any agent.
- New letters use exact `## Open Points`; exact legacy `## Open Questions` remains supported; prose keyword matching is forbidden.
- Summary extraction remains the existing first non-empty `## Conclusion` / `## Blocker` paragraph.
- `查看本次更新` exists only for a non-empty update that cannot fit safely on the compact card.
- Compact updates render current changed text only; no old/removed text appears in Telegram.
- Existing original-page, receive, primary-handoff, generation-staleness, and no-agent-invocation behavior must remain unchanged.

---

## File Structure

- `core/notify.go` — exact RESULT-section parsing, rolling diff-base I/O, receipt update data, compact/update page formatting, watcher integration.
- `core/notify_test.go` — parser, cache, card, and watcher lifecycle tests.
- `core/engine.go` — `/receipt update` callback parsing and page renderer.
- `core/engine_test.go` — update-page callback remains platform-only and rejects stale generations.
- `core/i18n.go` — compact card and update-page/button strings in existing supported languages.
- `docs/protocols/Boss–Secretary–Engineer-protocol.md` — canonicalize future RESULT heading to `## Open Points`, while stating legacy compatibility.

### Task 1: Deterministic section parser and rolling diff base

**Files:**
- Modify: `core/notify.go:60-205`
- Test: `core/notify_test.go`

**Interfaces:**
- Produces `type receiptSection struct { Heading string; Body string }` and `type receiptUpdate struct { Sections []receiptSection }`.
- Produces `func extractOpenPoints(body string) []string`, `func diffResultSections(previous, current string) receiptUpdate`, and `func (s *notifyStore) updateDiffBase(letter string, current []byte) (receiptUpdate, error)`.
- `receiptRecord` gains `OpenPoints []string` and `Update receiptUpdate` JSON fields.

- [ ] **Step 1: Write failing parser and diff tests**

```go
func TestExtractOpenPointsUsesExactHeadingsOnly(t *testing.T) {
	body := "## Conclusion\nready\n\n## Open Points\n- ship it\n- test it\n\ntext: open points are elsewhere\n"
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
	if !reflect.DeepEqual(got.Sections, want) { t.Fatalf("update = %#v", got) }
}
```

- [ ] **Step 2: Run the focused tests to verify they fail**

Run: `go test ./core -run 'TestExtractOpenPointsUsesExactHeadingsOnly|TestDiffResultSectionsReturnsCurrentChangedTextOnly' -count=1`

Expected: FAIL because the parser and update types do not exist.

- [ ] **Step 3: Add exact section extraction and diff-base storage**

```go
type receiptSection struct { Heading string `json:"heading"`; Body string `json:"body"` }
type receiptUpdate struct { Sections []receiptSection `json:"sections,omitempty"` }

func (s *notifyStore) diffBasePath(letter string) string {
	return filepath.Join(filepath.Dir(s.path), "notify_diff_cache", letter+".md")
}

func (s *notifyStore) updateDiffBase(letter string, current []byte) (receiptUpdate, error) {
	path := s.diffBasePath(letter)
	previous, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) { return receiptUpdate{}, err }
	update := diffResultSections(string(previous), string(current))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { return receiptUpdate{}, err }
	if err := AtomicWriteFile(path, current, 0o644); err != nil { return receiptUpdate{}, err }
	return update, nil
}
```

Implement `resultSections` by splitting only on exact `## ` headings, preserving current section bodies, and extracting Markdown list items only under the two allowed Open Points headings. Treat a missing prior base as `receiptUpdate{}`.

- [ ] **Step 4: Wire parsed fields into arrival rows without losing delivery on cache failure**

Add `OpenPoints []string` and `Update receiptUpdate` to `indexResultRow` and `receiptRecord`. In `checkNewResults`, read the current result bytes once per fresh file; call `updateDiffBase`; on error log `notify: diff base unavailable` and set an empty update, then still call `notifyLetterArrived`. Pass `extractResultSummary`, `extractResultStatus`, and `extractOpenPoints` the same already-read content via new `...FromBody` helpers.

- [ ] **Step 5: Run focused tests to verify they pass**

Run: `go test ./core -run 'TestExtractOpenPointsUsesExactHeadingsOnly|TestDiffResultSectionsReturnsCurrentChangedTextOnly' -count=1`

Expected: PASS.

- [ ] **Step 6: Commit**

```powershell
git add core/notify.go core/notify_test.go
git commit -m "feat(notify): track result section updates"
```

### Task 2: Compact envelope rendering with inline Open Points and updates

**Files:**
- Modify: `core/notify.go:444-480`
- Modify: `core/i18n.go:524-860`
- Test: `core/notify_test.go`

**Interfaces:**
- Consumes `receiptRecord.OpenPoints` and `receiptRecord.Update` from Task 1.
- Produces `func formatReceiptUpdateBody(update receiptUpdate) string` and `func receiptUpdatePages(update receiptUpdate) []string`.

- [ ] **Step 1: Write failing compact-card tests**

```go
func TestFormatReceiptInboxCardRendersOpenPointsAndShortUpdateInline(t *testing.T) {
	record := receiptRecord{Thread: "alpha", Status: "DONE", Summary: "ready", ArrivedAt: "2026-07-16T20:00:00Z", ResultPath: "C:\\x.md", Generation: "g1",
		OpenPoints: []string{"decide retention"}, Update: receiptUpdate{Sections: []receiptSection{{Heading: "Conclusion", Body: "new text"}}}}
	content, buttons := formatReceiptInboxCard(NewI18n(LangEnglish), "L-0430", record, "", 0, 0)
	for _, want := range []string{"📬 L-0430 · Updated", "Open points:", "• decide retention", "Changes:", "Conclusion\nnew text"} {
		if !strings.Contains(content, want) { t.Fatalf("card missing %q: %s", want, content) }
	}
	if strings.Contains(content, "View this update") || len(buttons) != 1 { t.Fatalf("short update must be inline: %#v", buttons) }
}
```

- [ ] **Step 2: Run the focused test to verify it fails**

Run: `go test ./core -run TestFormatReceiptInboxCardRendersOpenPointsAndShortUpdateInline -count=1`

Expected: FAIL because the compact formatter has no Open Points or update rendering.

- [ ] **Step 3: Implement card text and conditional update button**

Keep `MsgReceiptCardCompact` for the existing base fields. Append `\n\nOpen points:\n• ...` only when `record.OpenPoints` is non-empty. Render `Updated` in the title only when `len(record.Update.Sections) > 0`. Use a named `receiptCompactUpdateLimit` below the Telegram text maximum; if `formatReceiptUpdateBody(record.Update)` fits, append it as `Changes:`. If it does not fit, append one `MsgReceiptViewUpdate` callback:

```go
{Text: i18n.T(MsgReceiptViewUpdate), Data: "cmd:/receipt update " + letter + " " + generation + " 0"}
```

Do not add this callback for an empty update. Keep the existing original, receive, and primary buttons on all compact cards.

- [ ] **Step 4: Add i18n keys**

Add `MsgReceiptOpenPoints`, `MsgReceiptChanges`, `MsgReceiptUpdated`, `MsgReceiptUpdatePage`, and `MsgReceiptViewUpdate` beside existing receipt keys. Provide English, Chinese, Japanese, Korean, and Spanish values using the existing map style. The Chinese strings are `开放点：`, `本次更新：`, `已更新`, `本次更新（第 %d/%d 页）\n%s`, and `查看本次更新`.

- [ ] **Step 5: Add long-update button test and run formatter tests**

```go
func TestFormatReceiptInboxCardAddsUpdateButtonOnlyForLongUpdate(t *testing.T) {
	record := receiptRecord{Generation: "g1", Update: receiptUpdate{Sections: []receiptSection{{Heading: "Conclusion", Body: strings.Repeat("x", receiptCompactUpdateLimit)}}}}
	_, buttons := formatReceiptInboxCard(NewI18n(LangEnglish), "L-0430", record, "", 0, 0)
	if !hasButton(buttons, "cmd:/receipt update L-0430 g1 0") { t.Fatal("missing conditional update button") }
}
```

Run: `go test ./core -run 'TestFormatReceiptInboxCard' -count=1`

Expected: PASS.

- [ ] **Step 6: Commit**

```powershell
git add core/notify.go core/notify_test.go core/i18n.go
git commit -m "feat(notify): render inbox open points and updates"
```

### Task 3: Conditional update-page callbacks without agent turns

**Files:**
- Modify: `core/engine.go:7280-7420`
- Test: `core/engine_test.go`

**Interfaces:**
- Consumes `receiptUpdatePages(update receiptUpdate) []string` from Task 2.
- Produces `func (e *Engine) showReceiptUpdatePage(p Platform, msg *Message, letter string, page int, generation ...string)`.

- [ ] **Step 1: Write failing callback tests**

```go
func TestEngineReceiptUpdatePageUpdatesInboxCardWithoutAgentTurn(t *testing.T) {
	// Set up a pending receipt with Generation "g1" and a long receiptUpdate.
	// Invoke /receipt update L-0430 g1 0 and assert UpdateMessageWithButtons
	// receives current update text while the agent call count remains zero.
}

func TestEngineReceiptUpdatePageRejectsStaleGeneration(t *testing.T) {
	// Invoke /receipt update L-0430 old-generation 0 and assert the platform
	// receives MsgReceiptUnavailable rather than an update edit.
}
```

- [ ] **Step 2: Run the focused tests to verify they fail**

Run: `go test ./core -run 'TestEngineReceiptUpdatePage' -count=1`

Expected: FAIL because `/receipt update` is not recognized.

- [ ] **Step 3: Add the command branch and renderer**

Add this sibling branch before `page` in the existing `/receipt` parser:

```go
if args[0] == "update" && (len(args) == 3 || len(args) == 4) {
	page, err := strconv.Atoi(args[len(args)-1])
	if err != nil { e.reply(p, msg.ReplyCtx, e.i18n.T(MsgReceiptUnavailable)); return true }
	generation := ""
	if len(args) == 4 { generation = args[2] }
	e.showReceiptUpdatePage(p, msg, args[1], page, generation)
	return true
}
```

`showReceiptUpdatePage` must use the same receipt availability and generation check as `showReceiptPage`, build pages from `receipt.Update`, validate the page range, and call `UpdateMessageWithButtons`. Its footer has Prev/Next callbacks using `cmd:/receipt update`, plus collapse/receive/primary buttons; it never calls `AgentSession.Send`.

- [ ] **Step 4: Run the focused tests to verify they pass**

Run: `go test ./core -run 'TestEngineReceipt(UpdatePage|PageUpdatesInboxCardWithoutAgentTurn)' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add core/engine.go core/engine_test.go
git commit -m "feat(receipt): page long inbox updates"
```

### Task 4: Watcher lifecycle, cache failure, protocol, and regression verification

**Files:**
- Modify: `core/notify_test.go`
- Modify: `docs/protocols/Boss–Secretary–Engineer-protocol.md`

**Interfaces:**
- Consumes all Task 1-3 interfaces.
- Produces regression evidence that updates retain existing card lifecycle and ordinary receipt delivery survives cache failure.

- [ ] **Step 1: Write lifecycle and failure tests**

```go
func TestNotifyLetterArrivedUpdatesPendingCardWithCurrentSectionChanges(t *testing.T) {
	// First arrival has no Update. A changed pending generation updates the same
	// card once and its rendered content includes only the current changed text.
}

func TestNotifyLetterArrivedDeliversWhenDiffCacheWriteFails(t *testing.T) {
	// Configure a notifyStore whose diff-cache parent is a file. A fresh RESULT
	// must still send a normal receipt card with no Changes block.
}

func TestNotifyMtimeOnlyChangeHasNoUpdate(t *testing.T) {
	// Re-run the watcher with identical result bytes and a later ModTime; the
	// stored record has an empty Update and compact card has no Updated marker.
}
```

- [ ] **Step 2: Run the focused tests to verify they fail**

Run: `go test ./core -run 'TestNotify(LetterArrivedUpdatesPendingCardWithCurrentSectionChanges|LetterArrivedDeliversWhenDiffCacheWriteFails|MtimeOnlyChangeHasNoUpdate)' -count=1`

Expected: FAIL until the lifecycle and cache behavior are fully wired.

- [ ] **Step 3: Implement any minimal wiring needed for the tests**

Ensure initial watcher seeding writes a diff base for every existing RESULT without sending cards. Ensure an acknowledged record that later changes preserves the current generation’s update data while reopening as pending. Ensure `recordArrivalTransition` writes `OpenPoints` and `Update` for any strictly newer generation, but leaves a same-generation record unchanged.

- [ ] **Step 4: Update the letter protocol**

Replace the RESULT template heading `## Open Questions` with `## Open Points` and state directly below it: `Use this section for unresolved questions, decisions, risks, or follow-up work. Readers must accept legacy ## Open Questions.` Do not rewrite historical RESULT files.

- [ ] **Step 5: Run targeted and package verification**

Run:

```powershell
go test ./core -count=1
go test ./platform/telegram -count=1
go vet ./core
git diff --check
```

Expected: all commands exit 0.

- [ ] **Step 6: Commit**

```powershell
git add core/notify.go core/notify_test.go docs/protocols/Boss–Secretary–Engineer-protocol.md
git commit -m "test(notify): cover inbox update lifecycle"
```

## Self-Review

- Spec coverage: Task 1 implements exact heading parsing and bounded previous-body state; Task 2 implements direct Open Points and conditional compact changes; Task 3 implements only-long-update paging; Task 4 covers watcher failure, initial seeding, pending replacement, and the protocol migration.
- Placeholder scan: no unresolved marker or deferred implementation step appears; each test, function, path, and command is named.
- Type consistency: `receiptSection`, `receiptUpdate`, `receiptRecord.Update`, `receiptUpdatePages`, and `showReceiptUpdatePage` are introduced before later tasks consume them.

## Execution Handoff

Plan saved at `docs/superpowers/plans/2026-07-16-l0430-inbox-diff-and-open-points.md`.

1. **Subagent-Driven (recommended):** dispatch a fresh agent per task and review each task before the next.
2. **Inline Execution:** execute sequentially in this worktree with checkpoints.
