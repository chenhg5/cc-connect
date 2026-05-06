# cc-connect Installation & Configuration Guide

> **This document is designed to be read by AI coding agents (Claude Code, Cursor, Gemini CLI, etc.) to help users install and configure cc-connect.** You can feed this file directly to your AI agent.

## What is cc-connect?

cc-connect bridges local AI coding assistants to messaging platforms (Feishu, DingTalk, Telegram, Slack, Discord, LINE, WeChat Work, QQ, **personal WeChat** via the **Weixin (ilink)** platform).

GitHub: https://github.com/chenhg5/cc-connect

**IMPORTANT: If you are an AI agent helping a user install cc-connect, use interactive tools (like AskUserQuestion) to guide them through configuration choices:**
- Agent selection (Claude Code, Cursor, Gemini, etc.)
- Platform selection (Feishu, Telegram, Discord, etc.)
- API keys and authentication tokens
- Project paths and preferences

Don't guess values—always ask the user to choose via interactive prompts.

## Step 1: Install cc-connect

### Option A: npm (recommended for most users)

```bash
npm install -g cc-connect
```

After installation, the `cc-connect` binary will be available globally.


### Option B: Homebrew (macOS / Linux)

```bash
brew install cc-connect
```

### Option C: Download binary from GitHub Releases

Go to https://github.com/chenhg5/cc-connect/releases and download the binary for your platform.

Typical artifact names (check the release page for exact filenames):

- Linux: `cc-connect-<version>-linux-amd64` (or `.tar.gz`)
- macOS: `cc-connect-<version>-darwin-amd64` / `arm64`
- Windows: `cc-connect-<version>-windows-amd64.exe` (or `.zip`)

```bash
# Example for Linux amd64 (replace URL with the asset link from the release you chose):
curl -L -o cc-connect https://github.com/chenhg5/cc-connect/releases/latest/download/cc-connect-linux-amd64
chmod +x cc-connect
sudo mv cc-connect /usr/local/bin/
```

On macOS, you may need to remove the quarantine attribute:

```bash
xattr -d com.apple.quarantine cc-connect
```

### Option D: Build from source

Requires Go 1.22+.

```bash
git clone https://github.com/chenhg5/cc-connect.git
cd cc-connect
make build
# Binary will be at ./cc-connect
```

## Step 2: Install your AI Agent

cc-connect supports multiple local coding agents. Install at least one:

```bash
# Claude Code
npm install -g @anthropic-ai/claude-code

# Codex
npm install -g @openai/codex

# Gemini CLI
npm install -g @google/gemini-cli

# iFlow CLI
npm install -g @iflow-ai/iflow-cli

# Qoder CLI
curl -fsSL https://qoder.com/install | bash
```

For **Cursor Agent** and **OpenCode**, follow their official install docs:
- Cursor Agent: https://docs.cursor.com/agent
- OpenCode: https://github.com/opencode-ai/opencode

Verify your selected agent works:

```bash
claude --version
codex --version
gemini --version
iflow --version
opencode --version
qodercli --version
```

## Step 3: Create config.toml

> **💡 Recommended: Use the Web UI** — After installing, run `cc-connect web` to configure the web admin and open the dashboard in your browser. You can visually create projects, add platforms, manage API providers, and even chat with your agent directly from the browser — no need to edit TOML files by hand. **Note:** `cc-connect web` only configures and opens the browser — you still need to run `cc-connect` separately to start the service.

If you prefer manual configuration, cc-connect looks for config in this order:
1. `-config <path>` flag (explicit)
2. `./config.toml` (current directory)
3. `~/.cc-connect/config.toml` (global, **recommended**)

If no config file exists, running `cc-connect` will auto-create a starter template at `~/.cc-connect/config.toml`.

**Manual config location:**

```bash
mkdir -p ~/.cc-connect
# If you cloned the repo, copy the example:
cp config.example.toml ~/.cc-connect/config.toml
# Or just run cc-connect once — it will create a starter config automatically
```

You can also use a local config in the current directory:

```bash
cp config.example.toml config.toml
```

The configuration has this structure:

