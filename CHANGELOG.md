# Changelog

## v1.1.0-beta.5 (2026-03-01)

### New Features

- **Gemini CLI Agent Support**: Full integration with [Google Gemini CLI](https://github.com/google-gemini/gemini-cli) (`gemini -p --output-format stream-json`).
  - Headless mode with streaming JSON event parsing (`init`, `message`, `tool_use`, `tool_result`, `error`, `result`)
  - Multi-turn conversations via `--resume <session_id>`
  - 4 permission modes: `default`, `auto_edit`, `yolo`, `plan`
  - Provider switching via `GEMINI_API_KEY` env var, plus Vertex AI support via `env` map
  - Session listing from `~/.gemini/tmp/<project>/chats/*.json`
  - Image support via temp file references
  - Free tier: 60 requests/min, 1,000 requests/day with Google account

- **Cursor Agent Support**: Full integration with [Cursor Agent CLI](https://docs.cursor.com/agent) (`agent --print --output-format stream-json`).
  - Multi-turn conversations via `--resume <chatId>`
  - 4 permission modes: `default`, `force`, `plan`, `ask`
  - Session listing from Cursor's SQLite database
  - Provider switching via `CURSOR_API_KEY` env var

### Improvements

- **npm auto-update**: `run.js` now checks binary version at runtime and auto-reinstalls if outdated, ensuring `npm install -g cc-connect@beta` always gets the correct binary version
- **Updated documentation**: README, INSTALL.md, and config.example.toml updated with Gemini CLI and Cursor Agent examples

### Configuration

```toml
# Gemini CLI
[[projects]]
name = "my-gemini-project"

[projects.agent]
type = "gemini"

[projects.agent.options]
work_dir = "/path/to/project"
mode = "yolo"   # "default" | "auto_edit" | "yolo" | "plan"
# model = "gemini-2.5-flash"

# Cursor Agent
[[projects]]
name = "my-cursor-project"

[projects.agent]
type = "cursor"

[projects.agent.options]
work_dir = "/path/to/project"
mode = "force"  # "default" | "force" | "plan" | "ask"
```

---

## v1.1.0-beta.3 (2026-03-01)

### New Features

- **Voice Message Support (Speech-to-Text)**: Send voice messages on any supported platform — cc-connect transcribes them to text via configurable STT provider, then forwards to the agent.
  - Supported platforms: Feishu, WeChat Work, Telegram, LINE, Discord, Slack
  - STT providers: OpenAI Whisper, Groq Whisper, SiliconFlow SenseVoice, or any OpenAI-compatible endpoint
  - Audio format conversion via `ffmpeg` (AMR/OGG → MP3)
  - Configurable language hint for better recognition accuracy
- **Image Message Support**: Send images directly from messaging platforms to AI agents.
  - Supported platforms: Feishu, WeChat Work, Telegram, LINE, Discord, Slack
  - Images are saved locally and sent to Claude Code as multimodal content (base64 + file path fallback)
- **CLI Send Command**: `cc-connect send` allows external processes to send messages to active sessions via Unix socket API, enabling scheduled tasks (e.g., "daily summary at 6 AM")

### Bug Fixes

- **WeChat Work duplicate messages**: Added `MsgId`-based deduplication to prevent WeChat Work callback retries from causing duplicate processing
- **WeChat Work media download timeout**: Moved image/voice download to async goroutines to prevent blocking the callback handler and triggering retries

### Improvements

- **Bilingual config template**: `config.example.toml` now includes both English and Chinese comments for all settings
- **Updated README**: Added Voice Messages and Image Messages to support matrix and documentation

### Configuration Changes

```toml
# New: Speech-to-Text configuration
[speech]
enabled = true
provider = "openai"    # "openai" or "groq"
language = "zh"        # empty = auto-detect

[speech.openai]
api_key = "sk-xxx"
base_url = ""          # custom endpoint (SiliconFlow, etc.)
model = "whisper-1"
```

---

## v1.1.0-beta.2 (2026-03-01)

### Bug Fixes

- **Fix .gitignore**: The pattern `cc-connect` was inadvertently ignoring `cmd/cc-connect/` directory at any depth. Changed to `/cc-connect` to only match the binary at repo root. This caused all entrypoint source files (`main.go`, `update.go`, `provider.go`) to be untracked.
- **Auto-create config on first run**: Running `cc-connect` without any config file no longer crashes with "no such file or directory". It now auto-creates `~/.cc-connect/config.toml` with a starter template and prints a friendly setup guide.

### Improvements

- **Expanded Agent roadmap**: README now lists Crush (OpenCode), Goose, Aider as planned agents, and Kimi Code, GLM Code / CodeGeeX, MiniMax Code as exploring.

---

## v1.1.0-beta.1 (2026-02-28)

### New Features

- **Codex Agent Support**: Full integration with [OpenAI Codex CLI](https://github.com/openai/codex) (`codex exec --json`). Supports session persistence via `codex exec resume`, multi-turn conversations, and all permission modes (suggest / auto-edit / full-auto / yolo).

- **API Provider Management**: Switch between multiple API providers (Anthropic direct, relay services, AWS Bedrock, etc.) at runtime without restarting.
  - **Chat commands**: `/provider list`, `/provider add`, `/provider remove`, `/provider switch <name>`
  - **CLI commands**: `cc-connect provider add/list/remove`
  - **Import from cc-switch**: `cc-connect provider import` reads providers from [cc-switch-cli](https://github.com/SaladDay/cc-switch-cli)'s SQLite database
  - Provider credentials are injected as environment variables into agent subprocesses — no live config file modifications
  - Supports arbitrary env vars via `env` map for Bedrock, Vertex, Azure, etc.

- **Global Config Location**: Default config path changed to `~/.cc-connect/config.toml`. Search order: `-config` flag → `./config.toml` → `~/.cc-connect/config.toml`. Backward compatible with local configs.

- **Session Data in ~/.cc-connect/sessions/**: Session files moved to `~/.cc-connect/sessions/` by default (configurable via `data_dir`). Old local `.cc-connect/` files are auto-detected for backward compatibility.

- **Self-Update with Pre-release Support**:
  - `cc-connect update` — update to latest stable release
  - `cc-connect update --pre` — update to latest pre-release (for beta testers)
  - `cc-connect check-update [--pre]` — check for updates without installing

- **Language Switching**: `/lang [en|zh|auto]` command to switch bot language at runtime, with auto-detection from user messages.

- **Markdown Stripping**: Platforms that don't support Markdown (WeChat Work on mobile, LINE) now receive clean plain-text output.

### Improvements

- **Session History from Backend**: `/history` command now falls back to reading agent backend session files (Claude Code JSONL, Codex thread transcripts) when in-memory history is empty (e.g., after `/switch`).
- **Codex Session Resumption**: Restarting cc-connect preserves Codex session context via `codex exec resume <threadID>`.
- **12-char Session IDs**: `/list` now shows 12-character session ID prefixes (up from 8) to reduce collisions.
- **Feishu Read Receipt Handling**: Silently handles `im.message.message_read_v1` events instead of logging errors.
- **Upgrade Documentation**: Added upgrade instructions to INSTALL.md for npm, binary, and source users.

### Configuration Changes

```toml
# New: global config path
data_dir = "/custom/path"  # default: ~/.cc-connect

# New: provider management
[projects.agent.options]
provider = "anthropic"     # active provider name

[[projects.agent.providers]]
name = "anthropic"
api_key = "sk-ant-xxx"

[[projects.agent.providers]]
name = "relay"
api_key = "sk-xxx"
base_url = "https://api.relay-service.com"
model = "claude-sonnet-4-20250514"

[[projects.agent.providers]]
name = "bedrock"
env = { CLAUDE_CODE_USE_BEDROCK = "1", AWS_PROFILE = "bedrock" }
```

### Full Command Reference (v1.1.0)

| Command | Description |
|---------|-------------|
| `/new [name]` | Start a new session |
| `/list` | List agent sessions |
| `/switch <id>` | Resume an existing session |
| `/current` | Show current session info |
| `/history [n]` | Show last n messages |
| `/provider [list\|add\|remove\|switch]` | Manage API providers |
| `/allow <tool>` | Pre-allow a tool |
| `/mode [name]` | View/switch permission mode |
| `/lang [en\|zh\|auto]` | View/switch language |
| `/quiet` | Toggle thinking/tool progress |
| `/stop` | Stop current execution |
| `/help` | Show available commands |

---

## v1.0.1 (2026-02-27)

- Initial npm package distribution
- Gitee mirror for China downloads
- macOS Gatekeeper quarantine auto-removal
- Bug fixes

## v1.0.0 (2026-02-26)

- Initial release
- Claude Code agent support
- Platforms: Feishu, DingTalk, Telegram, Slack, Discord, LINE, WeChat Work
- Interactive permission handling
- Multi-project configuration
