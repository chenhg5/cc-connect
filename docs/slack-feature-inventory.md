# Slack Platform Feature Inventory

## What Existed Before Our Work (on main)

The Slack platform was added in commit `eaec71f` with basic functionality:

- **Message handling**: Direct messages only (`*slackevents.MessageEvent`)
- **File attachments**: Image and audio download via `downloadSlackFile()`
- **Threading**: Reply contexts capture channel + timestamp for threaded replies
- **Socket Mode**: Connection via `app_token` + `bot_token`
- **Session keys**: Format `slack:channel:user`
- **Methods**: `New()`, `Start()`, `Reply()`, `Send()`, `Stop()`, `ReconstructReplyCtx()`

## What We Added (feat/multi-workspace branch)

### 1. App Mention Support
- Handle `@bot` mentions in channels (`AppMentionEvent`)
- `stripAppMentionText()` helper to extract clean message text
- Commits: `ef37f6f`, `abc46cf`, `33cf135`

### 2. Slash Command Support
- Handle `socketmode.EventTypeSlashCommand` events
- Converts Slack `/command` to engine command format
- Enables native `/btw`, `/new`, `/stop`, etc. from Slack
- Commits: `81c6aec`, `2bd8518`

### 3. Multi-Workspace / Shared Sessions
- `share_session_in_channel` config option
- Session key can be channel-only (`slack:channel`) or user-scoped (`slack:channel:user`)
- `ResolveChannelName()` via `ChannelNameResolver` interface
- Channel name caching with `sync.RWMutex`
- Commits: `647398f`, `62def03`

### 4. Typing Indicator (Emoji Reactions)
- `StartTyping()` adds progressive emoji reactions to user's message
- Timeline: immediately eyes, after 2min clock, then every 5min random emoji
- All reactions cleaned up when agent completes
- Commit: `231883c`

### 5. Security
- `allow_from` config option with `core.CheckAllowFrom()` validation
- Token redaction in error messages
- Old message filtering via `core.IsOldMessage()`
- Commits: `ae13e23`, `90f0e22`

### 6. Slack mrkdwn Formatting
- System prompt instructs agent to use Slack's mrkdwn format (not standard Markdown)
- `*bold*` instead of `**bold**`, no `## headings`, etc.
- Commit: `b4a1144`

## Uncommitted Work (stashed)

### Context % Fix
- SDK reports garbage `input_tokens` (single digits like 3, 22)
- When SDK tokens < 100, falls back to agent's self-reported `[ctx: ~XX%]`
- Previously: self-reported value was always stripped and replaced with broken SDK value

### --continue on First Connection (hasConnectedOnce)
- On first session creation after engine startup, always uses `--continue`
- Picks up most recent CLI session regardless of what's stored in session manager
- Bridges direct CLI usage and cc-connect sessions
- `hasConnectedOnce` atomic.Bool prevents subsequent connections from using --continue

## Current Configuration Options

| Option | Required | Description |
|--------|----------|-------------|
| `bot_token` | Yes | Slack bot OAuth token |
| `app_token` | Yes | Slack app-level token for Socket Mode |
| `allow_from` | No | User allowlist |
| `share_session_in_channel` | No | Share session across all users in channel |

## Architecture Compliance

All Slack-specific code lives in `platform/slack/`. Core uses interface-based capability checks:
- `ChannelNameResolver` for channel name lookup
- `StartTyping()` via `TypingIndicator` interface (if implemented)
- No hardcoded "slack" references in core/
