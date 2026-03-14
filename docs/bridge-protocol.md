# Bridge Platform Protocol Specification

> Version: 1.0-draft  
> Status: Draft — subject to change before implementation

## Overview

The Bridge Protocol allows **external platform adapters** written in any programming language to connect to cc-connect at runtime via WebSocket. This eliminates the requirement to write Go code and recompile the binary for every new platform integration.

### Architecture

```
┌──────────────────────────────────────────────────────┐
│                    cc-connect                        │
│                                                      │
│   ┌────────────┐ ┌────────────┐ ┌────────────────┐  │
│   │  Telegram   │ │   Feishu   │ │ BridgePlatform │  │
│   │  (native)   │ │  (native)  │ │  (WebSocket)   │  │
│   └─────┬──────┘ └─────┬──────┘ └───────┬────────┘  │
│         │              │                │            │
│         └──────────────┴────────────────┘            │
│                        │                             │
│                  ┌─────┴─────┐                       │
│                  │   Engine   │                       │
│                  └───────────┘                       │
└──────────────────────────────────────────────────────┘
                         │ WebSocket
              ┌──────────┴───────────┐
              │                      │
   ┌──────────┴──────┐  ┌───────────┴─────┐
   │  Python Adapter  │  │ Node.js Adapter  │
   │ (WeChat, Line…)  │  │ (Custom Chat…)   │
   └─────────────────┘  └─────────────────┘
```

The `BridgePlatform` is a built-in platform inside cc-connect that:

1. Exposes a WebSocket endpoint for external adapters to connect.
2. Translates WebSocket messages into `core.Platform` interface calls.
3. Routes engine replies back to the adapter over the same WebSocket connection.

---

## Connection

### Endpoint

```
ws://<host>:<port>/bridge/ws
```

The port and path are configured in `config.toml`:

```toml
[bridge]
enabled = true
port = 9810
path = "/bridge/ws"       # optional, default "/bridge/ws"
token = "your-secret"     # required for authentication
```

### Authentication

The adapter must authenticate on connection using one of:

| Method | Example |
|--------|---------|
| Query parameter | `ws://host:9810/bridge/ws?token=your-secret` |
| Header | `Authorization: Bearer your-secret` |
| Header | `X-Bridge-Token: your-secret` |

Unauthenticated connections are rejected with HTTP 401.

### Connection Lifecycle

```
Adapter                          cc-connect
  │                                  │
  │──── WebSocket Connect ──────────→│  (with token)
  │                                  │
  │──── register ──────────────────→│  (declare platform name & capabilities)
  │←─── register_ack ──────────────│  (confirm or reject)
  │                                  │
  │←──→ message / reply exchange ──→│  (bidirectional)
  │                                  │
  │──── ping ──────────────────────→│  (keepalive, every 30s recommended)
  │←─── pong ──────────────────────│
  │                                  │
  │──── close ─────────────────────→│  (graceful disconnect)
```

---

## Message Protocol

All messages are JSON objects with a required `type` field. The protocol uses newline-delimited JSON over WebSocket text frames (one JSON object per frame).

### Adapter → cc-connect

#### `register`

Must be the first message after connection. Declares the adapter identity and capabilities.

```json
{
  "type": "register",
  "platform": "wechat",
  "capabilities": ["text", "image", "file", "audio", "card", "buttons", "typing", "update_message", "preview"],
  "metadata": {
    "version": "1.0.0",
    "description": "WeChat Official Account adapter"
  }
}
```

