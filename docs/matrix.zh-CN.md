# Matrix 配置指南

本指南将帮助你把 **cc-connect** 连接到 [Matrix](https://matrix.org/)——一个去中心化通信的开放标准。配置完成后，你可以通过任何 Matrix 客户端（Element、FluffyChat、Nheko 等）与本地 AI 编码助手对话。

## 前提条件

- 一个 Matrix 账号（可使用 `matrix.org` 等公共服务器，也可自建）
- 一台可以运行 cc-connect 的机器（无需公网 IP）
- 已安装并配置好 Claude Code（或其他支持的编码助手）

> **优势**：使用 `/sync` 长轮询——无需公网 IP、无需域名、无需反向代理。在 NAT 和防火墙后也能正常工作。

---

## 第 1 步：创建 Matrix 账号

如果你还没有 Matrix 账号：

1. 访问 [https://app.element.io](https://app.element.io)（或你自建的 Element 实例）
2. 点击 **注册**
3. 选择一个服务器（默认的 `matrix.org` 适用于大多数用户）
4. 完成注册

你也可以使用已有的 Matrix 账号——建议使用专用的机器人账号，但不是必须的。

---

## 第 2 步：获取 Access Token

你需要一个 access token 让 cc-connect 以你的 Matrix 用户身份进行认证。

### 通过 curl（推荐）

使用 curl 登录并创建专用设备，这样可以获得独立的 device ID，确保 E2EE（端到端加密）正常工作：

```bash
curl -XPOST "https://matrix.org/_matrix/client/v3/login" \
  -H "Content-Type: application/json" \
  -d '{"type":"m.login.password","user":"your-username","password":"your-password","device_id":"CC-CONNECT"}'
```

响应中包含 `"access_token": "syt_..."`，将它记录下来用于配置。

> **推荐**：将 `device_id` 设为 `CC-CONNECT` 或其他易于识别的名称。专用设备能确保加密密钥正确分发。

### 通过 Element（网页/桌面端）

1. 登录 **Element**（[app.element.io](https://app.element.io)）
2. 打开 **设置**（点击头像 → **设置**）
3. 进入 **帮助与关于** → 滚动到 **高级**
4. 点击 **Access Token** → 复制令牌

> **注意**：从 Element 获取的 token 会复用 Element 的设备 ID，可能导致 E2EE 加密消息无法解密。建议使用上面的 curl 方式创建专用设备。

> **警告**：请像对待密码一样保护你的 access token。任何拥有它的人都可以以你的身份发送消息。如果令牌泄露，可以在 Element 中登出所有会话来使其失效。

---

## 第 3 步：查找用户 ID（可选）

你的用户 ID 格式为 `@username:matrix.org`。cc-connect 可以从 access token 自动检测，但你也可以在配置中显式指定。

在 Element 中：点击头像，顶部显示的就是你的用户 ID。

---

## 第 4 步：配置 cc-connect

在 `config.toml` 中添加 Matrix 平台：

```toml
[[projects]]
name = "my-project"

[projects.agent]
type = "claudecode"

[projects.agent.options]
work_dir = "/path/to/your/project"
mode = "default"

[[projects.platforms]]
type = "matrix"

[projects.platforms.options]
homeserver = "https://matrix.org"
access_token = "syt_xxx_xxx"

# ── 可选设置 ──────────────────────────────────────────
# user_id = "@bot:matrix.org"        # 省略则自动检测
# allow_from = "*"                   # "*" = 所有用户，或 "id1,id2"
# auto_join = true                   # 自动接受房间邀请（默认：true）
# auto_verify = true                 # 自动接受 SAS 密钥验证（默认：true）
# cross_signing_password = ""        # 用于跨签名初始化的 bot 账号密码（一次性）
# share_session_in_channel = false   # true = 房间内所有用户共享一个会话
# group_reply_all = false            # true = 回复群聊中的所有消息
# proxy = ""                         # HTTP/SOCKS5 代理，例如 "http://proxy:8080"
```

> **常见错误**：`homeserver` 必须包含协议前缀（`https://`），且必须与你账号注册的服务器一致。

---

## 第 5 步：启动 cc-connect

```bash
cc-connect
# 或指定配置文件
cc-connect -config /path/to/config.toml
```

你应该能看到类似日志：

```
level=INFO msg="matrix: E2EE enabled" device_id=CC-CONNECT
level=INFO msg="matrix: connected" user=@bot:matrix.org
level=INFO msg="platform started" project=my-project platform=matrix
level=INFO msg="cc-connect is running" projects=1
```

如果看到 `E2EE not available`，说明加密初始化失败，加密房间的消息将无法正常收发。请参考下方常见问题。

---

## 第 6 步：开始对话

### 6.1 私聊

1. 打开你的 Matrix 客户端（Element、FluffyChat 等）
2. 向机器人的用户 ID（如 `@bot:matrix.org`）发起新的私聊
3. 发送消息——cc-connect 会回复

### 6.2 群聊

1. 创建或打开一个房间
2. 邀请机器人的用户 ID 加入房间
3. 如果 `auto_join = true`（默认），机器人会自动加入
4. 在房间中发送消息

> **注意**：在群聊中，机器人只在被 @ 提及时才会回复，除非设置了 `group_reply_all = true`。

---

## 架构

```
┌─────────────────────────────────────────────────────────────┐
│                    Matrix 服务器                              │
│                                                              │
│   用户消息 ──→ /sync 端点 ◄── 长轮询                         │
│                          ▲                                   │
└──────────────────────────┼───────────────────────────────────┘
                           │
                           │ HTTPS（无需公网 IP）
                           ▼
┌─────────────────────────────────────────────────────────────┐
│                    你的本地机器                                │
│                                                              │
│   cc-connect ◄──► Claude Code CLI ◄──► 你的项目代码           │
│                                                              │
└─────────────────────────────────────────────────────────────┘
```

---

## 配置参考

| 选项 | 必填 | 默认值 | 说明 |
|------|------|--------|------|
| `homeserver` | 是 | — | Matrix 服务器 URL（如 `https://matrix.org`） |
| `access_token` | 是 | — | 认证用的 access token |
| `user_id` | 否 | 自动检测 | Matrix 用户 ID（如 `@bot:matrix.org`） |
| `allow_from` | 否 | `"*"` | 允许交互的用户 ID 列表，逗号分隔，或 `"*"` 表示所有人 |
| `auto_join` | 否 | `true` | 自动接受房间邀请 |
| `auto_verify` | 否 | `true` | 自动接受 SAS 密钥验证请求 |
| `cross_signing_password` | 否 | `""` | Bot 账号密码，用于跨签名密钥初始化（一次性，仅首次运行或密钥重置时需要） |
| `share_session_in_channel` | 否 | `false` | 房间内所有用户共享同一个 Agent 会话 |
| `group_reply_all` | 否 | `false` | 回复群聊中的所有消息（不仅限于 @ 提及） |
| `proxy` | 否 | `""` | HTTP 或 SOCKS5 代理 URL |

---

## 常见问题

### 机器人不回复消息？

1. cc-connect 是否在运行，且日志中显示 `matrix: connected`？
2. access token 是否有效？尝试重新生成。
3. 在群聊中，机器人是否被 @ 提及？或是否设置了 `group_reply_all = true`？
4. 如果日志显示 `E2EE not available` 或 `decrypt failed`，请参考下方 E2EE 相关问题。

### 如何限制谁可以使用机器人？

设置 `allow_from` 为逗号分隔的 Matrix 用户 ID 列表：

```toml
allow_from = "@alice:matrix.org,@bob:matrix.org"
```

### 机器人不加入房间？

确保 `auto_join = true`（这是默认值）。如果机器人在 cc-connect 启动之前就被邀请了，请重新邀请一次。

### E2EE（端到端加密）

cc-connect 自动支持加密房间（E2EE），无需额外配置。启动时如果看到 `matrix: E2EE enabled`，说明加密功能正常。

#### 日志显示 "E2EE not available"？

可能原因和解决方案：

1. **`device ID not available from whoami`** — 服务器未返回 device ID。请使用 curl 创建带 `device_id` 的专用设备。
2. **`not marked as shared, but there are keys on the server`** — 旧的加密数据与当前设备冲突。cc-connect 会自动尝试修复。如果持续失败，删除旧的加密数据库：`rm ~/.cc-connect/matrix-crypto-*.db*`
3. **`mismatching device ID in client and crypto store`** — token 对应的 device ID 与加密数据库不匹配。删除对应数据库：`rm ~/.cc-connect/matrix-crypto-*.db*`

#### 日志显示 "decrypt failed: no session found"？

说明对方客户端没有把加密密钥发给 bot 的设备。这通常发生在以下情况：

1. **复用了 Element 的 access token** — Element 的设备 ID 和 bot 的加密密钥冲突。请使用 curl 创建专用设备（见第 2 步）。
2. **刚更换了 access token** — 对方客户端可能还没发现 bot 的新设备。等待 1-2 分钟后重新发送消息。
3. **加密数据库损坏** — 删除数据库后重启：`rm ~/.cc-connect/matrix-crypto-*.db*`

#### 如何获取专用的 access token（推荐方式）？

使用 Matrix API 创建一个专用设备，避免与 Element 等 App 冲突：

```bash
# 替换 homeserver URL、用户名和密码
curl -XPOST "https://your-homeserver.com/_matrix/client/v3/login" \
  -H "Content-Type: application/json" \
  -d '{
    "type": "m.login.password",
    "user": "your-bot-username",
    "password": "your-password",
    "device_id": "CC-CONNECT"
  }'
```

响应中的 `access_token` 即可用于配置。`device_id` 为 `CC-CONNECT`，便于识别和管理。

#### 机器人消息有红叹号（"由未经其所有者验证的设备加密"）？

这说明机器人的设备未被跨签名。cc-connect 首次运行时会自动设置跨签名，但部分 Matrix 服务器需要密码认证（UIA）才能发布跨签名密钥。

如果日志显示 `no supported UIA flow for cross-signing`，请在配置中添加 bot 账号的密码：

```toml
cross_signing_password = "你的bot密码"
```

这是一次性操作——跨签名密钥发布并保存后，密码就不再需要了。

#### 如何验证机器人的设备？

cc-connect 会自动接受 SAS 密钥验证请求（`auto_verify = true`，默认开启）。从 Element 验证机器人的步骤：

1. 打开与机器人的私聊
2. 点击机器人头像 → **验证**（或在 **设置** → **安全** 中找到机器人的设备）
3. 机器人会自动接受并确认验证
4. Element 将显示该设备已验证

验证完成后，机器人的加密消息将不再显示警告。

### 如何使用自建 Matrix 服务器？

将 `homeserver` 设为你的服务器 URL（如 `https://synapse.example.com`）。确保运行 cc-connect 的机器可以访问该 URL。

### 如何使用代理？

```toml
proxy = "http://proxy-host:8080"
# 或 SOCKS5：
proxy = "socks5://proxy-host:1080"
```

---

## 参考资料

- [Matrix 协议规范](https://spec.matrix.org/)
- [Element 网页客户端](https://app.element.io)
- [Matrix.org](https://matrix.org/)

---

## 相关文档

- [Telegram 配置](./telegram.md)
- [Discord 配置](./discord.md)
- [Slack 配置](./slack.md)
- [返回 README](../README.md)
