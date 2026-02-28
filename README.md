# cc-connect

English | [中文](./README.zh-CN.md)

Bridge your local AI coding assistants (Claude Code / Cursor / Gemini CLI / Codex) to messaging platforms like Feishu (Lark), DingTalk, Slack, and more. Chat with your local AI agent from anywhere — no public IP required.

## Architecture

```
┌──────────────┐     ┌────────────┐     ┌──────────────┐
│ Feishu/Ding  │◄───►│   Engine    │◄───►│  Claude Code │
│ Slack/...    │     │  (Router)   │     │  Cursor/...  │
└──────────────┘     └────────────┘     └──────────────┘
    Platform              Core               Agent
```

- **Platform** — Messaging platform adapter. Handles receiving/sending messages over WebSocket, Stream, etc.
- **Agent** — AI assistant adapter. Invokes the local AI tool and collects its response.
- **Engine** — Core router. Manages sessions, routes messages between platforms and agents, handles slash commands.

All components are decoupled via Go interfaces — fully pluggable and extensible.

## Support Matrix

| Component | Type | Status |
|-----------|------|--------|
| Agent | Claude Code | ✅ Supported |
| Agent | Cursor Agent | 🔜 Planned |
| Agent | Gemini CLI | 🔜 Planned |
| Agent | Codex | 🔜 Planned |
| Platform | Feishu (Lark) | ✅ Supported (WebSocket) |
| Platform | DingTalk | ✅ Supported (Stream) |
| Platform | Telegram | ✅ Supported (Long Polling) |
| Platform | Slack | ✅ Supported (Socket Mode) |
| Platform | Discord | ✅ Supported (Gateway) |
| Platform | LINE | ✅ Supported (Webhook) |
| Platform | WeChat Work (企业微信) | ✅ Supported (Webhook) |

## Quick Start

### Prerequisites

