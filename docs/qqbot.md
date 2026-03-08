# QQ Bot 官方平台接入指南 / QQ Bot Official Platform Setup Guide

cc-connect 通过 [QQ 官方机器人 API v2](https://bot.q.qq.com/wiki/) 连接 QQ，无需第三方适配器，无需公网 IP。

cc-connect connects to QQ via the [official QQ Bot Platform API v2](https://bot.q.qq.com/wiki/). No third-party adapter needed, no public IP required.

## 与 QQ (OneBot) 的区别 / Difference from QQ (OneBot)

| | QQ Bot 官方 (`qqbot`) | QQ OneBot (`qq`) |
|--|----------------------|------------------|
| 协议 / Protocol | QQ 官方 API v2 | OneBot v11 (第三方) |
| 适配器 / Adapter | 不需要 / Not needed | 需要 NapCat 等 / Requires NapCat etc. |
| 封号风险 / Ban risk | 无 / None (腾讯官方) | 有 / Possible |
| 公网 IP / Public IP | 不需要 (WebSocket) | 不需要 (WebSocket) |
| 注册 / Registration | 需要开发者认证 / Developer verification required | 仅需 QQ 账号 / QQ account only |
| 群消息 / Group messages | 仅 @机器人 时 / Only when @mentioned | 所有消息 / All messages |

## 架构 / Architecture

```
QQ Open Platform ←WebSocket→ cc-connect ←→ Agent (Claude Code / etc.)
```

## 前置条件 / Prerequisites

1. 访问 [QQ 开放平台](https://q.qq.com) 注册开发者账号
   Visit [QQ Open Platform](https://q.qq.com) and register a developer account

2. 创建机器人应用，获取 **AppID** 和 **AppSecret**
   Create a bot application and obtain **AppID** and **AppSecret**

3. 在机器人管理页面配置权限和上线
   Configure permissions and publish in the bot management page

## 步骤 / Steps

### 1. 创建机器人 / Create Bot

1. 登录 [QQ 开放平台](https://q.qq.com)
   Log in to [QQ Open Platform](https://q.qq.com)

2. 点击 **创建机器人** → 填写基本信息
   Click **Create Bot** → fill in basic information

3. 在 **开发 → 开发设置** 中获取 `AppID` 和 `AppSecret`
   Get `AppID` and `AppSecret` from **Development → Development Settings**

### 2. 配置 cc-connect / Configure cc-connect

在 `config.toml` 中添加 QQ Bot 平台 / Add QQ Bot platform to `config.toml`:

```toml
[[projects.platforms]]
type = "qqbot"

[projects.platforms.options]
app_id = "your-app-id"           # 机器人 AppID
app_secret = "your-app-secret"   # 机器人 AppSecret
sandbox = false                  # 使用沙箱环境（测试用）/ Use sandbox (for testing)
allow_from = "*"                 # 允许的用户 openid，"*" 表示所有 / Allowed user openids, "*" for all
```

**配置项说明 / Configuration options:**

| 参数 / Option | 必填 / Required | 说明 / Description |
|---|---|---|
| `app_id` | ✅ | 机器人 AppID / Bot AppID |
| `app_secret` | ✅ | 机器人 AppSecret / Bot AppSecret |
| `sandbox` | ❌ | 使用沙箱 API（默认 false）/ Use sandbox API (default false) |
| `allow_from` | ❌ | 允许的用户 openid 列表或 `"*"`（默认允许所有）/ Allowed user openids or `"*"` |
| `intents` | ❌ | 自定义事件意图位掩码 / Custom intents bitmask (advanced) |

### 3. 启动 / Start

```bash
cc-connect
```

看到如下日志表示连接成功 / You should see:

```
qqbot: connected to QQ Bot gateway   sandbox=false
qqbot: gateway READY                 session_id=...
```

现在可以在 QQ 群聊中 @机器人 或私聊机器人了！
Now you can @mention the bot in group chats or send private messages!

## 群聊使用 / Group Chat

在群聊中，机器人**仅在被 @提及 时**收到消息。这是 QQ 官方 API 的限制。

In group chats, the bot **only receives messages when @mentioned**. This is a limitation of the official QQ Bot API.

每个用户在每个群中拥有独立的会话。
Each user gets an independent session per group.

## 私聊 / Private Messages (C2C)

支持一对一私聊消息，无需 @提及。
One-on-one private messages are supported without @mention.

## 支持的消息类型 / Supported Message Types

| 类型 / Type | 接收 / Receive | 发送 / Send |
|------------|----------------|-------------|
| 文字 / Text | ✅ | ✅ |
| 图片 / Image | ✅ | ❌ |
| 语音 / Voice | ❌ | ❌ |
| @提及 / @mention | ✅ (自动剥离) | — |

## 常见问题 / FAQ

**Q: 连接失败？/ Connection failed?**
- 确认 `app_id` 和 `app_secret` 是否正确 / Verify `app_id` and `app_secret` are correct
- 检查网络是否能访问 `api.sgroup.qq.com` / Check network access to `api.sgroup.qq.com`
- 如果使用沙箱环境，确认 `sandbox = true` / If using sandbox, set `sandbox = true`

**Q: 收不到群消息？/ Not receiving group messages?**
- 群消息仅在 @机器人 时触发 / Group messages require @mention
- 确认机器人已被添加到群中 / Verify the bot has been added to the group
- 检查 `allow_from` 配置 / Check `allow_from` configuration

**Q: 提示 token 获取失败？/ Token acquisition failed?**
- 确认 `app_secret` 正确 / Verify `app_secret` is correct
- 检查机器人是否已上线（未上线只能使用沙箱）/ Check if the bot is published (unpublished bots can only use sandbox)

**Q: 断线重连？/ Reconnection?**
- cc-connect 内置自动重连机制，断线后会自动尝试恢复（最多 30 次）
- cc-connect has built-in automatic reconnection with resume support (up to 30 attempts)

## 沙箱环境 / Sandbox

开发测试时可以使用沙箱环境，设置 `sandbox = true`。沙箱环境使用独立的 API 端点 (`sandbox.api.sgroup.qq.com`)，不影响生产环境。

For development and testing, set `sandbox = true`. The sandbox uses a separate API endpoint (`sandbox.api.sgroup.qq.com`) and doesn't affect production.
