# Telegram 交互式菜单系统设计文档

**日期**: 2026-03-16
**状态**: 待实施
**项目**: cc-connect / platform/telegram

---

## 1. 目标

为 cc-connect Telegram Bot 实现一套完全基于鼠标点击的交互式菜单系统，让用户无需手动输入任何命令参数即可完成所有操作。菜单支持中文说明、二级导航、翻页和个性化自定义。

---

## 2. 用户体验目标

- **零输入操作**：所有功能通过点击按钮完成，包括参数选择
- **中文友好**：所有菜单项、描述、状态信息使用中文
- **移动端优先**：按钮布局适合手机屏幕（2列网格为主）
- **状态可见**：主菜单显示当前模型、权限模式等关键状态

---

## 3. 功能范围

### 3.1 核心功能（P0）

1. `/menu` 命令作为统一入口，在 Telegram 命令列表中排第一位
2. 两级导航：主菜单（5大类）→ 子菜单（命令列表）→ 执行/选项
3. 原地更新（edit message）：点击后更新同一条消息，不发新消息
4. 翻页：上下翻页按钮，每页5条
5. Telegram 命令列表全中文描述

### 3.2 核心功能（P1）

6. `⚙️ 自定义` 面板：在 /menu 内管理固定快捷命令、显示/隐藏分类、选择展示哪些自定义命令

### 3.3 不在范围内

- 修改现有命令的行为逻辑（仅增加 UI 入口）
- 支持其他平台（仅 Telegram）
- 通过对话方式动态创建新命令（自定义命令通过 config.toml 定义，菜单仅负责选择显示哪些）

---

## 4. 菜单结构

### 4.1 主菜单（第一级）

```
🤖 cc-connect 控制面板
当前：claude-sonnet-4-6 · bypassPermissions

[💬 会话]    [🤖 AI设置]
[📋 任务]    [🔧 系统]
[    ⚡ 高级功能    ]
[    ⚙️ 自定义菜单  ]
```

标题行显示当前模型名和权限模式，由 `MenuNavigationHandler`（见第6节）在渲染时注入。

### 4.2 五大分类与命令映射

| 分类 | 回调key | 包含命令（中文名） |
|------|---------|-----------------|
| 💬 会话管理 | `session` | 新建 / 列表 / 切换 / 删除 / 搜索 / 重命名 / 当前 / 历史 |
| 🤖 AI设置 | `ai` | 切换模型 / 权限模式 / 切换语言 / 推理强度 / 切换提供商 / 静默模式 |
| 📋 任务 | `task` | 记忆文件 / 定时任务 / 压缩上下文 / 语音合成 / 停止 |
| 🔧 系统 | `system` | 状态 / 用量 / 帮助 / 版本 / 配置 / 诊断 / 升级 / 重启 |
| ⚡ 高级功能 | `advanced` | Shell / 绑定中继 / 工作区 / 技能 / 自定义命令 / 别名 / 允许工具 / 心跳 |

### 4.3 子菜单布局

```
🤖 AI设置
点击命令直接执行

[🧠 切换模型]  [🔒 权限模式]
[🌐 切换语言]  [💡 推理强度]
[    🔌 切换提供商    ]

[      ◀ 返回主菜单      ]
```

规则：
- 2列网格排列，最后一列不满时合并占满一行
- 末行固定为「◀ 返回主菜单」按钮（`menu:main` 回调）
- 点击命令后发**新消息**展示结果（菜单消息不覆盖）

### 4.4 列表选项（带翻页）

适用命令：切换模型、切换会话、删除会话、切换提供商、搜索结果。

```
🧠 选择模型
第 1/2 页 · 当前：claude-sonnet-4-6

[✅ claude-sonnet-4-6 ]
[   claude-opus-4-6   ]
[   claude-haiku-4-5  ]
[   claude-sonnet-4-5 ]
[   gpt-4o            ]

[◀ 上页]  [↩ 返回]  [下页 ▶]
```

