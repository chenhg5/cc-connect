# Usage Command Design

**Date:** 2026-03-12

**Goal:** Add a built-in `/usage` command that reports model/account quota usage, starting with Codex running under ChatGPT OAuth, while keeping the retrieval path generic so other agents can plug in later.

## Scope

- Add a new built-in slash command: `/usage`.
- The command is independent from `/status` and `/doctor`.
- Usage retrieval is exposed as an optional agent capability, not hardcoded into the engine for a single vendor.
- First implementation targets the Codex agent when local ChatGPT OAuth credentials are available in `~/.codex/auth.json`.
- Unsupported agents should return a clear “not supported” style message rather than failing the whole command system.

## Architecture

### Command Layer

`core/engine.go` will register and dispatch `/usage` as a normal built-in command. The engine should not know how ChatGPT, Gemini, or any future provider exposes quota data. It only detects whether the current agent implements a usage-reporting interface and formats the response.

### Agent Capability Layer

Add a new optional interface in `core/interfaces.go`, for example:

- `UsageReporter`
- a method such as `GetUsage(context.Context) (*UsageReport, error)`

The report type should be generic enough to cover multiple providers:

- provider/agent name
- subject or account label
- plan type / tier if available
- one or more rate-limit buckets/windows
- optional credits/balance fields
- raw provider-specific metadata only if needed for debugging

This keeps future integrations local to each agent package.

### Codex Implementation

`agent/codex` will implement the new interface by:

1. Reading `~/.codex/auth.json`
2. Extracting:
   - `tokens.access_token`
   - `tokens.account_id`
3. Calling:
   - `GET https://chatgpt.com/backend-api/wham/usage`
4. Passing headers:
   - `Authorization: Bearer <access_token>`
   - `ChatGPT-Account-Id: <account_id>`
   - `User-Agent: codex-cli`
5. Mapping the JSON response into the generic usage report

If `auth.json` is missing, fields are absent, or the HTTP call fails, the agent should return a normal error so `/usage` can present a concise failure message.

## Data Model

The generic report should support at least:

- identity:
  - provider
  - account_id
  - user/email if available
- plan:
  - plan_type
- standard rate limits:
  - allowed
  - limit_reached
  - primary window
  - secondary window
- review/code-review limits:
  - same window shape when available
- credits:
  - has_credits
  - unlimited
  - balance

Each window should preserve:

- used percent
- total window seconds
- reset-after seconds
- reset timestamp if supplied

This is enough for the current ChatGPT OAuth response and still general enough for other providers with multiple quota windows.

## Output Format

The first output version should be plain text and compact. Suggested shape:

- title line with agent/provider
- plan line
- standard usage section
- code review usage section if present
- credits section if present

For each window:

- whether requests are currently allowed
- whether the limit is reached
- used percent
- reset timing

Prefer rendering both relative time and absolute time if easy to do consistently. If absolute rendering is added, it should use local time formatting already used by the project, or a simple RFC3339 fallback.

## Error Handling

- Agent does not implement usage reporting:
  - reply with a user-facing “current agent does not support `/usage`”
- Codex auth file missing:
  - explain that ChatGPT OAuth login data was not found
- Token/account id missing:
  - explain credentials are incomplete
- HTTP non-200:
  - include status code in logs; show concise failure text to user
- JSON decode failure:
  - report provider response parse failure

Do not expose bearer tokens or raw auth file contents in user-visible output or logs.

## Testing

Minimum tests should cover:

- command dispatch recognizes `/usage`
- engine returns unsupported message when agent lacks the interface
- engine formats a successful generic usage report
- Codex usage fetch maps a representative `wham/usage` payload correctly
- Codex usage fetch errors on missing auth file / missing token fields

Network-dependent tests should use injected transport or `httptest`, not live requests.

## Non-Goals

- No merging into `/status` or `/doctor`
- No card UI in the first version
- No polling or background caching in the first version
- No support for non-Codex agents in this change beyond the shared interface
