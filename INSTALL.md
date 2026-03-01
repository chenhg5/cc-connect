# cc-connect Installation & Configuration Guide

> **This document is designed to be read by AI coding agents (Claude Code, Cursor, Gemini CLI, etc.) to help users install and configure cc-connect.** You can feed this file directly to your AI agent.

## What is cc-connect?

cc-connect bridges local AI coding assistants to messaging platforms (Feishu, DingTalk, Telegram, Slack, Discord, LINE, WeChat Work), enabling bidirectional chat with your local AI agent from anywhere.

GitHub: https://github.com/chenhg5/cc-connect

## Step 1: Install cc-connect

### Option A: npm (recommended for most users)

```bash
npm install -g cc-connect
```

Install beta version:

```bash
npm install -g cc-connect@beta
```

After installation, the `cc-connect` binary will be available globally.

### Option B: Download binary from GitHub Releases

Go to https://github.com/chenhg5/cc-connect/releases and download the binary for your platform:

- `cc-connect-linux-amd64` / `cc-connect-linux-arm64`
- `cc-connect-darwin-amd64` / `cc-connect-darwin-arm64`
- `cc-connect-windows-amd64.exe`

```bash
# Example for Linux amd64:
curl -L -o cc-connect https://github.com/chenhg5/cc-connect/releases/latest/download/cc-connect-linux-amd64
chmod +x cc-connect
sudo mv cc-connect /usr/local/bin/
```

On macOS, you may need to remove the quarantine attribute:

```bash
xattr -d com.apple.quarantine cc-connect
```

### Option C: Build from source

Requires Go 1.22+.

```bash
git clone https://github.com/chenhg5/cc-connect.git
cd cc-connect
make build
# Binary will be at ./cc-connect
```

## Step 2: Install your AI Agent

cc-connect currently supports Claude Code. Make sure it is installed:

```bash
# Install Claude Code CLI (requires Node.js)
npm install -g @anthropic-ai/claude-code
```

Verify it works:

```bash
claude --version
```

## Step 3: Create config.toml

Create a `config.toml` file in the directory where you will run cc-connect. You can also copy the example:

```bash
# If you cloned the repo:
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
type = "claudecode"  # or "codex" for OpenAI Codex

[projects.agent.options]
work_dir = "/absolute/path/to/your/project"
mode = "default"

# --- Claude Code mode options ---
# "default", "acceptEdits" (alias: "edit"), "plan", "bypassPermissions" (alias: "yolo")
# allowed_tools = ["Read", "Grep", "Glob"]  # optional: pre-approve specific tools

# --- Codex mode options ---
# "suggest" (default), "auto-edit", "full-auto", "yolo"
# model = "o3"  # optional: specify model

# Add one or more platform sections below
```

## Step 4: Configure a Messaging Platform

Choose one or more platforms to connect. Each platform requires creating a bot/app on the platform's developer console and copying credentials into config.toml.

---

### Feishu (Lark) — No public IP needed

Connection: WebSocket long connection (SDK auto-negotiates)

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
enable_markdown = false  # true = Markdown messages (WeChat Work app only; personal WeChat shows "unsupported")
# proxy = "http://your-vps-ip:8888"  # optional: forward proxy if your IP is dynamic
```

**Detailed guide:** [docs/wecom.md](docs/wecom.md)

---

## Step 5: Run cc-connect

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
/mode [name]     — View/switch permission mode (default/edit/plan/yolo)
/quiet           — Toggle thinking/tool progress messages
/allow <tool>    — Pre-allow a tool (next session)
/provider [...]  — Manage API providers (list/add/remove/switch) [Beta]
/stop            — Stop current execution
/help            — Show available commands
```

During a session, Claude may ask for tool permissions. Reply:
- `allow` or `允许` — approve this request
- `deny` or `拒绝` — reject this request
- `allow all` or `允许所有` — auto-approve all remaining requests this session

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

## Beta Features

The following features are available in beta (`npm install -g cc-connect@beta` or `cc-connect update --pre`):

- **Codex Agent**: OpenAI Codex CLI integration (`codex exec --json`)
- **Voice Messages (STT)**: Speech-to-text via Whisper API (OpenAI / Groq / SiliconFlow). Requires `ffmpeg` and `[speech]` config.
- **Image Messages**: Send images to Claude Code for multimodal analysis
- **API Provider Management**: Runtime switching between API providers via `/provider` command or CLI
- **CLI Send**: `cc-connect send` to inject messages into active sessions from external processes

## Troubleshooting

- **"session already in use"** — A previous Claude Code process may still be running. Use `/new` to start a fresh session.
- **No response from bot** — Check `cc-connect` logs. Set `level = "debug"` in `[log]` for verbose output.
- **WeChat Work can't send messages** — Ensure your outbound IP is in the Trusted IP whitelist. If using a proxy, check the proxy is reachable.
- **LINE/WeChat Work can't receive messages** — Ensure your webhook URL is publicly accessible (ngrok/cloudflared running).
- **macOS binary won't open** — Run `xattr -d com.apple.quarantine cc-connect` to remove quarantine flag.