规则：
- 每页5条，单列展示
- 当前选中项加 ✅ 前缀
- 上页不可用时显示为灰色（仍渲染按钮但 callback_data 为 `menu:noop`）
- 每条选项的回调为 `menu:sel:{cmd}:{idx}`，idx 为全局索引（跨页唯一）

---

## 5. 自定义菜单面板（⚙️）

### 5.1 入口

主菜单底部固定「⚙️ 自定义菜单」按钮。

### 5.2 功能选项

```
⚙️ 自定义菜单

[📌 固定快捷命令]
[👁️ 显示/隐藏分类]
[➕ 展示自定义命令]
[🔤 修改命令列表描述]
[↺  恢复默认设置  ]

[◀ 返回主菜单]
```

**📌 固定快捷命令**：选择最多4个命令固定显示在主菜单顶部「我的快捷」行。从所有内置命令中用 toggle 按钮选择，每次点击立即保存。

**👁️ 显示/隐藏分类**：通过 toggle 按钮（✅/⬜）控制5个分类的可见性。「会话管理」始终可见不可隐藏。每次点击立即保存并刷新菜单。

**➕ 展示自定义命令**：从 config.toml 中已定义的 `[[commands]]` 和已安装的 skills 中，选择哪些追加到对应分类的子菜单中。用 toggle 按钮选择，保存后自动归入「高级功能」分类。

**🔤 修改命令列表描述**：展示当前 Telegram 命令列表（`setMyCommands`）中各命令的描述，列出可编辑项。用户在 Telegram 命令列表里看到的文字通过此功能自定义，修改后立即调用 `RegisterCommands` 生效。编辑方式：点击某命令描述项，bot 回复「请发送新的描述文字（最多50字）」，用一次性输入状态机接收下一条文字消息。

**↺ 恢复默认**：二次确认后清除该 chat_id 的所有配置，恢复出厂默认，并调用 `RegisterCommands` 重置命令描述。

### 5.3 数据持久化

配置存入 `bot.db`，新建 `menu_config` 表：

```sql
CREATE TABLE IF NOT EXISTS menu_config (
    chat_id     INTEGER NOT NULL,
    key         TEXT    NOT NULL,
    value       TEXT    NOT NULL,      -- JSON 格式
    updated_at  INTEGER NOT NULL,
    PRIMARY KEY (chat_id, key)
);
```

key 枚举值及 value 格式：

| key | value 示例 | 说明 |
|-----|-----------|------|
| `pinned` | `["new","stop","status","model"]` | 固定快捷命令名列表，最多4个 |
| `hidden_cats` | `["advanced"]` | 隐藏的分类 key 列表 |
| `custom_cmds` | `["daily","weekly"]` | 展示的自定义命令名列表 |
| `cmd_descriptions` | `{"menu":"打开控制面板","new":"新建会话"}` | 覆盖的命令描述 |

按 `chat_id` 隔离，重启后保留。

---

## 6. 技术实现方案

### 6.1 核心接口：MenuNavigationHandler（新增到 core/interfaces.go）

复用 `CardNavigationHandler` 的相同模式，在 `core/interfaces.go` 新增：

```go
// MenuState holds the information needed to render the menu header.
type MenuState struct {
    Model    string
    Mode     string
    Project  string
}

// MenuPage is the rendering result returned by MenuNavigationHandler.
type MenuPage struct {
    Title   string           // 消息主标题
    Subtitle string          // 副标题（如当前状态）
    Buttons [][]ButtonOption // inline keyboard 二维数组
}

// MenuNavigationHandler is called by the platform when a menu callback arrives.
// action uses "menu:" prefixed strings (e.g. "menu:main", "menu:cat:ai").
// sessionKey identifies the chat session for state lookup.
type MenuNavigationHandler func(action string, sessionKey string) *MenuPage

// MenuNavigable is an optional interface for platforms that support
// the interactive /menu panel.
type MenuNavigable interface {
    SetMenuNavigationHandler(h MenuNavigationHandler)
}
```

Engine 在启动时检查平台是否实现 `MenuNavigable`，若是则注册 handler：