```toml
# Optional global settings
# language = "en"  # "en", "zh", or "" (auto-detect)

[log]
level = "info"  # debug, info, warn, error

# Each [[projects]] entry connects one code folder to one or more messaging platforms
[[projects]]
name = "my-project"

[projects.agent]
type = "claudecode"  # or "codex", "cursor", "gemini", "qoder", "opencode", "iflow"

[projects.agent.options]
work_dir = "/absolute/path/to/your/project"
mode = "default"

# --- Claude Code mode options ---
# "default", "acceptEdits" (alias: "edit"), "plan", "auto", "bypassPermissions" (alias: "yolo")
# allowed_tools = ["Read", "Grep", "Glob"]  # optional: pre-approve specific tools

# --- Codex mode options ---
# "suggest" (default), "auto-edit", "full-auto", "yolo"
# model = "o3"  # optional: specify model

# --- Qoder CLI mode options ---
# "default", "yolo"
# model = "auto"  # "auto", "ultimate", "performance", "efficient", "lite"

# --- iFlow CLI mode options ---
# "default", "auto-edit", "plan", "yolo"
# model = "Qwen3-Coder"  # optional: specify model

# Add one or more platform sections below
```

## Step 4: Configure a Messaging Platform

Choose one or more platforms to connect. Each platform requires creating a bot/app on the platform's developer console and copying credentials into config.toml.

---

### Feishu (Lark) — No public IP needed

Connection: WebSocket long connection (SDK auto-negotiates)

**CLI shortcut (recommended):**

```bash
# Recommended: unified entry
cc-connect feishu setup --project my-project
cc-connect feishu setup --project my-project --app cli_xxx:sec_xxx

# Force modes (usually unnecessary)
cc-connect feishu new --project my-project

cc-connect feishu bind --project my-project --app cli_xxx:sec_xxx
```

Notes:
- `setup` is the unified entry:
  - no credentials => same as `new`
  - with `--app`/`--app-id` => same as `bind`
- `setup/new` prints a terminal QR code + URL for mobile scanning.
- If `--project` does not exist, cc-connect creates it automatically.
- This flow fills `app_id` / `app_secret`; in QR onboarding flow, Feishu usually pre-configures permissions and event subscriptions.
- Still verify app publish status and availability scope in Feishu Open Platform.

**Setup steps:**
1. Go to https://open.feishu.cn → Console → Create Enterprise App
2. Enable **Bot** capability (App Capabilities → Bot)
3. Go to **Permissions** → add `im:message.receive_v1`, `im:message:send_as_bot`
4. Go to **Event Subscriptions** → select **WebSocket long connection mode** → add event `im.message.receive_v1`
5. Publish the app version
6. Copy App ID and App Secret

**Config:**

```toml
[[projects.platforms]]
type = "feishu"

[projects.platforms.options]
app_id = "cli_xxxxxxxxxxxx"
app_secret = "xxxxxxxxxxxxxxxxxxxxxxxx"
```

**Detailed guide:** [docs/feishu.md](docs/feishu.md)

---

### DingTalk — No public IP needed

Connection: Stream mode (WebSocket)

**Setup steps:**
1. Go to https://open-dev.dingtalk.com → Create App
2. Enable **Bot** capability, select **Stream mode**
3. Configure permissions for messaging
4. Copy Client ID (AppKey) and Client Secret (AppSecret)

**Config:**

```toml
[[projects.platforms]]
type = "dingtalk"

[projects.platforms.options]
client_id = "dingxxxxxxxxxxxxxxxxx"
client_secret = "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
```

**Detailed guide:** [docs/dingtalk.md](docs/dingtalk.md)

---

### Telegram — No public IP needed

Connection: Long Polling

**Setup steps:**
1. Message @BotFather on Telegram → send `/newbot`
2. Follow prompts to set bot name and username (must end with `bot`)
3. Copy the bot token

**Config:**

```toml
[[projects.platforms]]
type = "telegram"

[projects.platforms.options]
token = "1234567890:ABCdefGHIjklMNOpqrsTUVwxyz"
```

**Detailed guide:** [docs/telegram.md](docs/telegram.md)

---

### Slack — No public IP needed

Connection: Socket Mode (WebSocket)

