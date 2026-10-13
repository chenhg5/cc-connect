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

## 在聊天中创建后续机器人（此 fork）

本节是单 Bot 的创建与绑定入口。Agent Team 的角色成员、项目建群和自动协调使用另一套入口，见[团队使用指南](agent-team.md)；Team 成员推荐在管理者私聊中使用 `/team bot create`。

已有第一个机器人连接 cc-connect 后，可以在**与它的私聊**中发送：

```text
/new bot developer
```

`/new`、`/new 会话名称` 继续用于创建对话；新增的 `bot` 子命令创建独立的飞书应用机器人身份。
名称是 cc-connect 内的唯一标识，使用 1–48 位小写字母、数字、`-`、`_`，以字母或数字开头。
飞书客户端显示的应用名称由创建页面设置，本命令不会自动改名。

首次使用需要在第一个机器人的 `[[projects]]` 下配置 `admin_from`：先向它发送 `/whoami`，
将返回的 **User ID** 填入该字段，再重启 cc-connect。不要把其他应用下的 open_id 填到这里。
已有管理员配置可以直接使用；群聊、机器人发送的消息、未知发送者及非管理员都不能发起创建。
第一台机器人需允许私聊（`group_only = false`）。

1. 机器人回复飞书/Lark 创建授权链接。用手机打开链接，按页面完成创建确认；仍需要本人操作，默认等待至多 10 分钟。
2. cc-connect 获取新身份凭据后，将一个新的 `[[projects]]` 原子写入当前配置，并启动该机器人的连接，无需重启原有机器人。
3. 原机器人回复新机器人的会话链接及一次性 `/new claim ...` 命令。
4. 在**新机器人的私聊**中发送该绑定命令。此时才记录新应用下的管理员 ID，并允许 Agent 执行任务。
5. 绑定完成后，新机器人也可以使用 `/new bot reviewer` 创建下一个身份。

创建过程中可以继续聊天，用 `/new bot cancel` 取消当前创建请求。已经在飞书创建成功的应用不会被删除。
如果保存或启动连接失败，使用同一个 `/new bot 名称` 重试；已收到的凭据会从本机恢复，不会再次发起扫码创建。
原发起人可在绑定完成前用同名命令重新获取绑定指引。应用已创建但连接不可用时，也可以排除问题后重启 cc-connect。

新身份继承原机器人的 Agent 类型、完整 Agent 配置和工作区模式，拥有独立会话存储。
它不继承原应用的管理员 open_id、用户角色、平台凭据、定时心跳或终端观察任务。
当前新身份仅接受其已绑定管理员的消息；多角色互派任务、建群邀请及细化访问控制属于后续能力。

凭据只保存在本机配置中；待绑定的恢复记录位于 `data_dir/bot-registrations/`，文件权限为 `0600`，
成功绑定后删除恢复记录。聊天中不会发送 App Secret 或设备授权码，绑定命令也不会交给模型。
连接启动成功只表示已开始连接；应用发布、可用范围和飞书侧服务状态仍会影响实际收发。

