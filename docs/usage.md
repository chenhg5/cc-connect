# Usage Guide

Complete guide to using cc-connect features.

## Table of Contents

- [Session Management](#session-management)
- [Permission Modes](#permission-modes)
- [API Provider Management](#api-provider-management)
- [Claude Code Router Integration](#claude-code-router-integration)
- [Voice Messages (STT)](#voice-messages-speech-to-text)
- [Voice Reply (TTS)](#voice-reply-text-to-speech)
- [Scheduled Tasks (Cron)](#scheduled-tasks-cron)
- [Multi-Bot Relay](#multi-bot-relay)
- [Daemon Mode](#daemon-mode)
- [Multi-Workspace Mode](#multi-workspace-mode)
- [Configuration Reference](#configuration-reference)

---

## Session Management

Each user gets an independent session with full conversation context. Manage sessions via slash commands:

| Command | Description |
|---------|-------------|
| `/new [name]` | Start a new session |
| `/list` | List all agent sessions for this project |
| `/switch <id>` | Switch to a different session |
| `/current` | Show current session info |
| `/history [n]` | Show last n messages (default 10) |
| `/usage` | Show account/model quota usage (if supported) |
| `/provider [...]` | Manage API providers |
| `/allow <tool>` | Pre-allow a tool (next session) |
| `/reasoning [level]` | View or switch reasoning effort (Codex) |
| `/mode [name]` | View or switch permission mode |
| `/quiet` | Toggle thinking/tool progress messages |
| `/stop` | Stop current execution |
| `/help` | Show available commands |

During a session, the agent may request tool permissions. Reply **allow** / **deny** / **allow all**.

---

## Permission Modes

All agents support permission modes switchable at runtime via `/mode`.

### Claude Code Modes

| Mode | Config Value | Behavior |
|------|-------------|----------|
| Default | `default` | Every tool call requires approval |
| Accept Edits | `acceptEdits` / `edit` | File edits auto-approved |
| Plan Mode | `plan` | Claude only plans, no execution |
| YOLO | `bypassPermissions` / `yolo` | All tools auto-approved |

### Codex Modes

| Mode | Config Value | Behavior |
|------|-------------|----------|
| Suggest | `suggest` | Only trusted commands run without approval |
| Auto Edit | `auto-edit` | Model decides when to ask |
| Full Auto | `full-auto` | Auto-approve with sandbox |
| YOLO | `yolo` | Bypass all approvals and sandbox |

### Cursor Agent Modes

| Mode | Config Value | Behavior |
|------|-------------|----------|
| Default | `default` | Trust workspace, ask before tools |
| Force (YOLO) | `force` / `yolo` | Auto-approve all |
| Plan | `plan` | Read-only analysis |
| Ask | `ask` | Q&A style, read-only |

### Gemini CLI Modes

| Mode | Config Value | Behavior |
|------|-------------|----------|
| Default | `default` | Prompt for approval |
| Auto Edit | `auto_edit` / `edit` | Auto-approve edits |
| YOLO | `yolo` | Auto-approve all |
| Plan | `plan` | Read-only plan mode |

### Qoder CLI / OpenCode / iFlow CLI

| Mode | Config Value | Behavior |
|------|-------------|----------|
| Default | `default` | Standard permissions |
| YOLO | `yolo` | Skip all checks |

### Configuration

```toml
[projects.agent.options]
mode = "default"
# allowed_tools = ["Read", "Grep", "Glob"]
```

Switch at runtime:
```
/mode          # show current and available modes
/mode yolo     # switch to YOLO mode
/mode default  # switch back
```

---

## API Provider Management

Switch between API providers at runtime without restart.

### Configure Providers

```toml
[projects.agent.options]
work_dir = "/path/to/project"
provider = "anthropic"   # active provider

[[projects.agent.providers]]
name = "anthropic"
api_key = "sk-ant-xxx"

[[projects.agent.providers]]
name = "relay"
api_key = "sk-xxx"
base_url = "https://api.relay-service.com"
model = "claude-sonnet-4-20250514"

# For Bedrock, Vertex, etc.
[[projects.agent.providers]]
name = "bedrock"
env = { CLAUDE_CODE_USE_BEDROCK = "1", AWS_PROFILE = "bedrock" }
```

### CLI Commands

```bash
cc-connect provider add --project my-backend --name relay --api-key sk-xxx --base-url https://api.relay.com
cc-connect provider list --project my-backend
cc-connect provider remove --project my-backend --name relay
cc-connect provider import --project my-backend  # from cc-switch
```

### Chat Commands

```
/provider                   Show current provider
/provider list              List all providers
/provider add <name> <key> [url] [model]
/provider remove <name>
/provider switch <name>
/provider <name>            Shortcut for switch
```

### Env Var Mapping

| Agent | api_key → | base_url → |
|-------|-----------|------------|
| Claude Code | `ANTHROPIC_API_KEY` | `ANTHROPIC_BASE_URL` |
| Codex | `OPENAI_API_KEY` | `OPENAI_BASE_URL` |
| Gemini CLI | `GEMINI_API_KEY` | use `env` map |
| OpenCode | `ANTHROPIC_API_KEY` | use `env` map |
| iFlow CLI | `IFLOW_API_KEY` | `IFLOW_BASE_URL` |

---

## Claude Code Router Integration

[Claude Code Router](https://github.com/musistudio/claude-code-router) routes requests to different model providers.

### Setup

1. Install: `npm install -g @musistudio/claude-code-router`

2. Configure `~/.claude-code-router/config.json`:
```json
{
  "APIKEY": "your-secret-key",
  "Providers": [
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
    "think": "deepseek,deepseek-reasoner"
  }
}
```

3. Start: `ccr start`

4. Configure cc-connect:
```toml
[projects.agent.options]
router_url = "http://127.0.0.1:3456"
router_api_key = "your-secret-key"  # optional
```

---

## Voice Messages (Speech-to-Text)

Send voice messages — cc-connect transcribes them automatically.

**Supported:** Feishu, WeChat Work, Telegram, LINE, Discord, Slack

**Requirements:** OpenAI/Groq API key, `ffmpeg`

### Configure

```toml
[speech]
enabled = true
provider = "openai"    # or "groq"
language = ""          # "zh", "en", or auto-detect

[speech.openai]
api_key = "sk-xxx"
# base_url = ""
# model = "whisper-1"

# [speech.groq]
# api_key = "gsk_xxx"
# model = "whisper-large-v3-turbo"
```

### Install ffmpeg

```bash
# Ubuntu/Debian
sudo apt install ffmpeg

# macOS
brew install ffmpeg
```

---

## Voice Reply (Text-to-Speech)

Synthesize AI replies into voice messages.

**Supported:** Feishu (Lark)

### Configure

```toml
[tts]
enabled = true
provider = "qwen"        # or "openai"
voice = "Cherry"
tts_mode = "voice_only"  # "voice_only" | "always"
max_text_len = 0         # 0 = no limit

[tts.qwen]
api_key = "sk-xxx"
# model = "qwen3-tts-flash"
```

### TTS Modes

| Mode | Behavior |
|------|----------|
| `voice_only` | Reply with voice only when user sends voice |
| `always` | Always send voice reply |

Switch: `/tts always` or `/tts voice_only`

---

## Scheduled Tasks (Cron)

Create scheduled tasks that run automatically.

### Chat Commands

```
/cron                                          List all jobs
/cron add <min> <hour> <day> <mon> <wk> <prompt>   Create job
/cron del <id>                                 Delete job
/cron enable <id>                              Enable job
/cron disable <id>                             Disable job
```

Example:
```
/cron add 0 6 * * * Summarize GitHub trending repos
```

### CLI Commands

```bash
cc-connect cron add --cron "0 6 * * *" --prompt "Summarize GitHub trending" --desc "Daily Trending"
cc-connect cron list
cc-connect cron del <job-id>
```

### Natural Language (Claude Code)

> "Every day at 6am, summarize GitHub trending"

Claude Code auto-creates the cron job.

---

## Multi-Bot Relay

Cross-platform bot communication in group chats.

### Group Chat Binding

```
/bind              Show bindings
/bind claudecode   Add claudecode project
/bind gemini       Add gemini project
/bind -claudecode  Remove claudecode
```

### Bot-to-Bot Communication

```bash
cc-connect relay send --to gemini "What do you think about this architecture?"
```

---

## Daemon Mode

Run as background service.

```bash
cc-connect daemon install --config ~/.cc-connect/config.toml
cc-connect daemon start
cc-connect daemon stop
cc-connect daemon restart
cc-connect daemon status
cc-connect daemon logs [-f]
cc-connect daemon uninstall
```

---

## Multi-Workspace Mode

One bot serving multiple workspaces per channel.

### Configure

```toml
[[projects]]
name = "my-project"
mode = "multi-workspace"
base_dir = "~/workspaces"

[projects.agent]
type = "claudecode"
```

### Commands

```
/workspace                    Show current binding
/workspace bind <name>        Bind local folder
/workspace init <git-url>     Clone and bind repo
/workspace unbind             Remove binding
/workspace list               List all bindings
```

### How It Works

- Channel name `#project-a` → auto-binds to `base_dir/project-a/`
- Each channel has isolated sessions and agent state

---

## Configuration Reference

See [config.example.toml](../config.example.toml) for full examples.

### Project Structure

```toml
[[projects]]
name = "my-project"

[projects.agent]
type = "claudecode"  # or codex, cursor, gemini, qoder, opencode, iflow

[projects.agent.options]
work_dir = "/path/to/project"
mode = "default"
provider = "anthropic"

[[projects.platforms]]
type = "feishu"  # or dingtalk, telegram, slack, discord, wecom, line, qq, qqbot

[projects.platforms.options]
# platform-specific options
```
