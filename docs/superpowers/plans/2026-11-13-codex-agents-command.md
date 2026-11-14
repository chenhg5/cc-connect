# Codex Agents Command Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `cc-connect agents` / `cc-connect agents list` to show every top-level Codex session across all working directories under the effective Codex home.

**Architecture:** Preserve `core.Agent.ListSessions` as the project-scoped API used by bridge `/list`. Add an optional `core.AllAgentSessionsLister` capability implemented by the Codex agent with an unfiltered JSONL scan. Add a config-aware CLI command that creates configured Codex agents, invokes the capability, deduplicates sessions, and renders stable table or JSON output.

**Tech Stack:** Go 1.25, standard `flag`, `encoding/json`, `text/tabwriter`, existing cc-connect config/agent registry, Go tests.

**Spec:** `docs/superpowers/specs/2026-11-13-codex-agents-command-design.md`

## Global Constraints

- `cc-connect agents` and `cc-connect agents list` are equivalent.
- The default scan includes all Codex working directories and excludes Codex subagent rollouts.
- Existing project-scoped `Agent.ListSessions` and bridge `/list` behavior must not change.
- The command supports `--project NAME` and `--json`.
- Core must remain independent of concrete agent packages.
- User-facing CLI text must be available in the top-level usage output.

## Review Focus

- A Codex home with sessions from two different `cwd` values must return both sessions; test in the Codex list package.
- A subagent rollout must remain excluded from the all-workdir result; reuse the existing parser test fixture.
- Duplicate IDs found in multiple configured Codex projects/homes must appear once; test CLI aggregation.
- Missing Codex session directories must produce a successful empty result, not a panic or noisy failure.
- `--json` output must be valid JSON and contain stable field names; test the renderer directly.

### Task 1: Add the all-session capability and Codex implementation

**Files:**
- Modify: `core/interfaces.go`
- Modify: `agent/codex/list.go`
- Modify: `agent/codex/codex.go`
- Test: `agent/codex/list_test.go`

**Interfaces:**
- Produces `core.AllAgentSessionsLister` with `ListAllSessions(context.Context) ([]AgentSessionInfo, error)`.
- Keeps `Agent.ListSessions` cwd-filtered and makes `Agent.ListAllSessions` scan without a cwd filter.

- [ ] **Step 1: Write the failing tests**

  Add a test fixture containing two top-level JSONL sessions with different `cwd` values and one subagent rollout; assert `ListAllSessions` returns the two top-level sessions, sorted newest first, while `ListSessions` still returns only the configured work directory.

- [ ] **Step 2: Run the focused test and verify it fails**

  Run `go test ./agent/codex -run 'TestAgentListAllSessions' -v` from `/tmp/cc-connect-src`.
  Expected: compile failure because the all-session method/capability does not exist.

- [ ] **Step 3: Implement the optional capability and unfiltered scan**

  Define the optional interface in `core/interfaces.go`. Add `ListAllSessions` to the Codex Agent and pass an empty filter into the existing parser. Keep malformed-file handling, title lookup, deduplication, sorting, and subagent exclusion unchanged.

- [ ] **Step 4: Run the focused tests and verify they pass**

  Run `go test ./agent/codex -run 'TestAgentListAllSessions|TestAgentListSessions' -v`.

### Task 2: Add the `agents` CLI command and renderers

**Files:**
- Create: `cmd/cc-connect/agents.go`
- Test: `cmd/cc-connect/agents_test.go`
- Modify: `cmd/cc-connect/main.go`
- Modify: `cmd/cc-connect/main_test.go`

**Interfaces:**
- Consumes `core.AllAgentSessionsLister`, `config.Load`, `core.CreateAgent`, and `buildAgentOptions`.
- Produces `cc-connect agents [list] [--project NAME] [--json]`.

- [ ] **Step 1: Write failing CLI tests**

  Test argument parsing for the optional `list` token, `--project`, and `--json`; test table output fields and valid JSON output; test aggregation deduplicates identical session IDs from multiple configured Codex projects; test a configured non-Codex project is reported as unsupported or skipped without changing the Codex result.

- [ ] **Step 2: Run focused tests and verify they fail**

  Run `go test ./cmd/cc-connect -run 'TestAgents' -v`.
  Expected: compile failure because the command and renderer functions do not exist.

- [ ] **Step 3: Implement command dispatch and config-aware listing**

  Register `agents` in `topLevelCommandHandlers`, pass the parsed global config path into top-level handlers, load the selected config, select all configured Codex projects or the requested project, create each agent, assert `core.AllAgentSessionsLister`, aggregate and deduplicate by session ID, sort by `ModifiedAt` descending, and print table/JSON output. Return clear non-zero errors for missing config, missing project, no Codex project, or an agent without the capability.

- [ ] **Step 4: Update top-level usage and command-dispatch tests**

  Document the command in `printUsage` and assert `agents` is registered and `--config` reaches the handler path without starting the long-running bridge.

- [ ] **Step 5: Run focused tests and verify they pass**

  Run `go test ./cmd/cc-connect -run 'TestAgents|TestTopLevelCommand' -v`.

### Task 3: Integrate, verify, and package the binary

**Files:**
- Modify: `README.md` or the appropriate CLI usage documentation if the existing project documents top-level commands there.
- Modify: `CHANGELOG.md` only if the repository convention requires unreleased feature entries.

- [ ] **Step 1: Run formatting and targeted regression tests**

  Run `gofmt -w core/interfaces.go agent/codex/list.go agent/codex/codex.go agent/codex/list_test.go cmd/cc-connect/agents.go cmd/cc-connect/agents_test.go cmd/cc-connect/main.go cmd/cc-connect/main_test.go`, then `go test ./agent/codex ./cmd/cc-connect`.

- [ ] **Step 2: Run the full suite**

  Run `go test ./...` and record any pre-existing failures separately.

- [ ] **Step 3: Build and smoke-test the command**

  Run `go build -o /tmp/cc-connect-agents ./cmd/cc-connect`, then execute `/tmp/cc-connect-agents --config <fixture-config> agents list --json` against a temporary Codex home fixture and validate the JSON contains sessions from multiple `cwd` values.

- [ ] **Step 4: Commit the feature branch**

  Run `git add` only for this feature's files and commit with `feat: add codex agents listing command`.

- [ ] **Step 5: Report the branch and install path**

  Report branch `zj/feat-codex-agents`, test results, and the built binary path. Do not create or merge an MR unless explicitly requested.
