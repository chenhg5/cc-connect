# L-0404 Permission Lifecycle Completion Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Complete PR #28's background permission lifecycle without capacity-based denials, and reliably settle every pending stdio request when a session ends.

**Architecture:** Extend the existing `interactiveState` pending/FIFO/approveAll state rather than adding a second permission subsystem. A small active-card lifecycle helper owns timer start/cancel, promotion, and idempotent resolution; teardown snapshots and denies every detached request.

**Tech Stack:** Go, existing `core.Engine`, Go unit tests.

## Global Constraints

- Do not change Secretary configuration or add `auto_approve_tools`.
- Do not alter Claude adapter permission mode or select `bypassPermissions`.
- Queue every background request without a fixed capacity-based deny.
- TTL is active-card-only, defaults to 60 seconds, and is bounded/configurable.
- Session ending paths deny each unresolved stdio request at most once.

---

### Task 1: Add regression tests for unbounded FIFO and `/yolo`

**Files:**
- Modify: `core/engine_test.go` near existing unsolicited permission tests and permission-keyword tests

**Interfaces:**
- Consumes: `Engine.handlePendingPermission`, `Engine.runUnsolicitedReader`, `interactiveState.pendingQueue`.
- Produces: tests proving a 17th queued request is retained, and `/yolo` routes to `allow all` only with a pending request.

- [ ] **Step 1: Write failing queue test**

```go
func TestUnsolicitedReader_PermissionQueueHasNoCapacityDeny(t *testing.T) {
    // create active req-0, then send 16 further requests.
    // assert pendingQueue has length 16 and RespondPermission has no deny calls.
}
```

- [ ] **Step 2: Run the test to verify the old capacity behavior fails**

Run: `go test ./core -run TestUnsolicitedReader_PermissionQueueHasNoCapacityDeny -count=1`

Expected: FAIL because the old 16th queued arrival is denied.

- [ ] **Step 3: Write failing `/yolo` tests**

```go
func TestYoloApprovesActiveQueuedAndFuturePermissionsWithoutChangingMode(t *testing.T) {
    // feed "/yolo" with active plus queued requests and assert all ordinary
    // requests receive allow, approveAll remains session scoped, and adapter mode is untouched.
}

func TestYoloWithoutPendingFallsThroughToAgent(t *testing.T) {
    // assert no permission resolver consumes /yolo without state.pending.
}
```

- [ ] **Step 4: Run the `/yolo` tests to verify they fail**

Run: `go test ./core -run 'TestYolo(ApprovesActiveQueuedAndFuturePermissionsWithoutChangingMode|WithoutPendingFallsThroughToAgent)' -count=1`

Expected: FAIL because `/yolo` is not currently a permission response.

- [ ] **Step 5: Commit**

```text
test(core): define unbounded FIFO and yolo lifecycle behavior
```

### Task 2: Remove capacity denial and normalize `/yolo`

**Files:**
- Modify: `core/engine.go:interactiveState`, `maxPendingPermissionQueue`, `handlePendingPermission`, `runUnsolicitedReader`
- Modify: `core/i18n.go` only if `MsgPermissionQueueFull` becomes unused
- Test: `core/engine_test.go`

**Interfaces:**
- Consumes: Task 1 tests and existing `approveAll` path.
- Produces: unbounded `pendingQueue` and `/yolo` as an active-pending alias for `allow all`.

- [ ] **Step 1: Implement the minimal queue change**

```go
// With state.mu held, append every non-auto-approved candidate:
if state.pending == nil {
    state.pending = candidate
    newPending = candidate
} else {
    state.pendingQueue = append(state.pendingQueue, candidate)
}
```

Remove the queue-cap constant and overflow-response branch; do not send a platform overflow notice or stdio denial for queue length.

- [ ] **Step 2: Implement `/yolo` only at the pending-response boundary**

```go
lower := strings.ToLower(strings.TrimSpace(content))
if lower == "/yolo" {
    lower = "allow all"
}
```

Apply normalization only after `lookupPending` confirms an active pending request; do not add a global command or touch adapter mode.

- [ ] **Step 3: Run Task 1 tests**

Run: `go test ./core -run 'Test(UnsolicitedReader_PermissionQueueHasNoCapacityDeny|YoloApprovesActiveQueuedAndFuturePermissionsWithoutChangingMode|YoloWithoutPendingFallsThroughToAgent)' -count=1`

Expected: PASS.

- [ ] **Step 4: Commit**

```text
fix(core): remove permission queue capacity denial
```

### Task 3: Add active-card TTL with FIFO promotion

