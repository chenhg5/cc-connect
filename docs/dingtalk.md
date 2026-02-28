# 钉钉 (DingTalk) 接入指南

本文档介绍如何将 **cc-connect** 接入钉钉，让你可以通过钉钉机器人远程调用 Claude Code。

## 前置要求

- 钉钉账号（个人或企业均可）
- 一台可运行 cc-connect 的设备（无需公网 IP）
- Claude Code 已安装并配置完成

> 💡 **优势**：使用 Stream 模式（WebSocket 长连接），无需公网 IP、无需域名、无需反向代理

---

## 第一步：创建钉钉应用

### 1.1 进入钉钉开放平台

访问 [钉钉开放平台](https://open.dingtalk.com/) 并登录你的钉钉账号。

### 1.2 创建应用

1. 点击「控制台」进入开发者后台
2. 选择「应用开发」→「企业内部开发」（或「H5微应用」）
3. 点击「创建应用」

> 💡 **个人开发者**：钉钉开放平台支持个人开发者创建应用。

### 1.3 填写应用信息

| 字段 | 填写建议 |
|------|---------|
| 应用名称 | `cc-connect` 或你喜欢的名称 |
| 应用描述 | `Claude Code 远程助手` |
| 应用图标 | 上传一个喜欢的图标 |

---

## 第二步：获取凭证

### 2.1 进入应用详情

在应用列表中点击刚创建的应用，进入应用详情页。

### 2.2 获取凭证信息

在「基础信息」页面，你会看到：

```
AppKey:     dingxxxxxxxxxxxxxxx
AppSecret:  xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
```

> ⚠️ **重要**：请妥善保存这两个凭证，后续配置 cc-connect 时需要用到。AppSecret 只会显示一次。

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
type = "dingtalk"

[projects.platforms.options]
client_id = "dingxxxxxxxxxxxxxxx"
client_secret = "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
```

---

## 第三步：配置机器人能力

### 3.1 启用机器人

1. 在应用详情页，找到「机器人配置」
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

在应用详情页，点击「权限管理」。

### 4.2 申请必要权限

搜索并申请以下权限：

| 权限名称 | 权限标识 | 用途 |
|---------|---------|------|
| 成员信息读权限 | `qyapi_get_member` | 获取用户信息 |
| 企业内消息通知发送 | `qyapi_chat_manage_send` | 发送消息 |
| 机器人消息发送 | `qyapi_robot_message_send` | 机器人发送消息 |
| 读取消息 | `qyapi_get_chat_message` | 读取消息内容 |

### 4.3 申请权限

点击「申请权限」，等待审批通过。

---

## 第五步：配置事件订阅（Stream 模式）

### 5.1 什么是 Stream 模式？

**Stream 模式**是钉钉开放平台提供的一种基于 WebSocket 长连接的集成方式：

| 特性 | 说明 |
|------|------|
| ✅ 无需公网 IP | 内网环境也能接入 |
| ✅ 无需域名 | 不需要配置域名 |
| ✅ 无需 HTTPS | 不需要 SSL 证书 |
| ✅ 自动重连 | 断线后自动恢复 |
| ✅ 简化配置 | 只需集成 SDK |

### 5.2 工作原理

```
┌─────────────────────────────────────────────────────────────┐
│                         钉钉云                               │
│                                                              │
│   用户消息 ──→ 钉钉开放平台 ──→ Stream Gateway               │
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

### 5.3 配置 Stream 模式

1. 在应用详情页，找到「事件订阅」
2. 选择「**Stream 模式**」
3. 无需配置回调地址

### 5.4 添加订阅事件

在事件配置中添加以下事件：

| 事件名称 | 事件标识 | 用途 |
|---------|---------|------|
| 机器人消息 | `chat_add_user` | 用户与机器人建立会话 |
| 收到消息 | `chat_add_message` | 收到用户消息 |

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

启动后，cc-connect 会自动与钉钉建立 Stream 长连接。你会在日志中看到：

```
level=INFO msg="dingtalk: stream connected" client_id=dingxxxxxxxxxxxxxxx
level=INFO msg="platform started" project=my-project platform=dingtalk
level=INFO msg="cc-connect is running" projects=1
```

---

## 第七步：发布应用

### 7.1 提交审核

1. 在应用详情页，点击「版本管理与发布」
2. 点击「创建版本」
3. 填写版本号和更新说明
4. 点击「申请发布」

### 7.2 等待审核

- **企业内部应用**：通常立即可用
- **企业应用**：需要管理员审批

---

## 第八步：添加机器人到会话

### 8.1 单聊使用

1. 在钉钉中，点击右上角「+」→「添加机器人」
2. 搜索你创建的机器人
3. 添加后即可发送消息

### 8.2 群聊使用

1. 进入目标群聊
2. 点击群设置 → 「群机器人」
3. 添加你创建的机器人

---

## 使用示例

配置完成后，你可以在钉钉中这样使用：

```
用户: 帮我分析一下当前项目的结构

cc-connect: 🤔 思考中...
cc-connect: 🔧 执行: Bash(ls -la)
cc-connect: ✅ 这是一个 Node.js 项目，包含以下目录...
```

---

## Stream 模式 vs Webhook 模式

| 对比项 | Stream 模式 | Webhook 模式 |
|-------|-------------|--------------|
| 公网 IP | ❌ 不需要 | ✅ 需要 |
| 域名 | ❌ 不需要 | ✅ 需要 |
| HTTPS 证书 | ❌ 不需要 | ✅ 需要 |
| 反向代理 | ❌ 不需要 | ✅ 需要 |
| 配置复杂度 | 简单 | 较复杂 |
| 连接方式 | WebSocket | HTTP 回调 |
| 适用场景 | 本地开发、内网 | 生产环境 |

---

## 常见问题

### Q: Stream 模式和 Webhook 模式如何选择？

- **开发/测试环境**：推荐 Stream 模式，无需公网资源
- **生产环境**：两者都可以，Stream 模式配置更简单

### Q: 长连接断开怎么办？

cc-connect 内置了自动重连机制，断开后会自动尝试重新连接。

### Q: 消息发送后没有响应？

检查以下项目：
1. cc-connect 服务是否正常运行
2. Stream 连接是否建立成功（查看日志）
3. 事件订阅是否配置正确

### Q: 提示权限不足？

确保已在「权限管理」中申请并获得了所有必要权限。

### Q: 如何调试？

使用钉钉开放平台的「调试工具」进行测试。

---

## 参考链接

- [钉钉开放平台](https://open.dingtalk.com/)
- [钉钉开放平台文档](https://open.dingtalk.com/document/)
- [Stream 模式介绍](https://open.dingtalk.com/document/development/introduction-to-stream-mode)
- [Stream 模式协议接入说明](https://open.dingtalk.com/document/direction/stream-mode-protocol-access-description)
- [机器人开发指南](https://open.dingtalk.com/document/org/robot-message-subscription)
- [Spring Boot Stream 模式教程](https://m.blog.csdn.net/andrew_dear/article/details/140853791)
- [Python Stream 模式开发指南](https://m.blog.csdn.net/gitblog_00219/article/details/155120234)

---

## 下一步

- [接入飞书](./feishu.md)
- [接入 Telegram](./telegram.md)
- [接入 Slack](./slack.md)
- [接入 Discord](./discord.md)
- [返回首页](../README.md)
