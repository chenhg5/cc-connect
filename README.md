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
| Platform | Feishu (Lark) | ✅ Supported |
| Platform | DingTalk | ✅ Supported |
| Platform | Slack | 🔜 Planned |
| Platform | Telegram | 🔜 Planned |

## Quick Start

### Prerequisites

- Go 1.22+
- [Claude Code CLI](https://docs.anthropic.com/en/docs/claude-code) installed and configured

### Install

```bash
git clone https://github.com/chenhg5/cc-connect.git
cd cc-connect
make build
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
```

## Execution Modes

Claude Code adapter supports two modes, controlled by the `mode` option:

| Mode | Behavior | Use Case |
|------|----------|----------|
| `interactive` (default) | Respects tool permissions. Shows tool-use details in every response. Use `allowed_tools` to grant specific tools. | Daily development — you stay in control. |
| `auto` | Auto-approves all operations (`--dangerously-skip-permissions`). | Trusted / sandboxed environments. |

```toml
[agent.options]
mode = "interactive"
# allowed_tools = ["Read", "Grep", "Glob", "Bash"]
```

In both modes, Claude Code can ask clarifying questions. The conversation continues naturally — just reply on the messaging platform.

## Session Management

Each user gets an independent session with full conversation context. You can manage multiple sessions via slash commands directly from the messaging platform:

| Command | Description |
|---------|-------------|
| `/new [name]` | Create a new session (and switch to it) |
| `/list` | List all your sessions |
| `/switch <id\|name>` | Switch to a different session |
| `/current` | Show current session info |
| `/history [n]` | Show last n messages (default 10) |
| `/help` | Show available commands |

Sessions are isolated — switching to a different session resumes a completely independent Claude Code conversation.

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
    mode = "interactive"

  [[projects.platforms]]
  type = "feishu"

    [projects.platforms.options]
    app_id     = "cli_xxxx"
    app_secret = "xxxx"

# Project 2 — different folder, different bot
[[projects]]
name = "my-frontend"

  [projects.agent]
  type = "claudecode"

    [projects.agent.options]
    work_dir = "/path/to/frontend"
    mode = "auto"

  [[projects.platforms]]
  type = "dingtalk"

    [projects.platforms.options]
    client_id     = "xxxx"
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

// Implement Name(), Start(), Reply(), Stop()
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
│   └── engine.go            # Routing engine + slash commands
├── platform/                # Platform adapters
│   ├── feishu/              # Feishu / Lark (WebSocket)
│   └── dingtalk/            # DingTalk (Stream)
├── agent/                   # Agent adapters
│   └── claudecode/          # Claude Code CLI (auto + interactive)
├── config/                  # Config loading
├── config.example.toml      # Config template
├── Makefile
└── README.md
```

## License

MIT
