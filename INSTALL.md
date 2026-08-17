# cc-connect-next installation runbook

This runbook is intentionally safe for a person or coding agent to follow. cc-connect-next can be installed beside official CC Connect, but the two runtimes must not connect to the same Feishu app at the same time.

Repository: <https://github.com/timmyagentic/cc-connect-next>

## 1. Inspect before changing anything

```bash
uname -s
uname -m
command -v cc-connect || true
command -v cc-connect-next || true
cc-connect --version 2>/dev/null || true
cc-connect-next --version 2>/dev/null || true
```

Do not stop, uninstall, overwrite, or edit official CC Connect during installation or migration.

## 2. Install

### Published release

```bash
npm install -g cc-connect-next
cc-connect-next --version
```

`cc-connect-next@beta` installs the newest prerelease instead, when one exists.

The npm installer downloads the same-version native asset from the GitHub release. Supported targets are macOS, Linux, and Windows on amd64 or arm64.

### Update a published installation

```bash
cc-connect-next update
```

`update` follows the stable channel only. It detects whether the running binary belongs to a global npm package or is a standalone executable. npm installs are updated in their existing global prefix to the exact stable version; standalone installs verify the release archive against `checksums.txt` before replacing the binary. Restart a running daemon after the command completes:

```bash
cc-connect-next daemon restart
```

Prerelease updates remain explicit through `npm install -g cc-connect-next@beta`; `cc-connect-next update --pre` and `--beta` are intentionally rejected.

### Current source

When testing an unreleased commit, use Go 1.25+, Node.js 20+, and Git:

```bash
git clone https://github.com/timmyagentic/cc-connect-next.git
cd cc-connect-next
make build
./cc-connect-next --version
```

`make build` builds the embedded web UI before compiling the native binary. For a backend-only development build:

```bash
make build-noweb
```

### GitHub release asset

Release archives use these names:

```text
cc-connect-next-v<VERSION>-darwin-amd64.tar.gz
cc-connect-next-v<VERSION>-darwin-arm64.tar.gz
cc-connect-next-v<VERSION>-linux-amd64.tar.gz
cc-connect-next-v<VERSION>-linux-arm64.tar.gz
cc-connect-next-v<VERSION>-windows-amd64.zip
cc-connect-next-v<VERSION>-windows-arm64.zip
```

