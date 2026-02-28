# cc-connect

[English](./README.md) | 中文

将本地 AI 编程助手（Claude Code / Cursor / Gemini CLI / Codex）连接到飞书、钉钉、Slack 等即时通讯平台，实现双向对话。无需公网 IP。

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

## 支持状态

| 组件 | 类型 | 状态 |
|------|------|------|
| Agent | Claude Code | ✅ 已支持 |
| Agent | Cursor Agent | 🔜 计划中 |
| Agent | Gemini CLI | 🔜 计划中 |
| Agent | Codex | 🔜 计划中 |
| Platform | 飞书 (Lark) | ✅ 已支持（WebSocket 长连接）|
| Platform | 钉钉 (DingTalk) | ✅ 已支持（Stream 模式）|
| Platform | Telegram | ✅ 已支持（Long Polling）|
| Platform | Slack | ✅ 已支持（Socket Mode）|
| Platform | Discord | ✅ 已支持（Gateway WebSocket）|
| Platform | LINE | ✅ 已支持（HTTP Webhook）|
| Platform | 企业微信 (WeChat Work) | ✅ 已支持（HTTP Webhook + Markdown）|

## 快速开始

### 前置条件

- Go 1.22+
- [Claude Code CLI](https://docs.anthropic.com/en/docs/claude-code) 已安装并配置

### 安装

**从源码编译：**

```bash
git clone https://github.com/chenhg5/cc-connect.git
cd cc-connect
make build
```

**通过 npm 安装：**

```bash
npm install -g cc-connect
```

### 配置

```bash
cp config.example.toml config.toml
vim config.toml
```

### 运行

```bash
./cc-connect                              # 默认使用 config.toml
./cc-connect -config /path/to/config.toml # 自定义路径
./cc-connect --version                    # 显示版本信息
```

## 权限模式

Claude Code 适配器支持四种权限模式（对应 Claude 的 `--permission-mode` 参数），可在运行时通过 `/mode` 命令切换：

| 模式 | 配置值 | 行为 |
|------|--------|------|
| **默认** | `default` | 每次工具调用都需要用户确认，完全掌控。 |
| **接受编辑** | `acceptEdits`（别名: `edit`）| 文件编辑类工具自动通过，其他工具（如 Bash）仍需确认。 |
| **计划模式** | `plan` | Claude 只做规划不执行，审批计划后再执行。 |
| **YOLO 模式** | `bypassPermissions`（别名: `yolo`）| 所有工具调用自动通过。适用于可信/沙箱环境。 |

```toml
[projects.agent.options]
mode = "default"
# 在 default/acceptEdits 模式下，还可以预授权特定工具：
# allowed_tools = ["Read", "Grep", "Glob"]
```

在聊天中切换模式：

```
/mode          # 查看当前模式和所有可用模式
/mode yolo     # 切换到 YOLO 模式
/mode default  # 切换回默认模式
```

## 会话管理

每个用户拥有独立的会话和完整的对话上下文。通过斜杠命令管理会话：

| 命令 | 说明 |
|------|------|
| `/new [名称]` | 创建新会话 |
| `/list` | 列出当前项目的 Claude Code 会话列表 |
| `/switch <id\|名称>` | 切换到指定会话 |
| `/current` | 查看当前活跃会话 |
| `/history [n]` | 查看最近 n 条消息（默认 10） |
| `/allow <工具名>` | 预授权工具（下次会话生效） |
| `/mode [名称]` | 查看或切换权限模式 |
| `/quiet` | 开关思考和工具进度消息推送 |
| `/stop` | 停止当前执行 |
| `/help` | 显示可用命令 |

会话进行中，Claude 可能请求工具权限。回复 **允许** / **拒绝** / **允许所有**（本次会话自动批准后续所有请求）。

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

# 项目 2 —— 不同目录、不同机器人
[[projects]]
name = "my-frontend"

[projects.agent]
type = "claudecode"

[projects.agent.options]
work_dir = "/path/to/frontend"
mode = "bypassPermissions"

[[projects.platforms]]
type = "dingtalk"

[projects.platforms.options]
client_id = "xxxx"
client_secret = "xxxx"
```

### 飞书配置

1. 前往 [飞书开放平台](https://open.feishu.cn) 创建应用
2. 开启**机器人**能力
3. 在「事件订阅」中添加 `im.message.receive_v1` 事件
4. 选择 **WebSocket 长连接**模式（无需公网 IP）
5. 将 App ID 和 App Secret 填入配置

### 钉钉配置

1. 前往 [钉钉开放平台](https://open-dev.dingtalk.com) 创建应用
2. 创建**机器人**，选择 **Stream 模式**
3. 将 Client ID 和 Client Secret 填入配置

### Telegram 配置

1. 在 Telegram 中找到 [@BotFather](https://t.me/BotFather)，发送 `/newbot` 创建机器人
2. 将 Bot Token 填入配置
3. 连接方式：Long Polling（无需公网 IP）

### Slack 配置

1. 前往 [Slack API](https://api.slack.com/apps) 创建应用
2. 开启 **Socket Mode**（Settings > Socket Mode）
3. 订阅 Bot 事件：`message.channels`、`message.im`
4. 安装应用到工作区，复制 Bot Token（`xoxb-...`）和 App Token（`xapp-...`）
5. 连接方式：Socket Mode WebSocket（无需公网 IP）

### Discord 配置

1. 前往 [Discord 开发者门户](https://discord.com/developers/applications) 创建应用
2. 在 **Bot** 页面创建机器人并复制 Token
3. 开启 **Message Content Intent**（Privileged Gateway Intents 下）
4. 通过 OAuth2 URL Generator 邀请机器人加入服务器（scopes: `bot`；权限: `Send Messages`）
5. 连接方式：Gateway WebSocket（无需公网 IP）

### LINE 配置

1. 前往 [LINE Developers Console](https://developers.line.biz/console/) 创建 **Messaging API** 频道
2. 复制 Channel Secret 和 Channel Access Token（长期有效）
3. 在 LINE 控制台设置 Webhook URL 为 `http(s)://<your-domain>:<port>/callback`
4. 连接方式：HTTP Webhook —— 需要通过 ngrok、cloudflared 等工具将本地端口暴露到公网

### 企业微信配置

1. 登录[企业微信管理后台](https://work.weixin.qq.com/wework_admin/frame)
2. **应用管理** → 创建自建应用 → 记录 AgentId 和 Secret
3. **我的企业** → 记录企业 ID (CorpId)
4. 进入应用 → **接收消息** → 设置 API 接收：
   - URL：`http(s)://<your-domain>:<port>/wecom/callback`
   - Token：任意随机字符串
   - EncodingAESKey：点击「随机生成」
   - 需要**先启动 cc-connect**，再保存以通过验证
5. **企业可信 IP** → 添加服务器出口公网 IP
6. （可选）**我的企业** → **微信插件** → 扫码关联个人微信，即可在个人微信中直接对话
7. 连接方式：HTTP Webhook —— 需要通过 ngrok、cloudflared 等工具将本地端口暴露到公网
8. 消息以 Markdown 格式发送（自动降级为纯文本）

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
│   └── engine.go            # 路由引擎 + 斜杠命令
├── platform/                # 平台适配器
│   ├── feishu/              # 飞书（WebSocket 长连接）
│   ├── dingtalk/            # 钉钉（Stream 模式）
│   ├── telegram/            # Telegram（Long Polling）
│   ├── slack/               # Slack（Socket Mode）
│   ├── discord/             # Discord（Gateway WebSocket）
│   ├── line/                # LINE（HTTP Webhook）
│   └── wecom/               # 企业微信（HTTP Webhook + AES + Markdown）
├── agent/                   # AI 助手适配器
│   └── claudecode/          # Claude Code CLI（交互式会话）
├── config/                  # 配置加载
├── config.example.toml      # 配置模板
├── Makefile
└── README.md
```

## License

MIT