本功能由聊天命令处理器直接执行，不依赖模型解析自然语言。

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
# single_card_reply = true   # 可选：聚合一次请求的进度和审批
# separate_final_card = true # 可选：完成后另发结论卡（同时启用上述聚合）
# thread_isolation = true    # 可选：按飞书 thread/root 隔离群聊会话
# group_chat_history_share = false  # 可选：共享未 @ 机器人的群消息作为下一次触发的上下文；消息本身不会触发回复
# progress_style = "legacy"  # 可选：legacy | compact | card
# ack_emoji = "Get"           # 可选：消息被接受处理或入队时立即添加并保留的确认表情；默认禁用
# reaction_emoji = "OnIt"      # 可选：agent 处理期间的临时表情，结束后移除
# done_emoji = "none"          # 可选：agent 完成回复后添加的表情回复（如 "Done"）；设为 "none" 可禁用
# image_batch_window_ms = 500  # 可选：连续多图合批窗口（默认 500ms，详见下文）
```

> 如果应用没有交互卡片权限，或后台未配置卡片回调，可将 `enable_feishu_card = false`，让所有命令统一走纯文本回复，避免卡片发送失败后用户看不到内容。
> 如果开启 `thread_isolation = true`，群聊里每个根消息 / reply thread 会对应一个独立 agent session；私聊行为保持原样。
> `group_chat_history_share = true` 时，cc-connect 只在内存中保留当前进程观察到的、允许访问的群聊 text/post 消息，并在下一次明确 @ 机器人且真正进入 agent turn 时注入；未 @ 的消息不会触发回复。`/status` 等由 cc-connect 处理的命令不会消费这段待处理上下文，`/new` 会清空对应主频道或话题的上下文。
> 在 multi-workspace 模式下，`thread_isolation = true` 也会让每个话题独立绑定 workspace；在话题内执行 `/workspace bind <name>` 不会影响同群的其他话题。已有的群级 binding 会保留为默认值，由尚未显式绑定的话题继承，因此回退到旧版本时仍可使用。
> `single_card_reply = true` 时，一次 Agent 请求持续更新当前卡片，内容超过单张容量时自动续发新卡，之前的内容保留在聊天记录中，不再使用分页按钮。再启用 `separate_final_card = true`，完成后另发“结论”卡，超长结论同样自动续卡。要求开启交互卡片和卡片回调；显示内容仍受 `display` 设置控制。新建 Team Bot 默认开启两项，普通配置默认关闭。
> `progress_style = "compact"` 会把思考/工具进度合并到一条可更新消息里，减少刷屏；`legacy` 保持原有逐条发送；`card` 会使用结构化卡片（标题 + 进度块）持续更新同一条消息，观感比纯文本更清晰。
> `domain` 只影响运行时 API / WebSocket 请求地址；CLI `setup/new/bind` 的引导域名仍然使用内置默认值。
> `ack_emoji = "Get"` 会在消息通过校验、被引擎接受处理或成功入队后异步添加确认表情，无需等待模型启动或前一轮结束。它表示“请求已接收”，会在完成、失败或取消后保留，不表示模型已开始执行或任务已成功。现有 `reaction_emoji`（默认 `"OnIt"`）仍表示实际处理，`done_emoji` 表示完成；若 `ack_emoji` 与 `reaction_emoji` 相同，该表情由接收确认保留，不再作为临时状态移除。
> 确认默认关闭；不配置、空值或 `"none"` 保持原有行为。权限或 @ 过滤、重复/过时消息、满队列拒绝、已处理的命令与无用户消息的定时任务不会产生确认。确认从引擎接受消息时开始；若后续消息仍在等待已有的会话启动锁，也会等待接受决定。表情 API 采用 5 秒超时的异步尽力请求，失败不影响模型处理；发送前的附件下载、消息解析以及多图合批仍需先完成，多图批次确认在最后一条（批次的主消息）上。
> **English:** Optional `ack_emoji = "Get"` adds a persistent receipt asynchronously once the engine accepts a message for processing or queueing, before waiting for agent startup/execution. It confirms acceptance, not execution or success, and survives completion/failure/cancellation. Omitted, empty or `"none"` keeps existing behavior. Processing (`reaction_emoji`) and completion (`done_emoji`) remain independent; if receipt and processing use the same emoji, the receipt owns it and typing cleanup does not remove it. Rejected/duplicate/stale messages, handled commands and synthetic scheduled work are not acknowledged. The receipt begins at engine acceptance; subsequent messages waiting on the existing session-startup lock still wait for admission. Receipt API calls have a five-second timeout and never block processing. Parsing/media preparation and image batching precede acceptance; a merged image batch acknowledges its newest canonical message.

> `done_emoji` 设置后，agent 每次完成回复时会在用户消息上添加指定表情（如 `"Done"` → ✅）。先清理临时处理表情（与接收确认相同的表情会保留），再添加 done 表情。在 quiet 模式下特别有用，因为飞书卡片原地更新不触发推送，done 表情可以通知用户 agent 已完成。设为 `"none"` 或不配置则禁用。
> `image_batch_window_ms` 控制连续多张图片合并成一条 agent 消息的等待窗口（默认 500ms）。飞书手机端一次连发多张图时，每张图是独立事件；cc-connect 会在窗口内将它们合并成一条多图消息再分发给 agent。如果你的网络/设备发送间隔超过 500ms 且仍被拆成多轮回复（每张图独立处理），可调高到 800–1200ms；如果以单图为主、希望响应更快，可适当调低。设为 `0` 时回退到默认 500ms。

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
| 获取群组中所有消息（敏感权限） | `im:message.group_msg` | 读取群消息内容 |
| 读取单聊消息 | `im:message.p2p_msg:readonly` | 读取私聊内容 |
| 以应用身份发送群消息 | `im:message:send_as_bot` | 发送消息回复用户 |

### 4.3 发布权限申请

配置完权限后，点击「申请发布」使权限生效。

如果启用了 `group_chat_history_share`，必须为应用申请并发布 `im:message.group_msg`，否则飞书只会向机器人推送被 @ 的群消息，未提及消息无法进入共享上下文。该功能不会回溯 cc-connect 启动前的历史，也不会持久化待处理消息。

---

## 第五步：配置事件与回调订阅（长连接模式）

### 5.1 进入事件与回调页面

左侧导航栏点击 **「事件与回调」**。

### 5.2 选择事件配置

在标签页中点击： **「事件配置」**。

在「订阅方式」中选择：

```
✅ 使用长连接接收事件
```

点击**保存**。

点击**添加事件**。

在事件配置中添加以下事件：

| 事件名称 | 事件标识 | 用途 |
|---------|---------|------|
| 接收消息 | `im.message.receive_v1` | 接收用户发送的消息 |

### 5.3 选择回调配置

在标签页中点击： **「回调配置」**。

在「订阅方式」中选择：

```
✅ 使用长连接接收事件
```

点击**保存**。

点击**添加回调**。

在回调配置中添加以下回调：

| 回调名称 | 回调标识 | 用途 |
|---------|---------|------|
| 卡片回调 | `card.action.trigger` | 响应交互卡片按钮点击（权限确认、provider 切换等） |

> ⚠️ **重要**：如果不订阅 `card.action.trigger` 回调，用户点击卡片上的按钮（如权限确认、provider 选择等）时将无法正常响应，飞书客户端可能会显示加载超时或错误提示。如果暂时无法添加该回调，可以在配置中设置 `enable_feishu_card = false` 关闭交互卡片功能，所有交互将回退到纯文本模式。

### 5.4 创建版本

点击 **「创建版本」** 发布新版本以应用事件与回调配置。

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

## 机器人间 @ 通知（`mention_map`）

`resolve_mentions` 通过匹配**群成员显示名**来解析 `@name`，但当目标是**另一个机器人 / Agent**（而非真人成员）时，机器人不一定出现在群成员列表中，名字匹配会失败。

`mention_map` 选项用于这种场景：手动把「显示名」映射到机器人的 `open_id`，让 cc-connect 直接生成原生飞书 `<at user_id="...">` 标签，触发真正的 @ 通知。

### 配置

```toml
[projects.platforms.options]
resolve_mentions = true                       # mention_map 依赖 resolve_mentions = true
mention_map = { BOT-B = "ou_bot_b_open_id", BOT-A = "ou_bot_a_open_id" }
```

> `mention_map` 与 `resolve_mentions` 是叠加关系，并非二选一：
> - `resolve_mentions` 负责按群成员显示名匹配（覆盖普通用户）
> - `mention_map` 负责显式 open_id 映射（覆盖不在群成员列表里的机器人）
> - 当同一个 `@name` 两者都能匹配时，**`mention_map` 优先级更高**，确保显式配置不会被群成员匹配覆盖。

### 使用示例

Agent 输出 `@BOT-B 请复核巡检报告` 时，cc-connect 在发送到飞书前会把它替换为：

```
<at user_id="ou_bot_b_open_id">BOT-B</at> 请复核巡检报告
```

BOT-B 机器人会收到飞书 @ 事件并被触发。

典型场景：

- **多 Agent 协作**：BOT-A 巡检发现问题 → 在回复里 @BOT-B 触发修复 Agent。
- **跨 Agent 通知**：长任务（Cron）由一个 Agent 完成后，@另一个 Agent 接力。
- **@ 机器人触发 Hook**：飞书机器人收到 @ 事件可触发 cc-connect 的会话路由。

### 如何获取机器人 open_id

机器人的 `open_id`（`ou_` 开头）**不是** App ID（`cli_` 开头），两者在飞书里是完全不同的标识符。「凭证与基础信息」页只显示 App ID / App Secret，**不显示** bot 的 `open_id`。

获取方式（按推荐顺序）：

1. **读 cc-connect 启动日志（最简单，适用于自己控制的应用）**
   cc-connect 启动时会自动调用 `/open-apis/bot/v3/info` 拉取自身 `open_id`，并打印：
   ```
   feishu: bot identified open_id=ou_xxxxxxxxxxxxxxxx
   ```
   直接复制日志里的值即可。

2. **调用「获取机器人信息」API（`/open-apis/bot/v3/info`）**
   用任意有效 `tenant_access_token` 发起请求：
   ```bash
   curl -H "Authorization: Bearer t-xxxx" https://open.feishu.cn/open-apis/bot/v3/info
   # 响应：{ "code": 0, "bot": { "open_id": "ou_xxx", ... } }
   ```

### 注意事项

- `mention_map` 必须配合 `resolve_mentions = true` 才会生效；单独配置 `mention_map` 不会触发解析。
- `@name` 必须与 `mention_map` 的 key 完全一致（区分大小写）。
- 飞书机器人的 `open_id` 是**应用级别**的，与群聊无关；同一个机器人在不同群的 `open_id` 一致。
- 被 @ 的机器人需要在**目标群里**，且该群已开启机器人能力，否则飞书不会派发 @ 事件。

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

### 任务执行进度与模型排队

项目的 compact / standard 模式在原任务卡展示负责人、预期交付、当前阶段、阶段和总用时、最近工作进展、计划步骤、下一步及最近三个工具结果。只使用公开说明和结构化执行事件，不展示内部推理；没有上报计划时明确显示“尚未上报执行计划”，不推测完成百分比。工具完成与任务验收分别记录。

执行卡默认展示摘要：负责人、当前状态、当前步骤、已完成步骤数和总用时。点击「执行详情」可展开查看交付要求、执行计划、下一步、最近执行结果和公开进展说明，再次点击收起。详情是当前执行状态，不是完整原始日志；工具日志仍可从任务详情获取。折叠使用飞书原生组件，不依赖翻页回调，也不会唤醒机器人或发送新消息。内容超过单卡容量时继续使用新卡，保留各段详情。审批操作、异常与长时间等待提示始终显示在折叠区外，最终结论也直接展示。

运行时每 15 秒刷新原卡并同步项目总览。无新执行事件 2 分钟时提示“进展待确认”，不会把仍在运行的工具直接判为失败；等待审批不会被此提示误判。Trae ACP 的队列诊断默认启用（可用 agent option `queue_diagnostics = false` 关闭），队列事件每 10 秒最多接收一次，只保留排队位置，不保存或显示队列 token。排队超过 5 分钟提示可继续等待，或先停止再更换模型；不会自动更换模型、自动重派，也不因心跳而重复唤醒管理者。

- `/status`：本会话当前执行状态；无需等待模型返回。
- `/project board`：所有成员的任务和执行进度。
- `/project task T001`：单个任务的交付要求、进度和验收状态。
- `/stop`：停止当前会话执行。在任务话题内发送可停止对应任务。
- `/project pause`：停止整个项目的成员和排队任务。

已存在的执行事件空闲超时和单轮总时限仍然有效；队列心跳仅更新连接活动时间，不增加计划完成数。卡片显示的队列位置是最近一次上报值，不是预计等待时间。升级后需要重启服务，新会话才会启用 ACP 队列诊断；重启中断的任务需核对已有交付后恢复。