```go
// core/engine.go — Start() 方法中
if mn, ok := p.(MenuNavigable); ok {
    mn.SetMenuNavigationHandler(e.handleMenuNavigation)
}
```

`e.handleMenuNavigation` 函数：接收 action + sessionKey，通过 `e.sessionContextForKey(sessionKey)` 查询对应 agent 实例，再通过 `ModelSwitcher`/`ModeSwitcher` 等接口获取当前状态，`Project` 字段来自 `e.name`。函数内耗时操作（如 `AvailableModels()`）使用 `e.ctx` 的子 context 配合3秒 timeout，与 `handleCardNav` 保持一致。返回 `*MenuPage`，由平台渲染为 Telegram inline keyboard。

**注意**：`MessageUpdater` 接口（用于流式预览更新，接收 `*telegramPreviewHandle` 类型）与菜单原地更新的类型不匹配，因此菜单的 `editMessageText` 调用直接在 `platform/telegram/menu.go` 内部发出，不通过 `MessageUpdater` 接口。

### 6.2 原地更新：message_id 存储

`Platform` 结构体新增字段：

```go
menuMsgIDs sync.Map // key: int64(chatID), value: int(messageID)
```

流程：
1. 发送菜单消息后，将返回的 `message.MessageID` 存入 `menuMsgIDs`
2. 收到 `menu:` 回调时，查询 `menuMsgIDs` 获取 message_id，调用 `editMessageText`
3. 若 message_id 不存在或 edit 失败（消息被删除），降级为发送**新消息**，并更新 `menuMsgIDs`

每个 chat_id 只保留最近一条菜单消息 ID（覆盖写）。

**callback 处理时序**（避免 Telegram 10秒超时）：
1. 收到 `menu:` callback → 立即调用 `answerCallbackQuery`（空提示，清除加载动画）
2. 同步调用 `MenuNavigationHandler(action, sessionKey)` 获取 `*MenuPage`
3. 调用 `editMessageText` 原地更新菜单（或降级发新消息）

步骤 1 必须在步骤 2 之前执行，确保 Telegram 端不显示超时错误。步骤 2-3 的耗时操作（如 `AvailableModels()`）不影响 Telegram 的 callback 超时，因为 answer 已提前发出。

### 6.3 回调数据格式（全部在64字节限制内）

```
menu:main               → 返回主菜单           (9字节)
menu:cat:session        → 会话管理子菜单        (17字节)
menu:cat:ai             → AI设置子菜单          (12字节)
menu:cat:task           → 任务子菜单            (14字节)
menu:cat:system         → 系统子菜单            (16字节)
menu:cat:advanced       → 高级功能子菜单        (18字节)
menu:cat:custom         → 自定义面板            (16字节)
menu:exec:new           → 执行无参命令          (14字节)
menu:list:model:0       → 模型列表第0页         (18字节)
menu:list:session:0     → 会话列表第0页         (20字节)
menu:sel:model:3        → 选择模型第3项         (17字节)
menu:sel:session:3      → 选择会话第3项         (19字节)
menu:noop               → 无操作（禁用按钮）    (9字节)
menu:custom:pin         → 固定快捷配置          (15字节)
menu:custom:hide        → 隐藏分类配置          (16字节)
menu:custom:add         → 自定义命令配置        (15字节)
menu:custom:desc        → 命令描述配置          (16字节)
menu:custom:reset       → 恢复默认              (17字节)
menu:pin:toggle:new     → toggle固定命令        (19字节)
menu:hide:toggle:ai     → toggle隐藏分类        (19字节)
```

**约束**：`menu:exec:` 仅用于**无参数命令**（new/stop/status/help/version/compress/memory/usage/current/history/doctor/upgrade/restart）。需要参数的命令（model/switch/delete/provider/lang/mode/reasoning）必须走 `menu:list:` + `menu:sel:` 流程，动态内容**禁止**拼入 callback_data。

### 6.4 执行命令：构造合成 Message

