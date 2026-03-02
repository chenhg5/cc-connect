# QQ 平台接入指南 / QQ Platform Setup Guide

cc-connect 通过 [OneBot v11](https://github.com/botuniverse/onebot-11) 协议连接 QQ，需要搭配一个 OneBot 实现（如 NapCat）使用。

cc-connect connects to QQ via the [OneBot v11](https://github.com/botuniverse/onebot-11) protocol. You need a OneBot implementation (e.g., NapCat) running alongside.

## 架构 / Architecture

```
QQ Client ←→ NapCat (OneBot v11) ←WebSocket→ cc-connect ←→ Agent (Claude Code / etc.)
```

## 前置条件 / Prerequisites

- 一个 QQ 账号用作机器人 / A QQ account to act as the bot
- [NapCat](https://github.com/NapNeko/NapCatQQ) 或其他 OneBot v11 实现 / NapCat or another OneBot v11 implementation

## 步骤 / Steps

### 1. 部署 NapCat / Deploy NapCat

推荐使用 Docker（最简单）/ Docker is recommended (easiest):

```bash
docker run -d \
  --name napcat \
  -e ACCOUNT=<你的QQ号> \
  -p 3001:3001 \
  -p 6099:6099 \
  mlikiowa/napcat-docker:latest
```

首次启动需要扫码登录 / First launch requires QR code login:

```bash
docker logs -f napcat
```

在日志中找到二维码，用手机 QQ 扫码登录。
Find the QR code in the logs and scan it with your QQ mobile app.

### 2. 配置 NapCat 正向 WebSocket / Configure Forward WebSocket

打开 NapCat WebUI / Open the NapCat WebUI:

```
http://localhost:6099
```

在网络配置中：/ In network settings:
- 启用 **正向 WebSocket** (Forward WebSocket) / Enable **Forward WebSocket**
- 端口设为 `3001`（默认）/ Port: `3001` (default)
- 如果需要鉴权，设置 Access Token / Set Access Token if needed

### 3. 配置 cc-connect / Configure cc-connect

在 `config.toml` 中添加 QQ 平台 / Add QQ platform to `config.toml`:

```toml
[[projects.platforms]]
type = "qq"

[projects.platforms.options]
ws_url = "ws://127.0.0.1:3001"  # NapCat 正向 WebSocket 地址
token = ""                       # 可选：Access Token（需与 NapCat 一致）
allow_from = "*"                 # 允许交互的 QQ 号，"*" 表示所有人
```

**`allow_from` 配置说明 / `allow_from` options:**
- `"*"` — 允许所有人 / Allow everyone
- `"12345"` — 仅允许 QQ 号 12345 / Only allow QQ user 12345
- `"12345,67890"` — 允许多个 QQ 号 / Allow multiple QQ users

### 4. 启动 / Start

```bash
cc-connect
```

看到如下日志表示连接成功 / You should see:

```
qq: connected to OneBot   url=ws://127.0.0.1:3001
qq: logged in             qq=123456789 nickname=MyBot
```

现在可以在 QQ 上私聊或群聊机器人了！
Now you can chat with the bot via QQ private or group messages!

## 群聊使用 / Group Chat

支持群聊消息。在群中发送消息时，机器人会以独立的会话（按用户区分）处理每个人的请求。

Group chat is supported. Each user gets their own independent session, even in group chats.

## 支持的消息类型 / Supported Message Types

| 类型 / Type | 接收 / Receive | 发送 / Send |
|------------|----------------|-------------|
| 文字 / Text | ✅ | ✅ |
| 图片 / Image | ✅ | ❌ (文本描述) |
| 语音 / Voice | ✅ (需配置 STT) | ❌ |
| @提及 / @mention | ✅ (忽略) | — |

## 常见问题 / FAQ

**Q: 连接失败？/ Connection failed?**
- 确认 NapCat 正在运行且端口正确 / Check that NapCat is running and port is correct
- 确认 NapCat 已启用正向 WebSocket / Verify Forward WebSocket is enabled in NapCat
- 如果设置了 Token，确保两边一致 / If using Token, ensure it matches on both sides

**Q: 收不到消息？/ Not receiving messages?**
- 检查 `allow_from` 配置，确认你的 QQ 号在允许列表中 / Check `allow_from` includes your QQ ID
- 查看 NapCat 日志确认消息是否正确转发 / Check NapCat logs for message forwarding

**Q: NapCat 掉线？/ NapCat disconnected?**
- NapCat 使用 NTQQ 协议，长时间挂机可能需要重新登录 / NapCat may need re-login after long periods
- 建议使用 Docker restart policy: `--restart unless-stopped`

## 其他 OneBot 实现 / Other OneBot Implementations

除了 NapCat，以下 OneBot v11 实现也应该兼容 / Besides NapCat, these should also work:

- [LLOneBot](https://github.com/LLOneBot/LLOneBot) — NTQQ 插件 / NTQQ plugin
- [Lagrange.Core](https://github.com/LagrangeDev/Lagrange.Core) — 跨平台 / Cross-platform
- [OpenShamrock](https://github.com/whitechi73/OpenShamrock) — Xposed 模块 / Xposed module (Android)

只要支持正向 WebSocket 的 OneBot v11 实现都可以使用。
Any OneBot v11 implementation with Forward WebSocket support should work.
