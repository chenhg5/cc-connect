# 使用指南

cc-connect 完整功能使用指南。

## 目录

- [会话管理](#会话管理)
- [权限模式](#权限模式)
- [API Provider 管理](#api-provider-管理)
- [模型选择](#模型选择)
- [工作目录切换（`/dir`、`/cd`）](#工作目录切换dircd)
- [引用查看（`/show`）](#引用查看show)
- [飞书配置 CLI](#飞书配置-cli)
- [微信个人号配置 CLI](#微信个人号配置-cli)
- [Claude Code Router 集成](#claude-code-router-集成)
- [语音消息（语音转文字）](#语音消息语音转文字)
- [语音回复（文字转语音）](#语音回复文字转语音)
- [图片与文件回传](#图片与文件回传)
- [定时任务 (Cron)](#定时任务-cron)
- [多机器人中继](#多机器人中继)
- [守护进程模式](#守护进程模式)
- [多工作区模式](#多工作区模式)
- [Web 管理后台（Beta）](#web-管理后台beta)
- [Bridge — 外部适配器接入（Beta）](#bridge--外部适配器接入beta)
- [配置参考](#配置参考)

---

## 会话管理

每个用户拥有独立的会话和完整的对话上下文。通过斜杠命令管理：

| 命令 | 说明 |
|------|------|
| `/new [名称]` | 创建新会话 |
| `/list` | 列出当前项目的会话 |
| `/switch <id>` | 切换到指定会话 |
| `/current` | 查看当前会话 |
| `/history [n]` | 查看最近 n 条消息 |
| `/usage` | 查看账号/模型限额使用情况 |
| `/provider [...]` | 管理 API Provider |
| `/model [switch <alias>]` | 列出可用模型或按别名切换 |
| `/dir [路径]` | 查看或切换 Agent 工作目录 |
| `/show <引用>` | 按引用查看文件、目录或代码片段 |
| `/allow <工具名>` | 预授权工具 |
| `/reasoning [等级]` | 查看或切换推理强度（Codex）|
| `/mode [名称]` | 查看或切换权限模式 |
| `/stop` | 停止当前执行 |
| `/help` | 显示可用命令 |

会话中 Agent 请求工具权限时，回复 **允许** / **拒绝** / **允许所有**。

也可以为项目开启“空闲后自动切换新会话”：

```toml
[[projects]]
name = "demo"
reset_on_idle_mins = 60
```

开启后，如果用户长时间未发消息，下一条普通消息会自动进入一个新的会话；旧会话仍会保留在 `/list` 中，不会被删除。

### 切换模型时保留历史

`/model` 切换模型时保留当前会话——agent 会在新模型下继续对话（不额外消耗 token）。注意模型切换作用于共享的 agent 实例——如果多个平台使用同一个 project，模型变更会影响所有平台。

---

## 权限模式

所有 Agent 支持运行时切换权限模式，通过 `/mode` 命令。

### Claude Code 模式

| 模式 | 配置值 | 行为 |
|------|--------|------|
| 默认 | `default` | 每次工具调用需确认 |
| 接受编辑 | `acceptEdits` / `edit` | 文件编辑自动通过 |
| 自动模式 | `auto` | 由 Claude 自动判断何时需要确认 |
| 计划模式 | `plan` | 只规划不执行 |
| YOLO | `bypassPermissions` / `yolo` | 全部自动通过 |

### Codex 模式

| 模式 | 配置值 | 行为 |
|------|--------|------|
| 建议 | `suggest` | 仅受信命令自动执行 |
| 自动编辑 | `auto-edit` | 模型自行决定 |
| 全自动 | `full-auto` | 自动通过 + 沙箱保护 |
| YOLO | `yolo` | 跳过所有审批 |

### Cursor Agent 模式

| 模式 | 配置值 | 行为 |
|------|--------|------|
| 默认 | `default` | 工具调用前询问 |
| 强制执行 | `force` / `yolo` | 自动批准所有 |
| 规划模式 | `plan` | 只读分析 |
| 问答模式 | `ask` | 问答风格，只读 |

### Gemini CLI 模式

| 模式 | 配置值 | 行为 |
|------|--------|------|
| 默认 | `default` | 每次需确认 |
| 自动编辑 | `auto_edit` / `edit` | 编辑自动通过 |
| 全自动 | `yolo` | 自动批准所有 |
| 规划模式 | `plan` | 只读规划 |

### Qoder CLI / OpenCode / iFlow CLI

| 模式 | 配置值 | 行为 |
|------|--------|------|
| 默认 | `default` | 标准权限 |
| YOLO | `yolo` | 跳过所有检查 |

### 配置示例

```toml
[projects.agent.options]
mode = "default"
# allowed_tools = ["Read", "Grep", "Glob"]
```

运行时切换：
```
/mode          # 查看当前和可用模式
/mode yolo     # 切换到 YOLO 模式
/mode default  # 切回默认
```

---

## API Provider 管理

运行时切换 API Provider，无需重启。

### 配置 Provider

```toml
[projects.agent.options]
work_dir = "/path/to/project"
provider = "anthropic"

[[projects.agent.providers]]
name = "anthropic"
api_key = "sk-ant-xxx"

[[projects.agent.providers]]
name = "relay"
api_key = "sk-xxx"
base_url = "https://api.relay-service.com"
model = "claude-sonnet-4-20250514"

[[projects.agent.providers.models]]
model = "claude-sonnet-4-20250514"
alias = "sonnet"

[[projects.agent.providers.models]]
model = "claude-opus-4-20250514"
alias = "opus"

[[projects.agent.providers.models]]
model = "claude-haiku-3-5-20241022"
alias = "haiku"

# MiniMax — 兼容 OpenAI 接口，1M 超长上下文
[[projects.agent.providers]]
name = "minimax"
api_key = "your-minimax-api-key"
base_url = "https://api.minimax.io/v1"
model = "MiniMax-M2.7"

# Bedrock、Vertex 等
[[projects.agent.providers]]
name = "bedrock"
env = { CLAUDE_CODE_USE_BEDROCK = "1", AWS_PROFILE = "bedrock" }
```

### CLI 命令

```bash
cc-connect provider add --project my-backend --name relay --api-key sk-xxx --base-url https://api.relay.com
cc-connect provider list --project my-backend
cc-connect provider remove --project my-backend --name relay
cc-connect provider import --project my-backend  # 从 cc-switch 导入
```

### 聊天命令

```
/provider                   查看当前 Provider
/provider list              列出所有
/provider add <名称> <key> [url] [model]
/provider remove <名称>
/provider switch <名称>
/provider <名称>            切换快捷方式
```

### 环境变量映射

| Agent | api_key → | base_url → |
|-------|-----------|------------|
| Claude Code | `ANTHROPIC_API_KEY` | `ANTHROPIC_BASE_URL` |
| Codex | `OPENAI_API_KEY` | `OPENAI_BASE_URL` |
| Gemini CLI | `GEMINI_API_KEY` | 使用 `env` 字段 |
| OpenCode | `ANTHROPIC_API_KEY` | 使用 `env` 字段 |
| iFlow CLI | `IFLOW_API_KEY` | `IFLOW_BASE_URL` |

---

## 模型选择

通过 `[[providers.models]]` 为每个 Provider 预配置可选模型列表。每个条目包含 `model`（模型标识符）和可选的 `alias`（别名，显示在 `/model` 中）。

### 配置模型

```toml
[[projects.agent.providers]]
name = "openai"
api_key = "sk-xxx"

[[projects.agent.providers.models]]
model = "gpt-5.3-codex"
alias = "codex"

[[projects.agent.providers.models]]
model = "gpt-5.4"
alias = "gpt"

[[projects.agent.providers.models]]
model = "gpt-5.3-codex-spark"
alias = "spark"
```

### 聊天命令

```
/model              列出可用模型（格式：alias - model）
/model switch <alias>      按别名切换模型
/model switch <name>       按完整名称切换模型
/model <alias>             兼容旧写法，仍然可用
```

配置了 `models` 时，`/model` 直接显示该列表，不发起 API 请求。未配置时，自动从 Provider API 获取或使用内置备选列表。

---

## 工作目录切换（`/dir`、`/cd`）

可直接在聊天中切换 Agent 下一次会话的工作目录。

### 聊天命令

```
/dir                    查看当前工作目录和最近历史
/dir <路径>             切换到指定路径（相对或绝对）
/dir <序号>             按历史序号切换目录
/dir -                  返回上一个目录
/dir help               查看命令用法
/cd <路径>              `/dir <路径>` 的兼容别名
```

### 行为说明

- 目录切换会作用于当前项目的下一次会话。
- 相对路径基于当前 Agent 工作目录解析。
- 目录历史按项目隔离，可通过序号快速切换。
- `/cd` 为兼容保留，建议优先使用 `/dir`。

示例：

```text
/dir ../another-repo
/dir 2
/dir -
```

---

## 本地引用展示配置（`[projects.references]`）

可选启用对 Agent 输出中的本地文件 / 目录 / 代码位置引用进行标准化与重渲染，提升在 IM 平台中的可读性。

这是一个 **opt-in** 功能：

- 未配置 `[projects.references]` 时，现有行为保持不变
- 只有命中 `normalize_agents` 和 `render_platforms` 时，才会启用

### 推荐配置

```toml
[projects.references]
normalize_agents = ["all"]
render_platforms = ["all"]
display_path = "relative"
marker_style = "emoji"
enclosure_style = "code"
```

### 字段说明

- `normalize_agents`
  - 控制哪些 Agent 输出参与这套引用处理
  - 当前初始支持：`codex`、`claudecode`、`all`

- `render_platforms`
  - 控制在哪些平台发送前应用展示重写
  - 当前初始支持：`feishu`、`weixin`、`all`

- `display_path`
  - 控制路径主体的显示层级
  - 可选值：`absolute`、`relative`、`basename`、`dirname_basename`、`smart`

- `marker_style`
  - 控制前缀标记样式
  - 可选值：`none`、`ascii`、`emoji`

- `enclosure_style`
  - 控制路径主体的包裹样式
  - 可选值：`none`、`bracket`、`angle`、`fullwidth`、`code`

### 支持的引用输入

当前初始支持识别这些常见形式：

- 绝对路径
- 相对路径
- 文件 / 目录引用
- `path:line`
- `path:line:col`
- `path:start-end`
- `path#L42`
- Markdown 本地文件链接
- Claude 风格的反引号绝对路径引用

### 行为说明

- 只处理 Agent 输出：
  - thinking
  - final response
  - stream preview
  - progress / card 中的 Agent 文本

- 不处理：
  - 系统消息
  - `/workspace`、`/dir`、`/status` 等命令回复
  - raw tool result

- 网页链接会保持原样，不会被本地引用重写逻辑污染

### 推荐默认值说明

当前最推荐的组合是：

- `display_path = "relative"`
- `marker_style = "emoji"`
- `enclosure_style = "code"`

这样通常会得到类似：

- `📄 ui/recovery_contact_form.tsx:11`
- `📁 docs/spec.v1/`

如果不希望使用 emoji，更推荐：

- `display_path = "dirname_basename"`
- `marker_style = "ascii"`
- `enclosure_style = "code"`

---

## 引用查看（`/show`）

可直接基于一个文件 / 目录 / 代码位置引用查看内容，而不必手写 `/shell sed ...`。

### 聊天命令

```text
/show <路径>                  查看文件前 80 行
/show <路径:行号>             查看该行附近上下文
/show <路径:起止行>           查看指定 range
/show <目录路径/>             查看一级目录列表
```

支持的输入形式包括：

- 绝对路径
- 相对路径（相对当前 Agent 工作目录）
- `path:line`
- `path:line:col`
- `path:start-end`
- `path#L42`
- Markdown 本地文件链接，如：
  - `[file.ts](/abs/path/file.ts#L42)`

### 行为说明

- 文件，无位置：
  - 默认显示文件前 80 行
- `path:line` / `path#L42`：
  - 默认显示该位置附近上下文
- `path:start-end`：
  - 默认显示该 range
- 目录：
  - 默认显示一级目录内容

说明：

- `/show` 只解析“纯引用文本”，不解析前端展示层包装后的 `📄 ...` / `[FILE] ...` 这类样式
- `/show` 属于本地文件系统查看命令，与 `/shell`、`/dir` 类似，默认受 `admin_from` 权限控制
- 执行 Shell 命令支持 `!` 快捷前缀：`!ls -la` 等同于 `/shell ls -la`，`! --timeout 300 npm install` 可指定超时时间

示例：

```text
/show ui/recovery_contact_form.tsx
/show svc/recovery_session_reconciler.go:12
/show svc/recovery_session_reconciler_test.go:8-17
/show docs/spec.v1/
```

---

## 飞书配置 CLI

可以直接通过 CLI 完成飞书/Lark 机器人创建或关联，并自动写回 `config.toml`：

```bash
# 推荐：统一入口
cc-connect feishu setup --project my-project
cc-connect feishu setup --project my-project --app cli_xxx:sec_xxx

# 强制模式（一般不需要）
cc-connect feishu new --project my-project
cc-connect feishu bind --project my-project --app cli_xxx:sec_xxx
```

区别说明：
- `setup`：统一入口。没传凭证时等价 `new`，传了 `--app` 时等价 `bind`。
- `new`：强制二维码新建，不接受 `--app`。
- `bind`：强制关联已有机器人，必须提供凭证。

行为说明（通用）：
- `setup` 默认走二维码新建；传入 `--app` 时自动切换到关联已有机器人。
- `--project` 不存在会自动创建。
- 项目存在但没有 `feishu/lark` 平台时会自动补一个平台配置。
- 命令会回填凭证（`app_id` / `app_secret`）；扫码新建场景下飞书通常会预配权限和事件订阅。
- 建议在飞书开放平台再核验一次发布状态与可用范围。
- 运行时平台配置还支持可选 `domain` 覆盖 Feishu/Lark API 域名；这不会改变 `setup/new/bind` 的引导地址。

---

## 微信个人号配置 CLI

个人微信走 **ilink 机器人网关**（HTTP 长轮询，与 OpenClaw `openclaw-weixin` 同类）。可直接用 CLI 扫码登录或绑定已有 Token，并写回 `config.toml`。

**完整图文流程与配置项说明见：[docs/weixin.md](./weixin.md)。**

```bash
# 推荐：终端展示二维码 + URL，微信扫码确认后自动写配置
cc-connect weixin setup --project my-project

# 已有 Bearer Token（例如从 OpenClaw 导出）
cc-connect weixin bind --project my-project --token '<token>'
cc-connect weixin setup --project my-project --token '<token>'

# 强制只走扫码（不接受 --token）
cc-connect weixin new --project my-project
```

区别说明：

- `setup`：未传 `--token` 时走扫码；传了 `--token` 时等同绑定并可选校验。
- `new`：强制扫码。
- `bind`：强制绑定，必须 `--token`。

行为说明：

- `--project` 不存在时会自动创建项目；项目里没有 `weixin` 平台时会自动追加一块 `[[projects.platforms]]`。
- 扫码成功后会写入 `token`，以及网关返回的 `base_url`（若有）、`ilink_bot_id` → `account_id` 等。
- 默认 `--set-allow-from-empty=true`：若 `allow_from` 为空，会用扫码用户的 ilink ID 预填，便于收紧权限。
- 绑定时默认调用 `getUpdates` 校验 Token；可用 `--skip-verify` 跳过。
- 首次使用后请在微信里 **先发一条消息**，以便缓存 `context_token`，否则可能无法回复。

常用参数：`--api-url`、`--cdn-url`、`--timeout`、`--qr-image`、`--route-tag`、`--bot-type`、`--debug`（详见 `cc-connect weixin help` 或 [weixin.md](./weixin.md)）。

---

## Claude Code Router 集成

[Claude Code Router](https://github.com/musistudio/claude-code-router) 可将请求路由到不同模型提供商。

### 安装配置

1. 安装：`npm install -g @musistudio/claude-code-router`

2. 配置 `~/.claude-code-router/config.json`：
```json
{
  "APIKEY": "your-secret-key",
  "Providers": [
    {
      "name": "deepseek",
      "api_base_url": "https://api.deepseek.com/chat/completions",
      "api_key": "sk-xxx",
      "models": ["deepseek-chat", "deepseek-reasoner"],
      "transformer": { "use": ["deepseek"] }
    }
  ],
  "Router": {
    "default": "deepseek,deepseek-chat",
    "think": "deepseek,deepseek-reasoner"
  }
}
```

3. 启动：`ccr start`

4. 配置 cc-connect：
```toml
[projects.agent.options]
router_url = "http://127.0.0.1:3456"
router_api_key = "your-secret-key"
```

---

## 语音消息（语音转文字）

发送语音消息，自动转文字。

**支持平台：** 飞书、企业微信、Telegram、LINE、Discord、Slack

**前置条件：** OpenAI/Groq API Key，`ffmpeg`

### 配置

```toml
[speech]
enabled = true
provider = "openai"    # 或 "groq"
language = ""          # "zh"、"en" 或留空自动检测

[speech.openai]
api_key = "sk-xxx"

# [speech.groq]
# api_key = "gsk_xxx"
# model = "whisper-large-v3-turbo"
```

### 安装 ffmpeg

```bash
# Ubuntu/Debian
sudo apt install ffmpeg

# macOS
brew install ffmpeg
```

---

## 语音回复（文字转语音）

将 AI 回复合成语音发送。

**支持平台：** 飞书

### 配置

```toml
[tts]
enabled = true
provider = "qwen"        # 或 "openai"
voice = "Cherry"
tts_mode = "voice_only"  # "voice_only" | "always"
max_text_len = 0

[tts.qwen]
api_key = "sk-xxx"
```

### TTS 模式

| 模式 | 行为 |
|------|------|
| `voice_only` | 仅当用户发语音时才语音回复 |
| `always` | 始终语音回复 |

切换：`/tts always` 或 `/tts voice_only`

---

## 图片与文件回传

当 Agent 在本地生成了图片、PDF、日志包、报表等文件，需要把结果直接发回当前聊天时，可以使用 `cc-connect send` 的附件模式。

**当前支持平台：**
- 飞书
- Telegram

### 什么时候需要先执行 setup

如果当前 Agent 不是“原生 system prompt 注入”类型，升级到包含该功能的版本后，建议先在聊天里执行一次：

```text
/bind setup
```

或者：

```text
/cron setup
```

这两个命令写入的是同一份 cc-connect 指令。执行任意一个即可。这样 Agent 才会知道：
- 普通文本回复直接正常输出
- 生成附件后用 `cc-connect send --image/--file` 回传

如果你以前已经执行过 setup，也建议升级后重新执行一次，以刷新到最新指令。

### 配置开关

如果你想禁用 agent 主动回传附件，可以在 `config.toml` 里加入：

```toml
attachment_send = "off"
```

默认值是 `on`。这个开关与 agent 的 `/mode` 独立，只影响 `cc-connect send --image/--file` 这条图片/文件回传路径。

### CLI 用法

```bash
cc-connect send --image /absolute/path/to/chart.png
cc-connect send --file /absolute/path/to/report.pdf
cc-connect send --file /absolute/path/to/report.pdf --image /absolute/path/to/chart.png
```

说明：
- `--image` 用于图片附件。
- `--file` 用于任意文件附件。
- `--message` 可选，用于先发一段说明文字，再发附件。
- `--image` 和 `--file` 都可以重复多次。
- 建议使用绝对路径，避免 Agent 当前工作目录变化导致找不到文件。
- 如果设置了 `attachment_send = "off"`，图片/文件回传会被拒绝，但普通文本回复仍然正常。

### 典型场景

1. Agent 生成了截图或图表，需要直接发给用户。
2. Agent 生成了 PDF、Markdown 导出、日志包或补丁文件，需要作为附件交付。
3. Agent 想告诉用户“结果已生成”，同时附上一个或多个文件。

### 注意事项

- 这个命令是给“附件回传”用的，不要拿它代替普通文本回复。
- 只能发送本机上 Agent 可访问到的文件。
- 必须存在活跃会话；如果当前项目没有活动聊天上下文，命令会失败。
- 平台本身仍可能有文件大小或文件类型限制。

---

## 定时任务 (Cron)

创建自动执行的定时任务。

### 聊天命令

```
/cron                                          列出所有任务
/cron add <分> <时> <日> <月> <周> <任务描述>      创建任务
/cron del <id>                                 删除任务
/cron enable <id>                              启用
/cron disable <id>                             禁用
```

示例：
```
/cron add 0 6 * * * 帮我收集 GitHub trending 并总结
```

### CLI 命令

```bash
cc-connect cron add --cron "0 6 * * *" --prompt "总结 GitHub trending" --desc "每日趋势"
cc-connect cron list
cc-connect cron del <job-id>
```

可选：`--session-mode new-per-run` 每次触发使用新的 agent 会话（默认 `reuse` 与旧行为一致）。`--timeout-mins N` 设置单次调度最长等待分钟数（`0` 表示不限制；省略为 30 分钟）。

### 自然语言（Claude Code）

> "每天早上6点帮我总结 GitHub trending"

Claude Code 会自动创建定时任务。对依赖记忆文件的其他 Agent，先执行一次 `/cron setup` 或 `/bind setup`，效果相同。

---

## 多机器人中继

跨平台机器人通信，群聊多机器人协作。

### 群聊绑定

```
/bind              查看绑定
/bind claudecode   添加 claudecode 项目
/bind gemini       添加 gemini 项目
/bind -claudecode  移除 claudecode
```

### 机器人间通信

```bash
cc-connect relay send --to gemini "你觉得这个架构怎么样？"
```

---

## 守护进程模式

后台服务运行。

```bash
cc-connect daemon install --config ~/.cc-connect/config.toml
cc-connect daemon start
cc-connect daemon stop
cc-connect daemon restart
cc-connect daemon status
cc-connect daemon logs [-f]
cc-connect daemon uninstall
```

---

## 多工作区模式

一个 bot 服务多个工作区，每个频道一个独立工作目录。

### 配置

```toml
[[projects]]
name = "my-project"
mode = "multi-workspace"
base_dir = "~/workspaces"

[projects.agent]
type = "claudecode"
```

### 命令

```
/workspace                    查看当前绑定
/workspace bind <名称>        绑定本地文件夹
/workspace init <git-url>     克隆仓库并绑定
/workspace unbind             解除绑定
/workspace list               列出所有绑定
```

### 工作原理

- 频道名 `#project-a` → 自动绑定 `base_dir/project-a/`
- 每个频道有独立的会话和 Agent 状态

---

## Web 管理后台（Beta）

> **状态：Beta。** 此功能自 v1.2.2-beta.5 起可用，UI 和 API 在后续版本中可能调整。

内嵌在二进制中的全功能管理界面，支持项目管理、会话管理、定时任务编辑、全局设置、聊天界面、多语言等。

### 快速启用（聊天命令）

最简单的方式，在聊天中发送：

```
/web setup
```

该命令会自动在 `config.toml` 中启用 **Management API** 和 **Bridge**，生成 token，并返回访问地址。首次启用后需要执行 `/restart` 使配置生效。

启用后，打开返回的地址（默认 `http://localhost:9820`），用显示的 token 登录即可。

### 查看状态

```
/web           # 或 /web status — 查看 Web 管理后台的地址和启用状态
```

### 手动配置

在 `config.toml` 中添加：

```toml
[management]
enabled = true
port = 9820                     # 管理后台监听端口
token = "your-secret-token"     # 登录 token；/web setup 会自动生成
cors_origins = ["*"]            # 允许的 CORS 来源；留空则不设置 CORS 头
```

然后重启 cc-connect。

### 构建选项

Web 前端资源默认编译进二进制。如果想排除（减小约 1MB）：

```bash
make build-noweb
# 或
go build -tags 'no_web' ./cmd/cc-connect
```

使用 `no_web` 构建时，`/web` 命令会提示 Web 管理后台不可用。

### Management API

API 与 Web UI 共用同一端口。基础 URL：`http://<host>:<port>/api/v1`

所有 API 请求需要 `Authorization: Bearer <token>` 请求头。

主要接口：

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/api/v1/status` | 系统状态（版本、运行时间、已连接平台） |
| `POST` | `/api/v1/restart` | 重启 cc-connect |
| `POST` | `/api/v1/reload` | 重新加载配置 |
| `GET` | `/api/v1/projects` | 项目列表 |
| `GET` | `/api/v1/sessions?project=<name>` | 查询项目的会话列表 |
| `GET` | `/api/v1/cron` | 定时任务列表 |
| `GET` | `/api/v1/settings` | 获取全局设置 |
| `PATCH` | `/api/v1/settings` | 更新全局设置 |

完整 API 参考：[management-api.md](./management-api.md)（[中文版](./management-api.zh-CN.md)）

---

## Bridge — 外部适配器接入（Beta）

> **状态：Beta。** 此功能自 v1.2.2-beta.5 起可用，协议在后续版本中可能调整。

Bridge 提供 WebSocket + REST 服务，让外部适配器（自定义 UI、机器人、脚本等）可以接入 cc-connect —— 发送消息、接收 Agent 事件、管理会话。

### 通过聊天启用

`/web setup` 命令会同时启用 Bridge 和管理后台，无需额外操作。

### 手动配置

在 `config.toml` 中添加：

```toml
[bridge]
enabled = true
port = 9810                     # Bridge 监听端口（与管理后台分开）
token = "your-bridge-secret"    # WebSocket 和 REST 的认证 token
path = "/bridge/ws"             # WebSocket 端点路径
cors_origins = ["*"]            # 允许的 CORS 来源；留空则不设置 CORS
```

然后重启 cc-connect。

### 认证方式

所有 Bridge 连接需要 token 认证，支持三种方式：

- URL 参数：`?token=<bridge-token>`
- 请求头：`Authorization: Bearer <bridge-token>`
- 请求头：`X-Bridge-Token: <bridge-token>`

### WebSocket 接入

连接地址：

```
ws://<host>:<bridge-port>/bridge/ws?token=<bridge-token>
```

WebSocket 支持双向通信 —— 向 Agent 发送消息，并实时接收 Agent 的文本回复、工具调用、权限请求等事件。

### REST API

与 WebSocket 共用同一端口。

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/bridge/sessions?session_key=...&project=...` | 查询会话列表 |
| `POST` | `/bridge/sessions` | 创建新会话 |
| `GET` | `/bridge/sessions/{id}?session_key=...&project=...` | 获取会话详情及历史 |
| `DELETE` | `/bridge/sessions/{id}?session_key=...&project=...` | 删除会话 |
| `POST` | `/bridge/sessions/switch` | 切换当前活跃会话 |

完整协议参考：[bridge-protocol.md](./bridge-protocol.md)（[中文版](./bridge-protocol.zh-CN.md)）

### 端口汇总

| 服务 | 默认端口 | 配置块 |
|------|---------|--------|
| 管理后台（Web UI + API） | 9820 | `[management]` |
| Bridge（WebSocket + REST） | 9810 | `[bridge]` |

---

## 配置参考

完整配置示例见 [config.example.toml](../config.example.toml)。

### 项目结构

```toml
[[projects]]
name = "my-project"

[projects.agent]
type = "claudecode"  # 或 codex, cursor, gemini, qoder, opencode, iflow

[projects.agent.options]
work_dir = "/path/to/project"
mode = "default"
provider = "anthropic"

[[projects.platforms]]
type = "feishu"  # 或 dingtalk, telegram, slack, discord, wecom, weixin, line, qq, qqbot

[projects.platforms.options]
# 平台特定配置
```
