# cc-connect

[English](./README.md) | 中文

将本地 AI 编程助手（Claude Code / Cursor / Gemini CLI / Codex）连接到飞书、钉钉、Slack 等即时通讯平台，实现双向对话。大部分平台无需公网 IP。

## 架构

```
┌──────────────┐     ┌────────────┐     ┌──────────────┐
│   飞书/钉钉   │◄───►│   Engine    │◄───►│  Claude Code │
│   Slack/...  │     │  (路由中心)  │     │  Cursor/...  │
└──────────────┘     └────────────┘     └──────────────┘
    Platform              Core               Agent
```

- **Platform**：消息平台适配器，负责接收/发送消息（WebSocket / Stream / Webhook）
- **Agent**：AI 助手适配器，负责调用 AI 工具并获取响应
- **Engine**：核心路由引擎，管理会话、路由消息、处理斜杠命令

所有组件通过接口解耦，支持即插即用扩展。

## 效果截图

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
| Agent | Codex (OpenAI) | ✅ 已支持 (Beta) |
| Agent | Gemini CLI (Google) | 🔜 计划中 |
| Agent | Crush / OpenCode | 🔜 计划中 |
| Agent | Goose (Block) | 🔜 计划中 |
| Agent | Aider | 🔜 计划中 |
| Agent | Cursor Agent | 🔜 计划中 |
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
| Platform | WhatsApp | 🔜 计划中 (Business Cloud API) |
| Platform | Microsoft Teams | 🔜 计划中 (Bot Framework) |
| Platform | Google Chat | 🔜 计划中 (Chat API) |
| Platform | Mattermost | 🔜 计划中 (Webhook + Bot) |
| Platform | Matrix (Element) | 🔜 计划中 (Client-Server API) |
| Feature | 语音消息（语音转文字） | ✅ Beta — Whisper API (OpenAI / Groq) + ffmpeg |
| Feature | 图片消息 | ✅ Beta — 多模态 (Claude Code) |
| Feature | API Provider 管理 | ✅ Beta — 运行时切换 Provider |
| Feature | CLI 发送 (`cc-connect send`) | ✅ Beta — 通过命令行发送消息到会话 |

## 快速开始

### 前置条件

- **Claude Code**: [Claude Code CLI](https://docs.anthropic.com/en/docs/claude-code) 已安装并配置，或
- **Codex**: [Codex CLI](https://github.com/openai/codex) 已安装（`npm install -g @openai/codex`）

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

安装beta版本：

```bash
npm install -g cc-connect@beta
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
npm install -g cc-connect@beta      # 内测版

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
```

## 权限模式

两种 Agent 均支持权限模式，可在运行时通过 `/mode` 命令切换。

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

```toml
# Claude Code
[projects.agent.options]
mode = "default"
# allowed_tools = ["Read", "Grep", "Glob"]

# Codex
[projects.agent.options]
mode = "full-auto"
# model = "o3"
```

在聊天中切换模式：

```
/mode          # 查看当前模式和所有可用模式
/mode yolo     # 切换到 YOLO 模式
/mode default  # 切换回默认模式
```

## API Provider 管理 `Beta`

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

Provider 配置中的 `env` 字段支持设置任意环境变量，可用于 Bedrock、Vertex、Azure、自定义代理等各种场景。

## 语音消息（语音转文字） `Beta`

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
│   └── wecom/               # 企业微信（HTTP Webhook）
├── agent/                   # AI 助手适配器
│   ├── claudecode/          # Claude Code CLI（交互式会话）
│   └── codex/               # OpenAI Codex CLI（exec --json）
├── docs/                    # 平台接入指南
├── config.example.toml      # 配置模板
├── INSTALL.md               # AI agent 友好的安装配置指南
├── Makefile
└── README.md
```

## 微信用户群

!(用户群)[https://quick.go-admin.cn/ai/article/cc-connect_wechat_group.JPG]

## License

MIT
