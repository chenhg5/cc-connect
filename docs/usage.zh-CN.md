# 使用指南

cc-connect 完整功能使用指南。

## 目录

- [会话管理](#会话管理)
- [权限模式](#权限模式)
- [API Provider 管理](#api-provider-管理)
- [Claude Code Router 集成](#claude-code-router-集成)
- [语音消息（语音转文字）](#语音消息语音转文字)
- [语音回复（文字转语音）](#语音回复文字转语音)
- [定时任务 (Cron)](#定时任务-cron)
- [多机器人中继](#多机器人中继)
- [守护进程模式](#守护进程模式)
- [多工作区模式](#多工作区模式)
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
| `/allow <工具名>` | 预授权工具 |
| `/reasoning [等级]` | 查看或切换推理强度（Codex）|
| `/mode [名称]` | 查看或切换权限模式 |
| `/quiet` | 开关思考/工具进度消息 |
| `/stop` | 停止当前执行 |
| `/help` | 显示可用命令 |

会话中 Agent 请求工具权限时，回复 **允许** / **拒绝** / **允许所有**。

---

## 权限模式

所有 Agent 支持运行时切换权限模式，通过 `/mode` 命令。

### Claude Code 模式

| 模式 | 配置值 | 行为 |
|------|--------|------|
| 默认 | `default` | 每次工具调用需确认 |
| 接受编辑 | `acceptEdits` / `edit` | 文件编辑自动通过 |
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
/mode yolo     # 切换到 YOLO
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

### 自然语言（Claude Code）

> "每天早上6点帮我总结 GitHub trending"

Claude Code 会自动创建定时任务。

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
type = "feishu"  # 或 dingtalk, telegram, slack, discord, wecom, line, qq, qqbot

[projects.platforms.options]
# 平台特定配置
```