**Setup steps:**
1. Go to https://api.slack.com/apps → Create New App → From scratch
2. Enable **Socket Mode** (Settings → Socket Mode) → generate App-Level Token (`xapp-...`)
3. Subscribe to bot events: `message.im`, `app_mention` (Event Subscriptions)
4. Add Bot Token Scopes: `chat:write`, `im:history`, `im:read`, `im:write`, `app_mentions:read`
5. Install App to Workspace → copy Bot Token (`xoxb-...`)

**Config:**

```toml
[[projects.platforms]]
type = "slack"

[projects.platforms.options]
bot_token = "xoxb-your-bot-token"
app_token = "xapp-your-app-level-token"
```

**Detailed guide:** [docs/slack.md](docs/slack.md)

---

### Discord — No public IP needed

Connection: Gateway WebSocket

**Setup steps:**
1. Go to https://discord.com/developers/applications → New Application
2. Go to **Bot** → Add Bot → copy Token
3. Enable **Message Content Intent** (under Privileged Gateway Intents)
4. Go to **OAuth2** → URL Generator → select scope `bot` → select permissions `Send Messages`, `Read Message History`
5. Open the generated URL to invite bot to your server

**Config:**

```toml
[[projects.platforms]]
type = "discord"

[projects.platforms.options]
token = "your-discord-bot-token"
```

**Detailed guide:** [docs/discord.md](docs/discord.md)

---

### LINE — Requires public URL

Connection: HTTP Webhook (you need ngrok, cloudflared, or a server with public IP)

**Setup steps:**
1. Go to https://developers.line.biz/console/ → Create Messaging API channel
2. Copy Channel Secret and Channel Access Token (long-lived)
3. Set webhook URL in LINE console: `https://<your-public-domain>:<port>/callback`
4. Expose local port using ngrok/cloudflared: `ngrok http 8080` or `cloudflared tunnel --url http://localhost:8080`

**Config:**

```toml
[[projects.platforms]]
type = "line"

[projects.platforms.options]
channel_secret = "your-channel-secret"
channel_token = "your-channel-access-token"
port = "8080"
callback_path = "/callback"
```

---

### WeChat Work (企业微信) — Requires public URL

Connection: HTTP Webhook (you need ngrok, cloudflared, or a server with public IP)

**Setup steps:**
1. Log in to https://work.weixin.qq.com/wework_admin/frame
2. **App Management** → Create custom app → note AgentId and Secret
3. **My Enterprise** → note Corp ID
4. In the app → **Receive Messages** → Set API Receive:
   - URL: `https://<your-public-domain>:<port>/wecom/callback`
   - Token: any random string
   - EncodingAESKey: click "Random Generate" (43 chars)
   - **Start cc-connect FIRST, then save** (to pass URL verification)
5. **Trusted IP** → add your server's outbound public IP
6. (Optional) **WeChat Plugin** → scan QR to link personal WeChat

**Config:**

```toml
[[projects.platforms]]
type = "wecom"

[projects.platforms.options]
corp_id = "wwxxxxxxxxxxxxxxxxx"
corp_secret = "your-app-secret"
agent_id = "1000002"
callback_token = "your-callback-token"
callback_aes_key = "your-43-char-encoding-aes-key"
port = "8081"
callback_path = "/wecom/callback"
api_base_url = "https://qyapi.weixin.qq.com"  # optional: override WeChat Work API base URL (for private deployments)
enable_markdown = false  # true = Markdown messages (WeChat Work app only; personal WeChat shows "unsupported")
# proxy = "http://your-vps-ip:8888"  # optional: forward proxy if your IP is dynamic
```

**Detailed guide:** [docs/wecom.md](docs/wecom.md)

### Weixin (personal, ilink) — No public IP needed

Personal WeChat uses Tencent’s **ilink bot HTTP API** (same family as OpenClaw `openclaw-weixin`). The recommended flow is CLI QR login, which writes `token` (and related fields) into `config.toml`.

1. Run:

   ```bash
   cc-connect weixin setup --project my-project
   ```

2. Scan the QR code (or open the printed URL) in WeChat and confirm.

3. Restart cc-connect, then send a message from WeChat once so `context_token` is cached.

If you already have a Bearer token, use `cc-connect weixin bind --project my-project --token '<token>'`.

