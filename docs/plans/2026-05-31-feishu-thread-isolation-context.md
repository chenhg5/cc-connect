# 飞书 Reply in Thread 会话隔离与上下文设计

**日期：** 2026-05-31
**状态：** 第一阶段已实现

## 目标

让飞书群聊里的普通 Reply 和显式 Reply in Thread 有清楚区分。

普通 Reply 应该继续作为“引用上下文”处理，不应该自动开新 agent session。Reply in Thread 是用户显式打开的子会话，更适合作为独立 agent session 边界。

这个设计需要满足几件事：

- 默认行为不破坏已有用户。
- 不引入跨 adapter 的抽象误导。
- 用户扫码接入 Feishu Bot 后，不应该再被要求去开放平台手动申请额外权限，至少第一阶段不能依赖这种额外步骤。

## 当前行为

当前飞书实现里混在一起的是两件事：

- `thread_isolation = true` 时，群聊 session key 来自 `RootId`，没有 `RootId` 时回退到当前 `MessageId`。
- 普通 Reply 的上下文注入通过 `ParentId` 追溯引用链，然后把格式化结果放进 `core.Message.ExtraContent`。

也就是说，当前飞书的 `thread_isolation` 实际更像“按回复链隔离”：

- 普通群消息没有 `RootId`：按当前 `MessageId` 隔离。
- 普通 Reply 有 `RootId`：按回复根消息隔离。
- Reply in Thread 也会被隔离，但它不是因为被识别成飞书原生 thread，而是走了同一套 `RootId` 逻辑。

代码现在已经会打印这些字段：

- `root_id`
- `parent_id`
- `thread_id`

关键缺口是：`ThreadId` 才代表飞书原生话题 / thread 身份。`RootId` 和 `ParentId` 只能说明回复链关系，不能说明用户显式打开了一个 thread。

## 配置设计

保留现有总开关：

```toml
thread_isolation = true
```

新增飞书专属策略配置：

```toml
feishu_thread_isolation_mode = "reply_and_thread"
```

可选值：

- `reply_and_thread`
- `thread_only`

默认值：

```toml
feishu_thread_isolation_mode = "reply_and_thread"
```

默认值保持当前行为，减少上游兼容风险。

### 为什么是飞书专属配置

这个配置不应该做成所有 adapter 共用。

不同平台对 reply、thread、topic、channel 的语义不一样：

- 飞书有普通 Reply，也有原生 Reply in Thread。
- Discord 的 thread 是真实 thread channel。
- Telegram 的 Forum Topic 是原生 topic，但普通群 Reply 不应该拆 session。
- Slack 的 reply 本身就通过 `thread_ts` 表达 thread-like 关系，而且当前 Slack adapter 没有暴露 `thread_isolation`。

所以这里使用 adapter 自己的配置更清楚：

```toml
feishu_thread_isolation_mode = "thread_only"
```

如果以后 Discord 也需要类似策略，再由 Discord 自己定义，例如 `discord_thread_isolation_mode`。不要让一个看似通用的配置承载不同平台的不同含义。

## Session Key 行为

### `reply_and_thread`

这是兼容当前实现的模式。

当 `thread_isolation = true`，并且入站消息来自飞书群聊：

1. 如果有 `RootId`，使用 `RootId`。
2. 如果没有 `RootId`，使用当前 `MessageId`。
3. session key 继续使用现有 `root` 命名空间。

示例：

```text
feishu:<chatID>:root:<rootOrMessageID>
```

### `thread_only`

只有飞书原生 thread / 话题消息才创建独立 agent session。

当 `thread_isolation = true`，`feishu_thread_isolation_mode = "thread_only"`，且入站群消息有非空 `ThreadId`：

```text
feishu:<chatID>:thread:<threadID>
```

当 `ThreadId` 为空时，不创建 thread session，回退到普通非 thread session：

- `share_session_in_channel = true`：`feishu:<chatID>`
- 否则：`feishu:<chatID>:<userID>`

因此在 `thread_only` 模式下，普通 Reply 不会开新 session，只作为引用上下文处理。

## 回复路由

实时收到用户消息时，adapter 手里有当前消息的 `message_id`，所以可以继续用飞书 Reply API 回复。

如果 session key 被识别为 thread session，并且当前 `replyContext` 里有 message ID，就设置：

```go
ReplyInThread(true)
```

需要注意：

`ThreadId` 不是 `MessageId`。只存 `thread:<threadID>` 对实时消息处理够用，因为当前入站消息的 message ID 在 `replyContext` 里。但对 cron、延迟发送、重建 reply context 这类离线路径可能不够。

第一阶段不承诺 Feishu thread-only session 的 cron 能可靠回复回原 thread，除非后续额外持久化一个可用的 message ID。

## 上下文注入

第一阶段不要新增一个很大的 `feishu_context_mode` 枚举。

像 `reply_chain`、`recent_chat`、`recent_thread`、`auto` 这类值，对实现者精确，但对用户不够像人话。尤其 `recent_thread` 很容易被理解成“最近的 thread 列表”，而不是“当前 thread 里的最近消息”。

### 保留现有 Reply 引用上下文

继续保留当前普通 Reply 行为：

- 如果入站消息有 `ParentId`，拉取被回复消息 / 回复链。
- 把格式化后的内容放进 `ExtraContent`。
- core 在发送给 agent 前，把 `ExtraContent` 拼到用户正文前面。

这个行为在 `feishu_thread_isolation_mode = "thread_only"` 下也应该继续生效。也就是说，普通 Reply 不拆 session，但仍然给 agent 看被引用的内容。

### 后续可选：最近消息上下文

如果后续要做最近消息上下文，不使用复杂枚举，而是使用更直白的布尔开关：

