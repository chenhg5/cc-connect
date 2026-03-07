# Changelog

## v1.2.0-beta.7 (2026-03-07)

### New Features
- **Multi-Bot Relay Binding**: `/bind` now supports binding multiple bots in a group chat; use `/bind <project>` to add, `/bind -<project>` to remove specific project
- **System-level Systemd**: Daemon mode now supports system-level systemd (`/etc/systemd/system/`) when running as root, useful for servers and containers
- **Config Example Command**: `cc-connect config-example` prints embedded config template for quick reference
- **Interactive Command Buttons**: `/lang`, `/model`, `/mode` commands now show interactive button menus for easy selection
- **Exec Commands**: Custom commands can execute shell commands directly with `exec` field in config
- **Configurable Idle Timeout**: Agent idle timeout can be configured via `idle_timeout_mins` in config

### Improvements
- **Daemon Error Messages**: Improved systemd detection and error messages for WSL2, containers, and SSH environments
- **Codex CLI Visibility**: Patched codex session source to make CLI output visible

### Bug Fixes
- **Streaming Preview**: Fixed stale preview messages when streaming degrades

## v1.2.0-beta.6 (2026-03-06)

### New Features
- **Bot-to-Bot Relay**: Forward messages between different messaging platforms via CLI (`cc-connect relay`) and internal API; enables cross-platform bot communication
- **Session Search**: Search sessions by name, ID prefix, or summary with `/search <keyword>` command
- **List Pagination**: `/list` now supports pagination with `--page` and `--page-size` flags for large session counts
- **Per-Platform Streaming Preview Control**: Configure streaming preview per platform via `streaming_preview` setting (Telegram, Discord, Feishu)
- **Silent Cron Mode**: Suppress cron job notification messages with `silent = true` in cron job config
- **Voice Qwen Mode**: Voice function now supports Qwen audio model for speech-to-text
- **Feishu Three-Tier Rendering**: Intelligent markdown rendering strategy — simple text uses plain messages, rich markdown uses Post, code blocks/tables use Card

### Improvements
- **Status Display**: Improved `/status` command output with better formatting and Feishu message rendering fixes
- **Self-Update**: Auto-restart after update; added Gitee mirror support for Chinese users
- **Windows Self-Update**: Full Windows support for in-place binary updates
- **Message Splitting**: Improved boundary checks for cleaner message chunking
- **Platform Startup**: Better error handling and logging during platform initialization
- **Session Switch i18n**: Added translation for session switch success message

### Bug Fixes
- **Idle Session Timeout**: Added timeout for unresponsive agent sessions to prevent hangs
- **Streaming Preview**: Removed `maxChars` check that caused premature preview termination
- **Message Deduplication**: Deduplicate messages by process start time to prevent duplicate processing

## v1.2.0-beta.5 (2026-03-06)

### New Features
- **Streaming Preview**: Real-time message preview that updates in-place as the agent streams output; supported on Telegram, Discord, and Feishu with configurable interval, min delta, and max length
- **Rate Limiting**: Per-session sliding-window rate limiter to prevent message flooding; configurable `max_messages` and `window_secs`
- **Typing Indicators**: Visual processing feedback — Telegram/Discord show native typing action, Feishu adds emoji reaction (auto-removed on completion)
- **Command Aliases**: Define shortcut aliases for commands (`[[aliases]]` in config.toml or `/alias add`); e.g. map "帮助" → "/help"
- **Banned Words Filter**: Block messages containing configured sensitive words (`banned_words` in config.toml)
- **Project-level Command Disabling**: Disable specific commands per project via `disabled_commands` config
- **Session Deletion**: Delete sessions with `/del` command
- **`/switch` Fuzzy Matching**: Switch sessions by name, ID prefix, or summary substring in addition to numeric index

