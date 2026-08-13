# cc-connect-next

Privacy-first successor to [CC Connect](https://github.com/chenhg5/cc-connect), with a native Feishu Card 2.0 response lifecycle.

[中文说明](README.zh-CN.md) · [Install guide](INSTALL.md) · [Feishu guide](docs/feishu.md) · [Answer-card contract](docs/feishu-card-contract.md)

> Status: `0.1.0-beta.1` is under active development. The repository and runtime identity are independent from official CC Connect; no upstream patch, MCP server, proxy, message snapshot, or companion plugin is required.

## What changes for Feishu

One agent turn stays in one quoted native card:

1. Reply immediately with a non-empty `⏳ 正在思考…` card.
2. Show anonymous progress only: `推理 N 次 · 工具 N 次`.
3. Switch the same card to `⏳ 正在调用工具…` as tool calls occur.
4. Replace progress with `✍️ 正在回答` when answer text begins.
5. Stream the `main_text` element through CardKit when `card_id` is available; fall back to full-card updates safely.
6. Finalize the same card as `✅ Done`, or `⚠️ 未完成` on error.

Privacy is enforced at two layers: the engine stores only anonymous event kinds for rich-card progress, and the Feishu renderer ignores all reasoning/tool names, inputs, results, model, token, context, footer, and work-directory fields. The card payload has no expandable panel.

## Install

### npm release build

After the first beta is published:

```bash
npm install -g cc-connect-next@beta
cc-connect-next --version
```

The npm package and GitHub release use the same version and download the matching native binary.

### Build the current source

```bash
git clone https://github.com/timmyagentic/cc-connect-next.git
cd cc-connect-next
make build
./cc-connect-next --version
```

Run `cc-connect-next` once to create the secure starter config at `~/.cc-connect-next/config.toml`, then add the Feishu app credentials.

## Migrate from official CC Connect

The migration is explicit and does not stop, uninstall, or modify official CC Connect.

```bash
cc-connect-next migrate --dry-run
cc-connect-next migrate
cc-connect-next --config ~/.cc-connect-next/config.toml
```

The one-command migration inventories three layers before writing anything: the official configuration root, the effective `data_dir` (including a custom path), and every project-local `.cc-connect` directory discoverable from configured work directories, multi-workspace roots, project state, or workspace bindings. It therefore preserves configuration, sessions, project overrides, cron/timer and heartbeat state, bindings, local provider configuration, and staged images/attachments. External Agent stores such as Codex or Claude sessions stay in place and their existing IDs remain valid.

Every source file is hashed during preflight and the complete result is built and verified in sibling staging directories. Immediately before activation, migration rebuilds the full source inventory; any added or deleted file, changed content, changed project discovery, or changed project-local access metadata fails closed without activating an incomplete target. Existing destinations are also snapshotted before staging, revalidated after copying, checked again immediately before each promotion, and compared once more at the backup path after the atomic rename. If another cc-connect-next process creates or changes target state during the migration—even through an already-open writer at the rename boundary, and especially during a `--force` merge—the command restores and leaves that newer target untouched instead of activating a stale staged copy. Stable destinations are then activated with atomic renames. If a later destination fails, every earlier promoted tree is preserved in a unique `.failed-migration-*/preserved` recovery directory before its pre-migration backup is restored; rollback never deletes a tree that may contain post-promotion writes, and the error prints every recovery path. Every destination is canonicalized and refused if it overlaps any official source tree, including project-local targets discovered below the official configuration root. Project-local `.cc-connect-next` trees preserve the source `.cc-connect` directory/file modes and ownership for `run_as_user` compatibility. Runtime-only logs, sockets, locks, restart notifications, and daemon metadata are excluded; source symlinks are skipped. Existing targets are refused by default. With an explicit `--force`, the previous target is preserved as a timestamped `*.pre-migration-*` backup before activation. The result includes `migration-manifest.json` with every source, destination, size, and SHA-256. Use `--skip-project-data` only when project-local images and attachments are intentionally not wanted.

The official instance may remain installed and running. If it keeps writing persistent data during the migration window, the command asks you to rerun during a quieter moment instead of silently omitting new files.

Custom locations are supported:

```bash
cc-connect-next migrate \
  --source /path/to/official-data \
  --target /path/to/next-data \
  --dry-run
```

Relative `data_dir`, `work_dir`, and `base_dir` values are resolved from the official daemon's recorded working directory when available. If that metadata is stale or the official instance was only run manually, pass `--runtime-work-dir /absolute/original/cwd` explicitly.

Configuration paths use the same `${NAME}` placeholder rules as official CC Connect. A configured `data_dir` that has not been created yet is treated as empty, so the valid configuration root still migrates. If optional project data cannot be read, or project state/binding metadata is malformed, the global migration continues and still copies that metadata verbatim; every skipped discovery source is printed and recorded in `migration-manifest.json`. Grant access or repair the metadata, then rerun before treating project-local migration as complete.

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
Use the beta package if it is published; otherwise build the current source. Then run
`cc-connect-next migrate --dry-run`, report the plan,
then run the real one-command migration only after confirming the target is ~/.cc-connect-next.
Check its migration-manifest.json and report any timestamped pre-migration backups.
Validate `cc-connect-next --version`, config permissions, independent daemon name,
and independent API socket. Do not start both runtimes with the same Feishu app.
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
