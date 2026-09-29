# Codex `/ps` Turn Steering Design

## Problem

`/ps` is meant to add guidance to a turn that is already running. The engine currently implements that behavior by calling `AgentSession.Send`. For the Codex app-server backend, `Send` always issues `turn/start`, even when a turn is active. It then overwrites `currentTurn` and clears `pendingMsgs`.

That does not match the Codex app-server protocol. Mid-turn guidance has a dedicated `turn/steer` request with an `expectedTurnId` precondition. Using `turn/start` for `/ps` creates a race between `item/completed`, `turn/completed`, and `thread/status/changed`. The model can finish a full answer after cc-connect has already emitted an empty result, leaving the user with `(empty response)`.

## Chosen Approach

Keep the public `AgentSession` contract and `/ps` command unchanged. Make the Codex app-server session choose the correct protocol method based on its state:

- When no turn is active, `Send` starts a new turn with `turn/start`.
- When a turn is active, `Send` steers that turn with `turn/steer`.

This keeps core agent-agnostic and gives every existing caller the behavior it already expects: a normal message starts work, while a message injected into a busy session guides the work in progress.

## Protocol and State Handling

For an active turn, the steering request contains:

- `threadId`: the current Codex thread;
- `expectedTurnId`: the active turn captured before the request;
- `input`: the same structured text and image input used for a normal send.

The response `turnId` must match the expected active turn. Steering must not change `currentTurn`, clear `pendingMsgs`, resend the prompt preamble, or create a second completion path. If Codex rejects the precondition because the turn finished concurrently, cc-connect returns the request error rather than silently starting another turn.

For an idle session, the existing `turn/start` behavior remains unchanged. A newly returned turn ID becomes `currentTurn`, and stale pending messages are cleared at that new-turn boundary.

## Error Handling

Errors keep the existing contextual wrapping and distinguish `turn/start` from `turn/steer`. An empty or mismatched steering turn ID is treated as a protocol error. No fallback to `turn/start` is allowed after a steering failure because that would turn guidance into a separate user turn and recreate the behavior mismatch.

## Tests

Regression coverage will use the app-server JSON-RPC test harness and prove that:

1. an idle `Send` still emits `turn/start`;
2. an active `Send` emits `turn/steer` with the correct `threadId`, `expectedTurnId`, and input;
3. steering preserves `currentTurn` and buffered agent messages;
4. the original turn still emits its complete final text followed by one result event;
5. a mismatched steering response is rejected without mutating turn state;
6. existing `/ps`, Codex app-server, CUJ, full test, build, vet, and relevant race checks remain green.

## Pull Request Scope

The PR will be based on current upstream `origin/main` and contain only the Codex steering fix, its regression tests, and this design record. Unrelated local baseline failures will be diagnosed separately and included only if they represent a repository defect required for the PR checks to pass.