**Detailed guide (Chinese):** [docs/weixin.md](docs/weixin.md)

### QQ (via NapCat / OneBot v11) — No public IP needed

QQ integration requires a third-party OneBot v11 implementation (e.g., NapCat) as a bridge.

1. Deploy NapCat (recommended via Docker):
   ```bash
   docker run -d --name napcat -e ACCOUNT=<QQ号> -p 3001:3001 -p 6099:6099 --restart unless-stopped mlikiowa/napcat-docker:latest
   ```
2. First launch: check `docker logs -f napcat` for a QR code, scan with QQ mobile app to log in
3. Open NapCat WebUI at `http://localhost:6099`, enable **Forward WebSocket** on port 3001
4. Add to `config.toml`:

```toml
[[projects.platforms]]
type = "qq"

[projects.platforms.options]
ws_url = "ws://127.0.0.1:3001"  # NapCat Forward WebSocket URL
token = ""                       # optional: access_token (must match NapCat config)
allow_from = "*"                 # allowed QQ user IDs: "12345,67890" or "*" for all
```

**Detailed guide:** [docs/qq.md](docs/qq.md)

---

## Step 5: Run cc-connect

**Open the Web UI (recommended):**

```bash
cc-connect web    # configure web admin & open browser (does NOT start cc-connect)
cc-connect        # start the service
```

> **Note:** `cc-connect web` only configures the web admin and opens the dashboard in your browser — it does **not** start the cc-connect service itself. You still need to run `cc-connect` (or `cc-connect --config <path>`) separately to actually start the bridge. Think of it as two steps: configure first, then run.

**Important: If you are running inside a Claude Code session** (e.g., Claude Code helped you install and configure cc-connect), you must unset the `CLAUDECODE` environment variable before starting, otherwise Claude Code will refuse to launch as a subprocess:

```bash
unset CLAUDECODE && cc-connect
```

Alternatively, open a **separate terminal** and run cc-connect there — this avoids the issue entirely.

**Normal startup:**

```bash
# Run with config.toml in current directory
cc-connect

# Or specify config path
cc-connect -config /path/to/config.toml

# Check version
cc-connect --version
```

You should see logs like:

```
level=INFO msg="platform started" project=my-project platform=feishu
level=INFO msg="engine started" project=my-project agent=claudecode platforms=1
level=INFO msg="cc-connect is running" projects=1
```

## Step 6: Chat Commands

Once running, send messages to your bot on the configured platform. Available slash commands:

```
/new [name]      — Start a new session
/list            — List agent sessions
/switch <id>     — Resume an existing session
/current         — Show current active session
/history [n]     — Show last n messages (default 10)
/reasoning [level] — View/switch reasoning effort (Codex)
/mode [name]     — View/switch permission mode (default/edit/plan/yolo)
/quiet           — Toggle thinking/tool progress messages
/allow <tool>    — Pre-allow a tool (next session)
/provider [...]  — Manage API providers (list/add/remove/switch)
/stop            — Stop current execution
/help            — Show available commands
```

During a session, Claude may ask for tool permissions. Reply:
- `allow` or `允许` — approve this request
- `deny` or `拒绝` — reject this request
- `allow all` or `允许所有` — auto-approve all remaining requests this session

## Step 7: Enable Natural Language Scheduling (Non-Claude-Code Agents)

cc-connect supports scheduled tasks (cron jobs). You can always create them via slash commands (`/cron add ...`) or CLI (`cc-connect cron add ...`), but to let the agent **understand natural language** like "every day at 6am, summarize trending repos", the agent needs to know about cc-connect's cron CLI.

**Claude Code** handles this automatically via `--append-system-prompt` — no extra setup needed.

**For Codex, Cursor Agent, Qoder CLI, Gemini CLI, OpenCode, or iFlow CLI**, add the following instructions to the agent's project-level instruction file in your project's `work_dir`:

| Agent | File to create/edit |
|-------|-------------------|
| Codex | `AGENTS.md` |
| Cursor Agent | `.cursorrules` |
| Qoder CLI | `AGENTS.md` |
| Gemini CLI | `GEMINI.md` |
| OpenCode | `OPENCODE.md` |
| iFlow CLI | `IFLOW.md` |

