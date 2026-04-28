# Changelog

## v1.3.3-beta.1 (2026-04-25)

Beta release with new agents, new features, and broad platform fixes. No breaking changes.

### New Features
- **Devin agent**: add Devin CLI as a first-class agent with full `/list`, `/mode`, and session management (#672)
- **`/ps` command** (replaces `/btw`): send a message to a busy session mid-turn; `/btw` kept as alias for backward compatibility (#620)
- **`!` shell shortcut**: use `!ls -la` as shorthand for `/shell ls -la`, with optional `--timeout` parameter (#658)
- **NO_REPLY suppression**: agents can return `NO_REPLY` to silently skip platform delivery, useful for cron/analysis tasks (#682)
- **Feishu shared WebSocket**: multiple projects sharing the same `app_id` now share one WebSocket connection with per-project `allow_chat` / `group_only` filtering (#613)
- **Message queue depth configurable**: new `[queue] max_depth` config option (default 5) (#690)
- **Claude Code opus[1m]**: add 1M-context Opus model option with shorthand descriptions (#660)
- **QQ Bot file send/receive**: full file attachment support with robustness checks (#685)
- **Bridge ImageSender/FileSender**: `cc-connect send --image/--file` now works through bridge protocol (#712)
- **Provider presets**: add NekoCode, VisionCoder, and AIHubMix to provider presets; add Trae CLI ACP and COCO ACP config examples (#739)

### Fixed
- **OpenCode image handling**: inbound images from WeChat/WeCom are now correctly passed to OpenCode CLI via `--file` flags (#717)
- **Slack Markdown**: convert standard Markdown to Slack mrkdwn format (bold, italic, strike, links, headings) (#680)
- **QQ Bot reconnect**: cancel stale goroutines on WebSocket reconnect to prevent race conditions (#678)
- **Gemini multiline prompt**: pass prompt via stdin to preserve newlines (#695)
- **Telegram HTML fallback**: upgrade silent HTML parse failures to Warn-level logs (#674)
- **Telegram /skills**: show Telegram-safe skill command format (#571)
- **Feishu webhook mode**: skip bot open_id fetch in webhook mode for private deployments (#696)
- **Reply footer**: suppress footer when only workdir is known (#701)
- **Web UI add-platform**: fix "project not found" error when adding a new platform to an uncreated project

### Contributors
Thanks to all contributors who made this release possible:
- @YoungShook — Devin agent integration, Telegram HTML fallback
- @Cigarrr — /ps command, NO_REPLY feature
- @vinnyxiong — Feishu shared WebSocket and allow_chat
- @happyTonakai — Shell `!` prefix and `--timeout`
- @AaronZ345 — Claude Code opus[1m] model
- @ferocknew — QQ Bot file support
- @soaringk — OpenCode image fix
- @Zx55 — Telegram /skills fix
- @zhaomoran — Feishu webhook mode fix
- @LyInfi — Reply footer suppression
- @meloalright — Trae/COCO ACP config examples

## v1.3.2 (2026-04-21)

Hotfix release: session filtering is now configurable and defaults to showing all sessions.

### Fixed
- **`/list` shows all sessions by default**: the session filter introduced in v1.3.0 (which hid sessions not created by cc-connect) was accidentally merged and caused confusion. The filter is now **off by default** — `/list`, `/switch`, and `/delete` show all agent sessions regardless of origin.

### Added
- **`filter_external_sessions` config option**: users who *do* want to hide externally-created sessions can set `filter_external_sessions = true` in `[[projects]]` to restore the old filtering behavior.
- **Comprehensive integration tests**: real-agent E2E tests for both Codex and Claude Code covering the full `/list` → `/new` → conversation → `/list` lifecycle with provider-based authentication (no env-var API keys required). Plus 9 adapter-level filter tests using real Codex/Claude Code session file fixtures.

## v1.3.1 (2026-04-20)

Patch release with critical bug fixes for session management, config preservation, and Weibo media support.

### Fixed
- **Session visibility (`/list`)**: historical Codex sessions disappeared after upgrade due to `AgentSessionID` being cleared on `/new` or provider switch without preservation. Added `PastAgentSessionIDs` tracking with legacy data migration so existing sessions remain visible.
- **Session naming (`/new xxx`)**: custom session names from `/new` were not mapped to the agent session ID for agents where the ID is established asynchronously (Codex, Qoder, Kimi, etc.). Added name mapping to all `EventResult` and `EventText` handlers across interactive, relay, and drain paths.
- **Config comment preservation**: `/provider switch`, `/model`, `/lang`, display settings, and TTS changes now use surgical text-level editing instead of full TOML re-serialization, preserving all comments, unknown fields, and formatting.
- **Codex `codex_home` path**: session listing, history, and deletion now consistently use the configured `codex_home` instead of hardcoded `~/.codex`.
- **Feishu card callback hint**: log a reminder when interactive card mode is enabled but `card.action.trigger` may not be subscribed.

### Added
- **Weibo image & file support**: send and receive images and files in Weibo DMs via base64 encoding within the WebSocket `send_message` payload. Implements `ImageSender` and `FileSender` interfaces.
- **Comprehensive session tests**: 12 new `SessionManager` unit tests covering `PastAgentSessionIDs`, legacy data migration, and version-based schema detection. 9 new `Engine` integration tests covering `/list` visibility across `/new`, provider switch, and real-world legacy data scenarios, plus end-to-end session name mapping tests for all three agent ID patterns (immediate, EventText, EventResult).
- **Config preservation tests**: 8 new tests verifying comment and field preservation for `SaveActiveProvider`, `SaveAgentModel`, `SaveProviderModel`, `SaveLanguage`, `SaveDisplayConfig`, `SaveTTSMode`, multi-project config, and global provider refs.

## v1.3.0 (2026-04-19)

First stable release of the 1.3 series. 555 commits since v1.2.1 with major new features, platform improvements, and broad community contributions.

### Highlights

- **Web Admin UI** — Full management dashboard embedded in the binary via `go:embed`. Project CRUD, session monitoring, cron editor, provider management, chat interface, and i18n (en/zh/zh-TW/ja/es). Use `cc-connect web` to open directly in the browser with auto-login.
- **Lifecycle Event Hooks** — New `[[hooks]]` config to trigger shell commands or HTTP webhooks on 7 event types: `message.received`, `message.sent`, `session.started`, `session.ended`, `cron.triggered`, `permission.requested`, `error`. Async by default, fail-open, non-blocking.
- **Skill Management** — New `/skills` page in the web UI with local skill browser (per-project, per-agent) and recommended skill presets fetched from remote.
- **Global Provider Management** — Add, edit, delete providers in the web UI; import from cc-switch config; per-agent-type provider presets with featured/star badges.

### New Features
- `cc-connect web` CLI command: auto-configure web admin, open browser with token-based login
- Feishu: auto-resolve `@name` mentions to clickable at-tags (`resolve_mentions` config)
- Feishu: multi-level reply chain recognition; done-emoji reaction after streaming
- Feishu: configurable progress display styles (compact/card)
- Claude Code: support CLI wrappers via `cli_path`; `/effort` command for reasoning effort; `auto` permission mode; `disallowed_tools` config
- Codex: runtime reply footer; preserve workspace app-server options
- Kimi CLI: new agent support
- Pi: new agent support
- Discord: preserve table formatting; proxy support; `@everyone`/`@here` broadcast
- Telegram: forum topic support; markdown table monospace rendering; command menu adaptation
- WeCom: configurable `api_base_url` for private deployments; file receiving via HTTP callback
- Weixin (ilink): personal chat platform with CDN media, QR setup, image/file/audio send
- Config: support `${ENV_VAR}` placeholders in TOML values
- Core: `/workspace init` with local directory paths; `/dir` directory history; `agent-sid` command; auto-compress context on token threshold; outgoing rate limiting
- Daemon: preserve proxy env in systemd service

### Bug Fixes
- Fix Windows cross-compilation (duplicate runas stub file)
- Fix web footer double 'v' prefix in version display
- Fix web modal overlay not covering full viewport (portal rendering)
- Fix provider preset cards: action buttons pinned to card bottom
- Fix web page content overlapping footer (global layout restructure)
- Fix Gemini image handling: save to workspace, prompt-based file references
- Fix Claude Code: unblock readLoop when child subprocesses hold stdout pipe
- Fix Codex: multiline prompt on resume; force-kill process group on stop
- Fix core: race condition during session cleanup; follow symlinked skill directories; persist agent_session_id; filter `/list` to cc-connect owned sessions
- Fix Feishu: slash commands in thread/reply context; user/chat name resolution in async goroutine
- Fix Telegram: UTF-8-safe command menu descriptions
- Fix TTS: don't send empty language_type to Qwen TTS API
- Fix config: `formatTOML` no longer strips user-set zero values
- Security: mask bridge token in `/api/v1/status`; path traversal protection for static files

### Contributors

Thanks to all contributors who made this release possible:

- [@leoliang1997](https://github.com/leoliang1997) — Feishu card rendering, auto-resolve @mentions
- [@xukp20](https://github.com/xukp20) — Provider env handling, skill discovery, Codex options
- [@boyu-zhu](https://github.com/boyu-zhu) — Telegram markdown table rendering
- [@RukawaKaede](https://github.com/RukawaKaede) — Claude Code CLI wrapper support
- [@meishaoqing](https://github.com/meishaoqing) — Feishu multi-level reply chain
- [@Zx55](https://github.com/Zx55) — Telegram command menu, symlinked skill dirs
- [@leighstillard](https://github.com/leighstillard) — Claude Code `/effort` command
- [@ht290](https://github.com/ht290) — inject_sender display name
- [@Sentixxx](https://github.com/Sentixxx) — Claude Code readLoop subprocess fix
- [@bugwz](https://github.com/bugwz) — WeCom private deployment API base URL
- [@cold2600438-lgtm](https://github.com/cold2600438-lgtm) — Kimi CLI agent
- [@MeteorSkyOne](https://github.com/MeteorSkyOne) — Discord table formatting
- [@happyTonakai](https://github.com/happyTonakai) — Feishu done-emoji reaction
- [@xxb](https://github.com/xxb) — Codex reply footer, Discord session routing
- [@q107580018](https://github.com/q107580018) — Feishu delete/model card flows
- [@Cigarrr](https://github.com/Cigarrr) — Workspace binding parsing
- [@g1f9](https://github.com/g1f9) — Local directory workspace init
- [@0xsegfaulted](https://github.com/0xsegfaulted) — agent-sid command
- [@yzlu0917](https://github.com/yzlu0917) — Env var config placeholders
- [@sidney061212-ai](https://github.com/sidney061212-ai) — Agent session ID persistence
- [@zkunzhu](https://github.com/zkunzhu) — Daemon proxy env preservation
- [@Yuri0314](https://github.com/Yuri0314) — TTS language type fix

## v1.2.2-beta.5 (2026-03-31)

Beta release with embedded web admin, Discord proxy support, multimodal fixes, and major platform improvements.

### New Features
- **Embedded Web Admin**: Web frontend is now compiled into the binary via `go:embed` — no separate `npm install` needed. Use `/web setup` to configure, or build with `no_web` tag to exclude. Binary size increases ~1MB (#356)
- **Web Admin Dashboard**: Full-featured management UI with project CRUD, session management, cron job editor, global settings, chat interface with bridge WebSocket, slash commands, and i18n (en/zh/zh-TW/ja/es) (#316)
- **Discord Proxy Support**: Discord platform now supports `proxy`, `proxy_username`, `proxy_password` options for HTTP API and WebSocket Gateway connections
- **Feishu Progress Styles**: Configurable progress display styles (compact/card) to reduce message spam
- **Claude Code Auto-Permission Mode**: New `auto` permission mode for Claude Code agent (#329)
- **WeCom File Receiving**: WeCom HTTP callback now supports receiving files and forwarding them to the agent (#330)
- **Outgoing Rate Limiting**: Per-platform outgoing message rate limiting
- **Telegram Forum Topics**: Migrated to `go-telegram/bot` library with forum topic support (#321)
- **Global Settings UI**: Expose global configurations (language, quiet, display, stream preview, rate limit, log) in the web admin

### Bug Fixes
- **Gemini Image Handling**: Save attachments to workspace directory instead of `/tmp` so Gemini CLI tools can access them; use prompt-based file references instead of unsupported `--image` flag
- **Security**: Mask bridge token in `/api/v1/status` endpoint; add path traversal protection for static file serving
- **Codex**: Fix multiline prompt preservation on resume (#341); force kill session process group on stop (#340)
- **Session Recycling**: Wait for old session to close before creating new one (#352)
- **Discord**: Harden session routing and remove implicit continue bridge (#322); execute slash commands when defer fails (#300)
- **Slack**: Pass file uploads to agent (#296)
- **Telegram**: UTF-8-safe command menu descriptions (#301)
- **WeCom**: Strip @bot mentions from inbound text (#303)
- **Daemon**: macOS launchd do not respawn on clean exit (#304)
- **Core**: Route workspace model changes through session context (#339); outgoing rate limit refinements and i18n tightening
- **Config**: `formatTOML` no longer strips user-set zero values (e.g. `quiet = false`)

### Improvements
- **CI**: Add Node.js setup for web frontend build in CI pipeline; use `no_web` tag for e2e/smoke tests
- **Tests**: Expanded coverage across agents, config, and core packages
- **Selective Compilation**: Added `no_web` build tag to exclude web assets from binary

### Contributors

Special thanks to all contributors who made this release possible:

- **cg33** — Embedded web admin, Discord proxy, Gemini fix, security hardening
- **xxb** — Discord session routing fix, codex process kill, workspace reconnect (#322, #340, #315)
- **dev-null-sec** — Codex multiline prompt fix (#341)
- **xukp20** — Workspace model routing (#339)
- **zhengbuqian** — Telegram go-telegram/bot migration and forum topics (#321)
- **huangdijia** — Claude Code auto permission mode (#329)
- **buddhism5080** — Discord file sending (#307)

## v1.2.2-beta.4 (2026-03-22)

Beta release with Weixin (ilink) personal chat support, session/continue improvements, and platform fixes.

### New Features
- **Weixin Personal (ilink)**: New platform with long-poll `getUpdates` / `sendMessage`, QR `weixin setup`, CDN decrypt for inbound media and `ImageSender`/`FileSender` outbound (#257)
- **Telegram**: Voice/audio reply support (#225) and async startup recovery
- **Discord**: `@everyone` / `@here` broadcast support (#132)
- **Cron**: Optional new session per run and per-job timeout (#236)
- **Claude Code**: `disallowed_tools` configuration option (#232)
- **Auto-Compress**: Compress context when estimated tokens exceed threshold (#231)
- **Continue / Sessions**: Fork session on `--continue` to avoid context contamination (#244); replace persisted `ContinueSession` sentinel with real agent session id; reserve CLI `--continue` bridge for real user traffic
- **Core**: `/dir` directory history; `/model` switching aligned with provider flow (#246)
- **Providers**: MiniMax M2.7 high-speed model added to example configs (#217)

### Bug Fixes
- **Weixin**: Harden send path (empty body skip, response body cap, dedup keys, multi-voice segments); treat `sendMessage` JSON `ret != 0` as failure so quota/API errors surface correctly
- **Feishu**: Always reply to the original message; dispatch message handling asynchronously (#57)
- **Codex**: Mode switch and `--json` flag position fixes (#240, #239)
- **Multi-Workspace**: Workspace command prefix missing leading slash (#135)
- **Non-Claude Agents**: Ignore `ContinueSession` sentinel where inappropriate (#244 follow-up)
- **npm / Update**: Version sync after update; pre-release version comparison normalization

### Improvements
- **Tests**: Expanded coverage across `config`, `core`, agents, and platforms
- **Logging / Errors**: Additional error logging in several code paths

### Contributors

Special thanks to all contributors who made this release possible:

- **cg33** — Weixin ilink platform, setup CLI, and CDN media (#257)
- **Shawn** — Feishu async dispatch and reply-to-original fixes (#57)
- **quabug** — Discord broadcast and non-Claude ContinueSession handling (#132, #244)
- **huluma1314** — Auto-compress when token threshold exceeded (#231)
- **Leigh Stillard** — Fork session on `--continue` (#244)
- **Deeka Wong** — Telegram audio replies and core `/model` provider flow (#225, #246)
- **q107580018** — Telegram async startup recovery
- **just4zeroq** — Codex mode and JSON flag fixes (#240)
- **术士木星** — Cron session-per-run and job timeout (#236)
- **hushicai** — Claude `disallowed_tools` (#232)
- **Octopus** — MiniMax M2.7 high-speed in examples (#217)
- **alinnb** — `/dir` directory history
- **Claude** — Continue-session bridge fixes, auto-compress/cron edge cases, Weixin send hardening and API error handling, and broad test improvements

## v1.2.2-beta.3 (2026-03-19)

Beta release with major multi-user mode, improved workspace stability, and platform enhancements.

### New Features
- **Multi-User Mode**: Per-user rate limits, role-based ACL (allow_from/admin_from), and audit logging
- **ImageSender**: Unified image sending support for 6 platforms (Feishu, Telegram, Discord, Slack, DingTalk, QQ)
- **MiniMax M2.7**: Upgraded default model from M2.5 to M2.7 for improved reasoning
- **/whoami Command**: Display user ID for allow_from/admin_from configuration
- **/btw Command**: Inject messages into busy sessions without interrupting
- **/dir Command**: Dynamic runtime work directory switching
- **Cron Muting**: Mute/unmute cron jobs with platform wrapper and UI integration
- **Interrupt Support**: Send interrupt signal to agent sessions (Ctrl+C equivalent)
- **CORS Support**: Cross-origin requests enabled for Bridge API
- **Message Queuing**: Queue messages when agent is busy instead of discarding
- **QQ Bot Markdown**: Full Markdown message support for QQ Bot

### Bug Fixes
- **Workspace Session Persistence**: Sessions now persist to disk in multi-workspace mode
- **Race Conditions**: Multiple data race fixes (adminFrom, degraded field, userRolesMu)
- **Memory Leaks**: Fixed pendingAcks leak on WeCom WebSocket disconnect, goroutine leaks
- **i18n**: Complete translation coverage for error messages
- **Relay Timeout**: Return partial text after timeout instead of error
- **QQ Bot Reconnect**: Handle nil wsConn on failed reconnect

### Improvements
- **Message Queue**: Extracted message queue handling into dedicated method
- **Cron UX**: Improved human-readable cron expressions
- **Slack**: Typing indicator, file download error handling, auth diagnostics
- **Provider Config**: `models` list for per-provider model selection via alias
- **Build**: Test infrastructure with P0/P1分层测试targets

### Contributors

Special thanks to all contributors who made this release possible:

- **sean2077** - Multi-user mode, ACL, and audit logging
- **0xsegfaulted** - Multi-workspace fixes and interrupt support
- **octo-patch** - MiniMax M2.7 upgrade
- **windli2018** - Bridge CORS support
- **jenvan** - CORS fixes

## v1.2.2-beta.2 (2026-03-16)

Beta release with significant improvements to agent stability, platform onboarding, and user experience.

### New Features
- **Feishu/Lark CLI Onboarding**: New `cc-connect feishu setup` command with QR code terminal display for quick bot configuration, supporting both new bot creation and existing bot binding
- **Pi Agent**: Added support for Pi coding agent with full session management and tool handling
- **Session TUI Browser**: New `cc-connect sessions` subcommand with terminal UI for browsing session history
- **Multi-Workspace Mode**: Channel-based workspace resolution with auto-binding by convention and interactive init flow
- **Design Documentation**: Added comprehensive design plans for multi-workspace and session resilience features
- **Slack Enhancements**: Typing indicator via emoji reactions, mrkdwn formatting guidance in system prompt
- **Session Resilience**: Automatic `--continue` on first connection, resume-failure fallback, and context usage indicators
- **Management API**: HTTP REST API endpoints for external management tools with WebSocket bridge support
- **Cron Setup Command**: `/cron setup` for easy cron job configuration with memory file integration

### Bug Fixes
- **RateLimiter Goroutine Leak**: Fixed cleanup goroutine not stopped on replacement and engine shutdown
- **DrainEvents Infinite Loop**: Fixed infinite loop when channel is closed in `drainEvents`
- **InteractiveKey Consistency**: Fixed `executeCardAction` using wrong key for `interactiveStates` lookup in multi-workspace mode
- **Workspace Command Prefix**: Fixed missing leading slash in workspace command prefix check
- **Agent Session Close**: Always close events channel on session timeout to prevent goroutine leaks
- **Pi Agent Mutex**: Move thinking field read inside mutex in `StartSession` to prevent race condition
- **Session AgentID Protection**: Protect `Session.AgentSessionID` writes with mutex to prevent data races
- **Session Routing Race**: Prevent session routing race when `/new` runs during active turn
- **Discord Duplicate Messages**: Deduplicate gateway `MessageCreate` events causing duplicate responses
- **Codex JSON Lines**: Handle large stdout JSON lines without scanner buffer overflow
- **UTF-8 Safety**: Use rune-based splitting in `splitMessage` to prevent invalid UTF-8 sequences

### Improvements
- **Gemini Display**: Enhanced tool display with diff syntax highlighting and improved Telegram markdown rendering
- **Thread Safety**: Added comprehensive thread-safe accessors for Session fields
- **Test Engine**: Thread safety improvements to test engine and fixed test assertions
- **Input Validation**: Consolidated interactive state cleanup and added input validation
- **i18n**: Updated rate limit messages to mention `/btw` command for adding context during processing

### Contributors

Special thanks to all contributors who made this release possible:

- **kevinWangSheng** - Multiple critical bug fixes (RateLimiter, drainEvents, UTF-8 safety, session routing)
- **q107580018** - Feishu CLI onboarding with QR code integration
- **sean2077** - Session TUI browser and sessions management
- **quabug** - Pi agent implementation and Discord fixes
- **AtticusZeller** - Gemini tool display and Telegram markdown enhancements
- **leighstillard** - Multi-workspace design, session resilience, and Slack improvements
- **Shawn** - Thread safety fixes and test improvements
- **zhuguanqi** - Session management and data race fixes
- **Steve-Rye** - JSON lines handling improvements
- **Xihui He** - iFlow and agent enhancements
- **Mr.QiuW** - Various platform improvements

## v1.2.2-beta.1 (2026-03-12)

Beta release with major new features and security improvements.

### New Features
- **`/usage` Command**: Add a built-in quota usage command with a generic agent usage-reporting interface; Codex now supports ChatGPT OAuth usage lookup via `~/.codex/auth.json`
- **Feishu Interactive Cards**: Beautiful card-based UI for slash commands (/help, /list, /status, etc.) with tabbed navigation and in-place updates
- **Lark Platform Support**: Added support for Lark (飞书国际版) with proper domain handling
- **Codex Reasoning Effort**: New `/reasoning` command to switch reasoning effort levels (low/medium/high)
- **Codex Model Cache Fallback**: `/model` command now falls back to local `~/.codex/models_cache.json` when API is unavailable
- **Gemini Timeout Config**: New `timeout_mins` option to configure per-turn timeout for Gemini agent
- **Batch Session Deletion**: `/delete` now supports comma lists, ranges, and mixed forms for batch deletion
- **TTS Support**: Text-to-speech with Qwen and OpenAI providers
- **Admin Privilege System**: Admin-only commands for privileged operations
- **iFlow Tool Timeout**: Configurable tool timeout and reset timer on partial completion
- **Card-based Permission Prompts**: Permission requests now use interactive cards with callback support
- **Shared Session Support**: Share sessions across all platforms with `share_session_in_channel` option

### Bug Fixes
- **Security Hardening**: Socket permissions tightened (0600), token redaction in logs, warning for open `allow_from`
- **Slack @mention Support**: Fixed AppMentionEvent handling for channel @mentions
- **Update Fallback**: Self-update now falls back to .tar.gz/.zip archive when bare binary returns 404
- **Skill Symlink**: Fixed skill directory scanning to follow symbolic links
- **QQBot Error Handling**: Added error logging for json.Unmarshal and WriteJSON calls
- **Claude Code Path**: Fixed underscore handling in findProjectDir path matching

### Improvements
- **Daemon Config Flag**: Support daemon install with config file path
- **Message Tracing**: Added message tracing and threaded replies
- **Scanner Buffer**: Optimized scanner buffer sizes for large outputs

## v1.2.1 (2026-03-09)

Patch release with bug fixes and minor enhancements.

### Bug Fixes
- **Engine: Idle Timer During Permission Wait** - Stop idle timer while waiting for user permission response to prevent session termination
- **Feishu: Nil Pointer Checks** - Add nil checks for `SenderId.OpenId` and `msg.Content` to prevent panics
- **Feishu: URL Validation** - Validate URLs before creating hyperlinks to prevent rejection of non-HTTP(S) URLs
- **Cron: Error Logging** - Log `json.Unmarshal` errors instead of silently ignoring when cron file is corrupted
- **Engine: Stale Event Prevention** - Add `drainEvents` utility to clear buffered events between turns

### New Features
- **Bind Setup Command** - `/bind setup` writes relay instructions to memory file for better bot-to-bot relay configuration

## v1.2.0 (2026-03-08)

This is the first stable release of cc-connect 1.2.0, consolidating all beta changes and adding new features.

### New Features (since beta.7)
- **Official QQ Bot Platform**: Native integration with Tencent's official QQ Bot Platform via WebSocket, supporting text, image, and document messages
- **iFlow CLI Agent**: Full support for iFlow CLI agent with interactive tool-call handling and mode switching
- **Shell Command Execution**: Custom commands can execute shell commands directly with `exec` field in config
- **Telegram Bot Menu**: Auto-register bot command menu on startup for better discoverability
- **DingTalk Reply Preprocessing**: Improved markdown content preprocessing for reply messages
- **Multi-Bot Relay Persistence**: Relay bindings now persist across restarts with improved binding messages

### Improvements
- **Quiet Mode**: `/quiet` now supports both per-session and global scope modes
- **Compression Command**: Improved `/compress` command handling and code refactoring
- **i18n**: Added new message keys and improved command formatting

### All 1.2.0 Highlights (from beta releases)
- **Bot-to-Bot Relay**: Forward messages between different messaging platforms
- **Streaming Preview**: Real-time message preview on Telegram, Discord, and Feishu
- **Typing Indicators**: Visual processing feedback on supported platforms
- **Session Search**: Search sessions by name, ID prefix, or summary
- **Custom Slash Commands**: Define reusable prompt templates
- **Agent Skills Discovery**: Auto-discover and invoke user-defined skills
- **Daemon Mode**: Run as background service with systemd/launchd support
- **Rate Limiting**: Per-session sliding-window rate limiter
- **Command Aliases**: Define shortcut aliases for commands
- **Self-Update**: In-place binary updates with auto-restart
- And many more improvements and bug fixes...

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
- **Auto-Compress**: Added optional automatic context compression when estimated token usage exceeds a configurable threshold (`[projects.auto_compress]`).
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
