# Webex Platform Adapter — Design

> Status: Approved
> Date: 2026-06-05
> Target: Contribute Cisco Webex as a native platform to cc-connect

## Goal

Add Cisco Webex as a supported messaging platform in cc-connect, implemented
as a native Go adapter compiled into the binary. Users will be able to drive a
Claude Code (or any cc-connect agent) session from Webex 1:1 messages and group
spaces, the same way Telegram/Slack/Feishu already work.

This is intended as an upstream pull request to `chenhg5/cc-connect`.

## Decisions

| Topic | Decision |
|---|---|
| Adapter type | Native Go (`platform/webex/`), compiled into binary |
| Connection | Webex Device WebSocket API (real-time, no public IP) |
| Auth | Email allowlist via `allow_from` config option |
| Message types | Text + files/images |
| Chat scope | 1:1 DMs + group spaces (bot must be @mentioned in groups) |
| Outbound format | Markdown (Webex native) |

## Why WebSocket (not polling or webhooks)

- **No public IP required** — matches Feishu/Slack/Discord in cc-connect.
- **Real-time** — events arrive instantly vs. 2-3s polling latency.
- **No external dependencies** — unlike webhooks, which need a tunnel (ngrok)
  or public endpoint (only LINE uses this in cc-connect).

Webex's own SDKs use the Device WebSocket internally. The reconnection model is
the same solved problem as Telegram's `connectLoop` (exponential backoff).

## File Structure

```
platform/webex/
├── webex.go          # Platform struct, New(), Start(), Stop(), WebSocket loop
├── webex_reply.go    # Reply(), Send(), formatting, file uploads
├── webex_test.go     # Unit tests with stubbed REST API
```

Plus:
- `cmd/cc-connect/plugin_platform_webex.go` — build-tag file (`//go:build !no_webex`)
- `webex` added to `ALL_PLATFORMS` in `Makefile`
- Config example in `config.example.toml`

### Registration

```go
func init() {
    core.RegisterPlatform("webex", New)
}
```

### Config shape

```toml
[[projects]]
name = "my-project"
platform = "webex"

[projects.platform.options]
token = "BOT_ACCESS_TOKEN"            # required — Webex bot access token
allow_from = "a@cisco.com,b@cisco.com" # required — email allowlist
```

## Platform Struct

```go
type replyContext struct {
    roomID    string
    messageID string
    personID  string
}

type Platform struct {
    token     string
    allowFrom []string // email allowlist
    deviceURL string   // WebSocket URL from device registration
    deviceID  string   // for cleanup on Stop()

    mu      sync.RWMutex
    handler core.MessageHandler
    cancel  context.CancelFunc
    selfID  string // bot's own personId (to ignore own messages)
}
```

### `New(opts)` parses
- `token` (required)
- `allow_from` (required) — comma-separated email list, parsed into `allowFrom`

### `Start(handler)`
1. Fetch bot identity (`GET /v1/people/me`) → store `selfID`.
2. Register device (`POST /v1/devices`) → get WebSocket URL.
3. Launch `go p.connectLoop(ctx)` — opens WebSocket, handles reconnection.

### `Stop()`
1. Cancel context (kills WebSocket loop).
2. Delete device (`DELETE /v1/devices/{id}`) — best-effort clean shutdown.

## Inbound Flow

### Connect loop (background goroutine)

```
connectLoop(ctx):
    loop:
        open WebSocket to deviceURL
        on success → readLoop(ctx, ws)
        on disconnect → exponential backoff (1s → 30s max), retry
        on ctx.Done → return
```

### Read loop

Webex WebSocket sends JSON events. Message events carry
`resource: "messages"`, `event: "created"`. For compliance, the event payload
includes the message **ID but not the body** — we must fetch it.

### Message processing

```
onMessageEvent(event):
    if event.actorId == selfID → skip (own message)
    GET /v1/messages/{id} → full message (text, files, roomType, personEmail)
    if personEmail not in allowFrom → skip
    if roomType == "group" and bot not in mentionedPeople → skip

    build core.Message{
        SessionKey: "webex:{roomID}:{personID}"
        Platform:   "webex"
        MessageID:  message.id
        ChannelID:  roomID
        UserID:     personEmail
        UserName:   personDisplayName
        Content:    message.text (with @mention stripped for groups)
        Images:     []  // from message.files where MIME is image/*
        Files:      []  // from message.files otherwise
        ReplyCtx:   replyContext{roomID, messageID, personID}
    }

    handler(p, &msg)
```

### File/image handling (inbound)
- Webex returns file URLs in `message.files[]`.
- Fetch each with an authenticated `GET {fileURL}`.
- Content-Type `image/*` → `ImageAttachment`; otherwise → `FileAttachment`.

### @mention detection (groups)
- Webex includes `mentionedPeople[]` on the message — check `selfID` membership.
- Strip the `<spark-mention>` tag from text before passing to the engine.

## Outbound Flow

### `Reply(ctx, replyCtx, content)`
```
POST /v1/messages
{ "roomId": roomID, "parentId": messageID, "markdown": content }
```
`parentId` threads the reply under the original message (relevant in groups).

### `Send(ctx, replyCtx, content)` — proactive (cron, relay)
```
POST /v1/messages
{ "roomId": roomID, "markdown": content }
```

Both use markdown — Webex renders it natively, mapping directly to Claude's output.

### Message splitting
- Webex caps messages at 7439 bytes.
- Split on paragraph boundaries (same approach as Telegram's chunking).
- Send chunks sequentially.

### File/image sending (optional interfaces)

Implement `ImageSender` and `FileSender` via multipart upload:
```
POST /v1/messages   (Content-Type: multipart/form-data)
- roomId
- files (binary)
```

## Optional Interfaces

Implemented in v1:
- `ImageSender`, `FileSender` — attachment send-back.
- `ReplyContextReconstructor` — rebuild replyCtx from `"webex:{roomID}:{personID}"`
  for cron support.
- `FormattingInstructionProvider` — tell the agent Webex supports markdown.
- `AsyncRecoverablePlatform` — report connected/reconnecting state to the engine.

Skipped in v1 (future PRs):
- `CardSender` (Adaptive Cards)
- `StreamingCardPlatform`
- `MessageUpdater` (Webex supports edits, not critical for v1)
- `InlineButtonSender`
- `TypingIndicator` (Webex has no native bot typing API)

## Error Handling & Reconnection

- **WebSocket drop** → exponential backoff (1s → 30s). On reconnect, re-register
  device if the URL is stale.
- **429** → retry honoring `Retry-After`.
- **401** → log and stop retrying (bad/expired token).
- **5xx / network** → retry with backoff.
- **Device cleanup** → delete on `Stop()` (best-effort); Webex auto-expires idle
  devices, so no stale-device sweep needed at startup.
- **Logging** → `slog` throughout; redact token via `core.RedactToken()`.
  Connection-state changes at Info, message events at Debug.

## Testing

### Unit tests (`webex_test.go`)
Stub the Webex REST API behind an interface (mirrors how Telegram stubs
`telegramBot`). Cover:
- Message gate: allowed/denied emails, group @mention requirement.
- Message parsing: text, files, @mention stripping.
- Reply chunking at the 7439-byte boundary.
- Reconnect backoff logic.

### Integration testing
Manual, against a real Webex bot token and test space. Not in CI (needs creds).

## Out of Scope (v1)
- Adaptive Cards / interactive buttons.
- Message editing / streaming cards.
- Voice / STT / TTS.
- Multi-org or org-wide auth (email allowlist only).