### Improvements
- **Streaming Preview + Tool Messages UX**: In non-quiet mode, when thinking/tool messages are sent, the streaming preview freezes and the final response is delivered as a new message at the bottom of the chat (instead of silently updating an older message above the tool messages)
- **Telegram Markdown→HTML**: Full Markdown-to-HTML conversion with proper escaping, placeholder-based tag nesting, and automatic fallback to plain text on parse errors
- **Discord Code-Fence-Aware Splitting**: Message chunking now respects code block boundaries, closing and re-opening fences across splits
- **Feishu Dual Rendering**: Simple markdown uses Post messages (normal font), code blocks/tables use Card messages (native rendering); matches Claude-to-IM's approach
- **Feishu Permission Interaction**: Confirmed WebSocket mode incompatibility with card button callbacks; uses text-based `/perm` commands (consistent with Claude-to-IM)
- **Session Creation & Naming**: Improved session naming with last user message as summary
- **Graceful Shutdown**: Improved context handling and lock release during shutdown
- **Unit Tests**: Added ~50 new test cases covering markdown conversion, message splitting, session management, and engine logic

### Bug Fixes
- **Telegram HTML Crossed Tags**: Fixed `<b><i>...</b></i>` nesting issues by using placeholder-based formatting pipeline
- **Telegram HTML Attribute Escaping**: Fixed `"` in URLs breaking `<a href>` attributes (escape to `&quot;`)
- **Telegram Duplicate Messages**: Fixed duplicate sends caused by streaming preview optimization skipping final HTML update
- **Streaming Preview Cursor**: Removed trailing `▍` cursor from final messages
- **Feishu Message Recall**: Unified preview and final message types to Card, eliminating unnecessary delete-and-resend
- **Feishu Reaction Cleanup**: Register empty handler for `im.message.reaction.deleted_v1` to suppress error logs
- **`fmt.Sprintf` Warnings**: Remove non-constant format strings flagged by `go vet`

## v1.2.0-beta.2 (2026-03-01)

### New Features
- **`/upgrade` Command**: Check for available updates (including beta) and self-update the binary in-place; queries both GitHub and Gitee releases
- **`/restart` Command**: Restart cc-connect service from chat with post-restart success notification
- **`/config reload` Command**: Hot-reload configuration (display, providers, commands) without restarting
- **`/name` Command**: Set custom display names for sessions (e.g. `/name my-feature`, `/name 3 bugfix`); names persist across restarts and show in `/list`, `/switch`, `/status`
- **Default Quiet Mode**: Configure `quiet = true` globally or per-project in config.toml to suppress thinking/tool progress by default; users can still toggle with `/quiet`
- **Command Prefix Matching**: Type shortened commands like `/pro l` for `/provider list`, `/sw 2` for `/switch 2`; works for all commands and subcommands
- **Numeric Session Switching**: `/list` shows numbered sessions; `/switch 3` switches by number instead of copying long IDs
- **Group Chat Mention Filtering**: Feishu, Discord, and Telegram bots now only respond to @mentions in group chats instead of all messages
- **Claude Code Router Support**: Integration with Claude Code Router for enhanced routing capabilities
- **Third-party Provider Proxy**: Local reverse proxy rewrites incompatible `thinking` parameters for third-party LLM providers (e.g. SiliconFlow)

### Improvements
- **Session History for Claude Code**: `/history` now works after `/switch` by reading from agent JSONL files
- **List Summary**: `/list` now shows the most recent user message as summary instead of the first
- **Session Names in UI**: Custom session names display with 📌 prefix in `/list`, `/switch`, `/status`
- **API Server Shutdown**: Clean shutdown without "use of closed network connection" error
- **Agent Session Timeouts**: 8-second graceful shutdown timeout for all agent sessions with kill fallback
- **Feishu Rich Text**: Use Post (rich text) messages instead of Interactive Cards for normal font size

### Bug Fixes
- **DingTalk Startup**: Fix false startup failure when stream client returns nil error
- **Deadlock on /new and /switch**: Release lock before async agent session close to prevent hangs
- **Provider Command**: Correctly list providers when no active provider is set
- **Unknown Command Handling**: Show i18n-friendly warning and fall through to agent for native commands

### Security & Reliability
- **Race Condition Fixes**: `sync.Once` for channel close, mutex protection for concurrent fields, non-blocking event sends
- **Atomic File Writes**: Config, session, and cron files use temp+rename pattern
- **Message Deduplication**: Platform-level dedup for Feishu and DingTalk webhooks
- **HTTP Client Timeouts**: Shared 30s-timeout HTTP client for all outbound requests
- **Path Traversal Protection**: Validate command file paths
- **Sensitive Data Redaction**: Redact API keys and tokens in logs

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
