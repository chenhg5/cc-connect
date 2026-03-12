# Feishu Delete Card Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add a Feishu card-based multi-select delete flow triggered by `/delete` without changing existing explicit batch delete command semantics.

**Architecture:** Introduce a per-session delete-mode state keyed by `sessionKey`, render a dedicated delete-selection card and confirmation card, and route Feishu card `act:` callbacks through new delete-mode actions. Keep `/list` unchanged and keep explicit `/delete <args>` command execution on the existing code path.

**Tech Stack:** Go 1.24, `core.Engine`, shared card builder utilities, Feishu interactive cards, Go `testing`.

---

### Task 1: Add failing tests for delete-mode card flow

**Files:**
- Modify: `core/engine_test.go`

**Step 1: Write the failing test**

Add tests for:
- `/delete` with no args on a card-capable platform renders delete-mode card instead of usage text
- delete-mode list toggles selected session IDs through card actions
- confirmation card lists selected sessions
- submit deletes only selected sessions and clears delete-mode state
- cancel returns to normal list/current view and clears state

**Step 2: Run test to verify it fails**

Run: `go test ./core -run 'TestCmdDelete|TestDeleteMode'`
Expected: FAIL because delete-mode state and card actions do not exist.

**Step 3: Write minimal implementation**

Add the smallest delete-mode state and rendering hooks required for the first test to pass before expanding behavior.

**Step 4: Run test to verify it passes**

Run: `go test ./core -run 'TestCmdDelete|TestDeleteMode'`
Expected: PASS for the implemented slice.

**Step 5: Commit**

```bash
git add core/engine_test.go core/engine.go core/i18n.go
git commit -m "feat: add delete mode card flow scaffolding"
```

### Task 2: Implement delete-mode state and card rendering

**Files:**
- Modify: `core/engine.go`
- Modify: `core/i18n.go`
- Test: `core/engine_test.go`

**Step 1: Write the failing test**

Add tests covering:
- selected rows remain selected across pagination
- delete-mode footer buttons enable confirmation only when selection exists
- confirmation card back button preserves selection

**Step 2: Run test to verify it fails**

Run: `go test ./core -run 'TestDeleteMode'`
Expected: FAIL on missing state transitions or incorrect card rendering.

**Step 3: Write minimal implementation**

Implement:
- delete-mode state structure
- render function for delete-mode list
- render function for confirmation/result cards
- helper to resolve display names from selected session IDs

**Step 4: Run test to verify it passes**

Run: `go test ./core -run 'TestDeleteMode'`
Expected: PASS

**Step 5: Commit**

```bash
git add core/engine.go core/engine_test.go core/i18n.go
git commit -m "feat: render delete selection cards"
```

### Task 3: Wire Feishu card actions to delete-mode behavior

**Files:**
- Modify: `core/engine.go`
- Test: `core/engine_test.go`
- Inspect: `platform/feishu/feishu.go`

**Step 1: Write the failing test**

Add tests for:
- `act:/delete-mode toggle <id>`
- `act:/delete-mode confirm`
- `act:/delete-mode back`
- `act:/delete-mode submit`
- `act:/delete-mode cancel`

**Step 2: Run test to verify it fails**

Run: `go test ./core -run 'TestDeleteMode'`
Expected: FAIL because action dispatch does not recognize delete-mode actions.

**Step 3: Write minimal implementation**

Update `handleCardNav` and `executeCardAction` to:
- mutate delete-mode state in place
- re-render the correct card after each action
- clear state on submit/cancel

**Step 4: Run test to verify it passes**

Run: `go test ./core -run 'TestDeleteMode'`
Expected: PASS

**Step 5: Commit**

```bash
git add core/engine.go core/engine_test.go
git commit -m "feat: wire delete mode card actions"
```

### Task 4: Reuse deletion logic and protect edge cases

**Files:**
- Modify: `core/engine.go`
- Test: `core/engine_test.go`

**Step 1: Write the failing test**

Add tests for:
- active session in selected set is blocked and reported
- missing session ID in selected set is reported without aborting the whole batch
- explicit `/delete 1,2,3` and `/delete 1,3-5,8` still use command parsing path

**Step 2: Run test to verify it fails**

Run: `go test ./core -run 'TestCmdDelete|TestDeleteMode'`
Expected: FAIL on batch execution/reporting edge cases.

**Step 3: Write minimal implementation**

Refactor deletion helpers so card-mode submit can delete by selected session ID while sharing reply/result formatting with the command path.

**Step 4: Run test to verify it passes**

Run: `go test ./core -run 'TestCmdDelete|TestDeleteMode'`
Expected: PASS

**Step 5: Commit**

```bash
git add core/engine.go core/engine_test.go
git commit -m "feat: execute delete mode batch removal"
```

### Task 5: Run broader verification

**Files:**
- Test: `core/engine_test.go`
- Test: `platform/feishu/platform_test.go`

**Step 1: Run core verification**

Run: `go test ./core/...`
Expected: PASS

**Step 2: Run repository verification**

Run: `go test ./...`
Expected: PASS
