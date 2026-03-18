# Session Agent/Path Switching Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add session-scoped `/agent` and `/path` commands so one bot can switch between Codex and Claude Code and bind different working directories per conversation.

**Architecture:** Keep project config as the default source of truth, then layer session-scoped `AgentOverride` and `WorkDirOverride` on top. The engine resolves effective agent/path at runtime and derives a session-specific agent only when overrides are present.

**Tech Stack:** Go, `core.Engine`, JSON session persistence, agent registry/plugin architecture, `go test`

**Design doc:** `docs/plans/2026-03-17-session-agent-path-switching-design.md`

---

### Task 1: Add persistent session override fields

**Files:**
- Modify: `core/session.go`
- Test: `core/session_test.go`

**Step 1: Write the failing test**

Add a persistence round-trip test in `core/session_test.go` that:

- creates a `SessionManager` with a temp store file
- creates a session
- sets:
  - `AgentOverride = "codex"`
  - `WorkDirOverride = "/tmp/project-a"`
- saves and reloads the manager
- asserts both override fields survive reload

**Step 2: Run test to verify it fails**

Run:

```bash
go test ./core -run TestSessionOverridePersistence -v
```

Expected: FAIL because the fields and accessors do not exist yet.

**Step 3: Write minimal implementation**

In `core/session.go`:

- add `AgentOverride string` and `WorkDirOverride string` to `Session`
- add atomic getter/setter helpers for both fields
- keep JSON tags explicit and backward-compatible

Suggested helpers:

```go
func (s *Session) SetAgentOverride(v string)
func (s *Session) GetAgentOverride() string
func (s *Session) SetWorkDirOverride(v string)
func (s *Session) GetWorkDirOverride() string
```

**Step 4: Run test to verify it passes**

Run:

```bash
go test ./core -run TestSessionOverridePersistence -v
```

Expected: PASS

**Step 5: Commit**

```bash
git add core/session.go core/session_test.go
git commit -m "feat: persist session agent and path overrides"
```

---

### Task 2: Add effective agent/path resolution helpers

**Files:**
- Modify: `core/engine.go`
- Test: `core/engine_test.go`

**Step 1: Write the failing test**

Add tests in `core/engine_test.go` for helper behavior:

- default session resolves to project default agent/path
- session with `AgentOverride` resolves to override agent
- session with `WorkDirOverride` resolves to override path
- mixed case `claude` input normalizes to `claudecode`

**Step 2: Run test to verify it fails**

Run:

```bash
go test ./core -run 'TestResolveEffective(Agent|WorkDir)' -v
```

Expected: FAIL because helpers do not exist.

**Step 3: Write minimal implementation**

In `core/engine.go` add helpers that use session + defaults:

```go
func normalizeAgentName(name string) string
func (e *Engine) defaultWorkDir() string
func (e *Engine) effectiveAgentType(s *Session) string
func (e *Engine) effectiveWorkDir(s *Session) string
```

Rules:

- `claude` => `claudecode`
- empty override => fallback to project defaults
- path helper returns default work dir when override is empty

The engine will need stored default agent metadata:

- default agent type
- default agent options
- default providers

Add fields for these on `Engine` and wire them from startup construction.

**Step 4: Run test to verify it passes**

Run:

```bash
go test ./core -run 'TestResolveEffective(Agent|WorkDir)' -v
```

Expected: PASS

**Step 5: Commit**

```bash
git add core/engine.go core/engine_test.go
git commit -m "feat: resolve effective session agent and work dir"
```

---

### Task 3: Add derived-agent construction for session overrides

**Files:**
- Modify: `core/engine.go`
- Test: `core/engine_test.go`

**Step 1: Write the failing test**

Add a regression test that:

- builds an engine with default agent `codex`
- sets session override to `claudecode`
- calls the interactive-state bootstrap path
- asserts the engine does not reuse the default agent when override exists
- asserts the derived agent gets the overridden `work_dir`

Use a stub factory or injectable helper to observe the type/options passed to
`CreateAgent`.

**Step 2: Run test to verify it fails**

Run:

```bash
go test ./core -run TestGetOrCreateInteractiveStateUsesDerivedAgentForSessionOverride -v
```

Expected: FAIL because override-aware construction is not implemented.

**Step 3: Write minimal implementation**

In `core/engine.go`:

- add a helper to clone default agent options
- override `work_dir` when `Session.WorkDirOverride` is set
- call `CreateAgent(resolvedType, resolvedOptions)` when overrides exist
- if the derived agent implements `ProviderSwitcher`, copy provider wiring and
  the active provider from defaults

Suggested helper:

```go
func (e *Engine) buildSessionAgent(s *Session) (Agent, error)
```

Behavior:

- if no overrides: return `e.agent`
- if overrides exist: create a derived agent

**Step 4: Run test to verify it passes**

Run:

```bash
go test ./core -run TestGetOrCreateInteractiveStateUsesDerivedAgentForSessionOverride -v
```

Expected: PASS

**Step 5: Run nearby regression tests**

Run:

```bash
go test ./core -run 'TestSessionMismatch|TestGetOrCreateInteractiveState' -v
```

Expected: PASS