**Content to add** (copy-paste into the file):

```markdown
# cc-connect Integration

This project is managed via cc-connect, a bridge to messaging platforms.

## Scheduled tasks (cron)
When the user asks you to do something on a schedule (e.g. "every day at 6am",
"every Monday morning"), use the Bash/shell tool to run:

  cc-connect cron add --cron "<min> <hour> <day> <month> <weekday>" --prompt "<task description>" --desc "<short label>"

Environment variables CC_PROJECT and CC_SESSION_KEY are already set — do NOT
specify --project or --session-key.

Examples:
  cc-connect cron add --cron "0 6 * * *" --prompt "Collect GitHub trending repos and send a summary" --desc "Daily GitHub Trending"
  cc-connect cron add --cron "0 9 * * 1" --prompt "Generate a weekly project status report" --desc "Weekly Report"

To list, edit, or delete cron jobs:
  cc-connect cron list
  cc-connect cron edit <job-id> <field> <value>
  cc-connect cron del <job-id>

Use `cron edit` to modify a single field instead of delete-and-recreate.
Common editable fields: cron_expr, prompt, exec, description, enabled (true/false), mute (true/false), timeout_mins (int).
Run `cc-connect cron edit --help` for the full field list.

Examples:
  cc-connect cron edit abc123 cron_expr "0 9 * * *"
  cc-connect cron edit abc123 enabled false
  cc-connect cron edit abc123 prompt "Updated daily summary task"

## Send message to current chat
To proactively send a message back to the user's chat session (use --stdin heredoc for long/multi-line messages):

  cc-connect send --stdin <<'CCEOF'
  your message here (any special characters are safe)
  CCEOF

For short single-line messages:

  cc-connect send -m "short message"
```

After adding this file, the agent will be able to translate natural language scheduling requests into `cc-connect cron add` commands automatically.

> **Tip:** You may want to add `AGENTS.md` / `.cursorrules` / `GEMINI.md` to your `.gitignore` if you don't want cc-connect instructions committed to version control.

## Multi-Project Setup

A single cc-connect process can manage multiple projects. Each project has its own agent, work directory, and platforms:

```toml
[[projects]]
name = "backend"

[projects.agent]
type = "claudecode"

[projects.agent.options]
work_dir = "/path/to/backend"
mode = "default"

[[projects.platforms]]
type = "feishu"

[projects.platforms.options]
app_id = "cli_xxx"
app_secret = "xxx"

# Second project — using Codex
[[projects]]
name = "frontend"

[projects.agent]
type = "codex"

[projects.agent.options]
work_dir = "/path/to/frontend"
mode = "full-auto"

[[projects.platforms]]
type = "telegram"

[projects.platforms.options]
token = "xxx"

# Third project — using Cursor Agent
[[projects]]
name = "design-system"

[projects.agent]
type = "cursor"

[projects.agent.options]
work_dir = "/path/to/design-system"
mode = "force"

[[projects.platforms]]
type = "discord"

[projects.platforms.options]
token = "xxx"

# Fourth project — using Gemini CLI
[[projects]]
name = "my-gemini-project"

[projects.agent]
type = "gemini"

[projects.agent.options]
work_dir = "/path/to/gemini-project"
mode = "yolo"    # "default" | "auto_edit" | "yolo" | "plan"

[[projects.platforms]]
type = "slack"

[projects.platforms.options]
bot_token = "xoxb-xxx"
app_token = "xapp-xxx"

# Fifth project — using Qoder CLI
[[projects]]
name = "my-qoder-project"

[projects.agent]
type = "qoder"

[projects.agent.options]
work_dir = "/path/to/qoder-project"
mode = "default"    # "default" | "yolo"
# model = "auto"    # "auto" | "ultimate" | "performance" | "efficient" | "lite"

[[projects.platforms]]
type = "telegram"

[projects.platforms.options]
token = "xxx"

# Sixth project — using iFlow CLI
[[projects]]
name = "my-iflow-project"

[projects.agent]
type = "iflow"

[projects.agent.options]
work_dir = "/path/to/iflow-project"
mode = "default"    # "default" | "auto-edit" | "plan" | "yolo"
# model = "Qwen3-Coder"

[[projects.platforms]]
type = "slack"

[projects.platforms.options]
bot_token = "xoxb-xxx"
app_token = "xapp-xxx"
```

