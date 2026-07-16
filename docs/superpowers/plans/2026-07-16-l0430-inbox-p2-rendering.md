# L-0430 Inbox P2 Rendering Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `展开原信` more legible on Telegram without creating another message or changing the Inbox card lifecycle.

**Architecture:** Keep `SendReceiptCard` and `UpdateMessageWithButtons` on Telegram's editable HTML path. Extend the deterministic Markdown-to-HTML converter only for syntax common in RESULT letters that otherwise renders as literal notation. Receipt routing, callbacks, pagination, and the plain-text fallback remain unchanged.

**Tech Stack:** Go; `core.MarkdownToSimpleHTML`; Telegram HTML parse mode; Go tests.

## Global Constraints

- Exactly one Inbox message per pending RESULT; no extra Rich Message.
- Preserve inline keyboard actions and in-place card replacement.
- Never invoke a Secretary while expanding, paginating, or collapsing a card.
- Preserve readable plain text if Telegram rejects HTML.

---

### Task 1: Render RESULT task-list state in the editable HTML card

**Files:**
- Modify: `core/markdown_html_test.go`
- Modify: `core/markdown_html.go`

**Interfaces:**
- Consumes: `MarkdownToSimpleHTML(md string) string`.
- Produces: task-list Markdown rendered as Telegram-visible `☐`/`☑` bullets, retaining nesting and inline HTML formatting.

- [x] Write a failing `TestMarkdownToSimpleHTML_TaskList` asserting `- [ ] pending **review**` becomes `• ☐ pending <b>review</b>` and nested `- [x] finished \`build\`` becomes `  • ☑ finished <code>build</code>`.
- [x] Run `go test ./core -run TestMarkdownToSimpleHTML_TaskList -count=1`; it must fail because checkbox markers are still literal.
- [x] Add `reTaskList` and normalize a leading checkbox inside the existing unordered-list branch before `convertInlineHTML`.
- [x] Re-run the focused test; it must pass.

### Task 2: Verify receipt-card delivery remains editable and safe

**Files:**
- Modify: `platform/telegram/telegram_test.go`

**Interfaces:**
- Consumes: `Platform.SendReceiptCard`, `Platform.UpdateMessageWithButtons`.
- Produces: proof that both paths use HTML parse mode, render a task checkbox, and retain callback buttons.

- [x] Add `TestReceiptCardRendersTaskListAsEditableHTML` using `newStubTelegramBot` and a `ButtonOption`.
- [x] Assert sent and edited parameters have `models.ParseModeHTML`, contain `☑ done`, and retain the supplied button.
- [x] Re-run the focused test; it passes.

### Task 3: Keep the rolling cache non-accumulating and non-blocking

**Files:**
- Modify: `core/notify.go`
- Modify: `core/notify_test.go`

- [x] Add a failing store test proving absent RESULT letters have their diff base removed while active letters retain theirs.
- [x] Run it; it failed because `pruneDiffBases` did not exist.
- [x] Prune only cache files whose corresponding RESULT is absent from the successful scan; do not prune on receipt acknowledgement.
- [x] Add and run the watcher regression test proving a cache-directory failure still sends a receipt card.

### Task 4: Full verification

**Files:**
- Modify: this plan, marking completed checkboxes.

- [x] Run `go test ./core -count=1`, `go test ./platform/telegram -count=1`, `go vet ./core`, and `git diff --check`; all exit zero.
- [x] Commit the completed plan with `docs: record L-0430 Inbox P2 plan`.