```toml
feishu_include_recent_messages = false
feishu_recent_messages_limit = 10
feishu_recent_messages_max_chars = 4000
```

含义：

> 当 bot 处理飞书消息时，把最近几条飞书消息作为补充上下文给 agent。

用户不需要理解 chat/thread 容器差异，adapter 自己判断：

- 如果入站消息有 `ThreadId`，拉当前飞书 thread 里的最近消息。
- 否则，如果是群聊消息，拉当前群聊里的最近消息。
- 如果拉取失败，不阻断当前消息处理，只记录日志并降级为只处理当前消息。

这个能力不应该依赖 `thread_isolation`。普通群聊里 @bot 时，即使没有开启 thread session 隔离，也可能需要最近群消息作为上下文。

最近消息上下文是第二阶段功能。第一阶段先把 session 边界做准。

## 权限与工具调用

从目前扫码创建 Bot 的权限截图看，飞书 QR onboarding 似乎会授予比较完整的机器人权限，包括读取单聊和群聊消息。实现上仍然要把历史消息读取当成 best-effort：

- 不因为最近消息权限不可用而启动失败。
- 不因为历史消息 API 返回权限或可见性错误而阻断当前消息。
- 日志里记录足够排查的信息。

第一阶段不依赖 agent 自己调用飞书工具。cc-connect 已经持有 Feishu app 凭证，adapter 可以直接调用飞书 API。

后续可以考虑“提示 agent 自己查飞书上下文”，但那需要另外确认：

- 本机是否安装了对应 CLI。
- CLI 是否已经以正确身份鉴权。
- agent 是否能知道 chat/thread ID。
- 当前 agent permission mode 是否允许这种工具调用。

因此第一阶段不做 agent 侧工具提示。

## 实现计划

### 第一阶段：飞书 Thread 隔离模式

1. 在 `platform/feishu.Platform` 增加 `feishuThreadIsolationMode string`。
2. 在 `newPlatform` 里读取 `opts["feishu_thread_isolation_mode"]`。
3. 空值默认成 `reply_and_thread`。
4. 非法值直接返回清晰错误。
5. 修改 `makeSessionKey`：
   - `reply_and_thread`：保持当前 `RootId` 行为。
   - `thread_only`：只使用 `ThreadId`；没有 `ThreadId` 时回退普通 session key。
6. 确保 thread session helper 能识别 `thread:` 命名空间。
7. 确保 `thread_only` 下普通 Reply 回退到非 thread session 后，仍会注入引用上下文。
8. 更新 `config.example.toml`。
9. 更新 `docs/feishu.md`。

### 第二阶段：可选最近消息上下文

第一阶段验证后再做。

1. 增加 `feishu_include_recent_messages`。
2. 增加消息条数和字符数限制。
3. 实现 Feishu 最近消息拉取逻辑。
4. 把最近消息格式化成“不可信外部聊天上下文”。
5. 排除当前触发消息，避免重复。
6. 对权限、可见性、API 错误做优雅降级。

## 测试矩阵

### 配置解析

- 空 `feishu_thread_isolation_mode` 默认是 `reply_and_thread`。
- `reply_and_thread` 合法。
- `thread_only` 合法。
- 未知值返回错误。

### Session Key 派生

`thread_isolation = true` 且 `reply_and_thread`：

- 群聊根消息使用 `root:<messageID>`。
- 普通 Reply 使用 `root:<rootID>`。
- Reply in Thread 即使同时有 `RootId` 和 `ThreadId`，仍使用 `root:<rootID>`，保持兼容。

`thread_isolation = true` 且 `thread_only`：

- Reply in Thread 使用 `thread:<threadID>`。
- 普通 Reply 有 `ParentId` / `RootId` 但没有 `ThreadId` 时，回退普通 session key。
- 普通群消息没有 `ThreadId` 时，回退普通 session key。
- `share_session_in_channel = true` 时，回退为 `feishu:<chatID>`。

`thread_isolation = false`：

- `feishu_thread_isolation_mode` 不产生影响。

### 回复路由

- thread session 回复会设置 `ReplyInThread(true)`。
- 普通 fallback session 回复不会设置 `ReplyInThread(true)`。

### 引用上下文注入

- `thread_only` 模式下，普通 Reply 仍然拉取并注入引用上下文。
- Reply in Thread 默认不走普通 Reply quote chain，除非后续设计明确改变。

### Active Thread 附件跟进

- `thread:<threadID>` session 可以被标记为 active。
- 已 active 的飞书 thread 内，附件类消息可以沿用现有免再次 @bot 逻辑。
- 非 thread session 不会被标记为 active。

## 需要确认的问题

1. 实际飞书事件字段样本：
   - 普通群消息
   - 普通 Reply
   - Reply in Thread

   当前设计假设 Reply in Thread 会稳定带非空 `ThreadId`。

2. 是否需要在 Feishu thread session key 里额外持久化 message ID。

   实时回复可以依赖当前入站消息 ID，但 cron / 延迟发送可能需要真实可用的 Feishu message ID。

3. 第二阶段最近消息上下文要走 Feishu API 还是本地滚动缓存。

   API 方案更简单，也避免长期存储无关群消息；但依赖飞书历史消息可见性。

4. 需要实验：扫码创建出来的 Bot，在截图权限集下，是否能用 `container_id_type=thread` 读取 thread 内消息。

## 非目标

- 不改 core session 管理。
- 不新增跨 adapter 的 thread isolation mode。
- 不改变 Discord、Slack、Telegram 行为。
- 第一阶段不要求用户手动去飞书开放平台申请额外权限。
- 第一阶段不加入 agent 侧飞书 CLI / 工具调用提示。
