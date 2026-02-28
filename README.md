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
- **Engine** — Core router. Forwards platform messages to the agent and relays responses back.

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
# Use default config file (config.toml)
./cc-connect

# Use a custom config file
./cc-connect -config /path/to/config.toml
```

## Configuration

```toml
[agent]
type = "claudecode"

  [agent.options]
  work_dir = "/path/to/your/project"

[[platforms]]
type = "feishu"

  [platforms.options]
  app_id = "cli_xxxx"
  app_secret = "xxxx"
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
│   ├── message.go           # Unified message types
│   ├── session.go           # Session management
│   └── engine.go            # Message routing engine
├── platform/                # Platform adapters
│   ├── feishu/              # Feishu / Lark (WebSocket)
│   └── dingtalk/            # DingTalk (Stream)
├── agent/                   # Agent adapters
│   └── claudecode/          # Claude Code CLI
├── config/                  # Config loading
├── config.example.toml      # Config template
├── Makefile
└── README.md
```

## License

MIT
