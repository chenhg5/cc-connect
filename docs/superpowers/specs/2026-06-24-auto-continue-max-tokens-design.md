# Auto-continue on max_tokens Design

## Context

cc-connect bridges messaging platforms to local coding agents. After recent Claude Code and cc-connect updates, users can see turns end with `stop=max_tokens`. In practice this means the agent response hit the output token ceiling and was truncated, not that the session failed. Today this leaves users with an incomplete answer and requires manual recovery. Users also reported that creating a new session loses prior chat context, but this first phase focuses only on same-session auto-continue.

Runtime observations from the local installation:

- Installed cc-connect: `v1.4.0-beta.2` via npm wrapper and compiled Go binary.
- Session storage already keeps per-session platform-visible `history` for recent sessions.
- `cc-connect sessions list` shows many zero-message sessions after `/new`, confirming that blank new sessions are working as designed but are not a solution for truncation.
- The source repository is at `/Users/ai/cc-connect`.

## Goals

1. Detect when a running agent turn ended because of `max_tokens` output truncation.
2. Automatically continue in the same cc-connect session and the same underlying agent session.
3. Limit automatic continuation to 5 attempts for a single user turn.
4. After 5 automatic attempts, stop and tell the user they can manually send “继续” to continue.
5. Do not open a new session during normal `max_tokens` recovery.
6. Raise the effective output ceiling where cc-connect can safely configure it, and prefer streaming behavior so users see partial progress before auto-continuation.

## Non-goals

- Implement `new-history` or `fork` in this phase.
- Summarize or migrate full historical conversations into new sessions in this phase.
- Change the existing semantics of `/new`; it remains a blank new session.
- Guarantee that all agents expose the same stop-reason signal. The implementation should support a common interface and agent-specific opt-in as needed.

## User-visible behavior

When an agent turn reaches `max_tokens`:

1. The current partial output remains visible using the existing streaming/progress path.
2. cc-connect sends a short status notice on the first auto-continue attempt:
   - Chinese: `回复达到输出上限，正在同一会话自动续写…`
   - English: `The response hit the output limit. Continuing in the same session…`
3. cc-connect sends an internal continuation prompt to the same agent session.
4. Continuation repeats until:
   - the agent finishes normally;
   - the user cancels or sends a new message;
   - the agent/session errors;
   - 5 auto-continuation attempts have been used.
5. If 5 attempts are used and the response still ends with `max_tokens`, cc-connect stops auto-continuing and notifies the user:
   - Chinese: `已自动续写 5 次，但仍达到输出上限。你可以发送“继续”手动续写，或把任务拆小。`
   - English: `I auto-continued 5 times but still hit the output limit. Send “continue” to keep going manually, or split the task into smaller parts.`

## Architecture

### Stop reason propagation

The clean boundary is the `core.AgentSession` event stream. Agent adapters already parse their own CLI/API output and emit events into core. Add or reuse an event-level stop reason field so core can know that a turn ended due to `max_tokens` without hardcoding Claude Code or any platform name.

Recommended shape:

- Add an optional `StopReason` string to the existing turn-complete event type, if such an event exists.
- If there is no single turn-complete event, add a minimal core-level event for turn completion metadata.
- Agents that can parse a stop reason set `StopReasonMaxTokens` when they see `max_tokens`, `max_token`, or equivalent normalized values.
- Agents that cannot detect this leave the field empty; behavior stays unchanged.

### Engine auto-continue state

Core owns cross-platform user behavior, so the continuation loop belongs in `core`, not in Feishu or in a single agent adapter.

For each active user turn/session key, core tracks an auto-continue state:

- `active`: whether this user turn is in an auto-continue chain.
- `attempts`: number of automatic continuation prompts already sent.
- `generation`: a turn identifier to prevent stale continuations from firing after a new user message.
- `cancelled`: set when `/cancel`, stop, session switch, or new user input supersedes the chain.

When the current turn completes with `StopReasonMaxTokens`, core checks the state and either sends the continuation prompt or stops at the 5-attempt limit.

### Continuation prompt

The continuation prompt is an internal message sent to the same agent session, not a normal platform-visible user message.

Prompt text:

`上一条回复因为 max_tokens 被截断。请从中断处继续，不要重复已经输出的内容。如果任务尚未完成，继续执行；如果已经完成，只做简短收尾。`

For non-Chinese config, use an English equivalent.

The prompt should be marked in history/metadata as internal auto-continue if the session history model supports metadata. If it does not, avoid appending this prompt to platform-visible history as a normal user message.

### Output ceiling and streaming

Add configurable options under agent options with safe defaults:

- `auto_continue_on_max_tokens = true`
- `auto_continue_max_attempts = 5`
- `max_tokens` or `output_tokens` if the relevant agent/provider already supports such an option.

The implementation must not blindly add unsupported flags to every agent CLI. It should:

1. Use existing provider/agent max-token configuration if already present.
2. Add the option only to adapters that can safely pass it through.
3. Prefer streaming where an adapter already supports streaming; do not regress non-streaming adapters.
4. Log when a configured max-token option is ignored because the adapter does not support it.

## Error handling

- If the continuation `Send` call fails, stop the chain and notify the user with a concise error.
- If the agent session is gone or cannot resume, do not silently create a new session in phase 1. Tell the user that same-session recovery failed and a new history-carrying session can be added in the next phase.
- If the user sends a new message while auto-continuation is active, the new message wins and the old chain stops.
- `/cancel` stops the active auto-continuation chain.
- Repeated `max_tokens` after the 5th attempt produces the manual-continue notice and does not send a 6th internal prompt.

## i18n

All new user-facing strings go through `core/i18n.go` with translations for the repository-supported languages: EN, ZH, ZH-TW, JA, ES.

Required messages:

- auto-continue started
- auto-continue attempt progress, if implemented
- auto-continue max attempts reached
- auto-continue failed due to send/session error

## Tests

The implementation must include regression coverage.

Recommended tests:

1. Core unit test: a stub agent emits normal completion; no auto-continue is sent.
2. Core unit test: a stub agent emits `StopReasonMaxTokens`; core sends one continuation prompt to the same agent session.
3. Core unit test: five consecutive `max_tokens` completions send exactly five continuation prompts and then send the manual-continue notice.
4. Core unit test: a new user message during auto-continue cancels the old chain.
5. History test: internal continuation prompt is not shown as a normal platform user message in `/history` if metadata support exists.
6. CUJ test if core engine message flow is modified: user sends a long task, agent hits max tokens, user sees continuation notice, same session continues, and after 5 attempts user sees the manual continuation instruction.
7. Agent adapter tests for any adapter-specific stop-reason parsing added, especially Claude Code.

Run at minimum:

- `go test ./core/ -run TestCUJ -v` for core flow changes.
- Package-specific tests for touched agent adapter packages.
- `go test ./...` before final completion if feasible.

## Rollout

Default behavior should be enabled but bounded:

- auto-continue enabled by default;
- max attempts defaults to 5;
- users can disable it or change the attempt count in config.

Existing configs remain valid because all new options have defaults.

## Phase 2 future work

After phase 1 is stable, implement `new-history` or `fork` as a separate feature:

- `new` stays blank.
- `new-history` / `fork` creates a new agent session and injects a concise summary plus recent messages from stored `history`.
- This should be designed separately so it does not complicate the `max_tokens` same-session recovery path.
