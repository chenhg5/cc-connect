# Usage Guide

Complete guide to using cc-connect features.

## Table of Contents

- [Session Management](#session-management)
- [Permission Modes](#permission-modes)
- [API Provider Management](#api-provider-management)
- [Model Selection](#model-selection)
- [Work Directory Switching (`/dir`, `/cd`)](#work-directory-switching-dir-cd)
- [Feishu Setup CLI](#feishu-setup-cli)
- [Weixin (personal) Setup CLI](#weixin-personal-setup-cli)
- [Claude Code Router Integration](#claude-code-router-integration)
- [Voice Messages (STT)](#voice-messages-speech-to-text)
- [Voice Reply (TTS)](#voice-reply-text-to-speech)
- [Image and File Send-Back](#image-and-file-send-back)
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
| `/model [switch <alias>]` | List available models or switch by alias |
| `/dir [path]` | Show or switch the agent work directory |
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
| Auto | `auto` | Claude decides when to ask for permission |
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

[[projects.agent.providers.models]]
model = "claude-sonnet-4-20250514"
alias = "sonnet"

[[projects.agent.providers.models]]
model = "claude-opus-4-20250514"
alias = "opus"

[[projects.agent.providers.models]]
model = "claude-haiku-3-5-20241022"
alias = "haiku"

# MiniMax — OpenAI-compatible, 1M context
[[projects.agent.providers]]
name = "minimax"
api_key = "your-minimax-api-key"
base_url = "https://api.minimax.io/v1"
model = "MiniMax-M2.7"

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

## Model Selection

Pre-configure a list of selectable models per provider using `[[providers.models]]`. Each entry has a `model` identifier and an optional `alias` (short name shown in `/model`).

### Configure Models

```toml
[[projects.agent.providers]]
name = "openai"
api_key = "sk-xxx"

[[projects.agent.providers.models]]
model = "gpt-5.3-codex"
alias = "codex"

[[projects.agent.providers.models]]
model = "gpt-5.4"
alias = "gpt"

[[projects.agent.providers.models]]
model = "gpt-5.3-codex-spark"
alias = "spark"
```

### Chat Commands

```
/model              List available models (format: alias - model)
/model switch <alias>      Switch to the model matching the alias
/model switch <name>       Switch to the model by its full name
/model <alias>             Legacy syntax, still supported
```

When `models` is configured, `/model` shows exactly that list without making an API round-trip. When omitted, models are fetched from the provider API or fall back to a built-in list.

---

## Work Directory Switching (`/dir`, `/cd`)

Switch where the next agent session starts, directly from chat.

### Chat Commands

```
/dir                    Show current work directory and recent history
/dir <path>             Switch to a path (relative or absolute)
/dir <number>           Switch to a directory from history
/dir -                  Switch back to previous directory
/dir help               Show command usage
/cd <path>              Backward-compatible alias of /dir <path>
```

### Behavior Notes

- Directory changes apply to the next session in the current project.
- Relative paths are resolved from the current agent work directory.
- Directory history is project-scoped and can be switched by index.
- `/cd` is kept for compatibility, but `/dir` is the primary command.

Examples:

```text
/dir ../another-repo
/dir 2
/dir -
```

---

## Feishu Setup CLI

Use CLI to create or bind Feishu/Lark bot credentials and write them back to `config.toml`.

```bash
# Recommended: unified entry
cc-connect feishu setup --project my-project
cc-connect feishu setup --project my-project --app cli_xxx:sec_xxx

# Force modes (usually unnecessary)
cc-connect feishu new --project my-project
cc-connect feishu bind --project my-project --app cli_xxx:sec_xxx
```

Differences:
- `setup`: unified entry. No credentials => behaves like `new`; with `--app` => behaves like `bind`.
- `new`: force QR onboarding flow; rejects `--app`.
- `bind`: force credential binding flow; requires credentials.

Behavior:
- `setup` uses QR onboarding by default, or bind mode when `--app` is provided.
- If `--project` does not exist, it is created automatically.
- If project exists but has no `feishu/lark` platform, one is added automatically.
- The command writes credentials (`app_id`, `app_secret`); in QR onboarding flow, Feishu usually pre-configures permissions and event subscriptions.
- Still verify app publish status and availability scope in Feishu Open Platform.

---

## Weixin (personal) Setup CLI

Weixin personal chat uses the **ilink bot HTTP API** (long polling + `sendMessage`, same family as OpenClaw `openclaw-weixin`). Use the CLI to scan a QR code or bind an existing Bearer token and write `config.toml`.

**Full walkthrough (Chinese): [docs/weixin.md](./weixin.md).**

```bash
cc-connect weixin setup --project my-project
cc-connect weixin bind --project my-project --token '<token>'
cc-connect weixin new --project my-project
```

Notes:
- `setup` without `--token` runs QR login; with `--token` behaves like bind.
- Auto-creates the project and/or a `weixin` platform block when missing.
- After login, send a message from WeChat once so `context_token` is cached.
- See `cc-connect weixin help` for flags (`--api-url`, `--cdn-url`, `--route-tag`, etc.).

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

## Image and File Send-Back

When an agent generates a local image, PDF, report, bundle, or other file and needs to deliver it directly to the current chat, use attachment mode in `cc-connect send`.

**Currently supported platforms:**
- Feishu
- Telegram

### When to run setup first

If the current agent does not natively inject the system prompt, run this once in chat after upgrading:

```text
/bind setup
```

or:

```text
/cron setup
```

These two commands write the same cc-connect instructions. Either one is enough. After that, the agent knows:
- normal text replies should be returned normally
- generated attachments should be sent back with `cc-connect send --image/--file`

If you have run setup before, run it again after upgrading so the instructions are refreshed to the latest version.

### Config switch

Add this to `config.toml` if you want to disable agent-driven attachment send-back:

```toml
attachment_send = "off"
```

The default is `on`. This switch is independent from the agent's `/mode` and only affects `cc-connect send --image/--file`.

### CLI examples

```bash
cc-connect send --image /absolute/path/to/chart.png
cc-connect send --file /absolute/path/to/report.pdf
cc-connect send --file /absolute/path/to/report.pdf --image /absolute/path/to/chart.png
```

Notes:
- `--image` is for image attachments.
- `--file` is for any file attachment.
- `--message` is optional and sends a text note before the attachments.
- `--image` and `--file` can both be repeated.
- Absolute paths are recommended so the command does not depend on the agent's current working directory.
- With `attachment_send = "off"`, image/file send-back is blocked but ordinary text replies still work.

### Typical use cases

1. The agent generates a screenshot or chart and should send it directly to the user.
2. The agent generates a PDF, Markdown export, log bundle, or patch file that should be delivered as an attachment.
3. The agent wants to send a short status message together with one or more generated files.

### Important notes

- This command is for attachment delivery, not ordinary text replies.
- The files must exist on the local machine where the agent runs.
- There must be an active session; otherwise the command fails because cc-connect has no chat context to deliver to.
- Platform-specific file size and file type limits still apply.

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

Optional: `--session-mode new-per-run` starts a fresh agent session on each run (default is `reuse`, same as before). `--timeout-mins N` sets how long the scheduler waits per run (`0` = no limit; omit = 30 minutes).

### Natural Language (Claude Code)

> "Every day at 6am, summarize GitHub trending"

Claude Code auto-creates the cron job. For other agents that rely on memory files, run `/cron setup` or `/bind setup` once first; both write the same instructions.

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
type = "feishu"  # or dingtalk, telegram, slack, discord, wecom, weixin, line, qq, qqbot

[projects.platforms.options]
# platform-specific options
```
