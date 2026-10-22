# Cloud Web Platform (Self-hosted IM Gateway)

> CWIP Protocol Version 1

## Overview

The **cloud_web** platform lets cc-connect treat your **self-hosted IM gateway** as a first-class messaging platform — the same way it integrates with Telegram, Discord, or Feishu.

cc-connect connects to your gateway using the **Cloud Web IM Protocol (CWIP)**, which reuses the message semantics of the [Bridge Protocol](./bridge-protocol.md) with reversed roles: cc-connect is the client, your gateway is the IM server.

### Three transport modes

| `transport` | cc-connect role | Use when |
|-------------|-------------------|----------|
| `websocket` | WebSocket client (dial out) | Real-time bidirectional; best default |
| `long_poll` | HTTP client (blocking poll) | Firewall-friendly; cc-connect initiates all traffic |
| `gateway` | HTTP webhook server | Your gateway pushes events to cc-connect |

All modes share the same JSON message types and capability model.

---

## Quick start

```toml
[[projects.platforms]]
type = "cloud_web"

[projects.platforms.options]
transport = "websocket"
token = "your-shared-secret"
base_url = "https://gateway.example.com"
ws_url = "wss://gateway.example.com/cloud-web/ws"
allow_from = "*"
```

Web UI: Project wizard → **Cloud Web (Self-hosted IM)** → fill transport + token + URLs.

---

## Authentication

Every request must include the shared `token`:

| Method | Example |
|--------|---------|
| Header | `Authorization: Bearer <token>` |
| Header | `X-Cloud-Web-Token: <token>` |
| Query | `?token=<token>` |

---

## Handshake

After connection (WebSocket) or via `POST /cloud-web/v1/register` (HTTP modes), cc-connect sends:

```json
{
  "type": "register",
  "platform": "cloud_web",
  "client": "cc-connect",
  "project": "my-project",
  "transport": "websocket",
  "metadata": { "protocol_version": 1 }
}
```

Gateway responds:

```json
{
  "type": "register_ack",
  "ok": true,
  "capabilities": ["text","image","file","audio","card","buttons","typing","update_message","preview","delete_message","reconstruct_reply"],
  "server": { "name": "my-gateway", "version": "1.0.0" }
}
```

Capabilities match [Bridge Protocol capabilities](./bridge-protocol.md#capabilities). Undeclared capabilities are automatically degraded by cc-connect.

---

## Endpoints (defaults)

| Mode | Inbound | Outbound |
|------|---------|----------|
| websocket | `wss://<host>/cloud-web/ws` | same connection |
| long_poll | `POST /cloud-web/v1/events` | `POST /cloud-web/v1/send` |
| gateway | cc-connect listens `webhook_path` (default `/cloud-web/webhook`) | `POST <base_url>/cloud-web/v1/send` |

Optional gateway registration:

```json
POST /cloud-web/v1/register
{ "register": { ... }, "public_url": "https://your-host/cloud-web/webhook" }
```

---

## Inbound messages (Gateway → cc-connect)

### `message`

```json
{
  "type": "message",
  "msg_id": "msg-001",
  "session_key": "cloud_web:chat123:user456",
  "chat_id": "chat123",
  "user_id": "user456",
  "user_name": "Alice",
  "content": "Hello",
  "reply_ctx": "opaque-context-for-replies",
  "chat_type": "group",
  "mentioned": true,
  "images": [],
  "files": [],
  "audio": null
}
```

- `session_key`: `{platform}:{chat_id}:{user_id}` (or `{platform}:{chat_id}` when `share_session_in_channel = true`)
- `reply_ctx`: opaque string/json echoed on every outbound message
- `chat_type`: `direct` | `group` | `channel` — with `group_reply_all = false`, non-`mentioned` group messages are ignored

### `card_action`

User clicked a card button (same semantics as Bridge `card_action`).

### `message_recall`

Message was recalled; maps to `Message.Recalled`.

---

## Outbound messages (cc-connect → Gateway)

Same types as Bridge outbound: `reply`, `reply_stream`, `card`, `buttons`, `typing_start`, `typing_stop`, `preview_start`, `update_message`, `delete_message`, `image`, `file`, `audio`.

See [Bridge Protocol — cc-connect → Adapter](./bridge-protocol.md) for full schemas.

---

## Configuration reference

| Option | Required | Description |
|--------|----------|-------------|
| `transport` | yes | `websocket`, `long_poll`, or `gateway` |
| `token` | yes | Shared secret |
| `base_url` | mode-dependent | Gateway HTTP base URL |
| `ws_url` | websocket | Override WebSocket URL |
| `long_poll_timeout_ms` | no | Default `30000` |
| `events_path` | no | Default `/cloud-web/v1/events` |
| `send_path` | no | Default `/cloud-web/v1/send` |
| `listen` | gateway | Default `:8099` |
| `webhook_path` | gateway | Default `/cloud-web/webhook` |
| `public_url` | gateway | Public callback URL for registration |
| `register_url` | gateway | Optional register endpoint |
| `allow_from` | no | User whitelist (`*` = all) |
| `share_session_in_channel` | no | Shared group session |
| `group_reply_all` | no | Reply to all group messages without @mention |

---

## Public URL for gateway mode

Expose the webhook with nginx, cloudflared, or similar. See [WeCom tunnel guide](./wecom.md) for cloudflared examples.

---

## Related

- [Bridge Protocol](./bridge-protocol.md) — message type reference
- [config.example.toml](../config.example.toml) — commented examples

---

## Minimal Gateway Example (Go)

Below is a minimal WebSocket Gateway for local testing. It accepts `register`, pushes a `message`, and prints outbound `reply` frames from cc-connect.

```go
package main

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

func main() {
	http.HandleFunc("/cloud-web/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, _ := upgrader.Upgrade(w, r, nil)
		defer conn.Close()
		_, reg, _ := conn.ReadMessage()
		log.Printf("register: %s", reg)
		_ = conn.WriteJSON(map[string]any{
			"type": "register_ack", "ok": true,
			"capabilities": []string{"text", "reconstruct_reply"},
		})
		_ = conn.WriteJSON(map[string]any{
			"type": "message", "msg_id": "1", "session_key": "cloud_web:chat1:user1",
			"user_id": "user1", "content": "hello from gateway", "reply_ctx": "ctx-1",
		})
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var m map[string]any
			_ = json.Unmarshal(raw, &m)
			log.Printf("outbound: %v", m)
		}
	})
	log.Println("gateway listening :8080/cloud-web/ws")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
```

Point cc-connect at `ws_url = "ws://127.0.0.1:8080/cloud-web/ws"` with matching `token`.
