# Cursor connector capabilities

This note covers the Cursor connector’s models, reasoning effort, streaming,
sessions, and memory, compared with Claude Code and Devin.

## History

Cursor Agent CLI stores one chat at:

`$XDG_CONFIG_HOME/Cursor/chats/<workspace-md5>/<session-id>/store.db`

or, when `XDG_CONFIG_HOME` is unset, `~/.config/Cursor/chats/...`, with a
legacy fallback of `~/.cursor/chats/...`.

`<workspace-md5>` is MD5 of the workspace path. Current CLI builds canonicalize
symlinks first (`/tmp/...` becomes `/private/tmp/...` on macOS). cc-connect
looks up the canonical hash first and the non-resolved absolute path second.

`store.db` has `meta` and `blobs`. `meta` key `0` is hex-encoded JSON. The
field `latestRootBlobId` points at a protobuf blob. Top-level field 1 entries
with a 32-byte payload are ordered SHA-256 ids of child message blobs. Child
blobs are JSON (`role` + `content`). `content` is either a string or an array
of typed parts.

`GetSessionHistory` walks every child ref. It does not stop at 80 messages and
does not read only the first 8KB of the root blob. A positive `limit` keeps
the last N user/assistant entries. `sqlite3` opens `file:…?mode=ro` first and
does not create a missing database. Some SQLite builds, including the tested macOS system SQLite, fail to open a
WAL database that way when `-wal`/`-shm` sidecars are absent
([SQLite WAL](https://sqlite.org/wal.html#read_only_databases));
only that open failure retries `mode=rw` with `PRAGMA query_only=ON`, which
creates the sidecars but rejects writes. `immutable=1` is not used, because it
would ignore a live WAL. Both attempts share one timeout. Blob reads are
batched, and the reader stops after the result cap instead of buffering all
stdout.
Ids must be 64 hex characters. Length fields in the root blob are checked with
`ln > remaining` so an overflowing varint cannot panic. Failures return
`cursor:` errors.

Skipped before they reach the user:

- `system` messages
- user turns whose text is only an injected wrapper (`<user_info>`,
  `<open_and_recently_viewed_files>`, `<attached_files>`,
  `<agent_transcripts>`, `<system_reminder>`, `<rules>`, or a bare
  `<timestamp>`) and that contain no `<user_query>`
- assistant `reasoning` parts

`<user_query>` bodies keep internal newlines. A user message that merely
starts with `<` and is not one of those wrappers is kept. The session-list
helper `extractCursorUserSummary` still drops every XML-like string; history
does not use that rule.

## Session paths

`safeSessionDir` rejects empty ids, whitespace, `.` / `..`, absolute paths,
slash or backslash, and any id whose `filepath.Clean` changes it. The
workspace hash directory itself must be a real directory. A symlink at that
hash is ignored, so it cannot alias another workspace. Ancestor symlinks such
as macOS `/tmp` -> `/private/tmp` stay valid because the hash entry is still a
real directory after those ancestors are traversed. The session directory must
be a real directory under that hash, not a symlink, and `store.db` must be a
regular file inside it.

`ValidateSessionID` is that check. `GetSessionTitle` uses `meta.name`, and
replaces an empty name or `New Agent` with the first real user query. Titles
use the same 60-rune summary as the session list.

## Memory

`ProjectMemoryFile` is `<work_dir>/AGENTS.md`. CLI 2026.10.14
(`LocalCursorRulesService.loadRulesFromDirAndAncestors`) also reads `AGENTS.md`
from ancestor directories, `.cursor/rules/**/*.mdc`, and `.cursorrules`. The
engine `/memory` command edits the single project path.

`GlobalMemoryFile` is empty. The same CLI build does not read a user-level
`AGENTS.md`. Home-directory `.cursor/skills` is skills, not the memory file.
User rules in transcripts are account context injected by the CLI.

## Compact

The interactive Cursor CLI documents `/summarize` (alias `/compress`) as a
slash command that reduces context:
<https://cursor.com/docs/cli/reference/slash-commands>.

The headless adapter does not surface that command. A probe with CLI 2026.10.14 using `--print` and `/summarize`
recorded it as an ordinary `user` message. The model answered with a conversational summary. `usage` stayed
ordinary `inputTokens` plus `cacheReadTokens`. The stream had no compaction
event. `(*Agent).CompressCommand` therefore stays `""`. That is a headless
adapter limit, not a claim that the interactive CLI lacks `/summarize`.

## Capability matrix

| | Claude Code | Devin | Cursor headless adapter |
|---|---|---|---|
| Model list | provider API, aliases | `devin models list` / ACP options | `agent models`, plus cmd prefix, workdir, merged env; cache is generation-scoped |
| `/model` | name or alias | fuzzy ACP option | exact id, case-insensitive id, display name, or alias; ambiguous prefixes rejected. `SetModel` beats the active provider until the next provider switch |
| `/effort` | low/medium/high/max | ACP `thought_level` | advertised suffix on the same family (`-thinking` before or after the effort, `-fast` kept). `auto` and families with no suffix variants list nothing. Display matches the id that will launch. Bracket text is not sent; Grok rejects `model[effort=high]` |
| Modes | default, accept-edits, plan, auto, bypass | ACP modes, aliases to accept-edits | `default`, `force` (yolo), `plan`, `ask` |
| Tool permission | CLI permission prompts | ACP permission | `interaction_query` on stdin; `force` passes `--force` |
| Streaming | stream-json deltas | ACP stream | `--stream-partial-output`; timestamped assistant events without `model_call_id` are deltas; complete snapshots, including timestamped ones with `model_call_id`, close each message without repeating its text |
| Attachments | native image/file | native images | images and files saved under the workspace and referenced in the prompt |
| List / resume | JSONL sessions | ACP sessions | `store.db` chats; `--resume <id>` |
| History / title | JSONL | ACP load | full root blob, last N visible turns; title from `meta.name` or first user query |
| Validate / delete | session file | ACP | real directory and regular `store.db` only; symlink session dirs and `..` ids are refused |
| Project / global memory | `CLAUDE.md`, `~/.claude/CLAUDE.md` | `AGENTS.md`, XDG `AGENTS.md` | project `AGENTS.md` only. CLI also reads ancestor `AGENTS.md`, `.cursor/rules`, and `.cursorrules`; `/memory` edits the project file |
| Skills | `.claude/skills` | Devin skill dirs | `<work>/.cursor/skills`, `<work>/.claude/skills`, and the home copies |
| Usage | result usage | ACP usage | result `usage` camelCase token counts |
| Compact | `/compact` | `/compact` | interactive `/summarize` (`/compress`) exists; headless `--print` does not emit a compaction event, so `CompressCommand` is empty |

`ListSessions` and `DeleteSession` use the same canonical chat directories
and `findCursorSessionDir` / `safeSessionDir` checks as history. Message
counts are the number of visible history turns from the full root blob.
`DeleteSession` refuses traversal and session-directory symlinks before
`RemoveAll`. A missing session returns an error and does not create files.

Install the CLI from <https://cursor.com/docs/cli/installation>. The old
`npm i -g @anthropic-ai/cursor-agent` package name is not the install path.

## Verification

Validated on macOS with Cursor CLI `2026.10.14-941492e`, using Grok 4.7 Low
and High, on 2026-10-20.

- Passed `go test ./...`, `go test ./core/ -run TestCUJ`, `go build ./...`,
  `go vet ./...`, `go test -race -count=1 -timeout 180s ./agent/cursor`, and
  `GOOS=windows GOARCH=amd64 go build ./agent/cursor`.
- Real Feishu user messages and reply read-back verified model listing,
  valid/invalid model and effort selection, actual Low/High reply footers,
  new/switch/history, project memory, mode changes, and `pwd` tool output.
- Switching back restored all four original messages. Repeated session lists
  had identical order and counts. Activity time comes from `store.db` and a
  newer nonempty regular WAL, so read-created sidecars cannot promote a chat.
- A real foreground `sleep 60` was observed before `/cancel`. The CLI, tool
  shell in a separate process group, and sleep all stopped; a subsequent
  ordinary question completed successfully.
- Old-code replays failed for duplicated tool-loop text, missing WAL sidecars,
  and detached process groups; the corresponding regressions pass after fixes.

The macOS test run used `TMPDIR=/private/tmp` to avoid an existing Codex test's
literal comparison of symlinked temporary paths. The source checkout lacked
built web assets, so a local `web/dist/.keep` embed placeholder was used; this
validation covers the connector and does not validate or package the web UI.
Generate the web assets separately for a release that includes that UI.
