# 飞书 (Feishu/Lark) 接入指南

本文档介绍如何将 **cc-connect** 接入飞书，让你可以通过飞书机器人远程调用 Claude Code。

## 前置要求

- 飞书账号（个人或企业均可）
- 一台可运行 cc-connect 的设备（无需公网 IP）
- Claude Code 已安装并配置完成

> 💡 **优势**：使用长连接模式，无需公网 IP、无需域名、无需反向代理（ngrok/frp）

---

## 快速配置（推荐）

如果你已经装好 `cc-connect`，可以直接用内置命令完成“新建机器人/关联已有机器人”，并自动写回 `config.toml`：

```bash
# 推荐：统一入口
cc-connect feishu setup --project my-project
cc-connect feishu setup --project my-project --app cli_xxx:sec_xxx

# 强制模式（一般不需要）
cc-connect feishu new --project my-project
cc-connect feishu bind --project my-project --app cli_xxx:sec_xxx
```

三者区别：

| 命令 | 作用 | 何时用 |
|------|------|--------|
| `setup` | 统一入口：无凭证走 `new`，有凭证走 `bind` | **默认就用这个** |
| `new` | 强制二维码新建（不接受 `--app`） | 明确要重走扫码新建 |
| `bind` | 强制关联已有凭证（必须 `app_id/app_secret`） | 明确只做凭证关联 |

补充：

- `setup --app ...` 与 `bind --app ...` 功能等价。

- `setup/new` 会在终端打印二维码和 URL，使用飞书/Lark 手机 App 扫码完成创建。
- `--project` 不存在时会自动创建该项目；若项目存在但没有 `feishu/lark` 平台，也会自动补一个。
- 写回配置时仅定点更新目标字段（`app_id`、`app_secret`、`allow_from` 等），尽量保留原有注释与排版。
- 该流程会回填凭证；通过扫码新建时，飞书通常会同时预配权限与事件订阅。
- 仍建议在开放平台核验：应用已发布、权限状态正常、可用范围符合预期。

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
# domain = "https://open.feishu.cn" # 可选：覆盖运行时 API/WebSocket 域名
# enable_feishu_card = true  # 可选：关闭后统一回退纯文本回复
# thread_isolation = true    # 可选：按飞书 thread/root 隔离群聊会话
# progress_style = "legacy"  # 可选：legacy | compact | card
# done_emoji = "none"          # 可选：agent 完成回复后添加的表情回复（如 "Done"）；设为 "none" 可禁用
```

> 如果应用没有交互卡片权限，或后台未配置卡片回调，可将 `enable_feishu_card = false`，让所有命令统一走纯文本回复，避免卡片发送失败后用户看不到内容。
> 如果开启 `thread_isolation = true`，群聊里每个根消息 / reply thread 会对应一个独立 agent session；私聊行为保持原样。
> `progress_style = "compact"` 会把思考/工具进度合并到一条可更新消息里，减少刷屏；`legacy` 保持原有逐条发送；`card` 会使用结构化卡片（标题 + 进度块）持续更新同一条消息，观感比纯文本更清晰。
> `domain` 只影响运行时 API / WebSocket 请求地址；CLI `setup/new/bind` 的引导域名仍然使用内置默认值。
> `done_emoji` 设置后，agent 每次完成回复时会在用户消息上添加指定表情（如 `"Done"` → ✅）。先移除 "OnIt" 表情（如果有），再添加 done 表情。在 quiet 模式下特别有用，因为飞书卡片原地更新不触发推送，done 表情可以通知用户 agent 已完成。设为 `"none"` 或不配置则禁用。

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
| 获取群组中用户@机器人消息 | `im:message.group_at_msg:readonly` | 接收群消息 |
| 读取用户发给机器人的单聊消息 | `im:message.p2p_msg:readonly` | 接收私聊消息 |
| 获取群组中所有消息（敏感权限） | `im:message.group_msg` | 读取群消息内容 |
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
| 卡片回调 | `card.action.trigger` | 响应交互卡片按钮点击（权限确认、provider 切换等） |

> ⚠️ **重要**：如果不订阅 `card.action.trigger` 事件，用户点击卡片上的按钮（如权限确认、provider 选择等）时将无法正常响应，飞书客户端可能会显示加载超时或错误提示。如果暂时无法添加该事件，可以在配置中设置 `enable_feishu_card = false` 关闭交互卡片功能，所有交互将回退到纯文本模式。

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

## Mention 功能

开启 `resolve_mentions = true` 后，机器人发出的消息中 `@显示名` 会自动替换为飞书原生 at 标签。

### 配置

```toml
[projects.platforms.options]
resolve_mentions = true
```

### 语法

直接使用 `@显示名`，无需特殊标记：

```
@张三 请查看巡检报告
```

### 使用示例

**Cron 定时任务：**

```bash
cc-connect cron add \
  --cron "0 9 * * *" \
  --prompt "执行每日巡检报告，完成后通知 @张三 和 @李四 查看" \
  --desc "每日巡检"
