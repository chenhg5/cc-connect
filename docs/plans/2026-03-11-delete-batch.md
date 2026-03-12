# Delete Batch Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add explicit batch deletion support for `/delete 1,2,3`, `/delete 3-7`, and `/delete 1,3-5,8` without introducing ambiguous whitespace-based parsing.

**Architecture:** Keep the existing single-delete flow intact for one plain argument, and add a narrow parser that only activates when `/delete` receives one argument containing comma/range syntax. Resolve list positions from a single session snapshot, deduplicate targets, then execute deletions with a combined reply that reports successes and blocked items.

**Tech Stack:** Go 1.24, existing `core.Engine` command handlers, `testing` package.

---

### Task 1: Add failing command tests

**Files:**
- Modify: `core/engine_test.go`

**Step 1: Write the failing test**

Add command-level tests for:
- `/delete 1,2,3`
- `/delete 3-7`
- `/delete 1,3-5,8`
- invalid explicit syntax like `/delete 1,3-a,8`
- non-supported whitespace-separated args staying non-batch

**Step 2: Run test to verify it fails**

Run: `go test ./core -run TestCmdDelete`
Expected: FAIL because batch parsing does not exist yet.

### Task 2: Implement explicit batch delete parsing

**Files:**
- Modify: `core/engine.go`
- Modify: `core/i18n.go`

**Step 1: Write minimal implementation**

Add a helper that:
- only recognizes one-argument explicit batch syntax
- parses comma-separated integers and inclusive ranges
- rejects malformed items
- deduplicates indices while preserving order

Update `cmdDelete` to:
- route explicit batch syntax through the new helper
- keep existing single-delete behavior for plain one-argument input
- reject ambiguous multi-argument inputs with usage text
- aggregate batch results into one reply

**Step 2: Run targeted tests**

Run: `go test ./core -run TestCmdDelete`
Expected: PASS

### Task 3: Verify no regression in core tests

**Files:**
- Test: `core/engine_test.go`
- Test: `core/i18n_test.go`

**Step 1: Run broader verification**

Run: `go test ./core/...`
Expected: PASS
