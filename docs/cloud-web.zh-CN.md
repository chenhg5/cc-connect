# Cloud Web 平台（自建 IM Gateway）

> CWIP 协议版本 1

## 概述

**cloud_web** 平台让 cc-connect 将**自建 IM Gateway** 当作一等公民 IM 平台接入，使用方式与 Telegram、Discord、飞书相同。

cc-connect 通过 **Cloud Web IM Protocol (CWIP)** 与 Gateway 通信。CWIP 复用 [Bridge 协议](./bridge-protocol.zh-CN.md) 的消息语义，但角色反转：cc-connect 为客户端，自建 Gateway 为 IM 服务端。

### 三种传输模式

| `transport` | cc-connect 角色 | 适用场景 |
|-------------|----------------|----------|
| `websocket` | WebSocket 客户端（主动连接） | 实时双向，推荐默认 |
| `long_poll` | HTTP 长轮询客户端 | 防火墙友好，流量均由 cc-connect 发起 |
| `gateway` | HTTP Webhook 服务端 | Gateway 主动推送事件到 cc-connect |

三种模式共用同一套 JSON 消息类型与能力模型。

---

## 快速开始

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

Web 管理端：项目向导 → **Cloud Web (自建 IM)** → 填写传输模式、Token 与 URL。

---

## 认证

所有请求需携带共享 `token`：

| 方式 | 示例 |
|------|------|
| Header | `Authorization: Bearer <token>` |
| Header | `X-Cloud-Web-Token: <token>` |
| Query | `?token=<token>` |

---

## 握手

WebSocket 连接后，或 HTTP 模式下 `POST /cloud-web/v1/register`，cc-connect 发送：

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

Gateway 响应：

```json
{
  "type": "register_ack",
  "ok": true,
  "capabilities": ["text","image","file","audio","card","buttons","typing","update_message","preview","delete_message","reconstruct_reply"],
  "server": { "name": "my-gateway", "version": "1.0.0" }
}
```

能力列表与 [Bridge 协议能力表](./bridge-protocol.zh-CN.md) 一致。未声明的能力由 cc-connect 自动降级。

---

## 默认端点

| 模式 | 入站 | 出站 |
|------|------|------|
| websocket | `wss://<host>/cloud-web/ws` | 同一连接 |
| long_poll | `POST /cloud-web/v1/events` | `POST /cloud-web/v1/send` |
| gateway | cc-connect 监听 `webhook_path`（默认 `/cloud-web/webhook`） | `POST <base_url>/cloud-web/v1/send` |

Gateway 模式可选注册：

```json
POST /cloud-web/v1/register
{ "register": { ... }, "public_url": "https://your-host/cloud-web/webhook" }
```

---

## 入站消息（Gateway → cc-connect）

### `message`

```json
{
  "type": "message",
  "msg_id": "msg-001",
  "session_key": "cloud_web:chat123:user456",
  "chat_id": "chat123",
  "user_id": "user456",
  "user_name": "Alice",
  "content": "你好",
  "reply_ctx": "opaque-context-for-replies",
  "chat_type": "group",
  "mentioned": true
}
```

- `session_key`：`{platform}:{chat_id}:{user_id}`（`share_session_in_channel = true` 时为 `{platform}:{chat_id}`）
- `reply_ctx`：opaque 上下文，出站消息原样回传
- `group_reply_all = false` 时，群聊未 @ 的消息会被忽略

### `card_action` / `message_recall`

与 Bridge 协议语义相同。

---

## 出站消息（cc-connect → Gateway）

支持：`reply`、`reply_stream`、`card`、`buttons`、`typing_start/stop`、`preview_start`、`update_message`、`delete_message`、`image`、`file`、`audio`。

详见 [Bridge 协议出站消息](./bridge-protocol.zh-CN.md)。

---

## 配置项

| 选项 | 必填 | 说明 |
|------|------|------|
| `transport` | 是 | `websocket` / `long_poll` / `gateway` |
| `token` | 是 | 共享密钥 |
| `base_url` | 视模式 | Gateway HTTP 根地址 |
| `ws_url` | websocket | WebSocket 地址（可省略，由 base_url 推导） |
| `long_poll_timeout_ms` | 否 | 默认 30000 |
| `listen` / `webhook_path` | gateway | 本地 Webhook 监听 |
| `public_url` / `register_url` | gateway | 公网回调注册 |
| `allow_from` | 否 | 用户白名单 |
| `share_session_in_channel` | 否 | 群聊共享 session |
| `group_reply_all` | 否 | 群内无需 @ 也回复 |

---

## Gateway 模式公网暴露

使用 nginx、cloudflared 等暴露 Webhook。可参考 [企业微信隧道文档](./wecom.md)。

---

## 相关文档

- [Bridge 协议](./bridge-protocol.zh-CN.md)
- [config.example.toml](../config.example.toml)

---

## 最小 Gateway 示例（Go）

下面是一个用于本地测试的最小 WebSocket Gateway：接收 `register`、推送 `message`、打印 cc-connect 发来的 `reply`。

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

cc-connect 配置：`ws_url = "ws://127.0.0.1:8080/cloud-web/ws"`，并设置相同的 `token`。
