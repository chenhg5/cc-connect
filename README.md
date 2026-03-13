# cc-connect

[![CI](https://github.com/chenhg5/cc-connect/actions/workflows/ci.yml/badge.svg)](https://github.com/chenhg5/cc-connect/actions/workflows/ci.yml)
[![GitHub release](https://img.shields.io/github/v/release/chenhg5/cc-connect?include_prereleases)](https://github.com/chenhg5/cc-connect/releases)
[![Discord](https://img.shields.io/discord/1478978944567869652?logo=discord&label=Discord)](https://discord.gg/kHpwgaM4kq)
[![Telegram](https://img.shields.io/badge/Telegram-Group-26A5E4?logo=telegram)](https://t.me/+odGNDhCjbjdmMmZl)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

English | [中文](./README.zh-CN.md)

**Control your local AI agents from any chat app. Anywhere, anytime.**

cc-connect bridges AI agents running on your machine to the messaging platforms you already use. Code review, research, automation, data analysis — anything an AI agent can do, now accessible from your phone, tablet, or any device with a chat app.

```
         You (Phone / Laptop / Tablet)
                    │
    ┌───────────────┼───────────────┐
    ▼               ▼               ▼
 Feishu          Slack          Telegram  ...9 platforms
    │               │               │
    └───────────────┼───────────────┘
                    ▼
              ┌────────────┐
              │ cc-connect │  ← your dev machine
              └────────────┘
              ┌─────┼─────┐
              ▼     ▼     ▼
         Claude  Gemini  Codex  ...7 agents
          Code    CLI   OpenCode / iFlow
```

### Why cc-connect?

> Time to uninstall OpenClaw — cc-connect gives you access to the most powerful AI agents available, not just one.

- **7 AI Agents** — Claude Code, Codex, Cursor Agent, Qoder CLI, Gemini CLI, OpenCode, iFlow CLI. Use whichever fits your workflow, or all of them at once.
- **9 Chat Platforms** — Feishu, DingTalk, Slack, Telegram, Discord, WeChat Work, LINE, QQ, QQ Bot (Official). Most need zero public IP.
- **Multi-Bot Relay** — Bind multiple bots in a group chat and let them communicate with each other. Ask Claude, get insights from Gemini — all in one conversation.
- **Full Control from Chat** — Switch models (`/model`), tune reasoning (`/reasoning`), change permission modes (`/mode`), manage sessions, all via slash commands.
- **Agent Memory** — Read and write agent instruction files (`/memory`) without touching the terminal.
- **Scheduled Tasks** — Set up cron jobs in natural language. "Every day at 6am, summarize GitHub trending" just works.
- **Voice & Images** — Send voice messages or screenshots; cc-connect handles STT/TTS and multimodal forwarding.
- **Multi-Project** — One process, multiple projects, each with its own agent + platform combo.

<p align="center">
  <img src="docs/images/screenshot/cc-connect-lark.JPG" alt="飞书" width="32%" />
  <img src="docs/images/screenshot/cc-connect-telegram.JPG" alt="Telegram" width="32%" />
  <img src="docs/images/screenshot/cc-connect-wechat.JPG" alt="微信" width="32%" />
</p>
<p align="center">
  <em>Left：Lark &nbsp;|&nbsp; Telegram &nbsp;|&nbsp; Right：Wechat</em>
</p>

## Quick Start

### Install & Configure via AI Agent (Recommended)

Send this to Claude Code or any AI coding agent, and it will handle the entire installation and configuration for you:

```
Please refer to https://raw.githubusercontent.com/chenhg5/cc-connect/refs/heads/main/INSTALL.md to help me install and configure cc-connect
```

### Manual Install

**Via npm:**

```bash
# Stable version
npm install -g cc-connect

# Beta version (more features, may be unstable)
npm install -g cc-connect@beta
```

**Download binary from [GitHub Releases](https://github.com/chenhg5/cc-connect/releases):**

```bash
# Linux amd64 - Stable
curl -L -o cc-connect https://github.com/chenhg5/cc-connect/releases/latest/download/cc-connect-linux-amd64
chmod +x cc-connect
sudo mv cc-connect /usr/local/bin/

# Beta version (from pre-release)
curl -L -o cc-connect https://github.com/chenhg5/cc-connect/releases/download/v1.x.x-beta/cc-connect-linux-amd64
```

**Build from source (requires Go 1.22+):**

```bash
git clone https://github.com/chenhg5/cc-connect.git
cd cc-connect
make build
```

### Configure

```bash
mkdir -p ~/.cc-connect
cp config.example.toml ~/.cc-connect/config.toml
vim ~/.cc-connect/config.toml
```

### Run

```bash
./cc-connect
```

### Upgrade

```bash
# npm
npm install -g cc-connect

# Binary self-update
cc-connect update           # Stable
cc-connect update --pre     # Beta (includes pre-releases)
```

## Support Matrix

| Component | Type | Status |
|-----------|------|--------|
| Agent | Claude Code | ✅ Supported |
| Agent | Codex (OpenAI) | ✅ Supported |
| Agent | Cursor Agent | ✅ Supported |
| Agent | Gemini CLI (Google) | ✅ Supported |
| Agent | Qoder CLI | ✅ Supported |
| Agent | OpenCode (Crush) | ✅ Supported |
| Agent | iFlow CLI | ✅ Supported |
| Agent | Goose (Block) | 🔜 Planned |
| Agent | Aider | 🔜 Planned |
| Platform | Feishu (Lark) | ✅ WebSocket — no public IP needed |
| Platform | DingTalk | ✅ Stream — no public IP needed |
| Platform | Telegram | ✅ Long Polling — no public IP needed |
| Platform | Slack | ✅ Socket Mode — no public IP needed |
| Platform | Discord | ✅ Gateway — no public IP needed |
| Platform | LINE | ✅ Webhook — public URL required |
| Platform | WeChat Work | ✅ WebSocket / Webhook |
| Platform | QQ (NapCat/OneBot) | ✅ WebSocket — Beta |
| Platform | QQ Bot (Official) | ✅ WebSocket — no public IP needed |

## Platform Setup Guides

| Platform | Guide | Connection | Public IP? |
|----------|-------|------------|------------|
| Feishu (Lark) | [docs/feishu.md](docs/feishu.md) | WebSocket | No |
| DingTalk | [docs/dingtalk.md](docs/dingtalk.md) | Stream | No |
| Telegram | [docs/telegram.md](docs/telegram.md) | Long Polling | No |
| Slack | [docs/slack.md](docs/slack.md) | Socket Mode | No |
| Discord | [docs/discord.md](docs/discord.md) | Gateway | No |
| WeChat Work | [docs/wecom.md](docs/wecom.md) | WebSocket / Webhook | No (WS) / Yes (Webhook) |
| QQ / QQ Bot | [docs/qq.md](docs/qq.md) | WebSocket | No |

## Key Features

### Session Management

```
/new [name]       Start a new session
/list             List all sessions
/switch <id>      Switch session
/current          Show current session
```

### Permission Modes

```
/mode             Show available modes
/mode yolo        # Auto-approve all tools
/mode default     # Ask for each tool
```

### Provider Management

```
/provider list              List providers
/provider switch <name>     Switch API provider at runtime
```

### Scheduled Tasks

```
/cron add 0 6 * * * Summarize GitHub trending
```

📖 **Full documentation:** [docs/usage.md](docs/usage.md)

## Documentation

- [Usage Guide](docs/usage.md) — Complete feature documentation
- [INSTALL.md](INSTALL.md) — AI-agent-friendly installation guide
- [config.example.toml](config.example.toml) — Configuration template

## Community

- [Discord](https://discord.gg/kHpwgaM4kq)
- [Telegram](https://t.me/+odGNDhCjbjdmMmZl)

## Contributors

<a href="https://github.com/chenhg5/cc-connect/graphs/contributors">
  <img src="https://contrib.rocks/image?repo=chenhg5/cc-connect" />
</a>

## Star History

<a href="https://www.star-history.com/#chenhg5/cc-connect&Date">
 <picture>
   <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/svg?repos=chenhg5/cc-connect&type=Date&theme=dark" />
   <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/svg?repos=chenhg5/cc-connect&type=Date" />
   <img alt="Star History Chart" src="https://api.star-history.com/svg?repos=chenhg5/cc-connect&type=Date" />
 </picture>
</a>

## License

MIT
