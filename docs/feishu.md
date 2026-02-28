# 飞书 (Feishu/Lark) 接入指南

本文档介绍如何将 **cc-connect** 接入飞书，让你可以通过飞书机器人远程调用 Claude Code。

## 前置要求

- 飞书账号（个人或企业均可）
- 一台可运行 cc-connect 的设备（无需公网 IP）
- Claude Code 已安装并配置完成

> 💡 **优势**：使用长连接模式，无需公网 IP、无需域名、无需反向代理（ngrok/frp）

---

## 第一步：创建飞书企业自建应用

### 1.1 进入飞书开放平台

访问 [飞书开放平台](https://open.feishu.cn/) 并登录你的飞书账号。

### 1.2 创建应用

1. 点击右上角「控制台」进入开发者后台
2. 点击「创建企业自建应用」

> 💡 **个人用户也可以创建**：飞书开放平台支持个人开发者创建应用，无需企业认证。

### 1.3 填写应用信息

| 字段 | 填写建议 |
|------|---------|
| 应用名称 | `cc-connect` 或你喜欢的名称 |
| 应用描述 | `Claude Code 远程助手` |
| 应用图标 | 上传一个喜欢的图标 |

---

## 第二步：获取凭证

### 2.1 进入凭据页面

在应用详情页，左侧导航栏点击 **「凭据与基础信息」**。

### 2.2 获取 App ID 和 App Secret

你会看到以下信息：

```
App ID:     cli_axxxxxxxxxxxx
App Secret: QhkMpxxxxxxxxxxxxxxxxxxxx
```

> ⚠️ **重要**：请妥善保存这两个凭证，后续配置 cc-connect 时需要用到。App Secret 只会显示一次，如果忘记了需要重置。

### 2.3 配置到 cc-connect

将凭证配置到 cc-connect 的 `config.toml` 中：

```toml
[[projects]]
name = "my-project"

[projects.agent]
type = "claudecode"

[projects.agent.options]
work_dir = "/path/to/your/project"
mode = "default"

[[projects.platforms]]
type = "feishu"

[projects.platforms.options]
app_id = "cli_axxxxxxxxxxxx"
app_secret = "QhkMpxxxxxxxxxxxxxxxxxxxx"
```

---

## 第三步：配置应用能力

### 3.1 启用机器人能力

1. 左侧导航栏点击 **「应用能力」** → **「机器人」**
2. 点击「启用机器人」

### 3.2 配置机器人信息

| 配置项 | 建议值 |
|-------|--------|
| 机器人名称 | `cc-connect` |
| 机器人描述 | `Claude Code 远程助手` |
| 机器人头像 | 与应用图标一致 |

---

## 第四步：配置权限

### 4.1 进入权限管理

左侧导航栏点击 **「权限管理」**。

### 4.2 申请必要权限

在「权限配置」中搜索并添加以下权限：

| 权限名称 | 权限标识 | 用途 |
|---------|---------|------|
| 获取与更新用户基本信息 | `contact:user.base:readonly` | 获取用户信息 |
| 接收群聊消息 | `im:message.group:receive` | 接收群消息 |
| 接收单聊消息 | `im:message.p2p:receive` | 接收私聊消息 |
| 读取群消息 | `im:message.group_msg:readonly` | 读取群消息内容 |
| 读取单聊消息 | `im:message.p2p_msg:readonly` | 读取私聊内容 |
| 以应用身份发送群消息 | `im:message:send_as_bot` | 发送消息回复用户 |

### 4.3 发布权限申请

配置完权限后，点击「申请发布」使权限生效。

---

## 第五步：配置事件订阅（长连接模式）

### 5.1 进入事件订阅页面

左侧导航栏点击 **「事件订阅」**。

### 5.2 选择长连接模式

在「订阅方式」中选择：

```
✅ 使用长连接接收事件
```

> 💡 **长连接的优势**：
> - 无需公网 IP
> - 无需配置域名和 HTTPS 证书
> - 无需使用 ngrok、frp 等反向代理工具
> - 适合本地开发和内网环境

### 5.3 启用长连接

1. 点击「启用长连接」
2. 系统会生成 WebSocket 连接信息

### 5.4 添加订阅事件

在事件配置中添加以下事件：

| 事件名称 | 事件标识 | 用途 |
|---------|---------|------|
| 接收消息 | `im.message.receive_v1` | 接收用户发送的消息 |

### 5.5 保存配置

点击「保存」完成事件订阅配置。

---

## 第六步：启动 cc-connect

### 6.1 启动服务

```bash
cc-connect
# 或指定配置文件
cc-connect -config /path/to/config.toml
```

### 6.2 验证连接

启动后，cc-connect 会自动与飞书建立 WebSocket 长连接。你会在日志中看到：

```
level=INFO msg="platform started" project=my-project platform=feishu
level=INFO msg="cc-connect is running" projects=1
[Info] connected to wss://msg-frontier.feishu.cn/ws/v2?...
```

---

## 第七步：发布应用

### 7.1 提交审核

1. 左侧导航栏点击 **「版本管理与发布」**
2. 点击「创建版本」
3. 填写版本号和更新说明
4. 点击「保存并发布」

### 7.2 可用性设置

- **企业版**：发布后需要管理员审批才能使用
- **个人版**：发布后立即可用

---

## 第八步：添加机器人到会话

### 8.1 单聊使用

在飞书中搜索你的机器人名称，直接发送消息即可开始对话。

### 8.2 群聊使用

1. 进入目标群聊
2. 点击群设置 → 「群机器人」
3. 添加你创建的机器人

---

## 使用示例

配置完成后，你可以在飞书中这样使用：

```
用户: 帮我分析一下当前项目的结构

cc-connect: 🤔 思考中...
cc-connect: 🔧 执行: Bash(ls -la)
cc-connect: ✅ 这是一个 Node.js 项目，包含以下目录...
```

---

## 架构图

```
┌─────────────────────────────────────────────────────────────┐
│                         飞书云                               │
│                                                              │
│   用户消息 ──→ 飞书开放平台 ──→ WebSocket Gateway            │
│                                      │                       │
└──────────────────────────────────────┼───────────────────────┘
                                       │
                                       │ WebSocket 长连接
                                       │ (无需公网IP)
                                       ▼
┌─────────────────────────────────────────────────────────────┐
│                      你的本地环境                            │
│                                                              │
│   cc-connect ◄──► Claude Code CLI ◄──► 你的项目代码         │
│                                                              │
└─────────────────────────────────────────────────────────────┘
```

---

## 常见问题

### Q: 长连接和 Webhook 有什么区别？

| 对比项 | 长连接模式 | Webhook 模式 |
|-------|-----------|-------------|
| 公网 IP | ❌ 不需要 | ✅ 需要 |
| 域名 | ❌ 不需要 | ✅ 需要 |
| HTTPS 证书 | ❌ 不需要 | ✅ 需要 |
| 反向代理 | ❌ 不需要 | ✅ 需要（ngrok/frp） |
| 配置复杂度 | 简单 | 较复杂 |
| 适用场景 | 本地开发、内网 | 生产环境 |

### Q: 长连接断开怎么办？

cc-connect 内置了自动重连机制，断开后会自动尝试重新连接。

### Q: 消息发送后没有响应？

检查以下项目：
1. cc-connect 服务是否正常运行
2. 长连接是否建立成功（查看日志）
3. 事件订阅是否配置了 `im.message.receive_v1`

### Q: 提示权限不足？

确保已在「权限管理」中申请并获得了所有必要权限，并发布了新版本。

### Q: 如何调试消息？

在飞书开放平台「开发调试」→「调试工具」中可以模拟发送消息进行测试。

---

## 参考链接

- [飞书开放平台](https://open.feishu.cn/)
- [飞书开放平台文档](https://open.feishu.cn/document/)
- [机器人开发指南](https://open.feishu.cn/document/ukTMukTMukTM/uYjNwUjL2YDM14iN2ATN)
- [事件订阅文档](https://open.feishu.cn/document/ukTMukTMukTM/uUTNz4SN1MjL1UzM)
- [权限列表](https://open.feishu.cn/document/ukTMukTMukTM/uQjNz4CN1MjL2czN)
- [OpenClaw 飞书接入教程](https://m.163.com/dy/article/KMO4FEP105566SCS.html)
- [飞书 WebSocket 长连接模式](https://m.blog.csdn.net/u014177256/article/details/158267848)

---

## 下一步

- [接入钉钉](./dingtalk.md)
- [接入 Telegram](./telegram.md)
- [接入 Slack](./slack.md)
- [接入 Discord](./discord.md)
- [返回首页](../README.md)