**Files:**
- Modify: `core/engine.go:pendingPermission`, active-prompt/promotion helpers
- Modify: `core/engine_test.go`

**Interfaces:**
- Consumes: a request becomes active only through the shared activation helper.
- Produces: `activatePending` starts one cancelable TTL; expiry denies once and promotes the next request.

- [ ] **Step 1: Write failing TTL tests**

```go
func TestPermissionTTLStartsOnlyWhenPromoted(t *testing.T) {
    // hold req-2 in queue longer than TTL, resolve req-1, then assert req-2
    // remains live for its own complete TTL.
}
func TestPermissionExpiryDeniesOnceAndAdvancesFIFO(t *testing.T) {
    // expire req-1, assert exactly one deny and req-2 becomes the sole active card.
}
```

- [ ] **Step 2: Add a test-configurable duration and active timer ownership**

```go
type pendingPermission struct {
    // existing fields
    ttlCancel context.CancelFunc
}

func (e *Engine) activatePending(state *interactiveState, pending *pendingPermission) {
    // set active state, send exactly one prompt, then start a context-bound timer.
}
```

Use a production default of 60 seconds and clamp configured duration to a finite positive range. Cancel the timer before a normal resolve, expiry, or teardown.

- [ ] **Step 3: Implement expiry**

```go
func (e *Engine) expirePending(state *interactiveState, pending *pendingPermission) {
    // verify pointer identity under state.mu; clear only that active request;
    // send PermissionResult{Behavior: "deny"}; resolve once; promote next.
}
```

- [ ] **Step 4: Run TTL tests**

Run: `go test ./core -run 'TestPermission(TTLStartsOnlyWhenPromoted|ExpiryDeniesOnceAndAdvancesFIFO)' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```text
fix(core): expire active permissions without expiring FIFO waiters
```

### Task 4: Deny all detached requests on session teardown

**Files:**
- Modify: `core/engine.go:cleanupInteractiveState`, `cleanupInteractiveStateForIdleToken`, `stopInteractiveSessionWithOptions`
- Modify: `core/engine_test.go`

**Interfaces:**
- Consumes: a helper that atomically detaches active/queued requests and cancels any active TTL.
- Produces: `denyDetachedPendingPermissions` sends exactly one denial for each unresolved stdio request and clears `approveAll`.

- [ ] **Step 1: Write failing teardown test**

```go
func TestPermissionCleanupDeniesEachQueuedRequestOnce(t *testing.T) {
    // set one active and two queued permissions, invoke each teardown entry
    // point, and assert each request ID gets exactly one deny and approveAll is false.
}
```

- [ ] **Step 2: Implement shared detach and deny helpers**

```go
func detachPendingPermissions(state *interactiveState) (AgentSession, []*pendingPermission) {
    // lock, collect active plus FIFO, clear fields and approveAll, cancel active TTL.
}

func denyDetachedPendingPermissions(session AgentSession, pending []*pendingPermission) {
    // for each item: RespondPermission deny, then pending.resolve().
}
```

Call this helper from every existing stop/reset/close path after the session is made unavailable to new events. Do not hold `state.mu` while calling `RespondPermission`.

- [ ] **Step 3: Run teardown tests**

Run: `go test ./core -run TestPermissionCleanupDeniesEachQueuedRequestOnce -count=1`

Expected: PASS.

- [ ] **Step 4: Commit**

```text
fix(core): deny unresolved permissions during session teardown
```

### Task 5: Verify the completed lifecycle

**Files:**
- Modify: `core/engine_test.go` only if a focused regression test is missing.

- [ ] **Step 1: Run focused lifecycle suite**

Run: `go test ./core -run 'Test(UnsolicitedReader_PermissionQueueHasNoCapacityDeny|YoloApprovesActiveQueuedAndFuturePermissionsWithoutChangingMode|YoloWithoutPendingFallsThroughToAgent|PermissionTTLStartsOnlyWhenPromoted|PermissionExpiryDeniesOnceAndAdvancesFIFO|PermissionCleanupDeniesEachQueuedRequestOnce)' -count=1`

Expected: PASS.

- [ ] **Step 2: Run existing related tests and race detector**

Run: `go test -race ./core -run 'Test(UnsolicitedReader_|HandlePendingPermission_|Yolo|PermissionTTL|PermissionExpiry|PermissionCleanup)' -count=1`

Expected: PASS.

- [ ] **Step 3: Inspect diff and commit**

Run: `git diff --check main...HEAD`

Expected: no output.

```text
test(core): cover complete permission lifecycle
```
