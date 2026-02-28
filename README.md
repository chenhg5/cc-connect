# cc-connect

English | [中文](./README.zh-CN.md)

Bridge your local AI coding assistants (Claude Code / Cursor / Gemini CLI / Codex) to messaging platforms like Feishu (Lark), DingTalk, Slack, and more. Chat with your local AI agent from anywhere — no public IP required for most platforms.

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
| Platform | Feishu (Lark) | ✅ WebSocket — no public IP needed |
| Platform | DingTalk | ✅ Stream — no public IP needed |
| Platform | Telegram | ✅ Long Polling — no public IP needed |
| Platform | Slack | ✅ Socket Mode — no public IP needed |
| Platform | Discord | ✅ Gateway — no public IP needed |
| Platform | LINE | ✅ Webhook — public URL required |
| Platform | WeChat Work (企业微信) | ✅ Webhook — public URL required |

## Quick Start

### Prerequisites

- [Claude Code CLI](https://docs.anthropic.com/en/docs/claude-code) installed and configured

### Install & Configure via AI Agent (Recommended)

Send this to Claude Code or any AI coding agent, and it will handle the entire installation and configuration for you:

```
Please refer to https://raw.githubusercontent.com/chenhg5/cc-connect/refs/heads/main/INSTALL.md to help me install and configure cc-connect
```

### Manual Install

**Via npm:**

```bash
npm install -g cc-connect
```

**Download binary from [GitHub Releases](https://github.com/chenhg5/cc-connect/releases):**

```bash
# Linux amd64
curl -L -o cc-connect https://github.com/chenhg5/cc-connect/releases/latest/download/cc-connect-linux-amd64
chmod +x cc-connect
sudo mv cc-connect /usr/local/bin/
```

**Build from source (requires Go 1.22+):**

```bash
git clone https://github.com/chenhg5/cc-connect.git
cd cc-connect
make build
```

### Configure

```bash
cp config.example.toml config.toml
vim config.toml   # Fill in your platform credentials
```

### Run

```bash
./cc-connect                              # uses config.toml by default
./cc-connect -config /path/to/config.toml # custom path
./cc-connect --version                    # show version info
```

## Platform Setup Guides

Each platform requires creating a bot/app on the platform's developer console. We provide detailed step-by-step guides:

| Platform | Guide | Connection | Public IP? |
|----------|-------|------------|------------|
| Feishu (Lark) | [docs/feishu.md](docs/feishu.md) | WebSocket | No |
| DingTalk | [docs/dingtalk.md](docs/dingtalk.md) | Stream | No |
| Telegram | [docs/telegram.md](docs/telegram.md) | Long Polling | No |
| Slack | [docs/slack.md](docs/slack.md) | Socket Mode | No |
| Discord | [docs/discord.md](docs/discord.md) | Gateway | No |
| LINE | [INSTALL.md](./INSTALL.md#line--requires-public-url) | Webhook | Yes |
| WeChat Work | [docs/wecom.md](docs/wecom.md) | Webhook | Yes |

Quick config examples for each platform:

```toml
# Feishu
[[projects.platforms]]
type = "feishu"
[projects.platforms.options]
app_id = "cli_xxxx"
app_secret = "xxxx"

# DingTalk
[[projects.platforms]]
type = "dingtalk"
[projects.platforms.options]
client_id = "dingxxxx"
client_secret = "xxxx"

# Telegram
[[projects.platforms]]
type = "telegram"
[projects.platforms.options]
token = "123456:ABC-xxx"

# Slack
[[projects.platforms]]
type = "slack"
[projects.platforms.options]
bot_token = "xoxb-xxx"
app_token = "xapp-xxx"

# Discord
[[projects.platforms]]
type = "discord"
[projects.platforms.options]
token = "your-discord-bot-token"

# LINE (requires public URL)
[[projects.platforms]]
type = "line"
[projects.platforms.options]
channel_secret = "xxx"
channel_token = "xxx"
port = "8080"

# WeChat Work (requires public URL)
[[projects.platforms]]
type = "wecom"
[projects.platforms.options]
corp_id = "wwxxx"
corp_secret = "xxx"
agent_id = "1000002"
callback_token = "xxx"
callback_aes_key = "xxx"
port = "8081"
enable_markdown = false  # true only if all users use WeChat Work app (not personal WeChat)
```

## Permission Modes

Claude Code adapter supports four permission modes (matching Claude's `--permission-mode`), switchable at runtime via the `/mode` command:

| Mode | Config Value | Behavior |
|------|-------------|----------|
| **Default** | `default` | Every tool call requires user approval. |
| **Accept Edits** | `acceptEdits` (alias: `edit`) | File edit tools auto-approved; other tools still ask. |
| **Plan Mode** | `plan` | Claude only plans — no execution until you approve. |
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

```
/new [name]       Start a new session
/list             List all Claude Code sessions for this project
/switch <id>      Switch to a different session
/current          Show current session info
/history [n]      Show last n messages (default 10)
/allow <tool>     Pre-allow a tool (takes effect on next session)
/mode [name]      View or switch permission mode
/quiet            Toggle thinking/tool progress messages
/stop             Stop current execution
/help             Show available commands
```

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

See [config.example.toml](config.example.toml) for a fully commented configuration template.

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
│   └── wecom/               # WeChat Work (HTTP Webhook)
├── agent/                   # Agent adapters
│   └── claudecode/          # Claude Code CLI (interactive sessions)
├── docs/                    # Platform setup guides
├── config.example.toml      # Config template
├── INSTALL.md               # AI-agent-friendly install guide
├── Makefile
└── README.md
```

## License

MIT