- Go 1.22+
- [Claude Code CLI](https://docs.anthropic.com/en/docs/claude-code) installed and configured

### Install

**From source:**

```bash
git clone https://github.com/chenhg5/cc-connect.git
cd cc-connect
make build
```

**Via npm:**

```bash
npm install -g cc-connect
```

### Configure

```bash
cp config.example.toml config.toml
vim config.toml
```

### Run

```bash
./cc-connect                              # uses config.toml by default
./cc-connect -config /path/to/config.toml # custom path
./cc-connect --version                    # show version info
```

## Permission Modes

Claude Code adapter supports four permission modes (matching Claude's `--permission-mode`), switchable at runtime via the `/mode` command:

| Mode | Config Value | Behavior |
|------|-------------|----------|
| **Default** | `default` | Every tool call requires user approval. You stay in full control. |
| **Accept Edits** | `acceptEdits` (alias: `edit`) | File edit tools are auto-approved; other tools (e.g. Bash) still ask. |
| **Plan Mode** | `plan` | Claude only plans — no execution until you approve the plan. |
| **YOLO** | `bypassPermissions` (alias: `yolo`) | All tool calls auto-approved. For trusted/sandboxed environments. |

```toml
[projects.agent.options]
mode = "default"
# In default/acceptEdits mode, you can also pre-approve specific tools:
# allowed_tools = ["Read", "Grep", "Glob"]
```

Switch mode at runtime from the chat:

```
/mode          # show current mode and all available modes
/mode yolo     # switch to YOLO mode
/mode default  # switch back to default
```

## Session Management

Each user gets an independent session with full conversation context. Manage sessions via slash commands:

| Command | Description |
|---------|-------------|
| `/new [name]` | Create a new session |
| `/list` | List all Claude Code sessions for this project |
| `/switch <id\|name>` | Switch to a different session |
| `/current` | Show current session info |
| `/history [n]` | Show last n messages (default 10) |
| `/allow <tool>` | Pre-allow a tool (takes effect on next session) |
| `/mode [name]` | View or switch permission mode |
| `/quiet` | Toggle thinking/tool progress messages |
| `/stop` | Stop current execution |
| `/help` | Show available commands |

During a session, Claude may request tool permissions. Reply **allow** / **deny** / **allow all** (auto-approve all remaining requests this session).

## Configuration

Each `[[projects]]` entry binds one code directory to its own agent and platforms. A single cc-connect process can manage multiple projects simultaneously.

```toml
# Project 1
[[projects]]
name = "my-backend"

[projects.agent]
type = "claudecode"

[projects.agent.options]
work_dir = "/path/to/backend"
mode = "default"

[[projects.platforms]]
type = "feishu"

[projects.platforms.options]
app_id = "cli_xxxx"
app_secret = "xxxx"

# Project 2 — different folder, different bot
[[projects]]
name = "my-frontend"

[projects.agent]
type = "claudecode"

[projects.agent.options]
work_dir = "/path/to/frontend"
mode = "bypassPermissions"

[[projects.platforms]]
type = "dingtalk"

[projects.platforms.options]
client_id = "xxxx"
client_secret = "xxxx"
```

### Feishu (Lark)

1. Create an app at [Feishu Open Platform](https://open.feishu.cn)
2. Enable the **Bot** capability
3. Add the `im.message.receive_v1` event under **Event Subscriptions**
4. Select **WebSocket long connection** mode (no public IP needed)
5. Copy the App ID and App Secret into your config

### DingTalk

1. Create an app at [DingTalk Open Platform](https://open-dev.dingtalk.com)
2. Create a **Bot** and select **Stream mode**
3. Copy the Client ID and Client Secret into your config

### Telegram

1. Message [@BotFather](https://t.me/BotFather) on Telegram, send `/newbot`
2. Copy the bot token into your config
3. Connection: Long polling (no public URL needed)

### Slack

1. Create an app at [Slack API](https://api.slack.com/apps)
2. Enable **Socket Mode** (Settings > Socket Mode)
3. Subscribe to bot events: `message.channels`, `message.im`
4. Install app to workspace, copy Bot Token (`xoxb-...`) and App Token (`xapp-...`)
5. Connection: Socket Mode WebSocket (no public URL needed)

### Discord

1. Create an app at [Discord Developer Portal](https://discord.com/developers/applications)
2. Under **Bot**, create a bot and copy the token
3. Enable **Message Content Intent** under Privileged Gateway Intents
4. Invite bot to server via OAuth2 URL Generator (scopes: `bot`; permissions: `Send Messages`)
5. Connection: Gateway WebSocket (no public URL needed)

### LINE

1. Create a **Messaging API** channel at [LINE Developers Console](https://developers.line.biz/console/)
2. Copy the Channel Secret and Channel Access Token (long-lived)
3. Set your webhook URL in LINE console to `http(s)://<your-domain>:<port>/callback`
4. Connection: HTTP Webhook — you must expose the local port via ngrok, cloudflared, etc.

### WeChat Work (企业微信)

1. Log in to [WeChat Work Admin](https://work.weixin.qq.com/wework_admin/frame)
2. **App Management** > Create a custom app > note the AgentId and Secret
3. **My Enterprise** > note the Corp ID
4. In your app > **Receive Messages** > Set API Receive:
   - URL: `http(s)://<your-domain>:<port>/wecom/callback`
   - Token: any random string
   - EncodingAESKey: click "Random Generate"
   - Start cc-connect **first**, then save to pass verification
5. **Trusted IP** > add your server's outbound public IP
6. (Optional) **My Enterprise** > **WeChat Plugin** > scan QR to link personal WeChat — this allows chatting from regular WeChat too
7. Connection: HTTP Webhook — you must expose the local port via ngrok, cloudflared, etc.
8. Messages are sent as Markdown (with automatic text fallback)

## Extending

### Adding a New Platform

Implement the `core.Platform` interface and register it:

```go
package myplatform

import "github.com/chenhg5/cc-connect/core"

func init() {
    core.RegisterPlatform("myplatform", New)
}

func New(opts map[string]any) (core.Platform, error) {
    return &MyPlatform{}, nil
}

// Implement Name(), Start(), Reply(), Send(), Stop()
```

Then add a blank import in `cmd/cc-connect/main.go`:

```go
_ "github.com/chenhg5/cc-connect/platform/myplatform"
```

### Adding a New Agent

Same pattern — implement `core.Agent` and register via `core.RegisterAgent`.

## Project Structure

```
cc-connect/
├── cmd/cc-connect/          # Entrypoint
│   └── main.go
├── core/                    # Core abstractions
│   ├── interfaces.go        # Platform + Agent interfaces
│   ├── registry.go          # Plugin-style factory registry
│   ├── message.go           # Unified message / event types
│   ├── session.go           # Multi-session management
│   ├── i18n.go              # Internationalization (en/zh)
│   └── engine.go            # Routing engine + slash commands
├── platform/                # Platform adapters
│   ├── feishu/              # Feishu / Lark (WebSocket)
│   ├── dingtalk/            # DingTalk (Stream)
│   ├── telegram/            # Telegram (Long Polling)
│   ├── slack/               # Slack (Socket Mode)
│   ├── discord/             # Discord (Gateway WebSocket)
│   ├── line/                # LINE (HTTP Webhook)
│   └── wecom/               # WeChat Work (HTTP Webhook + AES + Markdown)
├── agent/                   # Agent adapters
│   └── claudecode/          # Claude Code CLI (interactive sessions)
├── config/                  # Config loading
├── config.example.toml      # Config template
├── Makefile
└── README.md
```

## License

MIT
