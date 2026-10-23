# Codex `/ps` Turn Steering Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `/ps` guide the active Codex app-server turn through `turn/steer` without losing the final answer.

**Architecture:** Keep `core.AgentSession` and the `/ps` command unchanged. Inside `appServerSession.Send`, snapshot `currentTurn`: an active turn uses a dedicated steering request, while an idle session follows the existing start-turn path. Steering preserves the active turn and buffered messages.

**Tech Stack:** Go, Codex app-server JSON-RPC v2, standard `testing` package.

## Global Constraints

- Match Codex's native guidance behavior with `turn/steer`, `threadId`, `expectedTurnId`, and structured `input`.
- Do not fall back to `turn/start` when steering fails.
- Do not mutate `currentTurn`, `pendingMsgs`, or preamble state during steering.
- Keep core agent-agnostic and retain current behavior for every non-Codex agent.
- Base the PR on current upstream `origin/main`; do not include unrelated local commits.

---

### Task 1: Add the app-server steering regression test

**Files:**
- Modify: `agent/codex/appserver_session_test.go`

**Interfaces:**
- Consumes: `appServerSession.Send(prompt string, images []core.ImageAttachment, files []core.FileAttachment) error`
- Produces: regression coverage for active-turn steering and idle-turn start behavior

- [ ] **Step 1: Add a JSON-RPC response harness**

Add a helper that waits for one request written to `lockedWriteCloser`, decodes its ID/method/params, and calls `s.handleResponse` with the supplied result. The helper must expose the decoded request to the test so assertions cover the real JSON-RPC payload.

- [ ] **Step 2: Write the active-turn failing test**

Construct an alive session with `threadID="thread-1"`, `currentTurn="turn-1"`, and one buffered agent message. Call `Send` in a goroutine, answer the outgoing request with `{"turnId":"turn-1"}`, and assert:

```go
if request.Method != "turn/steer" {
    t.Fatalf("method = %q, want turn/steer", request.Method)
}
if request.Params["threadId"] != "thread-1" {
    t.Fatalf("threadId = %v, want thread-1", request.Params["threadId"])
}
if request.Params["expectedTurnId"] != "turn-1" {
    t.Fatalf("expectedTurnId = %v, want turn-1", request.Params["expectedTurnId"])
}
if got := currentTurn(s); got != "turn-1" {
    t.Fatalf("current turn = %q, want turn-1", got)
}
if got := pendingMessages(s); !reflect.DeepEqual(got, []string{"buffered answer"}) {
    t.Fatalf("pending messages = %v, want buffered answer", got)
}
```

- [ ] **Step 3: Run the focused test and confirm RED**

Run:

```bash
go test ./agent/codex -run '^TestAppServerSession_SendSteersActiveTurn$' -count=1 -v
```

Expected: fail because the request method is `turn/start`, the payload lacks `expectedTurnId`, and buffered state is cleared.

- [ ] **Step 4: Add the idle-turn and final-answer assertions**

Cover that an idle session still emits `turn/start`, stores the returned turn ID, and clears stale state only at that new-turn boundary. After steering, feed an `item/completed(agentMessage)` and `turn/completed` notification for the original turn and assert one full `EventText` followed by one `EventResult`.

### Task 2: Implement protocol-correct steering

**Files:**
- Modify: `agent/codex/appserver_session.go`
- Test: `agent/codex/appserver_session_test.go`

**Interfaces:**
- Consumes: current thread ID, `currentTurn`, structured app-server input, and `request`
- Produces: `startTurn(threadID string, input []map[string]any) error` and `steerTurn(threadID, expectedTurnID string, input []map[string]any) error`

- [ ] **Step 1: Add the steering response type**

```go
type turnSteerResponse struct {
    TurnID string `json:"turnId"`
}
```

- [ ] **Step 2: Split start and steering paths**

Snapshot `currentTurn` under `stateMu` after preparing the input. If it is non-empty, send:

```go
params := map[string]any{
    "threadId":       threadID,
    "expectedTurnId": expectedTurnID,
    "input":          input,
}
```

through `turn/steer`. Validate a non-empty response `turnId` equal to `expectedTurnID`. Return without modifying turn or message state.

If no turn is active, keep the current `turn/start` parameters, response validation, and new-turn state reset.

- [ ] **Step 3: Run focused tests and confirm GREEN**

```bash
go test ./agent/codex -run 'TestAppServerSession_Send(SteersActiveTurn|StartsIdleTurn)' -count=1 -v
```

Expected: both tests pass.

- [ ] **Step 4: Add precondition failure coverage**

Return an error for an empty or mismatched `turnId` from `turn/steer`. Assert that the error names `turn/steer` and that `currentTurn` plus `pendingMsgs` remain unchanged.

- [ ] **Step 5: Format and run the Codex package**

```bash
gofmt -w agent/codex/appserver_session.go agent/codex/appserver_session_test.go
go test ./agent/codex -count=1
go test -race ./agent/codex -run 'TestAppServerSession_Send' -count=1
```

Expected: pass with no race reports.

- [ ] **Step 6: Commit the implementation**

```bash
git add agent/codex/appserver_session.go agent/codex/appserver_session_test.go
git commit -m "fix(codex): steer active app-server turns"
```

### Task 3: Verify, review, and publish

**Files:**
- Verify: `agent/codex/appserver_session.go`
- Verify: `agent/codex/appserver_session_test.go`
- Verify: `docs/superpowers/specs/2026-07-11-codex-ps-turn-steer-design.md`

**Interfaces:**
- Consumes: completed branch diff
- Produces: a ready-for-review upstream pull request

- [ ] **Step 1: Run repository checks**

```bash
go test ./agent/codex -count=1
go test ./core -run 'TestCmdPs|TestCUJ' -count=1
go test ./...
go test -race ./agent/codex -count=1
go vet ./...
go build ./...
git diff --check origin/main...HEAD
```

Diagnose and resolve every relevant failure. If an unrelated upstream baseline test remains environment-dependent, document exact before/after evidence without weakening or skipping the check.

- [ ] **Step 2: Review the diff**

Compare `origin/main...HEAD` against the approved design. Check protocol field names against generated Codex app-server schema and verify no secret, generated artifact, or unrelated file is tracked.

- [ ] **Step 3: Request code review**

Use the code-reviewer workflow with `BASE_SHA=$(git rev-parse origin/main)` and `HEAD_SHA=$(git rev-parse HEAD)`. Resolve every Critical or Important finding and rerun the relevant checks.

- [ ] **Step 4: Push and open the PR**

```bash
git push -u fork agent/fix-codex-ps-steer
gh pr create --repo chenhg5/cc-connect --base main --head AaronZ345:agent/fix-codex-ps-steer --title "fix(codex): steer active app-server turns" --body-file /tmp/cc-connect-ps-steer-pr-body.md
```

Create a ready-for-review PR. The body must explain the root cause, protocol change, preserved `/ps` behavior, and complete validation evidence.

- [ ] **Step 5: Monitor checks until green**

Use `gh pr checks --watch` and inspect any failed GitHub Actions logs. Fix, commit, push, and continue monitoring until all required checks pass.
