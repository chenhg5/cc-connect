# cc-connect

[![Discord](https://img.shields.io/discord/1478978944567869652?logo=discord&label=Discord)](https://discord.gg/kHpwgaM4kq)
[![Telegram](https://img.shields.io/badge/Telegram-Group-26A5E4?logo=telegram)](https://t.me/+odGNDhCjbjdmMmZl)
[![GitHub release](https://img.shields.io/github/v/release/chenhg5/cc-connect?include_prereleases)](https://github.com/chenhg5/cc-connect/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

[English](./README.md) | 中文

**在任何聊天工具里，远程操控你的本地 AI 编程助手。**

cc-connect 把运行在你开发机上的 AI 编程助手桥接到你日常使用的即时通讯工具 — 地铁上 review 代码、手机上启动任务、躺在床上 pair programming，都不是问题。

```
         你（手机 / 电脑 / 平板）
                    │
    ┌───────────────┼───────────────┐
    ▼               ▼               ▼
   飞书           Slack         Telegram  ...8 个平台
    │               │               │
    └───────────────┼───────────────┘
                    ▼
              ┌────────────┐
              │ cc-connect │  ← 你的开发机
              └────────────┘
              ┌─────┼─────┐
              ▼     ▼     ▼
         Claude  Gemini  Codex  ...7 个 Agent
          Code    CLI   OpenCode / iFlow
```

### 核心亮点

> 是时候卸载 OpenClaw 了 — cc-connect 让你同时拥有最强的那几个 AI 编程助手，而不只是一个。

- **7 大 AI Agent** — Claude Code、Codex、Cursor Agent、Qoder CLI、Gemini CLI、OpenCode、iFlow CLI，按需选用，也可以同时使用
- **8 大聊天平台** — 飞书、钉钉、Slack、Telegram、Discord、企业微信、LINE、QQ，大部分无需公网 IP
- **多机器人中继** — 在群聊中绑定多个机器人，让它们相互协作。问 Claude，再听 Gemini 的见解 — 同一个对话搞定
- **聊天即控制** — 切换模型 `/model`、切换权限 `/mode`、管理会话，全部通过斜杠命令完成
- **Agent 记忆** — 在聊天中直接读写 Agent 指令文件 `/memory`，无需回到终端
- **定时任务** — 自然语言创建 cron 任务，"每天早上6点帮我总结 GitHub trending" 即刻生效
- **语音 & 图片** — 发语音或截图，cc-connect 自动转文字和多模态转发
- **多项目管理** — 一个进程同时管理多个项目，各自独立的 Agent + 平台组合

<p align="center">
  <img src="docs/images/screenshot/cc-connect-lark.JPG" alt="飞书" width="32%" />
  <img src="docs/images/screenshot/cc-connect-discord.png" alt="Discord" width="32%" />
  <img src="docs/images/screenshot/cc-connect-wechat.JPG" alt="微信" width="32%" />
</p>
<p align="center">
  <em>左：飞书 &nbsp;|&nbsp; 中：Discord &nbsp;|&nbsp; 右：个人微信（通过企业微信关联）</em>
</p>

## 支持状态

| 组件 | 类型 | 状态 |
|------|------|------|
| Agent | Claude Code | ✅ 已支持 |
| Agent | Codex (OpenAI) | ✅ 已支持 |
| Agent | Cursor Agent | ✅ 已支持 |
| Agent | Gemini CLI (Google) | ✅ 已支持 |
| Agent | Qoder CLI | ✅ 已支持 |
| Agent | OpenCode | ✅ 已支持 |
| Agent | iFlow CLI | ✅ 已支持 |
| Agent | Crush | 🔜 计划中 |
| Agent | Goose (Block) | 🔜 计划中 |
| Agent | Aider | 🔜 计划中 |
| Agent | Kimi Code (月之暗面) | 🔭 探索中 |
| Agent | GLM Code / CodeGeeX (智谱AI) | 🔭 探索中 |
| Agent | MiniMax Code | 🔭 探索中 |
| Platform | 飞书 (Lark) | ✅ WebSocket 长连接 — 无需公网 IP |
| Platform | 钉钉 (DingTalk) | ✅ Stream 模式 — 无需公网 IP |
| Platform | Telegram | ✅ Long Polling — 无需公网 IP |
| Platform | Slack | ✅ Socket Mode — 无需公网 IP |
| Platform | Discord | ✅ Gateway — 无需公网 IP |
| Platform | LINE | ✅ Webhook — 需要公网 URL |
| Platform | 企业微信 (WeChat Work) | ✅ Webhook — 需要公网 URL |
| Platform | QQ (通过 NapCat/OneBot) | ✅ WebSocket，无需公网 IP — **Beta** |
| Platform | WhatsApp | 🔜 计划中 (Business Cloud API) |
| Platform | Microsoft Teams | 🔜 计划中 (Bot Framework) |
| Platform | Google Chat | 🔜 计划中 (Chat API) |
| Platform | Mattermost | 🔜 计划中 (Webhook + Bot) |
| Platform | Matrix (Element) | 🔜 计划中 (Client-Server API) |
| Feature | 语音消息（语音转文字） | ✅ Whisper API (OpenAI / Groq) + ffmpeg |
| Feature | 图片消息 | ✅ 多模态 (Claude Code) |
| Feature | API Provider 管理 | ✅ 运行时切换 Provider |
| Feature | CLI 发送 (`cc-connect send`) | ✅ 通过命令行发送消息到会话 |
| Feature | 多机器人中继 | ✅ 跨平台机器人通信 & 群聊多机器人绑定 |

## 快速开始

### 前置条件

- **Claude Code**: [Claude Code CLI](https://docs.anthropic.com/en/docs/claude-code) 已安装并配置，或
- **Codex**: [Codex CLI](https://github.com/openai/codex) 已安装（`npm install -g @openai/codex`），或
- **Cursor Agent**: [Cursor Agent CLI](https://docs.cursor.com/agent) 已安装（`agent --version` 验证），或
- **Gemini CLI**: [Gemini CLI](https://github.com/google-gemini/gemini-cli) 已安装（`npm install -g @google/gemini-cli`），或
- **Qoder CLI**: [Qoder CLI](https://qoder.com) 已安装（`curl -fsSL https://qoder.com/install | bash`），或
- **OpenCode**: [OpenCode](https://github.com/opencode-ai/opencode) 已安装（`opencode --version` 验证），或
- **iFlow CLI**: [iFlow CLI](https://github.com/iflow-ai/iflow-cli) 已安装（`npm i -g @iflow-ai/iflow-cli` 或 `iflow --version`）

### 通过 AI Agent 安装配置（推荐）

把下面这段话发给 Claude Code 或其他 AI 编程助手，它会帮你完成整个安装和配置过程：

```
请参考 https://raw.githubusercontent.com/chenhg5/cc-connect/refs/heads/main/INSTALL.md 帮我安装和配置 cc-connect
```

### 手动安装

**通过 npm 安装：**

```bash
npm install -g cc-connect
```

**从 [GitHub Releases](https://github.com/chenhg5/cc-connect/releases) 下载二进制：**

```bash
# Linux amd64 示例
curl -L -o cc-connect https://github.com/chenhg5/cc-connect/releases/latest/download/cc-connect-linux-amd64
chmod +x cc-connect
sudo mv cc-connect /usr/local/bin/
```

**从源码编译（需要 Go 1.22+）：**

```bash
git clone https://github.com/chenhg5/cc-connect.git
cd cc-connect
make build
```

### 配置

```bash
# 全局配置（推荐）
mkdir -p ~/.cc-connect
cp config.example.toml ~/.cc-connect/config.toml
vim ~/.cc-connect/config.toml

# 或本地配置（也支持）
cp config.example.toml config.toml
```

### 运行

```bash
./cc-connect                              # 自动: ./config.toml → ~/.cc-connect/config.toml
./cc-connect -config /path/to/config.toml # 指定路径
./cc-connect --version                    # 显示版本信息
```

### 升级

```bash
# npm
npm install -g cc-connect           # 稳定版

# 二进制自更新
cc-connect update                   # 稳定版
cc-connect update --pre             # 内测版（含 pre-release）
```

## 平台接入指南

每个平台都需要在其开发者后台创建机器人/应用。我们提供了详细的分步指南：

| 平台 | 指南 | 连接方式 | 需要公网 IP? |
|------|------|---------|-------------|
| 飞书 (Lark) | [docs/feishu.md](docs/feishu.md) | WebSocket | 不需要 |
| 钉钉 | [docs/dingtalk.md](docs/dingtalk.md) | Stream | 不需要 |
| Telegram | [docs/telegram.md](docs/telegram.md) | Long Polling | 不需要 |
| Slack | [docs/slack.md](docs/slack.md) | Socket Mode | 不需要 |
| Discord | [docs/discord.md](docs/discord.md) | Gateway | 不需要 |
| LINE | [INSTALL.md](./INSTALL.md#line--requires-public-url) | Webhook | 需要 |
| 企业微信 | [docs/wecom.md](docs/wecom.md) | Webhook | 需要 |
| QQ (NapCat) | [docs/qq.md](docs/qq.md) | WebSocket (OneBot v11) | 不需要 |

各平台快速配置示例：

```toml
# 飞书
[[projects.platforms]]
type = "feishu"
[projects.platforms.options]
app_id = "cli_xxxx"
app_secret = "xxxx"

# 钉钉
[[projects.platforms]]
type = "dingtalk"
[projects.platforms.options]
client_id = "dingxxxx"
client_secret = "xxxx"

# Telegram
[[projects.platforms]]
type = "telegram"
[projects.platforms.options]
token = "123456:ABC-xxx"

# Slack
[[projects.platforms]]
type = "slack"
[projects.platforms.options]
bot_token = "xoxb-xxx"
app_token = "xapp-xxx"

# Discord
[[projects.platforms]]
type = "discord"
[projects.platforms.options]
token = "your-discord-bot-token"

# LINE（需要公网 URL）
[[projects.platforms]]
type = "line"
[projects.platforms.options]
channel_secret = "xxx"
channel_token = "xxx"
port = "8080"

# 企业微信（需要公网 URL）
[[projects.platforms]]
type = "wecom"
[projects.platforms.options]
corp_id = "wwxxx"
corp_secret = "xxx"
agent_id = "1000002"
callback_token = "xxx"
callback_aes_key = "xxx"
port = "8081"
enable_markdown = false  # 设为 true 则发送 Markdown 消息（仅企业微信应用内可渲染，个人微信显示"暂不支持"）

# QQ（通过 NapCat/OneBot v11，无需公网 IP）
[[projects.platforms]]
type = "qq"
[projects.platforms.options]
ws_url = "ws://127.0.0.1:3001"
allow_from = "*"  # 允许的 QQ 号，如 "12345,67890"，"*" 表示所有
```

## 权限模式

所有 Agent 均支持权限模式，可在运行时通过 `/mode` 命令切换。

**Claude Code** 模式（对应 `--permission-mode`）：

| 模式 | 配置值 | 行为 |
|------|--------|------|
| **默认** | `default` | 每次工具调用都需要用户确认，完全掌控。 |
| **接受编辑** | `acceptEdits`（别名: `edit`）| 文件编辑类工具自动通过，其他工具仍需确认。 |
| **计划模式** | `plan` | Claude 只做规划不执行，审批计划后再执行。 |
| **YOLO 模式** | `bypassPermissions`（别名: `yolo`）| 所有工具调用自动通过。适用于可信/沙箱环境。 |

**Codex** 模式（对应 `--ask-for-approval`）：

| 模式 | 配置值 | 行为 |
|------|--------|------|
| **建议** | `suggest` | 仅受信命令（ls、cat...）自动执行，其余需确认。 |
| **自动编辑** | `auto-edit` | 模型自行决定何时请求批准，沙箱保护。 |
| **全自动** | `full-auto` | 自动通过，工作区沙箱。推荐日常使用。 |
| **YOLO 模式** | `yolo` | 跳过所有审批和沙箱。 |

**Cursor Agent** 模式（对应 `--force` / `--mode`）：

| 模式 | 配置值 | 行为 |
|------|--------|------|
| **默认** | `default` | 信任工作区，工具调用前询问。 |
| **强制执行** | `force`（别名: `yolo`）| 自动批准所有工具调用。 |
| **规划模式** | `plan` | 只读分析，不做修改。 |
| **问答模式** | `ask` | 问答风格，只读。 |

**Gemini CLI** 模式（对应 `-y` / `--approval-mode`）：

| 模式 | 配置值 | 行为 |
|------|--------|------|
| **默认** | `default` | 每次工具调用都需要确认。 |
| **自动编辑** | `auto_edit`（别名: `edit`）| 编辑工具自动通过，其他仍需确认。 |
| **全自动** | `yolo` | 自动批准所有工具调用。 |
| **规划模式** | `plan` | 只读规划模式，不做修改。 |

**Qoder CLI** 模式：

| 模式 | 配置值 | 行为 |
|------|--------|------|
| **标准权限** | `default` | 标准权限，每次工具调用需确认。 |
| **YOLO 模式** | `yolo` | 跳过所有权限检查，自动批准。 |

**OpenCode** 模式：

| 模式 | 配置值 | 行为 |
|------|--------|------|
| **默认** | `default` | 标准模式。 |
| **全自动** | `yolo` | 自动批准所有工具调用。 |

**iFlow CLI** 模式：

| 模式 | 配置值 | 行为 |
|------|--------|------|
| **默认** | `default` | 手动审批模式。 |
| **自动编辑** | `auto-edit` | 自动编辑模式。 |
| **规划模式** | `plan` | 只读规划模式。 |
| **全自动** | `yolo` | 自动批准所有工具调用。 |

```toml
# Claude Code
[projects.agent.options]
mode = "default"
# allowed_tools = ["Read", "Grep", "Glob"]

# Codex
[projects.agent.options]
mode = "full-auto"
# model = "o3"

# Cursor Agent
[projects.agent.options]
mode = "default"

# Gemini CLI
[projects.agent.options]
mode = "default"

# Qoder CLI
[projects.agent.options]
mode = "default"

# OpenCode
[projects.agent.options]
mode = "default"

# iFlow CLI
[projects.agent.options]
mode = "default"
```

在聊天中切换模式：

```
/mode          # 查看当前模式和所有可用模式
/mode yolo     # 切换到 YOLO 模式
/mode default  # 切换回默认模式
```

## API Provider 管理

支持在运行时切换不同的 API Provider（如 Anthropic 直连、中转服务、AWS Bedrock 等），无需重启服务。Provider 凭证通过环境变量注入 Agent 子进程，不会修改本地配置文件。

### 配置 Provider

**在 `config.toml` 中：**

```toml
[projects.agent.options]
work_dir = "/path/to/project"
provider = "anthropic"   # 当前激活的 provider 名称

[[projects.agent.providers]]
name = "anthropic"
api_key = "sk-ant-xxx"

[[projects.agent.providers]]
name = "relay"
api_key = "sk-xxx"
base_url = "https://api.relay-service.com"
model = "claude-sonnet-4-20250514"

# 特殊环境（Bedrock、Vertex 等）使用 env 字段：
[[projects.agent.providers]]
name = "bedrock"
env = { CLAUDE_CODE_USE_BEDROCK = "1", AWS_PROFILE = "bedrock" }
```

**通过 CLI 命令：**

```bash
cc-connect provider add --project my-backend --name relay --api-key sk-xxx --base-url https://api.relay.com
cc-connect provider add --project my-backend --name bedrock --env CLAUDE_CODE_USE_BEDROCK=1,AWS_PROFILE=bedrock
cc-connect provider list --project my-backend
cc-connect provider remove --project my-backend --name relay
```

**从 [cc-switch](https://github.com/SaladDay/cc-switch-cli) 导入：**

如果你已经使用 cc-switch 管理 Provider，一条命令即可导入（需要 `sqlite3`）：

```bash
cc-connect provider import --project my-backend
cc-connect provider import --project my-backend --type claude     # 仅 Claude Provider
cc-connect provider import --db-path ~/.cc-switch/cc-switch.db    # 指定数据库路径
```

### 在聊天中管理 Provider

```
/provider                   查看当前 Provider
/provider list              列出所有可用 Provider
/provider add <名称> <key> [url] [model]   添加 Provider
/provider add {"name":"relay","api_key":"sk-xxx","base_url":"https://..."}
/provider remove <名称>     移除 Provider
/provider switch <名称>     切换 Provider
/provider <名称>            switch 的快捷方式
```

添加、移除、切换操作均自动持久化到 `config.toml`。切换时会自动重启 Agent 会话并加载新凭证。

**各 Agent 的环境变量映射：**

| Agent | api_key → | base_url → |
|-------|-----------|------------|
| Claude Code | `ANTHROPIC_API_KEY` | `ANTHROPIC_BASE_URL` |
| Codex | `OPENAI_API_KEY` | `OPENAI_BASE_URL` |
| Gemini CLI | `GEMINI_API_KEY` | —（使用 `env` 字段）|
| OpenCode | `ANTHROPIC_API_KEY` | —（使用 `env` 字段）|
| iFlow CLI | `IFLOW_API_KEY` / `IFLOW_apiKey` | `IFLOW_BASE_URL` / `IFLOW_baseUrl` |

Provider 配置中的 `env` 字段支持设置任意环境变量，可用于 Bedrock、Vertex、Azure、自定义代理等各种场景。

## Claude Code Router 集成

[Claude Code Router](https://github.com/musistudio/claude-code-router) 是一个强大的工具，可以将 Claude Code 请求路由到不同的模型提供商（OpenRouter、DeepSeek、Gemini 等），并支持自定义请求转换。cc-connect 现已支持与 Claude Code Router 无缝集成。

### 为什么使用 Claude Code Router？

- **多提供商支持**：路由请求到 OpenRouter、DeepSeek、Ollama、Gemini、火山引擎、SiliconFlow 等
- **模型路由**：针对不同任务使用不同模型（后台任务、思考、长上下文、网络搜索）
- **请求/响应转换**：自动适配不同提供商的 API
- **动态模型切换**：无需重启即可切换模型

### 安装配置

1. **安装 Claude Code Router**：

```bash
npm install -g @musistudio/claude-code-router
```

2. **配置 Router**（创建 `~/.claude-code-router/config.json`）：

```json
{
  "APIKEY": "your-secret-key",
  "Providers": [
    {
      "name": "openrouter",
      "api_base_url": "https://openrouter.ai/api/v1/chat/completions",
      "api_key": "sk-xxx",
      "models": ["anthropic/claude-sonnet-4", "google/gemini-2.5-pro-preview"],
      "transformer": { "use": ["openrouter"] }
    },
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
    "think": "deepseek,deepseek-reasoner",
    "longContext": "openrouter,google/gemini-2.5-pro-preview"
  }
}
```

3. **启动 Router**：

```bash
ccr start
```

4. **配置 cc-connect**（在 `config.toml` 中）：

```toml
[projects.agent.options]
work_dir = "/path/to/project"
mode = "default"

# Router 集成
router_url = "http://127.0.0.1:3456"        # Router URL（默认端口）
router_api_key = "your-secret-key"          # 可选：如果 router 需要认证
```

### 工作原理

配置 `router_url` 后，cc-connect 会自动：

- 设置 `ANTHROPIC_BASE_URL` 为 router URL
- 设置 `NO_PROXY=127.0.0.1` 防止代理干扰
- 禁用遥测和成本警告以获得更清洁的集成

所有 Claude Code 请求都会通过 router 路由，由 router 处理模型选择和提供商通信。

### 使用方式

配置完成后，像往常一样使用 cc-connect。Router 会透明地处理模型路由：

```
你：帮我重构这段代码
Router → DeepSeek（默认模型）

你：思考一下这个架构决策
Router → DeepSeek Reasoner（思考模型）

你：分析这个大型代码库
Router → Gemini Pro（长上下文模型）
```

### 重要说明

- **Provider 设置被忽略**：使用 router 时，`[[projects.agent.providers]]` 设置会被绕过，因为 router 管理模型选择
- **Router 必须运行**：确保在启动 cc-connect 之前执行 `ccr start`
- **配置更改**：修改 router 配置后，需要重启：`ccr restart`

更多详情请参考 [Claude Code Router 文档](https://github.com/musistudio/claude-code-router)。

## 语音消息（语音转文字）

直接发送语音消息 — cc-connect 自动将语音转为文字，再将文字转发给 Agent 处理。

**支持平台：** 飞书、企业微信、Telegram、LINE、Discord、Slack

**前置条件：**
- OpenAI 或 Groq 的 API Key（用于 Whisper 语音识别）
- 安装 `ffmpeg`（用于音频格式转换 — 大部分平台语音格式为 AMR/OGG，Whisper 不直接支持）

### 配置

```toml
[speech]
enabled = true
provider = "openai"    # "openai" 或 "groq"
language = ""          # 如 "zh"、"en"；留空自动检测

[speech.openai]
api_key = "sk-xxx"     # OpenAI API Key
# base_url = ""        # 自定义端点（可选，兼容 OpenAI 接口的服务）
# model = "whisper-1"  # 默认模型

# -- 或使用 Groq（更快更便宜） --
# [speech.groq]
# api_key = "gsk_xxx"
# model = "whisper-large-v3-turbo"
```

### 工作原理

1. 用户在任何支持的平台发送语音消息
2. cc-connect 从平台下载音频文件
3. 如需格式转换（AMR、OGG → MP3），由 `ffmpeg` 处理
4. 音频发送至 Whisper API 进行转录
5. 转录文字展示给用户，并转发给 Agent

### 安装 ffmpeg

```bash
# Ubuntu / Debian
sudo apt install ffmpeg

# macOS
brew install ffmpeg

# Alpine
apk add ffmpeg
```

## 定时任务 (Cron)

创建定时任务，自动执行 — 比如每日代码审查、定期趋势汇总、每周报告等。定时任务触发时，cc-connect 将 prompt 发送给 Agent，并将结果回传到你的聊天会话中。

### 通过斜杠命令管理

```
/cron                                          列出所有定时任务
/cron add <分> <时> <日> <月> <周> <任务描述>      创建定时任务
/cron del <id>                                 删除定时任务
/cron enable <id>                              启用任务
/cron disable <id>                             禁用任务
```

示例：

```
/cron add 0 6 * * * 帮我收集 GitHub trending 并发送总结
```

### 通过 CLI 管理

```bash
cc-connect cron add --cron "0 6 * * *" --prompt "总结 GitHub trending" --desc "每日趋势"
cc-connect cron list
cc-connect cron del <job-id>
```

### 自然语言创建定时任务（通过 Agent）

**Claude Code** 开箱即用 — 直接用自然语言告诉它：

> "每天早上6点帮我总结 GitHub trending"
> "每周一早上9点，生成周报"

Claude Code 会通过 `--append-system-prompt` 自动将你的请求转为 `cc-connect cron add` 命令。

**其他 Agent**（Codex、Cursor、Gemini CLI、Qoder CLI、OpenCode、iFlow CLI）需要在项目根目录的 Agent 指令文件中添加说明，让 Agent 知道如何创建定时任务。将以下内容添加到对应文件中：

| Agent | 指令文件 |
|-------|---------|
| Codex | `AGENTS.md` |
| Cursor | `.cursorrules` |
| Qoder CLI | `AGENTS.md`（项目级）、`~/.qoder/AGENTS.md`（全局） |
| Gemini CLI | `GEMINI.md` |
| OpenCode | `OPENCODE.md` |
| iFlow CLI | `IFLOW.md` |

**需要添加的内容：**

```markdown
# cc-connect Integration

This project is managed via cc-connect, a bridge to messaging platforms.

## Scheduled tasks (cron)
When the user asks you to do something on a schedule (e.g. "every day at 6am",
"每天早上6点"), use the Bash/shell tool to run:

  cc-connect cron add --cron "<min> <hour> <day> <month> <weekday>" --prompt "<task description>" --desc "<short label>"

Environment variables CC_PROJECT and CC_SESSION_KEY are already set — do NOT
specify --project or --session-key.

Examples:
  cc-connect cron add --cron "0 6 * * *" --prompt "Collect GitHub trending repos and send a summary" --desc "Daily GitHub Trending"
  cc-connect cron add --cron "0 9 * * 1" --prompt "Generate a weekly project status report" --desc "Weekly Report"

To list or delete cron jobs:
  cc-connect cron list
  cc-connect cron del <job-id>

## Send message to current chat
To proactively send a message back to the user's chat session (use --stdin heredoc for long/multi-line messages):

  cc-connect send --stdin <<'CCEOF'
  your message here (any special characters are safe)
  CCEOF

For short single-line messages:

  cc-connect send -m "short message"
```

## 守护进程模式

将 cc-connect 作为后台服务运行，由系统 init 管理（Linux systemd 用户服务，macOS launchd LaunchAgent）。

```bash
cc-connect daemon install --config ~/.cc-connect/config.toml   # 安装服务
cc-connect daemon start
cc-connect daemon stop
cc-connect daemon restart
cc-connect daemon status
cc-connect daemon logs [-f] [-n N] [--log-file PATH]
cc-connect daemon uninstall
```

**install 参数：** `--log-file PATH`、`--log-max-size N`（MB）、`--work-dir DIR`、`--force`。日志在达到大小限制时自动轮转，保留 1 个备份。

## 会话管理

每个用户拥有独立的会话和完整的对话上下文。通过斜杠命令管理会话：

```
/new [名称]            创建新会话
/list                  列出当前项目的会话列表
/switch <id>           切换到指定会话
/current               查看当前活跃会话
/history [n]           查看最近 n 条消息（默认 10）
/provider [list|add|remove|switch] 管理 API Provider
/allow <工具名>         预授权工具（下次会话生效）
/mode [名称]           查看或切换权限模式
/quiet                 开关思考和工具进度消息推送
/stop                  停止当前执行
/help                  显示可用命令
```

会话进行中，Agent 可能请求工具权限。回复 **允许** / **拒绝** / **允许所有**（本次会话自动批准后续所有请求）。

## 多机器人中继

cc-connect 支持跨平台机器人通信，让多个 AI Agent 在同一个群聊中协作。

### 群聊绑定

在群聊中绑定多个机器人，让用户在一个地方与所有机器人交互：

```
/bind              查看当前绑定
/bind claudecode   将 claudecode 项目添加到此聊天
/bind gemini       将 gemini 项目添加到此聊天
/bind -claudecode  从此聊天移除 claudecode
```

绑定后，群聊中的消息会被所有绑定的机器人接收。用户可以 @特定机器人，或让所有机器人响应。

### 机器人间通信

通过 CLI 或内部 API 在机器人之间发送消息：

```bash
# CLI：向另一个项目发送消息并获取响应
cc-connect relay send --to gemini "你觉得这个架构怎么样？"

# 在聊天中，让一个机器人咨询另一个
# 机器人可以使用 cc-connect relay 与其他 Agent 通信
```

这可以实现强大的工作流：
- 让 Claude Code 审查代码，然后让 Gemini 提供第二意见
- 让一个 Agent 处理前端问题，另一个处理后端
- 从多个 AI 模型交叉验证解决方案

## 配置说明

每个 `[[projects]]` 将一个代码目录绑定到独立的 agent 和平台。单个 cc-connect 进程可以同时管理多个项目。

```toml
# 项目 1
[[projects]]
name = "my-backend"

[projects.agent]
type = "claudecode"

[projects.agent.options]
work_dir = "/path/to/backend"
mode = "default"

[[projects.platforms]]
type = "feishu"

[projects.platforms.options]
app_id = "cli_xxxx"
app_secret = "xxxx"

# 项目 2 —— 使用 Codex 搭配 Telegram
[[projects]]
name = "my-frontend"

[projects.agent]
type = "codex"

[projects.agent.options]
work_dir = "/path/to/frontend"
mode = "full-auto"

[[projects.platforms]]
type = "telegram"

[projects.platforms.options]
token = "xxxx"
```

完整带注释的配置模板见 [config.example.toml](config.example.toml)。

## 扩展开发

### 添加新平台

实现 `core.Platform` 接口并注册：

```go
package myplatform

import "github.com/chenhg5/cc-connect/core"

func init() {
    core.RegisterPlatform("myplatform", New)
}

func New(opts map[string]any) (core.Platform, error) {
    return &MyPlatform{}, nil
}

// 实现 Name(), Start(), Reply(), Send(), Stop() 方法
```

然后在 `cmd/cc-connect/main.go` 中添加空导入：

```go
_ "github.com/chenhg5/cc-connect/platform/myplatform"
```

### 添加新 Agent

实现 `core.Agent` 接口并注册，方式与平台相同。

## 项目结构

```
cc-connect/
├── cmd/cc-connect/          # 程序入口
│   └── main.go
├── core/                    # 核心抽象层
│   ├── interfaces.go        # Platform + Agent 接口定义
│   ├── registry.go          # 工厂注册表（插件化）
│   ├── message.go           # 统一消息/事件类型
│   ├── session.go           # 多会话管理
│   ├── i18n.go              # 国际化（中/英）
│   ├── speech.go            # 语音转文字（Whisper API + ffmpeg）
│   └── engine.go            # 路由引擎 + 斜杠命令
├── platform/                # 平台适配器
│   ├── feishu/              # 飞书（WebSocket 长连接）
│   ├── dingtalk/            # 钉钉（Stream 模式）
│   ├── telegram/            # Telegram（Long Polling）
│   ├── slack/               # Slack（Socket Mode）
│   ├── discord/             # Discord（Gateway WebSocket）
│   ├── line/                # LINE（HTTP Webhook）
│   ├── wecom/               # 企业微信（HTTP Webhook）
│   └── qq/                  # QQ（NapCat / OneBot v11 WebSocket）
├── agent/                   # AI 助手适配器
│   ├── claudecode/          # Claude Code CLI（交互式会话）
│   ├── codex/               # OpenAI Codex CLI（exec --json）
│   ├── cursor/              # Cursor Agent CLI（--print stream-json）
│   ├── qoder/               # Qoder CLI（-p -f stream-json）
│   ├── gemini/              # Gemini CLI（-p --output-format stream-json）
│   ├── opencode/            # OpenCode（run --format json）
│   └── iflow/               # iFlow CLI（-p, -r, -o）
├── docs/                    # 平台接入指南
├── config.example.toml      # 配置模板
├── INSTALL.md               # AI agent 友好的安装配置指南
├── Makefile
└── README.md
```

## 社区

- [Discord](https://discord.gg/kHpwgaM4kq)
- [Telegram](https://t.me/+odGNDhCjbjdmMmZl)
- 微信用户群

<img src="https://quick.go-admin.cn/ai/article/cc-connect_wechat_group.JPG" alt="用户群" width="100px" />

## License

MIT
