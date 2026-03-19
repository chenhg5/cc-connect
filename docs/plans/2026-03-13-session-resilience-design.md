# Session Resilience Design

**Date:** 2026-03-13
**Status:** Approved
**Branch:** feat/multi-workspace

## Problem

Multi-workspace mode introduces long-lived, concurrent Claude Code sessions that are reaped on idle and resumed on demand. Several failure modes cause silent context loss ("context rot"):

1. **CWD mismatch** — workspace paths that differ by trailing slash, symlink, or relative segment map to different Claude Code session directories, causing resume to silently start a fresh session
2. **Resume failure** — when a session's context is too large, `--resume` fails with "Prompt is too long" and the session becomes permanently broken until manual `!new`
3. **Invisible context degradation** — users have no signal that context is filling up until Claude starts forgetting things
4. **Silent failures** — session lifecycle events (spawn, resume, reap, failure) lack diagnostic logging

## Design

### 1. Path Normalization

**Helper:** `normalizeWorkspacePath(path string) string` in `workspace_state.go`

```
filepath.Clean(path) → filepath.EvalSymlinks(cleanedPath)
```

If `EvalSymlinks` fails (path doesn't exist yet), fall back to `filepath.Clean` only.

**Applied at two sites:**
- `workspacePool.GetOrCreate(workspace)` — normalize the key before map lookup/insert
- Workspace binding resolution — normalize before the workspace string enters the system

**Logging:** `slog.Debug("workspace path normalized", "original", path, "normalized", result)` when normalization changes the input.

### 2. Resume Failure → Fresh Session Fallback

**Location:** `getOrCreateInteractiveStateWith()` in engine.go

**Current behavior:** `StartSession` failure → state with nil `agentSession` → broken until `!new`.

**New behavior:**

1. If `StartSession` fails AND `session.AgentSessionID != ""` (resume attempt):
   - Log failure with diagnostics: session ID, error message, cwd
   - Clear `session.AgentSessionID` and save
   - Retry `agent.StartSession(ctx, "")` for a fresh session
   - Post platform notification: *"Session context was too large to resume — starting fresh. Project context is preserved in CLAUDE.md."*
2. If fresh retry also fails → fall through to existing nil-state behavior
3. If original call was already fresh (`AgentSessionID == ""`) → no retry, fall through as today

**Notification:** Send via `p.Send(ctx, replyCtx, msg)` — both are available on the `interactiveState` being constructed.

### 3. Context Consumption Indicator

**Dual-track approach with logging to compare accuracy over time.**

#### Track A: SDK token counts (accurate, cc-connect-owned)

- In `processInteractiveEvents`, parse `result` events for `input_tokens` usage
- Store cumulative `input_tokens` on the `interactiveState` (updated each turn)
- Compute percentage: `input_tokens / 200_000 * 100` (model context window)
- Append `[ctx: XX%]` to every message relayed to the platform

#### Track B: Claude self-report (approximate, for comparison)

- Add to system prompt via `--append-system-prompt`: instruction to append `[ctx: ~XX%]` to every response
- Parse the self-reported value from Claude's output before relaying

#### Logging

On every turn that has both values:
```
slog.Info("context_usage",
    "sdk_pct", sdkPct,
    "self_reported_pct", selfReportedPct,
    "session_key", sessionKey,
    "input_tokens", inputTokens)
```

Over time, compare drift to decide whether the system prompt instruction adds value.

#### Display

Appended to every visible message relayed to the platform:
```
Here's the refactored auth module...
[ctx: 62%]
```

If no token data available yet (first message), skip the indicator.

### 4. Diagnostic Logging

Structured `slog` logging at key lifecycle points:

| Event | Level | Fields |
|-------|-------|--------|
| Session spawn | Info | normalized cwd, session ID (or "new"), model |
| Session resume | Info | session ID, JSONL file path, file size |
| Resume failure | Error | session ID, error, stderr, cwd, JSONL file size |
| Fresh fallback | Warn | original session ID, new session ID, cwd |
| Idle reap | Info | session key, workspace path, idle duration, token count at reap |
| Context per-turn | Info | session key, input_tokens, sdk_pct, self_reported_pct |
| Path normalization | Debug | original path, normalized path (only when changed) |

**JSONL file size:** Resolve via `findProjectDir` + stat at resume time. Log even if file not found (indicates cwd mismatch).

## Non-Goals

- **Proactive compaction** — likely to cause more trouble than it's worth; the context indicator gives users agency to compact manually
- **Session summary → new session pattern** — more robust but significantly more implementation work; revisit if resume-with-fallback proves insufficient
- **Disk/memory monitoring** — out of scope; can be added as operational tooling later

## Implementation Order

1. Path normalization (prerequisite for everything else being reliable)
2. Diagnostic logging (needed to verify the other changes work)
3. Resume failure fallback (highest-value fix)
4. Context consumption indicator (most complex, benefits from logging already being in place)
