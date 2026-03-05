# cc-connect

[![Discord](https://img.shields.io/discord/1478978944567869652?logo=discord&label=Discord)](https://discord.gg/kHpwgaM4kq)
[![Telegram](https://img.shields.io/badge/Telegram-Group-26A5E4?logo=telegram)](https://t.me/+odGNDhCjbjdmMmZl)
[![GitHub release](https://img.shields.io/github/v/release/chenhg5/cc-connect?include_prereleases)](https://github.com/chenhg5/cc-connect/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

English | [中文](./README.zh-CN.md)

**Control your local AI coding agents from any chat app. Anywhere, anytime.**

cc-connect bridges AI coding assistants running on your dev machine to the messaging platforms you already use — so you can review code on the subway, kick off tasks from your phone, or pair-program from bed.

```
         You (Phone / Laptop / Tablet)
                    │
    ┌───────────────┼───────────────┐
    ▼               ▼               ▼
 Feishu          Slack          Telegram  ...8 platforms
    │               │               │
    └───────────────┼───────────────┘
                    ▼
              ┌────────────┐
              │ cc-connect │  ← your dev machine
              └────────────┘
              ┌─────┼─────┐
              ▼     ▼     ▼
         Claude  Gemini  Codex  ...6 agents
          Code    CLI   OpenCode
```

### Why cc-connect?

> Time to uninstall OpenClaw — cc-connect gives you access to the most powerful coding agents available, not just one.

- **6 AI Agents** — Claude Code, Codex, Cursor Agent, Qoder CLI, Gemini CLI, OpenCode. Use whichever fits your workflow, or all of them at once.
- **8 Chat Platforms** — Feishu, DingTalk, Slack, Telegram, Discord, WeChat Work, LINE, QQ. Most need zero public IP.
- **Full Control from Chat** — Switch models (`/model`), change permission modes (`/mode`), manage sessions, all via slash commands.
- **Agent Memory** — Read and write agent instruction files (`/memory`) without touching the terminal.
- **Scheduled Tasks** — Set up cron jobs in natural language. "Every day at 6am, summarize GitHub trending" just works.
- **Voice & Images** — Send voice messages or screenshots; cc-connect handles STT and multimodal forwarding.
- **Multi-Project** — One process, multiple projects, each with its own agent + platform combo.

<p align="center">
  <img src="docs/images/screenshot/cc-connect-discord.png" alt="Discord" width="600" />
</p>

## Support Matrix

| Component | Type | Status |
|-----------|------|--------|
| Agent | Claude Code | ✅ Supported |
| Agent | Codex (OpenAI) | ✅ Supported |
| Agent | Cursor Agent | ✅ Supported |
| Agent | Gemini CLI (Google) | ✅ Supported |
| Agent | Qoder CLI | ✅ Supported |
| Agent | OpenCode | ✅ Supported |
| Agent | Crush | 🔜 Planned |
| Agent | Goose (Block) | 🔜 Planned |
| Agent | Aider | 🔜 Planned |
| Agent | Kimi Code (Moonshot) | 🔭 Exploring |
| Agent | GLM Code / CodeGeeX (ZhipuAI) | 🔭 Exploring |
| Agent | MiniMax Code | 🔭 Exploring |
| Platform | Feishu (Lark) | ✅ WebSocket — no public IP needed |
| Platform | DingTalk | ✅ Stream — no public IP needed |
| Platform | Telegram | ✅ Long Polling — no public IP needed |
| Platform | Slack | ✅ Socket Mode — no public IP needed |
| Platform | Discord | ✅ Gateway — no public IP needed |
| Platform | LINE | ✅ Webhook — public URL required |
| Platform | WeChat Work (企业微信) | ✅ Webhook — public URL required |
| Platform | QQ (via NapCat/OneBot) | ✅ WebSocket, no public IP needed |
| Platform | WhatsApp | 🔜 Planned (Business Cloud API) |
| Platform | Microsoft Teams | 🔜 Planned (Bot Framework) |
| Platform | Google Chat | 🔜 Planned (Chat API) |
| Platform | Mattermost | 🔜 Planned (Webhook + Bot) |
| Platform | Matrix (Element) | 🔜 Planned (Client-Server API) |
| Feature | Voice Messages (STT) | ✅ Whisper API (OpenAI / Groq) + ffmpeg |
| Feature | Image Messages | ✅ Multimodal (Claude Code) |
| Feature | API Provider Management | ✅ Runtime provider switching |
| Feature | CLI Send (`cc-connect send`) | ✅ Send messages to sessions via CLI |

## Quick Start

### Prerequisites

- **Claude Code**: [Claude Code CLI](https://docs.anthropic.com/en/docs/claude-code) installed and configured, OR
- **Codex**: [Codex CLI](https://github.com/openai/codex) installed (`npm install -g @openai/codex`), OR
- **Cursor Agent**: [Cursor Agent CLI](https://docs.cursor.com/agent) installed (`agent --version` to verify), OR
- **Gemini CLI**: [Gemini CLI](https://github.com/google-gemini/gemini-cli) installed (`npm install -g @google/gemini-cli`), OR
- **Qoder CLI**: [Qoder CLI](https://qoder.com) installed (`curl -fsSL https://qoder.com/install | bash`), OR
- **OpenCode**: [OpenCode](https://github.com/opencode-ai/opencode) installed (`opencode --version` to verify)

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
# Global config (recommended)
mkdir -p ~/.cc-connect
cp config.example.toml ~/.cc-connect/config.toml
vim ~/.cc-connect/config.toml

# Or local config (also supported)
cp config.example.toml config.toml
```

### Run

```bash
./cc-connect                              # auto: ./config.toml → ~/.cc-connect/config.toml
./cc-connect -config /path/to/config.toml # explicit path
./cc-connect --version                    # show version info
```

### Upgrade

```bash
# npm
npm install -g cc-connect

# Binary self-update
cc-connect update

cc-connect update --pre
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
| QQ (NapCat) | [docs/qq.md](docs/qq.md) | WebSocket (OneBot v11) | No |

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

# QQ (via NapCat/OneBot v11, no public IP needed)
[[projects.platforms]]
type = "qq"
[projects.platforms.options]
ws_url = "ws://127.0.0.1:3001"
allow_from = "*"  # QQ user IDs, e.g. "12345,67890" or "*" for all
```

## Permission Modes

All agents support permission modes switchable at runtime via `/mode`.

**Claude Code** modes (maps to `--permission-mode`):

| Mode | Config Value | Behavior |
|------|-------------|----------|
| **Default** | `default` | Every tool call requires user approval. |
| **Accept Edits** | `acceptEdits` (alias: `edit`) | File edit tools auto-approved; other tools still ask. |
| **Plan Mode** | `plan` | Claude only plans — no execution until you approve. |
| **YOLO** | `bypassPermissions` (alias: `yolo`) | All tool calls auto-approved. For trusted/sandboxed environments. |

**Codex** modes (maps to `--ask-for-approval`):

| Mode | Config Value | Behavior |
|------|-------------|----------|
| **Suggest** | `suggest` | Only trusted commands (ls, cat...) run without approval. |
| **Auto Edit** | `auto-edit` | Model decides when to ask; sandbox-protected. |
| **Full Auto** | `full-auto` | Auto-approve with workspace sandbox. Recommended. |
| **YOLO** | `yolo` | Bypass all approvals and sandbox. |

**Cursor Agent** modes (maps to `--force` / `--mode`):

| Mode | Config Value | Behavior |
|------|-------------|----------|
| **Default** | `default` | Trust workspace, ask before each tool use. |
| **Force (YOLO)** | `force` (alias: `yolo`) | Auto-approve all tool calls. |
| **Plan** | `plan` | Read-only analysis, no edits. |
| **Ask** | `ask` | Q&A style, read-only. |

**Gemini CLI** modes (maps to `-y` / `--approval-mode`):

| Mode | Config Value | Behavior |
|------|-------------|----------|
| **Default** | `default` | Prompt for approval on each tool use. |
| **Auto Edit** | `auto_edit` (alias: `edit`) | Auto-approve edit tools, ask for others. |
| **YOLO** | `yolo` | Auto-approve all tool calls. |
| **Plan** | `plan` | Read-only plan mode, no execution. |

**Qoder CLI** modes:

| Mode | Config Value | Behavior |
|------|-------------|----------|
| **Default** | `default` | Standard permissions; prompt for approval. |
| **YOLO** | `yolo` | Skip all permission checks, auto-approve. |

**OpenCode** modes:

| Mode | Config Value | Behavior |
|------|-------------|----------|
| **Default** | `default` | Standard mode. |
| **YOLO** | `yolo` | Auto-approve all tool calls. |

```toml
# Claude Code
[projects.agent.options]
mode = "default"
# allowed_tools = ["Read", "Grep", "Glob"]

# Codex
[projects.agent.options]
mode = "full-auto"
# model = "o3"

# Cursor Agent
[projects.agent.options]
mode = "default"

# Gemini CLI
[projects.agent.options]
mode = "default"

# Qoder CLI
[projects.agent.options]
mode = "default"

# OpenCode
[projects.agent.options]
mode = "default"
```

Switch mode at runtime from the chat:

```
/mode          # show current mode and all available modes
/mode yolo     # switch to YOLO mode
/mode default  # switch back to default
```

## API Provider Management

Switch between different API providers (e.g. Anthropic direct, relay services, AWS Bedrock) at runtime — no restart needed. Provider credentials are injected as environment variables into the agent subprocess, so your local config stays untouched.

### Configure Providers

**In `config.toml`:**

```toml
[projects.agent.options]
work_dir = "/path/to/project"
provider = "anthropic"   # active provider name

[[projects.agent.providers]]
name = "anthropic"
api_key = "sk-ant-xxx"

[[projects.agent.providers]]
name = "relay"
api_key = "sk-xxx"
base_url = "https://api.relay-service.com"
model = "claude-sonnet-4-20250514"

# For special setups (Bedrock, Vertex, etc.), use the env map:
[[projects.agent.providers]]
name = "bedrock"
env = { CLAUDE_CODE_USE_BEDROCK = "1", AWS_PROFILE = "bedrock" }
```

**Via CLI:**

```bash
cc-connect provider add --project my-backend --name relay --api-key sk-xxx --base-url https://api.relay.com
cc-connect provider add --project my-backend --name bedrock --env CLAUDE_CODE_USE_BEDROCK=1,AWS_PROFILE=bedrock
cc-connect provider list --project my-backend
cc-connect provider remove --project my-backend --name relay
```

**Import from [cc-switch](https://github.com/SaladDay/cc-switch-cli):**

If you already use cc-switch to manage providers, import them with one command (requires `sqlite3`):

```bash
cc-connect provider import --project my-backend
cc-connect provider import --project my-backend --type claude     # only Claude providers
cc-connect provider import --db-path ~/.cc-switch/cc-switch.db    # explicit DB path
```

### Manage Providers in Chat

```
/provider                   Show current active provider
/provider list              List all configured providers
/provider add <name> <key> [url] [model]   Add a provider
/provider add {"name":"relay","api_key":"sk-xxx","base_url":"https://..."}
/provider remove <name>     Remove a provider
/provider switch <name>     Switch to a provider
/provider <name>            Shortcut for switch
```

Adding, removing, and switching providers all persist to `config.toml` automatically. Switching restarts the agent session with the new credentials.

**Env var mapping by agent type:**

| Agent | api_key → | base_url → |
|-------|-----------|------------|
| Claude Code | `ANTHROPIC_API_KEY` | `ANTHROPIC_BASE_URL` |
| Codex | `OPENAI_API_KEY` | `OPENAI_BASE_URL` |
| Gemini CLI | `GEMINI_API_KEY` | — (use `env` map) |
| OpenCode | `ANTHROPIC_API_KEY` | — (use `env` map) |

The `env` map in provider config lets you set arbitrary environment variables for any setup (Bedrock, Vertex, Azure, custom proxies, etc.).

## Claude Code Router Integration

[Claude Code Router](https://github.com/musistudio/claude-code-router) is a powerful tool that routes Claude Code requests to different model providers (OpenRouter, DeepSeek, Gemini, etc.) with custom transformations. cc-connect now supports seamless integration with Claude Code Router.

### Why Use Claude Code Router?

- **Multi-Provider Support**: Route requests to OpenRouter, DeepSeek, Ollama, Gemini, Volcengine, SiliconFlow, and more
- **Model Routing**: Use different models for different tasks (background, thinking, long context, web search)
- **Request/Response Transformation**: Automatic adaptation for different provider APIs
- **Dynamic Model Switching**: Switch models on-the-fly without restarting

### Setup

1. **Install Claude Code Router**:

```bash
npm install -g @musistudio/claude-code-router
```

2. **Configure Router** (create `~/.claude-code-router/config.json`):

```json
{
  "APIKEY": "your-secret-key",
  "Providers": [
    {
      "name": "openrouter",
      "api_base_url": "https://openrouter.ai/api/v1/chat/completions",
      "api_key": "sk-xxx",
      "models": ["anthropic/claude-sonnet-4", "google/gemini-2.5-pro-preview"],
      "transformer": { "use": ["openrouter"] }
    },
    {
      "name": "deepseek",
      "api_base_url": "https://api.deepseek.com/chat/completions",
      "api_key": "sk-xxx",
      "models": ["deepseek-chat", "deepseek-reasoner"],
      "transformer": { "use": ["deepseek"] }
    }
  ],
  "Router": {
    "default": "deepseek,deepseek-chat",
    "think": "deepseek,deepseek-reasoner",
    "longContext": "openrouter,google/gemini-2.5-pro-preview"
  }
}
```

3. **Start Router**:

```bash
ccr start
```

4. **Configure cc-connect** (in `config.toml`):

```toml
[projects.agent.options]
work_dir = "/path/to/project"
mode = "default"

# Router integration
router_url = "http://127.0.0.1:3456"        # Router URL (default port)
router_api_key = "your-secret-key"          # Optional: if router requires auth
```

### How It Works

When `router_url` is configured, cc-connect automatically:

- Sets `ANTHROPIC_BASE_URL` to the router URL
- Sets `NO_PROXY=127.0.0.1` to prevent proxy interference
- Disables telemetry and cost warnings for cleaner integration

All Claude Code requests are then routed through the router, which handles model selection and provider communication.

### Usage

Once configured, use cc-connect as usual. The router transparently handles model routing:

```
You: Help me refactor this code
Router → DeepSeek (default model)

You: Think through this architecture decision
Router → DeepSeek Reasoner (thinking model)

You: Analyze this large codebase
Router → Gemini Pro (long context model)
```

### Important Notes

- **Provider settings are ignored**: When using router, the `[[projects.agent.providers]]` settings are bypassed as the router manages model selection
- **Router must be running**: Ensure `ccr start` is executed before starting cc-connect
- **Configuration changes**: After modifying router config, restart with `ccr restart`

For more details, see the [Claude Code Router documentation](https://github.com/musistudio/claude-code-router).

## Voice Messages (Speech-to-Text)

Send voice messages directly — cc-connect transcribes them to text using a configurable STT provider, then forwards the text to the agent.

**Supported platforms:** Feishu, WeChat Work, Telegram, LINE, Discord, Slack

**Prerequisites:**
- An API key for OpenAI or Groq (for Whisper STT)
- `ffmpeg` installed (for audio format conversion — most platforms send AMR/OGG which Whisper doesn't accept directly)

### Configure

```toml
[speech]
enabled = true
provider = "openai"    # "openai" or "groq"
language = ""          # e.g. "zh", "en"; empty = auto-detect

[speech.openai]
api_key = "sk-xxx"     # your OpenAI API key
# base_url = ""        # custom endpoint (optional, for OpenAI-compatible APIs)
# model = "whisper-1"  # default model

# -- OR use Groq (faster and cheaper) --
# [speech.groq]
# api_key = "gsk_xxx"
# model = "whisper-large-v3-turbo"
```

### How It Works

1. User sends a voice message on any supported platform
2. cc-connect downloads the audio from the platform
3. If the format needs conversion (AMR, OGG → MP3), `ffmpeg` handles it
4. Audio is sent to the Whisper API for transcription
5. Transcribed text is shown to the user and forwarded to the agent

### Install ffmpeg

```bash
# Ubuntu / Debian
sudo apt install ffmpeg

# macOS
brew install ffmpeg

# Alpine
apk add ffmpeg
```

## Scheduled Tasks (Cron)

Create scheduled tasks that run automatically — like daily code reviews, periodic trend summaries, or weekly reports. When a cron job fires, cc-connect sends the prompt to the agent in your chat session and delivers the result back to you.

### Manage via Slash Commands

```
/cron                                          List all cron jobs
/cron add <min> <hour> <day> <mon> <wk> <prompt>   Create a cron job
/cron del <id>                                 Delete a cron job
/cron enable <id>                              Enable a job
/cron disable <id>                             Disable a job
```

Example:

```
/cron add 0 6 * * * Collect GitHub trending repos and send me a summary
```

### Manage via CLI

```bash
cc-connect cron add --cron "0 6 * * *" --prompt "Summarize GitHub trending" --desc "Daily Trending"
cc-connect cron list
cc-connect cron del <job-id>
```

### Natural Language Scheduling (via Agent)

**Claude Code** supports this out of the box — just tell it in natural language:

> "每天早上6点帮我总结 GitHub trending"
> "Every Monday at 9am, generate a weekly status report"

Claude Code will automatically translate your request into a `cc-connect cron add` command via `--append-system-prompt`.

**For other agents** (Codex, Cursor, Gemini CLI), you need to add instructions to the agent's project-level instruction file so it knows how to create cron jobs. Add the following content to the corresponding file in your project root:

| Agent | Instruction File |
|-------|-----------------|
| Codex | `AGENTS.md` |
| Cursor | `.cursorrules` |
| Qoder CLI | `AGENTS.md` (project), `~/.qoder/AGENTS.md` (global) |
| Gemini CLI | `GEMINI.md` |
| OpenCode | `OPENCODE.md` |

**Content to add:**

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

To list or delete cron jobs:
  cc-connect cron list
  cc-connect cron del <job-id>

## Send message to current chat
To proactively send a message back to the user's chat session (use --stdin heredoc for long/multi-line messages):

  cc-connect send --stdin <<'CCEOF'
  your message here (any special characters are safe)
  CCEOF

For short single-line messages:

  cc-connect send -m "short message"
```

## Daemon Mode

Run cc-connect as a background service managed by the OS init system (Linux systemd user service, macOS launchd LaunchAgent).

```bash
cc-connect daemon install --config ~/.cc-connect/config.toml   # install service
cc-connect daemon start
cc-connect daemon stop
cc-connect daemon restart
cc-connect daemon status
cc-connect daemon logs [-f] [-n N] [--log-file PATH]
cc-connect daemon uninstall
```

**Install flags:** `--log-file PATH`, `--log-max-size N` (MB), `--work-dir DIR`, `--force`. Logs auto-rotate at the size limit and keep one backup.

## Session Management

Each user gets an independent session with full conversation context. Manage sessions via slash commands:

```
/new [name]       Start a new session
/list             List all agent sessions for this project
/switch <id>      Switch to a different session
/current          Show current session info
/history [n]      Show last n messages (default 10)
/provider [...]   Manage API providers (list/add/remove/switch)
/allow <tool>     Pre-allow a tool (takes effect on next session)
/mode [name]      View or switch permission mode
/quiet            Toggle thinking/tool progress messages
/stop             Stop current execution
/help             Show available commands
```

During a session, the agent may request tool permissions. Reply **allow** / **deny** / **allow all** (auto-approve all remaining requests this session).

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

# Project 2 — Codex agent with Telegram
[[projects]]
name = "my-frontend"

[projects.agent]
type = "codex"

[projects.agent.options]
work_dir = "/path/to/frontend"
mode = "full-auto"

[[projects.platforms]]
type = "telegram"

[projects.platforms.options]
token = "xxxx"
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
│   ├── speech.go            # Speech-to-text (Whisper API + ffmpeg)
│   └── engine.go            # Routing engine + slash commands
├── platform/                # Platform adapters
│   ├── feishu/              # Feishu / Lark (WebSocket)
│   ├── dingtalk/            # DingTalk (Stream)
│   ├── telegram/            # Telegram (Long Polling)
│   ├── slack/               # Slack (Socket Mode)
│   ├── discord/             # Discord (Gateway WebSocket)
│   ├── line/                # LINE (HTTP Webhook)
│   ├── wecom/               # WeChat Work (HTTP Webhook)
│   └── qq/                  # QQ (NapCat / OneBot v11 WebSocket)
├── agent/                   # Agent adapters
│   ├── claudecode/          # Claude Code CLI (interactive sessions)
│   ├── codex/               # OpenAI Codex CLI (exec --json)
│   ├── cursor/              # Cursor Agent CLI (--print stream-json)
│   ├── qoder/               # Qoder CLI (-p -f stream-json)
│   ├── gemini/              # Gemini CLI (-p --output-format stream-json)
│   └── opencode/            # OpenCode (run --format json)
├── docs/                    # Platform setup guides
├── config.example.toml      # Config template
├── INSTALL.md               # AI-agent-friendly install guide
├── Makefile
└── README.md
```

## Community

- [Discord](https://discord.gg/kHpwgaM4kq)
- [Telegram](https://t.me/+odGNDhCjbjdmMmZl)

## License

MIT