**Step 6: Commit**

```bash
git add core/engine.go core/engine_test.go
git commit -m "feat: derive session-specific agents from overrides"
```

---

### Task 4: Add `/agent` command

**Files:**
- Modify: `core/engine.go`
- Test: `core/engine_test.go`

**Step 1: Write the failing test**

Add tests covering:

- `/agent` shows effective/default state
- `/agent codex` sets `AgentOverride`
- `/agent claude` stores normalized `claudecode`
- `/agent reset` clears only `AgentOverride`
- `/agent codex /data/project-a` sets both agent and path
- successful command clears existing interactive state

**Step 2: Run test to verify it fails**

Run:

```bash
go test ./core -run 'TestCmdAgent' -v
```

Expected: FAIL because the command does not exist.

**Step 3: Write minimal implementation**

In `core/engine.go`:

- add `"agent"` to builtin command handling
- implement `cmdAgent`
- validate allowed agent names:
  - `codex`
  - `claudecode`
  - `claude` alias
- support optional second arg as absolute path
- call `cleanupInteractiveState(msg.SessionKey)` on success
- save session state

User-facing behavior:

- no args: report effective/default agent and path
- invalid agent: clear usage + allowed values

**Step 4: Run test to verify it passes**

Run:

```bash
go test ./core -run 'TestCmdAgent' -v
```

Expected: PASS

**Step 5: Commit**

```bash
git add core/engine.go core/engine_test.go
git commit -m "feat: add session-scoped agent switching command"
```

---

### Task 5: Add `/path` command

**Files:**
- Modify: `core/engine.go`
- Test: `core/engine_test.go`

**Step 1: Write the failing test**

Add tests covering:

- `/path` shows effective/default path
- `/path /abs/dir` stores `WorkDirOverride`
- `/path reset` clears only path override
- relative path is rejected
- nonexistent path is rejected
- file path is rejected
- success clears interactive state

**Step 2: Run test to verify it fails**

Run:

```bash
go test ./core -run 'TestCmdPath' -v
```

Expected: FAIL because the command does not exist.

**Step 3: Write minimal implementation**

In `core/engine.go`:

- add `"path"` to builtin command handling
- implement `cmdPath`
- validate:
  - `filepath.IsAbs(path)`
  - `os.Stat(path)` success
  - `fi.IsDir()`
- save override and clean up interactive state on success

**Step 4: Run test to verify it passes**

Run:

```bash
go test ./core -run 'TestCmdPath' -v
```

Expected: PASS

**Step 5: Commit**

```bash
git add core/engine.go core/engine_test.go
git commit -m "feat: add session-scoped work dir switching command"
```

---

### Task 6: Enhance `/current` output

**Files:**
- Modify: `core/engine.go`
- Test: `core/engine_test.go`

**Step 1: Write the failing test**

Extend the `/current` test to assert the output includes:

- effective agent
- effective path
- default values when different

**Step 2: Run test to verify it fails**

Run:

```bash
go test ./core -run TestCmdCurrent -v
```

Expected: FAIL because current output does not include the new fields.

**Step 3: Write minimal implementation**

Update `cmdCurrent` in `core/engine.go` to display:

- current session display name / ID
- effective agent
- effective path
- project default agent/path when overrides are active

Keep legacy text fallback compatible with current tests where possible.

**Step 4: Run test to verify it passes**

Run:

```bash
go test ./core -run TestCmdCurrent -v
```

Expected: PASS

**Step 5: Commit**

```bash
git add core/engine.go core/engine_test.go
git commit -m "feat: show effective agent and path in current session status"
```

---

### Task 7: Update docs

**Files:**
- Modify: `README.md`
- Modify: `README.zh-CN.md`
- Modify: `INSTALL.md`

**Step 1: Write the doc updates**

Add usage examples for:

- `/agent codex`
- `/agent claude`
- `/agent codex /data/project-a`
- `/path /data/project-a`
- `/path reset`

Explain that:

- changes are session-scoped
- `/switch` restores each session's own agent/path
- default config remains unchanged

**Step 2: Run doc grep verification**

Run:

```bash
rg -n '/agent|/path' README.md README.zh-CN.md INSTALL.md
```

Expected: matches in all updated files

**Step 3: Commit**

```bash
git add README.md README.zh-CN.md INSTALL.md
git commit -m "docs: describe session-scoped agent and path switching"
```

---

### Task 8: Run full verification

**Files:**
- No code changes expected

**Step 1: Run focused tests**

Run:

```bash
go test ./core -run 'Test(SessionOverridePersistence|ResolveEffective|GetOrCreateInteractiveStateUsesDerivedAgentForSessionOverride|CmdAgent|CmdPath|CmdCurrent)' -v
```

Expected: PASS

**Step 2: Run full core tests**

Run:

```bash
go test ./core/... -v
```

Expected: PASS

**Step 3: Run full repo tests**

Run:

```bash
go test ./... 
```

Expected: PASS

**Step 4: Build binary**

Run:

```bash
go build ./cmd/cc-connect
```

Expected: PASS

**Step 5: Commit**

```bash
git add -A
git commit -m "feat: support per-session agent and path switching"
```
