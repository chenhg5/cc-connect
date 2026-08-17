# cc-connect-next

Privacy-first successor to [CC Connect](https://github.com/chenhg5/cc-connect), with a native Feishu Card 2.0 response lifecycle.

[中文说明](README.zh-CN.md) · [Install guide](INSTALL.md) · [Feishu guide](docs/feishu.md) · [Answer-card contract](docs/feishu-card-contract.md) · [Migration matrix](docs/migration-compatibility.md) · [Upstream Feishu parity](docs/upstream-feishu-parity-2026-08-15.md) · [Upstream beta.3 audit](docs/upstream-v1.5.0-beta.3-audit.md)

> Current release: `0.1.0` — the first stable release. The repository and runtime identity are independent from official CC Connect; no upstream patch, MCP server, proxy, message snapshot, or companion plugin is required.

## What changes for Feishu

One agent turn stays in one quoted native card:

1. Reply immediately with a non-empty `⏳ 正在思考…` card.
2. Show anonymous progress only: `推理 N 次 · 工具 N 次`.
3. Switch the same card to `⏳ 正在调用工具…` as tool calls occur.
4. Replace progress with `✍️ 正在回答` when answer text begins.
5. Stream the `main_text` element through CardKit when `card_id` is available; even one-shot Agent answers retain a perceptible answering/typewriter phase before Done, with safe full-card fallback.
6. Finalize the same card as `✅ Done`, or `⚠️ 未完成` on error.

Privacy is enforced at two layers: the engine stores only anonymous event kinds for rich-card progress, and the Feishu renderer ignores all reasoning/tool names, inputs, results, model, token, context, footer, and work-directory fields. The card payload has no expandable panel.

The successor also carries the Feishu functionality merged by official CC Connect after the original fork point: topic-scoped workspace bindings, first-mention topic context bootstrap, privacy-gated quoted-file retrieval, topic-local relay visibility, and explicit bot-to-bot `mention_map`. These were adapted around Next's protected Card 2.0 and migration contracts instead of merging upstream wholesale. A resolved native @ is the deliberate exception to the one-card terminal shape: Feishu does not emit bot mention events from cards, so Next sends a tracked quoted text answer and removes the lifecycle card; answers without a native mention stay on CardKit.

## Install

### npm

```bash
npm install -g cc-connect-next
cc-connect-next --version
```

`@beta` still tracks the newest prerelease when one exists:

```bash
npm install -g cc-connect-next@beta
```

The npm package and GitHub release use the same version. The installer fetches that release's `checksums.txt`, selects the exact platform archive, verifies SHA-256, and only then extracts and atomically replaces the binary.

### Update

```bash
cc-connect-next update
```

The command installs stable releases only and detects the installation method automatically. For npm installs it updates the same global prefix to the exact stable package version; for standalone binaries it downloads the matching GitHub Release archive, verifies it against `checksums.txt`, and then replaces the executable. Use `npm install -g cc-connect-next@beta` explicitly when you want the prerelease channel.

### Build the current source

```bash
git clone https://github.com/timmyagentic/cc-connect-next.git
cd cc-connect-next
make build
./cc-connect-next --version
```

Run `cc-connect-next` once to create the secure starter config at `~/.cc-connect-next/config.toml`. It is rendered from the same recommended Feishu profile `cc-connect-next feishu setup` applies, and every value left for you is marked `REPLACE`. Startup refuses to run while a `REPLACE` value is still in place, naming the key and the step that resolves it, instead of reporting itself healthy and then failing to connect.

`cc-connect-next doctor` checks the configuration, the Agent CLI and its login state, the configured platforms, dependencies, and network without opening a platform connection, so it also works while the instance is down.

## Migrate from official CC Connect

The migration is explicit and does not stop, uninstall, or modify official CC Connect.

```bash
cc-connect-next migrate --dry-run
cc-connect-next migrate
cc-connect-next --config ~/.cc-connect-next/config.toml
```

The one-command migration covers three sources before writing anything: exactly the official `config.toml`, the effective `data_dir` (including a custom path), and every project-local `.cc-connect` directory discoverable from configured work directories, multi-workspace roots, project state, or workspace bindings. When the config file lives outside the effective data directory, no sibling file or directory beside it is inventoried. The command therefore preserves configuration, sessions, project overrides, cron/timer and heartbeat state, bindings, local provider configuration, and staged images/attachments without accidentally copying a repository, `.env`, backup tree, or service home. External Agent stores such as Codex or Claude sessions stay in place and their existing IDs remain valid.

Every source file is hashed during preflight and the complete result is built and verified in sibling staging directories. Immediately before activation, migration rebuilds the full source inventory; any added or deleted file, changed content, changed project discovery, or changed access metadata fails closed without activating an incomplete target. Existing destinations are also snapshotted before staging, revalidated after copying, checked again immediately before each promotion, and compared once more at the backup path after the atomic rename. If another cc-connect-next process creates or changes target state during the migration—even through an already-open writer at the rename boundary, and especially during a `--force` merge—the command restores and leaves that newer target untouched instead of activating a stale staged copy. Stable destinations are then activated with atomic renames. If a later destination fails, every earlier promoted tree is preserved in a unique `.failed-migration-*/preserved` recovery directory before its pre-migration backup is restored; rollback never deletes a tree that may contain post-promotion writes, and the error prints every recovery path. Every destination is canonicalized and refused if it overlaps any official source tree; the comparison uses filesystem identity, so symlinked ancestors and case-only aliases on case-insensitive volumes are rejected too. The effective global `data_dir` and project-local `.cc-connect-next` trees preserve source directory/file modes and ownership for `run_as_user` traversal; any missing target ancestors created by migration inherit the corresponding source root's traversal access and ownership, while pre-existing ancestors are never modified. The rewritten `config.toml` remains a generated `0600` file. Runtime-only logs, sockets, locks, restart notifications, and daemon metadata are excluded; source symlinks are skipped. Existing non-empty targets are refused by default. With an explicit `--force`, merging is deliberate; every previous target that existed, including an empty one, remains available as a timestamped `*.pre-migration-*` backup and is recorded in the report and manifest. The result includes `migration-manifest.json` with every source, destination, size, and SHA-256. Use `--skip-project-data` only when project-local images and attachments are intentionally not wanted.

The official instance may remain installed and running. If it keeps writing persistent data during the migration window, the command asks you to rerun during a quieter moment instead of silently omitting new files.

Custom locations are supported:

```bash
cc-connect-next migrate \
  --source /path/to/official-data \
  --target /path/to/next-data \
  --source-version v1.5.0-beta.3 \
  --dry-run
```

The current matrix covers the known persistent layout of official v1.4.1 and v1.5.0-beta.1 through beta.3. Default `--source-version auto` does not execute the binary recorded in daemon metadata; it validates the actual TOML schema, normal startup semantics, registered Agent/platform set, and persistent inventory. An exact known release can be recorded explicitly. A missing Agent/platform, invalid display mode, unsupported setting, or plugin whose behavior cannot be preserved fails before target writes; see the [migration compatibility matrix](docs/migration-compatibility.md).

Relative `data_dir`, `work_dir`, and `base_dir` values are resolved from the official daemon's recorded working directory when available. Migration reads that metadata from the official `$HOME/.cc-connect/daemon.json` even when `--source` points to a separate config directory; a same-named file beside an arbitrary config is not trusted. A malformed metadata file or a recorded working directory that is missing or inaccessible fails preflight instead of silently resolving relative paths against a different directory. If `data_dir` is omitted, migration likewise uses `$HOME/.cc-connect` even when `--source` is a custom config root such as `/etc/cc-connect`; only that root's `config.toml` is copied. If daemon metadata is stale or the official instance was only run manually, pass `--runtime-work-dir /absolute/original/cwd` explicitly; this override has highest priority.

For safety, migration refuses an effective `data_dir` that contains the official configuration root (for example, `data_dir = "~"`). A separate custom `data_dir` is also inventoried only through the persistent paths owned by the supported official releases: sessions, project state/model caches, cron/timer state, bindings, heartbeat/history state, MiniMax local config, Weixin state, Agent prompt files, and Matrix encryption state. Any unexpected regular file or directory makes preflight fail, even when the configuration root lives elsewhere, instead of recursively copying a service home, SSH keys, browser profiles, or unrelated datasets. Point the official installation at a dedicated data directory and verify its state before rerunning migration; the command will never silently create a partial target for this case.

Configuration paths use the same `${NAME}` placeholder syntax as official CC Connect. Every referenced variable must also be present in the migration process; an unset variable fails closed instead of being replaced with an empty string that could select the wrong directory. A configured `data_dir` that has not been created yet is treated as empty, so the valid configuration file still migrates. If optional project data cannot be read, or project state/binding metadata is malformed, the global migration continues and still copies that metadata verbatim; every skipped discovery source is printed and recorded in `migration-manifest.json`. Grant access or repair the metadata, then rerun before treating project-local migration as complete.

## Recommended Feishu configuration

New configs already use these defaults:

```toml
[display]
mode = "compact"
card_mode = "rich"
thinking_messages = false
tool_messages = false
show_context_indicator = false
reply_footer = false
hide_agent_footer = true

[[projects]]
name = "my-project"

[projects.agent]
type = "codex"

[projects.agent.options]
work_dir = "/absolute/path/to/project"

[projects.references]
normalize_agents = ["codex", "claudecode"]
render_platforms = ["feishu"]
display_path = "smart"
marker_style = "emoji"
enclosure_style = "code"

[[projects.platforms]]
type = "feishu"

[projects.platforms.options]
app_id = "${FEISHU_APP_ID}"
app_secret = "${FEISHU_APP_SECRET}"
reply_to_trigger = true
done_emoji = "Done"
# resolve_mentions = true
# mention_map = { Reviewer-Bot = "ou_reviewer_bot_open_id" }
```

Set `card_mode = "legacy"` to opt out and use the inherited CC Connect message rendering.

The exact lifecycle, privacy boundary, fallback behavior, locale coverage, and executable verification commands are defined in the [Feishu answer-card contract](docs/feishu-card-contract.md).

## Coexistence and switching

Official CC Connect and cc-connect-next can be installed side by side:

| Boundary | Official | cc-connect-next |
|---|---|---|
| Command | `cc-connect` | `cc-connect-next` |
| Data | `~/.cc-connect` | `~/.cc-connect-next` |
| macOS service | `com.cc-connect.service` | `com.cc-connect-next.service` |
| Linux service | `cc-connect.service` | `cc-connect-next.service` |
| API socket | `~/.cc-connect/run/api.sock` | `~/.cc-connect-next/run/api.sock` |

Do not run both against the same Feishu app credentials at the same time: two WebSocket consumers can race or duplicate handling. Use a separate test app for parallel runtime testing, or stop the official daemon only when you deliberately switch production traffic. Installation and migration themselves are safe to perform while the official daemon remains installed.

When switching the service, keep the migrated config and the original runtime working directory independent. Stop any separately configured test successor first. After stopping official CC Connect, rerun migration before starting production so sessions, bindings, timers, and project state written since the earlier test migration are not lost. The final `--force` run preserves the entire previous target in timestamped backups before refreshing it from the now-quiescent official source:

```bash
cc-connect daemon stop
cc-connect-next migrate --dry-run --force
cc-connect-next migrate --force
cc-connect-next daemon install \
  --config ~/.cc-connect-next/config.toml \
  --work-dir /absolute/original/cwd
```

Repeat any custom migration path options during this final synchronization. Inspect its manifest and backups before startup. The official config is authoritative during the refresh; deliberately reapply only required successor-specific settings from the backed-up tested config, never stale test-app credentials. If migration fails, do not start cc-connect-next; restart the official daemon and fix the reported problem. The migration command prints the detected `Official runtime work_dir`; use that exact value. `daemon status` reports both paths, and the installed launchd, systemd, or Windows task always passes the migrated config explicitly.

Rollback is simply:

```bash
cc-connect-next daemon stop
cc-connect daemon start
```

The official data directory remains untouched.

## Agent-readable install task

Paste this into a coding agent:

```text
Install cc-connect-next from https://github.com/timmyagentic/cc-connect-next.
First verify the OS/architecture and whether cc-connect is currently running.
Do not stop, uninstall, overwrite, or edit official CC Connect.
Install the published npm package, or build the current source. Then run
`cc-connect-next migrate --dry-run`, report the plan,
then run the real one-command migration only after confirming the target is ~/.cc-connect-next.
Check its migration-manifest.json and report any timestamped pre-migration backups.
Validate `cc-connect-next --version`, config permissions, independent daemon name,
and independent API socket. For the eventual service switch, stop official CC Connect,
rerun `cc-connect-next migrate --dry-run --force` and `cc-connect-next migrate --force`,
inspect the new manifest and backups, then install with both
`--config ~/.cc-connect-next/config.toml` and `--work-dir` set to the exact
`Official runtime work_dir` printed by migration. If final migration fails, restart the
official daemon and do not start Next. Never run both runtimes with the same Feishu app.
```

## Development

```bash
make web
go test ./...
make build-noweb
```

Focused card tests:

```bash
go test ./platform/feishu -run TestBuildRichCard -count=1
go test ./core -run TestProcessInteractiveEvents_RichCard -count=1
go test -tags no_web ./cmd/cc-connect -run TestMigrateLegacyData -count=1
```

## Attribution and license

cc-connect-next starts from CC Connect v1.4.1 and preserves its Git history. See [NOTICE](NOTICE) for attribution and [LICENSE](LICENSE) for MIT terms.
