# Read-only Telegram Outbox Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show registered, undispatched QUERY letters as independent, expandable Telegram Outbox cards without starting an agent or dispatching work.

**Architecture:** Add a dedicated `OutboxConfig` and watcher/store rather than widening `NotifyConfig`. The watcher discovers valid `*.query.md` files, requires an INDEX QUERY row, excludes letter IDs present in the dispatch ledger, and persists each card’s file-generation and Telegram locator in `outbox_ledger.json`.

**Tech Stack:** Go, existing cc-connect Engine, Telegram inline buttons, TOML option map.

## Global Constraints

- Use `telegram:-1003917051393:5025:7664413698` as the configured Outbox session.
- Keep the feature read-only: no `executeDispatch`, no QUERY/INDEX mutation, no agent turn.
- Missing `outbox_session_key` falls back to `notify_session_key`.
- A dispatched letter disappears from Outbox; no automatic dispatch or Hold/Cancel callback ships in this change.

---

### Task 1: Outbox discovery, eligibility, and persistence

**Files:** `core/outbox.go`, `core/outbox_test.go`

- [ ] Write failing tests proving a valid registered QUERY is emitted, an unregistered QUERY is ignored, and an ID in `dispatch_expectations.json` is removed.
- [ ] Run `go test ./core -run 'Test(ScanOutbox|Outbox)' -count=1`; expect failures because outbox types do not exist.
- [ ] Implement `OutboxConfig`, `queryFileInfo`, `outboxStore`, `scanQueryFiles`, and `checkOutbox`; use the query file `mtime` as the generation and `dispatchStore.letters()` as the exclusion set.
- [ ] Re-run the focused tests; expect PASS.

### Task 2: Telegram card and read-only commands

**Files:** `core/outbox.go`, `core/engine.go`, `core/i18n.go`, `core/outbox_test.go`, `core/engine_test.go`

- [ ] Write failing tests for compact card content, `cmd:/outbox page` generation guard, collapse, and `/outbox` pending listing; assert no synthetic message is sent to an agent.
- [ ] Run the focused tests; expect failure because `/outbox` is unknown.
- [ ] Implement `formatOutboxCard`, paging helpers, `showOutboxPage`, `showOutboxCompact`, and command routing. Reuse `ReceiptCardManager`, `InlineMessageUpdater`, and `MessageDeleter` interfaces only; cards offer View query/Collapse and no state-changing action.
- [ ] Re-run the focused tests; expect PASS.

### Task 3: Configuration wiring and Nexus deployment configuration

**Files:** `cmd/cc-connect/main.go`, `cmd/cc-connect/main_test.go` if existing option-wiring coverage applies, `F:\nexus\config.toml`

- [ ] Write failing config-wiring tests proving `outbox_enabled`, poll interval, and `outbox_session_key` are passed to `Engine.SetOutboxConfig`, with notify-session fallback.
- [ ] Run the targeted test; expect failure because no wiring exists.
- [ ] Add option parsing and set Secretary `outbox_enabled = true`, `outbox_session_key = "telegram:-1003917051393:5025:7664413698"`; preserve existing Inbox settings.
- [ ] Re-run configuration tests and TOML parse validation; expect PASS.

### Task 4: Regression verification and delivery

**Files:** all above; `docs/telegram.md` only if it already documents Inbox commands.

- [ ] Run `go test ./core/... -count=1`, `go test ./cmd/cc-connect/... -count=1`, `go test ./platform/telegram/... -count=1`, `go vet ./core`, and `go build ./cmd/cc-connect`.
- [ ] Check `git diff --check`; inspect that the diff contains no `executeDispatch` call from Outbox.
- [ ] Commit on `architect/L-0447-outbox-readonly`; do not push, merge, or restart the live service without further authorization.