当 `handleMenuCallback` 处理 `menu:exec:{cmd}` 时，从 `callbackQuery` 构造合成 `core.Message`：

```go
func (p *Platform) buildSyntheticMessage(cb *tgbotapi.CallbackQuery, text string) *core.Message {
    chatID := cb.Message.Chat.ID
    return &core.Message{
        SessionKey: p.buildSessionKey(chatID, cb.From.ID),
        Platform:   "telegram",
        MessageID:  strconv.FormatInt(int64(cb.Message.MessageID), 10),
        UserID:     strconv.FormatInt(cb.From.ID, 10),
        UserName:   cb.From.UserName,
        Content:    text,  // e.g. "/model"
        ReplyCtx:   replyContext{chatID: chatID, messageID: cb.Message.MessageID},
    }
}
```

然后调用 `p.handler(p, syntheticMsg)`，复用现有的 engine message 处理链。

**ReplyCtx 策略**：`replyContext.messageID` 置 0（不 reply_to 任何消息），命令结果作为普通新消息发出，避免结果消息引用菜单消息产生视觉噪音。

**执行后刷新菜单**：`menu:exec` 触发的命令执行完成后，自动调用一次 `MenuNavigationHandler("menu:main", sessionKey)` 并 `editMessageText` 刷新菜单，使主菜单副标题中的状态（模型、权限模式）反映最新值。有文字回复的命令（如 `/new`、`/stop`）正常发出回复消息，与菜单刷新并行不悖。

### 6.5 列表类命令的数据来源

`MenuNavigationHandler` 在处理 `menu:list:{cmd}:{page}` 时，通过 engine 内部现有逻辑获取数据：

| 命令 | 数据来源 |
|------|---------|
| `model` | `agent.(ModelSwitcher).AvailableModels(ctx)` |
| `session` | `agent.ListSessions(ctx)` |
| `provider` | `agent.(ProviderSwitcher).ListProviders()` |
| `lang` | 硬编码枚举：zh/en/zh-tw/ja/es |
| `mode` | `agent.(ModeSwitcher).PermissionModes()` |
| `reasoning` | `agent.(ReasoningEffortSwitcher).AvailableReasoningEfforts()` |

所有列表数据在 handler 调用时**实时获取**，不缓存。

**选择后的返回页码**：用户选择某项（`menu:sel:{cmd}:{idx}`）执行后，始终返回该命令列表第 0 页（第1页），不记忆翻页状态。此为 MVP 行为，P2 迭代可考虑记忆页码。

### 6.6 架构分层

新增文件：

```
platform/telegram/
├── telegram.go          # 修改：新增 menuMsgIDs、SetMenuNavigationHandler、
│                        #       handleMenuCallback()、buildSyntheticMessage()
├── menu.go              # 新文件：MenuPage 渲染为 tgbotapi.InlineKeyboardMarkup
│                        #   renderMainMenu(), renderCategoryMenu(),
│                        #   renderListMenu(), renderCustomMenu()
└── menu_config.go       # 新文件：menu_config 表 CRUD
                         #   LoadMenuConfig(), SaveMenuConfig(), ResetMenuConfig()

core/interfaces.go       # 修改：新增 MenuState, MenuPage,
                         #       MenuNavigationHandler, MenuNavigable

core/engine.go           # 修改：Start() 注册 MenuNavigable handler
                         #       新增 handleMenuNavigation() 函数
```

`core/` 改动严格遵守不引入 platform/agent 包的规则。

### 6.7 Telegram 命令列表中文描述

启动时 `RegisterCommands` 注册以下命令（`/menu` 置顶），其余通过 `/menu` 访问：

```
/menu        打开控制面板
/new         新建对话
/stop        停止当前任务
/status      查看当前状态
/list        查看会话列表
/help        帮助说明
```

用户通过「⚙️ → 修改命令列表描述」可覆盖以上描述文字，配置持久化后每次启动调用 `RegisterCommands` 重新注册。

### 6.8 一次性输入状态机（用于修改命令描述）

