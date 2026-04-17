# 微博私信接入指南

本文档介绍如何将 **cc-connect** 接入微博私信，让你可以通过微博私信远程调用 AI 编程 Agent。

## 前置要求

- 微博账号
- 一台可运行 cc-connect 的设备（无需公网 IP）
- AI 编程 Agent（Claude Code、Codex 等）已安装并配置完成

> 💡 **优势**：使用 WebSocket 长连接，无需公网 IP、无需域名、无需反向代理

---

## 第一步：注册微博开放平台应用

### 1.1 进入微博开放平台

访问 [微博开放平台](https://open.weibo.com/)，通过微博龙虾助手注册应用。

### 1.2 创建应用

按照平台指引创建一个新的应用，获取 Open IM 的 `app_id` 和 `app_secret`。

> ⚠️ **重要**：请妥善保存这两个凭证，后续配置 cc-connect 时需要用到。

---

## 第二步：配置 cc-connect

### 2.1 编辑配置文件

将凭证配置到 cc-connect 的 `config.toml` 中：

```toml
[[projects]]
name = "my-project"

[projects.agent]
type = "claudecode"

[projects.agent.options]
work_dir = "/path/to/your/project"

[[projects.platforms]]
type = "weibo"

[projects.platforms.options]
app_id = "your-weibo-app-id"
app_secret = "your-weibo-app-secret"
```

### 2.2 使用 CLI 引导配置（推荐）

也可以使用交互式 CLI 来配置：

```bash
cc-connect new
# 选择 weibo 平台，按提示输入 app_id 和 app_secret
```

### 2.3 可选配置项

```toml
[projects.platforms.options]
app_id = "your-weibo-app-id"
app_secret = "your-weibo-app-secret"
# allow_from = "*"           # 允许的微博用户 ID，逗号分隔；"*" 表示所有（默认）
# token_endpoint = ""        # 自定义 token 接口地址（默认：https://open-im.api.weibo.com/open/auth/ws_token）
# ws_endpoint = ""           # 自定义 WebSocket 地址（默认：ws://open-im.api.weibo.com/ws/stream）
```

---

## 第三步：启动 cc-connect

### 3.1 启动服务

```bash
cc-connect
# 或指定配置文件
cc-connect -config /path/to/config.toml
```

### 3.2 验证连接

启动后，cc-connect 会自动与微博建立 WebSocket 长连接。你会在日志中看到：

```
level=INFO msg="weibo: authenticated" uid=1234567890
level=INFO msg="weibo: websocket connected"
level=INFO msg="platform started" project=my-project platform=weibo
level=INFO msg="cc-connect is running" projects=1
```

---

## 第四步：开始使用

### 4.1 发送私信

在微博中给你的应用账号发送私信，即可与 AI Agent 对话：

```
用户: 帮我分析一下当前项目的结构

cc-connect: 🤔 思考中...
cc-connect: 🔧 执行: Bash(ls -la)
cc-connect: ✅ 这是一个 Go 项目，包含以下模块...
```

### 4.2 使用命令

所有 cc-connect 命令均可在微博私信中使用：

| 命令 | 功能 |
|------|------|
| `/status` | 查看 Agent 状态 |
| `/new` | 新建会话 |
| `/list` | 查看会话列表 |
| `/stop` | 停止当前会话 |
| `/help` | 查看帮助 |

---

## 连接方式说明

微博私信平台使用 WebSocket 长连接：

```
┌─────────────────────────────────────────────────────────────┐
│                      微博 Open IM                           │
│                                                              │
│   用户私信 ──→ open-im.api.weibo.com ──→ WebSocket Stream   │
│                                              │               │
└──────────────────────────────────────────────┼───────────────┘
                                               │
                                               │ WebSocket 长连接
                                               │ (无需公网IP)
                                               ▼
┌─────────────────────────────────────────────────────────────┐
│                      你的本地环境                            │
│                                                              │
│   cc-connect ◄──► AI Agent CLI ◄──► 你的项目代码            │
│                                                              │
└─────────────────────────────────────────────────────────────┘
```

| 特性 | 说明 |
|------|------|
| ✅ 无需公网 IP | 内网环境也能接入 |
| ✅ 无需域名 | 不需要配置域名 |
| ✅ 自动重连 | 断线后自动重连（指数退避） |
| ✅ 心跳保活 | 30 秒心跳间隔，40 秒超时检测 |
| ✅ Token 自动刷新 | 过期前自动续期 |

---

## 技术细节

### 消息长度限制

微博私信文本限制约 2000 字符。cc-connect 会自动将超长消息分块发送，接收端会按顺序收到完整内容。

### Token 管理

- 首次启动时通过 `app_id` + `app_secret` 获取 WebSocket Token
- Token 过期前 60 秒自动刷新
- WebSocket 断开时（code 4002 / invalid token）自动清除并重新获取

### 安全建议

- 使用 `allow_from` 限制允许使用的微博用户 ID
- 发送 `/whoami` 获取你的用户 ID
- 不要将 `app_secret` 提交到代码仓库

---

## 常见问题

### Q: 连接后收不到消息？

检查以下项目：
1. cc-connect 服务是否正常运行
2. WebSocket 连接是否建立成功（查看日志）
3. `app_id` 和 `app_secret` 是否正确

### Q: 长连接断开怎么办？

cc-connect 内置了自动重连机制（指数退避，最大 10 秒间隔），断开后会自动尝试重新连接。

### Q: 提示 Token 无效？

- Token 过期后会自动刷新，一般无需手动干预
- 如果持续失败，检查 `app_secret` 是否有效

### Q: 消息发送后显示不完整？

微博私信有约 2000 字符的限制，cc-connect 会自动分块发送。如果仍有问题，检查网络连接。

---

## 下一步

- [接入飞书](./feishu.md)
- [接入钉钉](./dingtalk.md)
- [接入 Telegram](./telegram.md)
- [接入 Discord](./discord.md)
- [接入 Slack](./slack.md)
- [返回首页](../README.md)