Download the exact asset shown on the [release page](https://github.com/timmyagentic/cc-connect-next/releases), verify it against `checksums.txt`, extract it, and place the binary on `PATH`.

Homebrew is not currently a supported cc-connect-next installation method.

## 3. Create or migrate configuration

### New installation

Run the binary once. It creates `~/.cc-connect-next/config.toml` with directory mode `0700` and file mode `0600`:

```bash
cc-connect-next
```

The generated file already carries the [recommended Feishu configuration](#the-recommended-feishu-configuration) — it is rendered from the same definition `feishu setup` applies, so the two cannot drift apart. What it leaves to you is marked `REPLACE`: the working directory and the Feishu credentials. Do not commit the configuration.

Startup refuses to run while any `REPLACE` value is still in place, and names both the key and the step that resolves it. That refusal is deliberate: with placeholder credentials the process would otherwise print `platform ready`, `engine started` and `cc-connect-next is running`, and only then fail to connect.

For Feishu, `cc-connect-next feishu setup` fills the credentials for you and then offers the recommended configuration — see [section 4](#4-configure-feishu).

### Migrate official CC Connect

Migration is explicit, local, and copy-only. Start with a dry run:

```bash
cc-connect-next migrate --dry-run
cc-connect-next migrate
```

The default `--source-version auto` validates the actual configuration and
persistent layout without executing any binary recorded by the official daemon.
The current compatibility matrix covers official v1.4.1 and v1.5.0-beta.1
through beta.3. When provenance is known, record it explicitly on both the dry
run and real migration, for example:

```bash
cc-connect-next migrate --source-version v1.5.0-beta.3 --dry-run
cc-connect-next migrate --source-version v1.5.0-beta.3
```

Unknown configuration fields and unavailable Agent/platform implementations
fail before target writes instead of being silently discarded. See the
[migration compatibility matrix](docs/migration-compatibility.md) for the exact
version-specific boundary.

The defaults are:

```text
source: ~/.cc-connect
target: ~/.cc-connect-next
```

The command reads exactly the official `config.toml`, inventories the effective `data_dir` (including a custom location), and inventories project-local `.cc-connect` directories referenced by configured work directories, multi-workspace roots, project state, or workspace bindings. If the config file is separate from the effective data directory, its sibling files and directories are never inventoried. It copies persistent configuration, sessions, project overrides, cron/timer/heartbeat state, bindings, local provider configuration, and project-local images/attachments without copying a surrounding repository, `.env`, backup tree, or service home. Agent-native stores such as Codex and Claude sessions remain in their original locations, so their existing IDs stay valid.

Before activation it hashes every source, builds the complete result in sibling staging directories, verifies the staged files, and checks the sources again. It activates each destination with an atomic rename and rolls back earlier activations if a later one fails, then writes `migration-manifest.json` with source, target, size, and SHA-256 records. Source/target overlap checks use filesystem identity, rejecting symlinked ancestors and case-only aliases on case-insensitive volumes. Global `data_dir` and project-local access modes and ownership are preserved so `run_as_user` agents retain traversal access. Missing custom-target ancestors created by migration inherit the corresponding source root's access and owner; existing ancestors are not modified. The rewritten config remains `0600`. Logs, sockets, locks, restart notifications, daemon metadata, and source symlinks are excluded. A non-empty target is rejected unless `--force` is explicit; with `--force`, the previous target is first preserved as a timestamped `*.pre-migration-*` backup. Use `--skip-project-data` only to deliberately omit project-local images and attachments. The official installation is never stopped or modified.

For custom locations:

```bash
cc-connect-next migrate \
  --source /absolute/path/to/official-data \
  --target /absolute/path/to/next-data \
  --dry-run
```

Relative `data_dir`, `work_dir`, and `base_dir` values are resolved from the official daemon's recorded working directory when available. That metadata is read from the official `$HOME/.cc-connect/daemon.json` even when `--source` is a separate config directory; a same-named config sibling is not trusted. Malformed metadata, or a recorded working directory that is missing or inaccessible, fails preflight instead of silently changing the base for relative paths. An omitted `data_dir` still means `$HOME/.cc-connect`, and only the custom config root's `config.toml` is copied. A separate custom `data_dir` is accepted only when every regular path matches known CC Connect persistent state; unexpected files or directories fail preflight instead of being copied from a broad service home. If daemon metadata is stale or the official process was launched manually from another directory, add `--runtime-work-dir /absolute/original/cwd`; the explicit override wins.

Configuration paths follow official CC Connect's `${NAME}` placeholder semantics. A configured `data_dir` that does not exist yet is treated as empty, so the valid configuration file still migrates. Unreadable optional project data or malformed project state/binding metadata does not discard the global migration, and the metadata file itself is still copied verbatim; each skipped discovery source is printed and recorded in `migration-manifest.json`. Grant access or repair the metadata, then rerun before treating project-local migration as complete.

## 4. Configure Feishu

Connect the bot and let setup offer the recommended configuration:

```bash
cc-connect-next feishu setup --project my-project --app cli_xxx:sec_xxx
```

After the credentials are saved, setup prints the recommended Feishu configuration and asks whether to apply it. Answer `--recommended` or `--no-recommended` on the command line to decide in advance; with neither flag and a non-interactive stdin, nothing is applied.

### The recommended Feishu configuration

This is the shape the project is operated with day to day, not a theoretical ideal: a quoted answer card that carries the final answer and nothing else, file references rendered so they can be clicked, and a bot that participates in its group without being @mentioned every time.

```toml
[projects.display]
card_mode = "rich"               # Feishu Card 2.0 answer card: counts only, never reasoning text
thinking_messages = false        # keep reasoning out of the chat
tool_messages = false            # keep tool calls and their arguments out of the chat
show_context_indicator = false   # keep model, token and context metadata out of the chat
reply_footer = false             # keep the per-turn status footer out of the chat
hide_agent_footer = true         # strip the equivalent lines the agent emits itself

[projects.references]
normalize_agents = ["codex"]     # the project's own agent; "all" when its syntax is not known
render_platforms = ["feishu"]
display_path = "smart"
marker_style = "emoji"
enclosure_style = "code"

[projects.platforms.options]
enable_feishu_card = true        # interactive card client instead of plain messages
reply_to_trigger = true          # reply in a quote of the triggering message
thread_isolation = false         # one session per chat, not per thread
done_emoji = "Done"              # react when a turn finishes, so the chat pushes a notification
group_reply_all = true           # answer every group message without an @mention
```

Most of these already match the built-in defaults. The profile still spells them out, so a configuration you accepted keeps producing the same chat surface when a future release changes what an unset key means.

Two things it deliberately does not touch, because they are specific to your installation rather than to Feishu: credentials, and the `allow_from` / `allow_chat` scope. **Set that scope before relying on `group_reply_all`** — without it the bot answers every message in every group it belongs to:

```toml
[projects.platforms.options]
allow_from = "ou_your_open_id"   # or "*"
allow_chat = "oc_your_chat_id"   # or "*"
```

The card appears immediately, shows only anonymous reasoning/tool counts, streams the answer in the same quoted card, and ends with a localized completion label (`✅ Done` in English, `✅ 已完成` in Chinese) or a localized generic failure label. Reasoning, tool details, model/token/context metadata, working directories, and reply footers are omitted from the card payload.

See the [Feishu answer-card contract](docs/feishu-card-contract.md) for the exact lifecycle, privacy boundary, fallback behavior, locale coverage, and executable verification commands.

Set `card_mode = "legacy"` only when intentionally opting back into inherited CC Connect rendering.

## 5. Validate without taking over production

Parse and inspect the migrated configuration before startup:

```bash
cc-connect-next --version
ls -ld ~/.cc-connect-next
ls -l ~/.cc-connect-next/config.toml
cc-connect-next migrate --dry-run
cc-connect-next doctor
```

`doctor` checks the configuration, the Agent CLI and its login state, every configured platform, local dependencies, and network reachability, then exits non-zero if any check failed. It never opens a platform connection, so it works before the first start and while the instance is down — which is when it is needed. Limit it to one project with `--project <name>`, or point it at another file with `--config <path>`.

If a platform cannot connect after startup, the process stays up and retries, and says so 30 seconds in:

```text
level=ERROR msg="platform startup incomplete" detail="my-project/feishu (1000040346: app_id is invalid) cannot deliver messages ..."
```

For live Feishu testing, use a separate test Feishu app while official CC Connect is running. Two WebSocket consumers using the same app credentials can race or duplicate message handling.

Expected isolated identities:

| Boundary | Official CC Connect | cc-connect-next |
|---|---|---|
| Command | `cc-connect` | `cc-connect-next` |
| Data | `~/.cc-connect` | `~/.cc-connect-next` |
| macOS launchd | `com.cc-connect.service` | `com.cc-connect-next.service` |
| Linux systemd | `cc-connect.service` | `cc-connect-next.service` |
| API socket | `~/.cc-connect/run/api.sock` | `~/.cc-connect-next/run/api.sock` |

## 6. Deliberate production switch

Only after a separate live test succeeds, stop any running test successor, stop the official daemon, and perform one final migration before starting production. This second migration is mandatory: it captures sessions, bindings, timers, and project state written by official CC Connect after the earlier test migration. Repeat any custom `--source`, `--target`, or `--runtime-work-dir` options used before. `--force` is deliberate here because the tested target already exists; it first preserves that entire target in timestamped backups.

```bash
# If a separately configured test successor is running, stop it first:
cc-connect-next daemon stop
cc-connect daemon stop

cc-connect-next migrate --dry-run --force
cc-connect-next migrate --force

cc-connect-next daemon install \
  --config ~/.cc-connect-next/config.toml \
  --work-dir /absolute/original/cwd
cc-connect-next daemon status
```

Inspect the final command's `migration-manifest.json` path and timestamped backups before startup. The official config is authoritative during this refresh; review the backed-up tested config and deliberately reapply only required successor-specific settings, never stale test-app credentials. If either final migration command fails, do not start cc-connect-next; restart `cc-connect` and resolve the reported source, permission, or concurrency problem. If cc-connect-next was already installed as a service with the exact migrated config and work directory, use `cc-connect-next daemon start` instead of reinstalling it.

Rollback leaves official data intact:

```bash
cc-connect-next daemon stop
cc-connect daemon start
```

## Agent completion checklist

An installing agent should report each item separately:

- exact cc-connect-next version and installation source;
- target OS and architecture;
- `~/.cc-connect-next` and config permission checks;
- dry-run migration result and whether a real migration was authorized;
- migration manifest path and every timestamped pre-migration backup;
- confirmation that a final `--force` migration ran after the official daemon stopped and before the successor started;
- the reported official runtime work directory when the config contains relative paths;
- confirmation that official files and services were not modified during install/migration;
- independent command, data directory, service, and API socket names;
- whether live Feishu validation used a separate app;
- any validation that remains unverified.