## Upgrade

### Check current version

```bash
cc-connect --version
```

### npm users

```bash
npm update -g cc-connect
```

### Binary users

Check the latest release at https://github.com/chenhg5/cc-connect/releases and compare with your local version. To upgrade:

```bash
# Linux/macOS — replace with your platform suffix
curl -L -o /usr/local/bin/cc-connect https://github.com/chenhg5/cc-connect/releases/latest/download/cc-connect-$(uname -s | tr '[:upper:]' '[:lower:]')-$(uname -m | sed 's/x86_64/amd64/' | sed 's/aarch64/arm64/')
chmod +x /usr/local/bin/cc-connect
```

### Source users

```bash
cd cc-connect
git pull
make build
```

After upgrading, restart the running cc-connect process.

## Step 8: Run as Background Service (Optional)

You can run cc-connect as a daemon managed by the OS init system (Linux systemd user service, macOS launchd LaunchAgent, Windows Task Scheduler task).

### Install the daemon

```bash
cc-connect daemon install --config ~/.cc-connect/config.toml
```

You can also point the daemon at the directory that contains `config.toml`:

```bash
cc-connect daemon install --work-dir ~/.cc-connect
```

Optional flags: `--config PATH`, `--log-file PATH`, `--log-max-size N` (MB), `--work-dir DIR`, `--force` (overwrite existing unit). `--config` points to a config file, while `--work-dir` points to the directory containing `config.toml`.

### Control the service

```bash
cc-connect daemon start
cc-connect daemon stop
cc-connect daemon restart
cc-connect daemon status
```

### View logs

```bash
cc-connect daemon logs           # tail current log
cc-connect daemon logs -f         # follow (like tail -f)
cc-connect daemon logs -n 100     # last 100 lines
cc-connect daemon logs --log-file /path/to/log  # custom log file
```

Logs auto-rotate at the configured max size and keep one backup.

On Windows, `daemon install` creates a native Task Scheduler task named `cc-connect`.
The task runs at user logon and is also started immediately after installation. The
installer writes a small PowerShell launcher under `~/.cc-connect` so the scheduled
task uses the selected config directory, log file, PATH, and proxy environment.

### Uninstall

```bash
cc-connect daemon uninstall
```

## Additional Features

The following additional features are available:

- **Codex Agent**: OpenAI Codex CLI integration (`codex exec --json`)
- **Cursor Agent**: Cursor Agent CLI integration (`agent --print --output-format stream-json`)
- **Gemini CLI**: Google Gemini CLI integration (`gemini -p --output-format stream-json`)
- **Qoder CLI**: Qoder CLI integration (`qodercli -p -f stream-json`)
- **OpenCode**: OpenCode CLI integration (`opencode run --format json`)
- **iFlow CLI**: iFlow CLI integration (`iflow -i -r -o`)
- **Voice Messages (STT)**: Speech-to-text via Whisper API (OpenAI / Groq / SiliconFlow). Requires `ffmpeg` and `[speech]` config.
- **Voice Reply (TTS)**: Text-to-speech via Qwen TTS / OpenAI TTS. Requires `ffmpeg` and `[tts]` config.
- **Image Messages**: Send images to Claude Code for multimodal analysis
- **API Provider Management**: Runtime switching between API providers via `/provider` command or CLI
- **CLI Send**: `cc-connect send` to inject messages into active sessions from external processes

## Troubleshooting

- **"session already in use"** — A previous Claude Code process may still be running. Use `/new` to start a fresh session.
- **No response from bot** — Check `cc-connect` logs. Set `level = "debug"` in `[log]` for verbose output.
- **WeChat Work can't send messages** — Ensure your outbound IP is in the Trusted IP whitelist. If using a proxy, check the proxy is reachable.
- **LINE/WeChat Work can't receive messages** — Ensure your webhook URL is publicly accessible (ngrok/cloudflared running).
- **macOS binary won't open** — Run `xattr -d com.apple.quarantine cc-connect` to remove quarantine flag.
