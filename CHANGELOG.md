# Changelog

## v1.2.0-beta.1 (2026-03-01)

### New Features
- **Custom Slash Commands**: Define reusable prompt templates as global slash commands (`[[commands]]` in config.toml or `/commands add`); supports positional parameters (`{{1}}`), rest parameters (`{{2*}}`), default values (`{{1:default}}`), and runtime add/del/list
- **Agent Skills Discovery**: Auto-discover and invoke user-defined skills from agent directories (e.g. `.claude/skills/<name>/SKILL.md`); list with `/skills`, invoke with `/<skill-name> [args]`; supports all agents (Claude Code, Cursor, Gemini, Codex, Qoder)
- **`/config` Command**: View and modify runtime configuration (e.g. `thinking_max_len`, `tool_max_len`) from chat, with persistent save to `config.toml`
- **`/doctor` Command**: Run system diagnostics covering agent authentication, platform connectivity, system resources, dependencies, and network latency; fully i18n-supported
- **Discord Slash Commands**: Register native Discord Application Commands so typing `/` shows an autocomplete menu; supports per-guild instant registration via `guild_id` config
- **Daemon Mode**: Run cc-connect as a background service (`cc-connect daemon install/start/stop/status/logs`); supports systemd (Linux) and launchd (macOS)
- **Qoder CLI Agent**: Full support for the Qoder coding agent with streaming JSON, mode switching, and model selection
- **Telegram Proxy**: Support HTTP/SOCKS5 proxy for Telegram bot API connections
- **WeChat Work Proxy Auth**: Add `proxy_username` / `proxy_password` for authenticated forward proxies
- **i18n Expansion**: Add Traditional Chinese (zh-TW), Japanese (ja), and Spanish (es) language support
- **`--stdin` Support**: Read prompt from stdin for CLI usage (`echo "hello" | cc-connect send --stdin`)

### Improvements
- **Slow Operation Monitoring**: Warn-level logs for slow platform send (>2s), agent start (>5s), agent close (>3s), agent send (>2s), and agent first event (>15s); turn completion logs now include `turn_duration`
- **`tool_max_len=0` Fix**: Remove hardcoded 200-char truncation in all agent sessions (Claude Code, Cursor, Codex, Gemini, Qoder), making the user-configurable `tool_max_len` setting authoritative
- **Cursor `/list` Improvements**: Parse binary blob structure to show accurate message counts and first user message summary

### Bug Fixes
- **Telegram proxy**: Only override `http.Transport` when proxy is actually configured
- **Discord interaction fallback**: Gracefully fallback to channel messages when interaction token expires

## v1.1.0 (2026-03-02)

### New Features
- **`/compress` Command**: Compress/compact conversation context by forwarding native commands to agents (Claude Code `/compact`, Codex `/compact`, Gemini `/compress`); keeps long sessions manageable
- **Telegram Inline Buttons**: Permission prompts on Telegram now use clickable inline keyboard buttons (Allow / Deny / Allow All) instead of requiring text replies
- **`/model` Command**: View and switch AI models at runtime; supports numbered quick-select and custom model names. Fetches available models from provider API in real-time (Anthropic, OpenAI, Google), with built-in fallback list
- **`/memory` Command**: View and edit agent memory files (CLAUDE.md, AGENTS.md, GEMINI.md) directly from chat; supports both project-level and global-level (`/memory global`)
- **`/status` Command**: Display system status including project, agent, platforms, uptime, language, permission mode, session info, and cron job count

### Improvements
- **Cron list display**: Multi-line card-style formatting with human-readable schedule translations and next execution time
- **Model switch resets session**: Switching model via `/model` now starts a fresh agent session instead of resuming the old one, preventing stale context from affecting the new model
- **Permission modes docs**: README now documents permission modes for all four agents (Claude Code, Codex, Cursor Agent, Gemini CLI)
- **Natural language scheduling docs**: INSTALL.md now explains how to enable cron job creation via natural language for non-Claude agents
- **README revamp**: Redesigned project header with architecture diagram, feature highlights, and multi-agent positioning

### Bug Fixes
- **Gemini `/list` summary**: Fixed session list showing raw JSON (`{"dummy": true}`) instead of actual user message summary
- **GitHub Issue Templates**: Added structured templates for bug reports, feature requests, and platform/agent support requests

## v1.1.0-beta.7 (2026-03-02)

(see v1.1.0 above — beta.7 changes are included in the stable release)

## v1.1.0-beta.6 (2026-02-28)

### New Features
- **QQ Platform** (Beta): Support QQ messaging via OneBot v11 / NapCat WebSocket
- **Cron Scheduling**: Schedule recurring tasks via `/cron` command or CLI (`cc-connect cron add`), with JSON persistence and agent-aware session injection
- **Feishu Emoji Reaction**: Auto-add emoji reaction (default: "OnIt") on incoming messages to confirm receipt; configurable via `reaction_emoji`
- **Display Truncation Config**: New `[display]` config section to control thinking/tool message truncation (`thinking_max_len`, `tool_max_len`); set to 0 to disable truncation
- **`/version` Command**: Check current cc-connect version from within chat

### Bug Fixes
- **Windows `/list` fix**: Claude Code sessions now discoverable on Windows despite drive letter colon in project key paths
- **CLAUDECODE env filter**: Prevent nested Claude Code session crash by filtering CLAUDECODE env var from subprocesses

### Docs
- Clarified global config path `~/.cc-connect/config.toml` in INSTALL.md
- Fixed markdown image syntax in Chinese README

## v1.1.0-beta.5 (2026-03-01)

### New Features
- **Gemini CLI Agent**: Full support for `gemini` CLI with streaming JSON, mode switching, and provider management
- **Cursor Agent**: Integration with Cursor Agent CLI (`agent`) with mode and provider support

## v1.1.0-beta.4 (2026-03-01)

### Bug Fixes
- Fixed npm install: check binary version on install, replace outdated binary instead of skipping
- Added auto-reinstall logic for outdated binaries in `run.js`

## v1.1.0-beta.3 (2026-03-01)

### New Features
- **Voice Messages (STT)**: Transcribe voice messages to text via OpenAI Whisper, Groq Whisper, or SiliconFlow SenseVoice; requires `ffmpeg`
- **Image Support**: Handle image messages across platforms with multimodal content forwarding to agents
- **CLI Send**: `cc-connect send` command and internal Unix socket API for programmatic message sending
- **Message Dedup**: Prevent duplicate processing of WeChat Work messages

## v1.1.0-beta.2 (2026-03-01)

### New Features
- **Provider Management**: `/provider` command for runtime API provider switching; CLI `cc-connect provider add/list`
- **Configurable Data Dir**: Session data stored in `~/.cc-connect/` by default (configurable via `data_dir`)
- **Markdown Stripping**: Plain text fallback for platforms that don't support markdown (e.g. WeChat)

## v1.1.0-beta.1 (2026-03-01)

### New Features
- **Codex Agent**: OpenAI Codex CLI integration
- **Self-Update**: `cc-connect update` and `cc-connect check-update` commands
- **I18n**: Auto-detect language, `/lang` command to switch between English and Chinese
- **Session Persistence**: Sessions saved to disk as JSON, restored on restart

## v1.0.1 (2026-02-28)

- Bug fixes and stability improvements

## v1.0.0 (2026-02-28)

- Initial release
- Claude Code agent support
- Platforms: Feishu, DingTalk, Telegram, Slack, Discord, LINE, WeChat Work
- Commands: `/new`, `/list`, `/switch`, `/history`, `/quiet`, `/mode`, `/allow`, `/stop`, `/help`
