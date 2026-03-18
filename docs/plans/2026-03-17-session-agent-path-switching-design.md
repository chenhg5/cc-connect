# Session Agent/Path Switching Design

**Date:** 2026-03-17
**Status:** Approved
**Branch:** TBD

## Problem

`cc-connect` currently binds each project to a single default agent type and a
single default `work_dir` from `config.toml`. This is too rigid for users who
want to operate one bot across multiple local CLIs and repositories.

The desired workflow is:

1. Use one bot account
2. Switch the current conversation between agents such as Codex and Claude Code
3. Change the current conversation's working directory without restarting
   `cc-connect`
4. Keep different sessions isolated so `/switch` restores the agent and path
   that belong to that session

## Goals

1. Add a session-scoped `/agent` command to switch the current session's agent
2. Add a session-scoped `/path` command to switch the current session's working
   directory
3. Allow a combined form: `/agent <agent> <abs_path>`
4. Preserve compatibility with existing single-agent projects
5. Avoid mutating `config.toml` at runtime

## Non-Goals

- Replacing the project model with dynamic per-message routing
- Supporting arbitrary agent aliases in the first iteration
- Adding agent/path pools or long-lived per-session agent caches
- Changing platform-level routing or webhook behavior

## Design

### 1. Session-Scoped Overrides

Add two persistent fields to `core.Session`:

- `AgentOverride string`
- `WorkDirOverride string`

These fields represent explicit user choices for the current bot session.

They do **not** replace:

- `AgentSessionID` — the backend CLI session ID
- `AgentType` — the actual agent type used by the current live session

Resolution order:

1. `Session.AgentOverride` if set
2. Project default `Agent.Type`

And:

1. `Session.WorkDirOverride` if set
2. Project default `Agent.Options["work_dir"]`

### 2. Runtime Agent Derivation in Engine

Keep startup behavior unchanged: `main.go` still constructs one default agent
from the project config.

At runtime, `Engine` gains access to the project's default agent type/options so
it can derive a session-specific agent when overrides exist.

When `getOrCreateInteractiveStateWith()` is about to call `StartSession(...)`:

1. Read the active `Session`
2. Resolve the effective agent type and work dir
3. If no overrides are present, reuse `e.agent`
4. If overrides are present:
   - copy the default agent options
   - overwrite `work_dir` when needed
   - call `CreateAgent(resolvedAgentType, resolvedOptions)`
   - wire provider settings into the derived agent exactly as startup does
5. Start or resume the backend session with the resolved agent instance

This keeps the design aligned with the existing registry/plugin architecture and
avoids modifying global config.

### 3. Command Surface

#### `/agent`

Supported forms:

- `/agent`
- `/agent codex`
- `/agent claude`
- `/agent claudecode`
- `/agent reset`
- `/agent codex /data/project-a`

Behavior:

- no args: show effective/default agent and path
- `claude`: normalize to `claudecode`
- `reset`: clear only `AgentOverride`
- `<agent> <abs_path>`: update both overrides in one command

First iteration whitelist:

- `codex`
- `claudecode`

If a requested agent is unavailable in the current binary, return a clear error.

#### `/path`

Supported forms:

- `/path`
- `/path /data/project-a`
- `/path reset`

Validation:

- must be an absolute path
- path must exist
- path must be a directory

Behavior:

- no args: show effective/default path
- `reset`: clear only `WorkDirOverride`

### 4. Session Lifecycle Behavior

Changing agent or path must invalidate the current interactive state.

After `/agent` or `/path` successfully updates the session:

1. save the session state
2. call `cleanupInteractiveState(sessionKey)`
3. confirm the new effective agent/path to the user

The next normal message starts a fresh backend process or resume attempt using
the updated session configuration.

This matches existing `/model` and `/mode` behavior and keeps state transitions
predictable.

### 5. `/switch` and `/current`

`/switch` should continue to select a different logical session. Because the
overrides are stored on `Session`, switching sessions automatically restores the
right agent and path.

`/current` should be enhanced to display:

- session ID / session name
- effective agent
- effective path
- project defaults when they differ

### 6. Compatibility and Migration

Existing session store files remain compatible:

- missing `AgentOverride` and `WorkDirOverride` decode as empty strings
- empty overrides mean "use project defaults"

Existing `config.toml` files remain unchanged.

Users who do not use `/agent` or `/path` should observe no behavior change.

## Files

Primary implementation targets:

- `core/session.go`
- `core/engine.go`
- `core/session_test.go`
- `core/engine_test.go`
- `docs/usage.md`
- `docs/usage.zh-CN.md`
- `INSTALL.md`

## Risks

### 1. Derived Agent Drift

If the runtime-derived agent is not wired with the same provider/model settings
as the startup agent, behavior may differ unexpectedly.

Mitigation:

- centralize derived-agent creation in one helper
- copy provider wiring from startup flow
- test Codex/Claude switching explicitly

### 2. Session Mismatch on Agent Change

If the old interactive state is not cleaned up, the session may continue talking
to the previous backend process.

Mitigation:

- always call `cleanupInteractiveState(sessionKey)` after successful updates
- add regression tests around `/agent` and `/path`

### 3. Path Validation Edge Cases

Relative paths, missing directories, or files passed as paths must fail early.

Mitigation:

- strict validation in command handlers
- explicit tests for invalid path input

## Recommended Implementation Order

1. Extend `Session` persistence with override fields
2. Add engine helpers for effective agent/path resolution
3. Add runtime-derived agent creation
4. Add `/agent` and `/path` commands
5. Enhance `/current`
6. Add docs and regression tests
