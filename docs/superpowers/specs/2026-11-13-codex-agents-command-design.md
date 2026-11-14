# Codex Agents Command Design

## Intent

Add a top-level `cc-connect agents` command that shows Codex's own saved agent sessions instead of cc-connect bridge sessions. The command must include sessions from every working directory under the active Codex home, while continuing to exclude Codex subagent rollouts.

## Goals

- Add `cc-connect agents` as an alias for `cc-connect agents list`.
- Read all top-level Codex sessions from the configured/effective `CODEX_HOME`.
- Display session ID, Codex thread title or fallback summary, working directory, message count, and last modification time.
- Support `--json` for scripts and `--all`/`--include-all` is unnecessary because all work directories are the default.
- Preserve the existing Agent.ListSessions behavior used by bridge `/list` (it remains scoped to the project work directory).
- Return clear errors and an empty result when the Codex session directory does not exist.

## Non-goals

- Do not change the Feishu `/list` or `cc-connect sessions list` output.
- Do not add session deletion, resume, or interactive selection to the new command.
- Do not include Codex subagent sessions.
- Do not change other agents' session discovery behavior.

## Approaches

1. **Add a Codex-wide optional capability (chosen).** Define a small optional interface in `core`, implement it in `agent/codex` by reusing the existing parser with no cwd filter, and have the CLI call it. This preserves the current project-scoped interface and avoids importing the Codex implementation into core.
2. **Make `Agent.ListSessions` unfiltered.** This would make the CLI easy, but would change the existing bridge `/list` semantics and could mix sessions from unrelated workspaces.
3. **Shell out to `codex resume`.** This depends on an interactive TTY, hides non-interactive details, and cannot provide stable JSON output, so it does not fit a scriptable cc-connect command.

## Interface and behavior

- `cc-connect agents` and `cc-connect agents list` use the active config path only to resolve `CODEX_HOME`/agent options; they do not require a platform to be configured.
- With one or more Codex projects, the command scans each distinct effective Codex home once, deduplicates sessions by ID, and prints a `Project` column when the same session source is associated with configured projects.
- A `--project NAME` option restricts the scan to one configured Codex project. Without it, all configured Codex projects are considered; the effective Codex home is still scanned across all work directories.
- The default output is a stable tabular list sorted by last modification descending. `--json` emits an array of the same records with machine-readable field names.
- If no Codex project is configured, the command fails with a clear message rather than silently showing unrelated sessions.

## Constraints

- Keep `core` independent of agent packages; use an optional capability interface.
- Keep user-facing CLI strings in the command layer and update the top-level usage text.
- Add unit tests for command parsing/formatting and Codex all-workdir discovery before implementation.
- Run `gofmt`, targeted tests, and `go test ./...` before shipping.