在 `Platform` 结构体中维护：

```go
pendingInput sync.Map // key: int64(chatID), value: pendingInputState
```

```go
type pendingInputState struct {
    kind    string    // "cmd_desc_edit:{cmdName}"
    expires time.Time // 60秒超时
}
```

流程：用户点击某命令描述项 → bot 发消息提示输入新描述 → 设置 pending state → 下一条文字消息（非命令）被拦截处理 → 保存配置 → 调用 RegisterCommands → 清除 pending state。

60秒超时后自动清除 pending state，避免干扰正常对话。

---

## 7. 边界情况处理

| 场景 | 处理方式 |
|------|---------|
| 回调到达时菜单消息已被删除 | edit 失败时降级为发新消息，更新 menuMsgIDs |
| 用户快速双击按钮（并发 edit） | 第二次 edit 若返回"message is not modified"静默忽略 |
| answerCallbackQuery 超时 | 收到回调立即调用 `answerCallbackQuery`（空提示），再异步处理逻辑 |
| menu:exec 触发耗时命令 | answerCallbackQuery 后异步执行，菜单消息不阻塞 |
| 所有分类被隐藏 | 「会话管理」分类不可隐藏，toggle 时禁用该选项 |
| model/session 列表为空 | 显示"暂无可选项"提示文字，仅渲染返回按钮 |
| 自定义命令描述超50字 | 截断到50字并提示用户 |
| pending input 超时 | 60秒后清除，用户输入被正常路由到 agent |

---

## 8. i18n

所有菜单标题、分类名、命令按钮文字、提示语定义为新的 `MsgKey` 常量，需为 ZH/EN/ZH-TW/JA/ES 五种语言添加翻译。命令按钮的中文名在 ZH 语言下显示，EN 语言下显示英文命令名。

---

## 9. 测试策略

- `menu.go`：单元测试各 render 函数，断言返回的 `ButtonOption` 数组内容和回调字符串格式
- `menu_config.go`：集成测试 CRUD，使用 in-memory SQLite
- `telegram.go handleMenuCallback`：单元测试用 stub `MenuNavigationHandler`，验证 edit/send 调用路径
- `core/engine.go handleMenuNavigation`：单元测试各 action 的 MenuPage 输出

---

## 10. 实施阶段

| 阶段 | 内容 |
|------|------|
| P0-1 | core/interfaces.go 新增 MenuState/MenuPage/MenuNavigable |
| P0-2 | core/engine.go 注册 handler + handleMenuNavigation（主菜单+5子菜单） |
| P0-3 | platform/telegram/menu.go 渲染函数 |
| P0-4 | platform/telegram/telegram.go 集成回调处理、message_id 存储 |
| P0-5 | Telegram 命令列表中文描述 + /menu 置顶 |
| P0-6 | 列表类命令翻页（model/session/provider/lang/mode/reasoning） |
| P1-1 | platform/telegram/menu_config.go SQLite CRUD |
| P1-2 | ⚙️ 自定义面板：固定快捷 + 显示/隐藏分类 + 展示自定义命令 |
| P1-3 | 修改命令描述（一次性输入状态机） |

---

## 11. 成功标准

- [ ] 输入 `/menu` 出现主菜单按钮面板，显示当前模型和权限模式
- [ ] 点击任意大类，菜单消息原地更新为子命令列表
- [ ] 点击子命令，执行对应逻辑，结果以新消息回复
- [ ] 列表类命令支持上下翻页，每页5条，当前选中项有 ✅ 标记
- [ ] 有「返回」按钮可回到上一级
- [ ] 所有按钮说明为中文（ZH 语言下）
- [ ] Telegram `/` 命令列表中 `/menu` 排第一，所有命令有中文描述
- [ ] 进入「⚙️ 自定义」可 pin 快捷命令、隐藏分类、选择展示自定义命令
- [ ] 修改命令描述后立即在 Telegram 命令列表生效
- [ ] 重启后自定义配置保留
- [ ] `go test ./...` 全部通过
