# cc-connect-next

Privacy-first successor to [CC Connect](https://github.com/chenhg5/cc-connect), with a native Feishu Card 2.0 response lifecycle.

[中文说明](README.zh-CN.md) · [Install guide](INSTALL.md) · [Feishu guide](docs/feishu.md)

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

It copies `config.toml` and persistent state such as sessions, projects, cron/timer data, bindings, and local provider configuration. It rewrites `data_dir` to `~/.cc-connect-next` and excludes runtime-only paths including logs, sockets, locks, restart notifications, and daemon metadata. Existing targets are refused unless `--force` is supplied deliberately.

Custom locations are supported:

```bash
cc-connect-next migrate \
  --source /path/to/official-data \
  --target /path/to/next-data \
  --dry-run
```

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

[[projects.platforms]]
type = "feishu"

[projects.platforms.options]
app_id = "${FEISHU_APP_ID}"
app_secret = "${FEISHU_APP_SECRET}"
reply_to_trigger = true
done_emoji = "Done"
```

Set `card_mode = "legacy"` to opt out and use the inherited CC Connect message rendering.

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
then run the real migration only after confirming the target is ~/.cc-connect-next.
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