**Fields:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `type` | string | yes | `"register"` |
| `platform` | string | yes | Unique platform name (lowercase, alphanumeric + hyphens). Used in session keys. |
| `capabilities` | string[] | yes | List of supported capabilities (see [Capabilities](#capabilities)). |
| `metadata` | object | no | Free-form metadata for logging/debugging. |

#### `message`

Delivers an incoming user message to the engine.

```json
{
  "type": "message",
  "msg_id": "msg-001",
  "session_key": "wechat:user123:user123",
  "user_id": "user123",
  "user_name": "Alice",
  "content": "Hello, what can you do?",
  "reply_ctx": "conv-abc-123",
  "images": [],
  "files": [],
  "audio": null
}
```

**Fields:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `type` | string | yes | `"message"` |
| `msg_id` | string | yes | Platform-specific message ID for tracing. |
| `session_key` | string | yes | Unique session identifier. Format: `{platform}:{scope}:{user}`. The adapter defines how to compose this. |
| `user_id` | string | yes | User identifier on the platform. |
| `user_name` | string | no | Display name. |
| `content` | string | yes | Text content. |
| `reply_ctx` | string | yes | Opaque context string the adapter needs to route replies back. cc-connect echoes this in every reply. |
| `images` | Image[] | no | Attached images (see [Image Object](#image-object)). |
| `files` | File[] | no | Attached files (see [File Object](#file-object)). |
| `audio` | Audio | no | Voice message (see [Audio Object](#audio-object)). |

#### `card_action`

User clicked a button or selected an option on a card.

```json
{
  "type": "card_action",
  "session_key": "wechat:user123:user123",
  "action": "cmd:/new",
  "reply_ctx": "conv-abc-123"
}
```

**Fields:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `type` | string | yes | `"card_action"` |
| `session_key` | string | yes | Session that triggered the action. |
| `action` | string | yes | The callback value from the button (e.g., `"cmd:/new"`, `"nav:/model"`, `"act:/heartbeat pause"`). |
| `reply_ctx` | string | yes | Reply context for routing the response. |

#### `preview_ack`

Acknowledges a preview start and returns a handle for subsequent updates.

```json
{
  "type": "preview_ack",
  "ref_id": "preview-req-001",
  "preview_handle": "platform-msg-id-789"
}
```

#### `ping`

Keepalive. cc-connect responds with `pong`.

```json
{
  "type": "ping",
  "ts": 1710000000000
}
```

---

### cc-connect → Adapter

#### `register_ack`

Confirms or rejects registration.

```json
{
  "type": "register_ack",
  "ok": true,
  "error": ""
}
```

#### `reply`

A complete reply message to send to the user.

```json
{
  "type": "reply",
  "session_key": "wechat:user123:user123",
  "reply_ctx": "conv-abc-123",
  "content": "I can help you with coding tasks!",
  "format": "text"
}
```

**Fields:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `type` | string | yes | `"reply"` |
| `session_key` | string | yes | Target session. |
| `reply_ctx` | string | yes | Echoed from the original message. |
| `content` | string | yes | Reply text content. |
| `format` | string | no | `"text"` (default) or `"markdown"`. |

#### `reply_stream`

Streaming delta for real-time typing preview. Only sent if the adapter declared `"preview"` capability.

```json
{
  "type": "reply_stream",
  "session_key": "wechat:user123:user123",
  "reply_ctx": "conv-abc-123",
  "delta": "partial content...",
  "full_text": "accumulated full text so far...",
  "preview_handle": "platform-msg-id-789",
  "done": false
}
```

| Field | Type | Description |
|-------|------|-------------|
| `delta` | string | New text since last stream message. |
| `full_text` | string | Full accumulated text. Adapters can use this for "replace entire message" updates. |
| `preview_handle` | string | Handle returned by `preview_ack`. Empty on first stream message. |
| `done` | bool | `true` on the final stream message. |

#### `preview_start`

Requests the adapter to create an initial preview message (for streaming).

```json
{
  "type": "preview_start",
  "ref_id": "preview-req-001",
  "session_key": "wechat:user123:user123",
  "reply_ctx": "conv-abc-123",
  "content": "Thinking..."
}
```

The adapter should send the message and respond with `preview_ack` containing the platform message ID.

#### `update_message`

Requests the adapter to edit an existing message in-place. Used for streaming preview updates.

```json
{
  "type": "update_message",
  "session_key": "wechat:user123:user123",
  "preview_handle": "platform-msg-id-789",
  "content": "Updated text content..."
}
```

#### `delete_message`

Requests the adapter to delete a message (e.g., cleaning up preview messages).

```json
{
  "type": "delete_message",
  "session_key": "wechat:user123:user123",
  "preview_handle": "platform-msg-id-789"
}
```

#### `card`

Send a structured card to the user. Only sent if the adapter declared `"card"` capability; otherwise cc-connect falls back to `reply` with `card.RenderText()`.

```json
{
  "type": "card",
  "session_key": "wechat:user123:user123",
  "reply_ctx": "conv-abc-123",
  "card": {
    "header": {
      "title": "Model Selection",
      "color": "blue"
    },
    "elements": [
      {
        "type": "markdown",
        "content": "Choose a model:"
      },
      {
        "type": "actions",
        "buttons": [
          {"text": "GPT-4", "btn_type": "primary", "value": "cmd:/model gpt-4"},
          {"text": "Claude", "btn_type": "default", "value": "cmd:/model claude"}
        ],
        "layout": "row"
      },
      {
        "type": "divider"
      },
      {
        "type": "note",
        "text": "Current: gpt-4"
      }
    ]
  }
}
```

See [Card Schema](#card-schema) for the full card element reference.

#### `buttons`

Send a message with inline buttons. Only sent if the adapter declared `"buttons"` capability.

```json
{
  "type": "buttons",
  "session_key": "wechat:user123:user123",
  "reply_ctx": "conv-abc-123",
  "content": "Allow tool execution: bash(rm -rf /tmp/old)?",
  "buttons": [
    [
      {"text": "✅ Allow", "data": "perm:req-123:allow"},
      {"text": "❌ Deny", "data": "perm:req-123:deny"}
    ]
  ]
}
```

`buttons` is a 2D array: each inner array is one row.

#### `typing_start`

Requests the adapter to show a typing indicator.

```json
{
  "type": "typing_start",
  "session_key": "wechat:user123:user123",
  "reply_ctx": "conv-abc-123"
}
```

#### `typing_stop`

Requests the adapter to hide the typing indicator.

```json
{
  "type": "typing_stop",
  "session_key": "wechat:user123:user123",
  "reply_ctx": "conv-abc-123"
}
```

#### `audio`

Send a voice/audio message. Only sent if the adapter declared `"audio"` capability.

```json
{
  "type": "audio",
  "session_key": "wechat:user123:user123",
  "reply_ctx": "conv-abc-123",
  "data": "<base64-encoded-audio>",
  "format": "mp3"
}
```

#### `pong`

Response to `ping`.

```json
{
  "type": "pong",
  "ts": 1710000000000
}
```

#### `error`

Notify the adapter of a server-side error.

```json
{
  "type": "error",
  "code": "session_not_found",
  "message": "No active session for the given key"
}
```

---

## Data Schemas

### Capabilities

| Capability | Description | Enables |
|------------|-------------|---------|
| `text` | Basic text messaging (required) | `message`, `reply` |
| `image` | Receiving images from users | `message.images` |
| `file` | Receiving files from users | `message.files` |
| `audio` | Sending/receiving voice messages | `message.audio`, `audio` reply |
| `card` | Structured rich card rendering | `card` reply |
| `buttons` | Inline clickable buttons | `buttons` reply, `card_action` |
| `typing` | Typing indicator | `typing_start`, `typing_stop` |
| `update_message` | Edit existing messages | `update_message` |
| `preview` | Streaming preview (requires `update_message`) | `preview_start`, `reply_stream` |
| `delete_message` | Delete messages | `delete_message` |
| `reconstruct_reply` | Can reconstruct reply context from session_key | Enables cron/heartbeat messages |

If a capability is not declared, cc-connect will automatically degrade:
- No `card` → cards are rendered as plain text via `RenderText()`.
- No `buttons` → buttons are omitted or rendered as text hints.
- No `preview` → streaming is disabled; only the final reply is sent.
- No `typing` → typing indicators are skipped.

### Image Object

```json
{
  "mime_type": "image/png",
  "data": "<base64-encoded>",
  "file_name": "screenshot.png"
}
```

### File Object

```json
{
  "mime_type": "application/pdf",
  "data": "<base64-encoded>",
  "file_name": "report.pdf"
}
```

### Audio Object

```json
{
  "mime_type": "audio/ogg",
  "data": "<base64-encoded>",
  "format": "ogg",
  "duration": 5
}
```

### Card Schema

A card consists of an optional header and a list of elements:

```json
{
  "header": {
    "title": "Card Title",
    "color": "blue"
  },
  "elements": [ ... ]
}
```

**Supported colors:** `blue`, `green`, `red`, `orange`, `purple`, `grey`, `turquoise`, `violet`, `indigo`, `wathet`, `yellow`, `carmine`.

#### Element Types

**Markdown**
```json
{"type": "markdown", "content": "**Bold** and _italic_"}
```

**Divider**
```json
{"type": "divider"}
```

**Actions (Button Row)**
```json
{
  "type": "actions",
  "buttons": [
    {"text": "Click Me", "btn_type": "primary", "value": "cmd:/do-something"}
  ],
  "layout": "row"
}
```

`btn_type`: `"primary"`, `"default"`, `"danger"`.  
`layout`: `"row"` (default), `"equal_columns"`.

**List Item (Description + Button)**
```json
{
  "type": "list_item",
  "text": "GPT-4 — Most capable model",
  "btn_text": "Select",
  "btn_type": "primary",
  "btn_value": "cmd:/model gpt-4"
}
```

**Select (Dropdown)**
```json
{
  "type": "select",
  "placeholder": "Choose a model",
  "options": [
    {"text": "GPT-4", "value": "cmd:/model gpt-4"},
    {"text": "Claude", "value": "cmd:/model claude"}
  ],
  "init_value": "cmd:/model gpt-4"
}
```

**Note (Footnote)**
```json
{
  "type": "note",
  "text": "Tip: use /help to see all commands",
  "tag": "optional-machine-tag"
}
```

---

## Session Key Format

Session keys follow the pattern:

```
{platform}:{scope}:{user_id}
```

- **platform**: The `platform` name from registration (e.g., `wechat`).
- **scope**: A grouping scope — could be a group/channel ID, or the same as `user_id` for 1-on-1 chats.
- **user_id**: The unique user identifier.

Examples:
- `wechat:user123:user123` — personal DM
- `wechat:group456:user123` — user in a group chat
- `matrix:room789:alice` — Matrix room

The adapter is responsible for constructing consistent session keys.

---

## Error Handling

### Reconnection

If the WebSocket connection drops, the adapter should:

1. Wait with exponential backoff (starting at 1s, max 60s).
2. Reconnect and send a new `register` message.
3. Resume normal operation — cc-connect maintains session state independently of the connection.

### Message Ordering

Messages within a single WebSocket connection are ordered. cc-connect processes adapter messages sequentially per session key.

### Timeouts

- **Ping interval**: Adapters should send `ping` at least every 30 seconds.
- **Connection timeout**: cc-connect closes idle connections after 90 seconds without a ping.
- **Reply timeout**: If an agent takes too long, cc-connect may send an error reply. The adapter does not need to handle this specially.

---

## Configuration Example

```toml
[bridge]
enabled = true
port = 9810
token = "a-strong-random-secret"

# Optional: restrict which adapters can connect (by platform name).
# Default: allow all registered adapters.
# allow_platforms = ["wechat", "matrix"]
```

No per-adapter project configuration is needed — adapters are associated with the **default project** or specify a `project` field in the `register` message to bind to a specific project.

---

## SDK Guidelines

When building an adapter, follow these guidelines:

1. **Keep it stateless** — the adapter should be a thin translation layer. All session state lives in cc-connect.
2. **Handle reconnection** — network failures are normal. Implement exponential backoff.
3. **Declare capabilities honestly** — only declare capabilities your platform actually supports.
4. **Use `reply_ctx` faithfully** — always echo back the `reply_ctx` from the original message.
5. **Base64 for binary data** — images, files, and audio are transferred as base64-encoded strings.
6. **Log errors, don't crash** — if you receive an unknown message type, log it and continue.

### Minimal Adapter Example (Python pseudocode)

```python
import asyncio
import json
import websockets

async def main():
    uri = "ws://localhost:9810/bridge/ws?token=your-secret"
    async with websockets.connect(uri) as ws:
        # 1. Register
        await ws.send(json.dumps({
            "type": "register",
            "platform": "my-chat",
            "capabilities": ["text", "buttons"]
        }))
        ack = json.loads(await ws.recv())
        assert ack["ok"], f"Registration failed: {ack['error']}"

        # 2. Start message loop
        async def recv_loop():
            async for raw in ws:
                msg = json.loads(raw)
                if msg["type"] == "reply":
                    send_to_chat_platform(msg["reply_ctx"], msg["content"])
                elif msg["type"] == "buttons":
                    send_buttons_to_chat(msg["reply_ctx"], msg["content"], msg["buttons"])
                # ... handle other types

        async def send_loop():
            while True:
                chat_msg = await get_next_chat_message()
                await ws.send(json.dumps({
                    "type": "message",
                    "msg_id": chat_msg.id,
                    "session_key": f"my-chat:{chat_msg.user_id}:{chat_msg.user_id}",
                    "user_id": chat_msg.user_id,
                    "user_name": chat_msg.user_name,
                    "content": chat_msg.text,
                    "reply_ctx": chat_msg.conversation_id
                }))

        await asyncio.gather(recv_loop(), send_loop())

asyncio.run(main())
```

---

## Versioning

The protocol version is declared in the `register` message via `metadata.protocol_version`. The current version is `1`. cc-connect will reject connections with incompatible versions and respond with a `register_ack` containing an error.

```json
{
  "type": "register",
  "platform": "my-chat",
  "capabilities": ["text"],
  "metadata": {
    "protocol_version": 1
  }
}
```
