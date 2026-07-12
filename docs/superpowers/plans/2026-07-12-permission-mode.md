# Claude Permission Bridge Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the Secretary pre-approve three Claude-native research tools and safely approve otherwise-background permission requests from Telegram.

**Architecture:** Reuse Claude Code's existing `allowed_tools` passthrough; do not parse tool rules in cc-connect.  Refactor the existing foreground pending-permission state into a per-session FIFO service used by both foreground and unsolicited event loops, with an active-card-only TTL. `/yolo` delegates to the existing shared `approveAll` state.

**Tech Stack:** Go, existing core Engine event loop, Telegram inline buttons, TOML.

## Global Constraints

- Preserve ordered, verbatim `allowed_tools` values; Claude Code owns rule parsing.
- No `auto_approve_tools`, wildcard parser, `WebFetch` entry, or default unscoped `WebFetch`.
- Only one displayed permission card per session; default queue limit 16 and active TTL 60 seconds.
- `/yolo` may set `approveAll` only when a pending request exists; never alter Claude `permission-mode`.
- Reset, stop, cancel, close, expiry, and late callbacks are idempotent; every request receives at most one response.

---

### Task 1: Configure native Claude allow rules

**Files:**
- Modify: `config.example.toml` (seat's `allowed_tools` entry)
- Test: `agent/claudecode/claudecode_test.go`

**Interfaces:**
- Consumes: existing `Agent.New(opts)` and `allowed_tools` handling.
- Produces: Secretary CLI arguments `--allowedTools Skill Workflow WebSearch` in configured order.

- [ ] **Step 1: Write the failing config/adapter test**

```go
func TestNew_AllowedToolsPreservesConfiguredOrder(t *testing.T) {
    a, err := New(map[string]any{"allowed_tools": []any{"Skill", "Workflow", "WebSearch"}})
    require.NoError(t, err)
    assert.Equal(t, []string{"Skill", "Workflow", "WebSearch"}, a.(*Agent).GetAllowedTools())
}
```

- [ ] **Step 2: Run the focused test**

Run: `go test ./agent/claudecode -run TestNew_AllowedToolsPreservesConfiguredOrder -count=1`

Expected: it verifies order and verbatim rule preservation.

- [ ] **Step 3: Add the Secretary TOML list**

```toml
allowed_tools = [
  "Skill",
  "Workflow",
  "WebSearch",
]
```

- [ ] **Step 4: Re-run adapter test and TOML parser test**

Run: `go test ./agent/claudecode -run AllowedTools -count=1; go test ./config -count=1`

Expected: PASS.

### Task 2: Queue background permissions behind the existing approval card

**Files:**
- Modify: `core/engine.go:689-700, 5072-5111, 5796-5883`
- Test: `core/engine_test.go`

**Interfaces:**
- Consumes: `pendingPermission`, `interactiveState.pending`, `AgentSession.RespondPermission`.
- Produces: `enqueuePermission(state, event, source)` and `resolveActivePermission(state, result)` that display exactly one pending request and advance FIFO.

- [ ] **Step 1: Write failing tests**

```go
func TestUnsolicitedPermissionQueuesAndDisplaysOnlyFirst(t *testing.T) { /* emit req-1, req-2; assert one card, queue contains req-2 */ }
func TestPermissionTTLStartsWhenRequestBecomesActive(t *testing.T) { /* queue req-2 beyond TTL; resolve req-1; assert req-2 receives its full TTL */ }
func TestPermissionExpiryDeniesOnceAndAdvancesFIFO(t *testing.T) { /* expire req-1 twice; assert one deny, req-2 card */ }
```

- [ ] **Step 2: Run focused tests and observe failure**

Run: `go test ./core -run 'TestUnsolicitedPermissionQueuesAndDisplaysOnlyFirst|TestPermissionTTLStartsWhenRequestBecomesActive|TestPermissionExpiryDeniesOnceAndAdvancesFIFO' -count=1`

Expected: FAIL because unsolicited requests are immediately denied and no queue exists.

- [ ] **Step 3: Implement the minimal queue lifecycle**

```go
type queuedPermission struct { event Event; enqueuedAt time.Time }
// interactiveState gains pendingQueue []queuedPermission and pendingTimer *time.Timer.
// activateNextPermission starts TTL only after sendPermissionPrompt succeeds.
// resolve/expiry clear active state under state.mu before exactly one RespondPermission call.
```

- [ ] **Step 4: Route both event loops through the lifecycle**

```go
// foreground EventPermissionRequest and unsolicited EventPermissionRequest
// call enqueuePermission; remove unsolicited immediate-deny behavior.
```

- [ ] **Step 5: Run focused tests**

Run: `go test ./core -run 'TestUnsolicitedPermissionQueuesAndDisplaysOnlyFirst|TestPermissionTTLStartsWhenRequestBecomesActive|TestPermissionExpiryDeniesOnceAndAdvancesFIFO' -count=1`

Expected: PASS.

### Task 3: Make `/yolo` a shared approve-all alias and clear state safely

**Files:**
- Modify: `core/engine.go:3099-3110, 3591-3731`, cleanup paths
- Test: `core/engine_test.go`

**Interfaces:**
- Consumes: `interactiveState.approveAll`, `handlePendingPermission`, queue lifecycle.
- Produces: `/yolo` command behavior that invokes the same `allow all` resolution path.

- [ ] **Step 1: Write failing tests**

```go
func TestYoloApprovesActiveQueuedAndFuturePermissionsWithoutChangingMode(t *testing.T) { /* assert approveAll, all three allows, mode unchanged */ }
func TestPermissionCleanupDeniesEachQueuedRequestOnce(t *testing.T) { /* stop/reset/late expiry; assert one deny per request */ }
func TestYoloWithoutPendingFallsThroughToAgent(t *testing.T) { /* assert command dispatcher returns false */ }
```

- [ ] **Step 2: Run focused tests and observe failure**

Run: `go test ./core -run 'TestYoloApprovesActiveQueuedAndFuturePermissionsWithoutChangingMode|TestPermissionCleanupDeniesEachQueuedRequestOnce|TestYoloWithoutPendingFallsThroughToAgent' -count=1`

Expected: FAIL because `/yolo` is not recognized.

- [ ] **Step 3: Implement alias and cleanup**

```go
// handleCommand recognizes yolo only if lookupPending finds an active request.
// It passes "allow all" into handlePendingPermission.
// queue activation checks state.approveAll and responds allow without a card.
// cleanup stops the active timer, atomically drains pending + queue, and denies each once.
```

- [ ] **Step 4: Run focused tests**

Run: `go test ./core -run 'TestYoloApprovesActiveQueuedAndFuturePermissionsWithoutChangingMode|TestPermissionCleanupDeniesEachQueuedRequestOnce|TestYoloWithoutPendingFallsThroughToAgent' -count=1`

Expected: PASS.

### Task 4: Full verification and documentation

**Files:**
- Modify: `docs/superpowers/specs/2026-07-12-permission-mode-design.md`
- Test: `core`, `agent/claudecode`, `config`

- [ ] **Step 1: Run full relevant suite**

Run: `go test ./core ./agent/claudecode ./config -count=1`

Expected: PASS with no failures.

- [ ] **Step 2: Review the diff against global constraints**

Run: `git diff main...HEAD -- core/engine.go agent/claudecode config.toml docs/superpowers`

Expected: no cc-connect rule parser, no `bypassPermissions` change, and no unscoped default `WebFetch`.

- [ ] **Step 3: Commit implementation and configuration separately**

```bash
git add core agent/claudecode docs/superpowers
git commit -m "feat: queue Claude background permission approvals"
git -C F:\nexus\worktrees\architect-L-0404-permission-mode add config.toml
git -C F:\nexus\worktrees\architect-L-0404-permission-mode commit -m "config: preapprove secretary research tools"
```
