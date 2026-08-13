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

### Published beta

Once the first npm beta and matching GitHub release exist:

```bash
npm install -g cc-connect-next@beta
cc-connect-next --version
```

The npm installer downloads the same-version native asset from the GitHub release. Supported targets are macOS, Linux, and Windows on amd64 or arm64.

### Current source

Until a beta is published, or when testing an unreleased commit, use Go 1.25+, Node.js 20+, and Git:

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

Edit that file and add the selected Agent, working directory, and platform credentials. Do not commit the configuration.

### Migrate official CC Connect

Migration is explicit, local, and copy-only. Start with a dry run:

```bash
cc-connect-next migrate --dry-run
cc-connect-next migrate
```

The defaults are:

```text
source: ~/.cc-connect
target: ~/.cc-connect-next
```

The command validates TOML, rewrites the top-level `data_dir`, and copies persistent configuration and state. It excludes logs, sockets, locks, restart notifications, and daemon metadata. It does not stop or modify official CC Connect. A non-empty target is rejected unless `--force` is explicitly supplied.

For custom locations:

```bash
cc-connect-next migrate \
  --source /absolute/path/to/official-data \
  --target /absolute/path/to/next-data \
  --dry-run
```

## 4. Configure native Feishu cards

New configs default to privacy-first Rich Card mode. The relevant settings are:

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

The card appears immediately, shows only anonymous reasoning/tool counts, streams the answer in the same quoted card, and ends as `✅ Done` or a generic `⚠️ 未完成`. Reasoning, tool details, model/token/context metadata, working directories, and reply footers are omitted from the card payload.

Set `card_mode = "legacy"` only when intentionally opting back into inherited CC Connect rendering.

## 5. Validate without taking over production

Parse and inspect the migrated configuration before startup:

```bash
cc-connect-next --version
ls -ld ~/.cc-connect-next
ls -l ~/.cc-connect-next/config.toml
cc-connect-next migrate --dry-run
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

Only after a separate live test succeeds, stop the official daemon and start the successor:

```bash
cc-connect daemon stop
cc-connect-next daemon install
cc-connect-next daemon status
```

If cc-connect-next was already installed as a service, use `cc-connect-next daemon start` instead of reinstalling it.

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
- confirmation that official files and services were not modified during install/migration;
- independent command, data directory, service, and API socket names;
- whether live Feishu validation used a separate app;
- any validation that remains unverified.