```

**AI 对话中：**

AI 输出中包含 `@某人` 时，发送到飞书前会自动匹配并替换。

### 工作原理

1. 开启 `resolve_mentions` 后，发送消息前拉取群成员列表（懒加载，首次才拉）
2. 成员列表缓存 1 小时，减少 API 调用
3. 按名字长度从长到短匹配（`@张三丰` 优先于 `@张三`），避免部分匹配
4. 未匹配到的 `@xxx` 保留原文不处理
5. 根据消息类型自动选择正确的飞书 at 语法（文本消息 vs 卡片消息）

### 权限要求

需要以下飞书应用权限之一：

- `im:chat`（获取与更新群组信息）
- `im:chat:readonly`（获取群组信息）
- `im:chat.members:read`（查看群成员）

### 注意事项

- 名字匹配为精确匹配（`@张三` 只匹配显示名恰好是「张三」的成员）
- 同名成员取第一个匹配到的
- 被 at 的人必须是当前群的成员
- 未开启 `resolve_mentions` 时不会触发任何成员查询

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

### Q: 点击卡片按钮没有反应或报错？

cc-connect 默认使用交互卡片显示权限确认、provider 选择等操作。如果点击按钮后无响应、显示加载超时或报错，请检查：

1. **事件订阅**：确认已在飞书开放平台订阅了 `card.action.trigger` 事件（详见第五步）
2. **应用发布**：修改事件订阅后需要重新发布应用版本
3. **权限配置**：确保应用有 `im:message:send_as_bot` 权限

**快速解决方案**：如果暂时无法配置卡片回调，可以在 `config.toml` 中关闭交互卡片：

```toml
[projects.platforms.options]
enable_feishu_card = false
```

关闭后，所有交互将回退为纯文本模式，权限确认等操作通过直接回复文字完成。

### Q: 提示权限不足？

确保已在「权限管理」中申请并获得了所有必要权限，并发布了新版本。

### Q: 扫码页显示 OpenClaw 文案，是不是配置错了？

通常是飞书注册模板侧的展示文案，不影响返回 `app_id/app_secret` 和接入 cc-connect。

### Q: 如何调试消息？

在飞书开放平台「开发调试」→「调试工具」中可以模拟发送消息进行测试。

---

## 参考链接

- [飞书开放平台](https://open.feishu.cn/)
- [飞书开放平台文档](https://open.feishu.cn/document/)
- [机器人开发指南](https://open.feishu.cn/document/ukTMukTMukTM/uYjNwUjL2YDM14iN2ATN)
- [事件订阅文档](https://open.feishu.cn/document/ukTMukTMukTM/uUTNz4SN1MjL1UzM)
- [权限列表](https://open.feishu.cn/document/server-docs/application-scope/scope-list)
- [OpenClaw 飞书接入教程](https://bytedance.larkoffice.com/docx/MFK7dDFLFoVlOGxWCv5cTXKmnMh)
- [飞书 WebSocket 长连接模式](https://m.blog.csdn.net/u014177256/article/details/158267848)

---

## 下一步

- [接入钉钉](./dingtalk.md)
- [接入微博](./weibo.md)
- [接入 Telegram](./telegram.md)
- [接入 Slack](./slack.md)
- [接入 Discord](./discord.md)
- [返回首页](../README.md)
