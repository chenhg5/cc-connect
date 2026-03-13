# cc-connect

[![CI](https://github.com/chenhg5/cc-connect/actions/workflows/ci.yml/badge.svg)](https://github.com/chenhg5/cc-connect/actions/workflows/ci.yml)
[![GitHub release](https://img.shields.io/github/v/release/chenhg5/cc-connect?include_prereleases)](https://github.com/chenhg5/cc-connect/releases)
[![Discord](https://img.shields.io/discord/1478978944567869652?logo=discord&label=Discord)](https://discord.gg/kHpwgaM4kq)
[![Telegram](https://img.shields.io/badge/Telegram-Group-26A5E4?logo=telegram)](https://t.me/+odGNDhCjbjdmMmZl)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

[English](./README.md) | 中文

**在任何聊天工具里，远程操控你的本地 AI Agent**

cc-connect 把运行在你机器上的 AI Agent 桥接到你日常使用的即时通讯工具。代码审查、资料研究、自动化任务、数据分析 —— 只要 AI Agent 能做的事，都能通过手机、平板或任何有聊天应用的设备来完成。

```
         你（手机 / 电脑 / 平板）
                    │
    ┌───────────────┼───────────────┐
    ▼               ▼               ▼
   飞书           Slack         Telegram  ...9 个平台
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

> 是时候卸载 OpenClaw 了 — cc-connect 让你同时拥有最强的那几个 AI Agent，而不只是一个。

- **7 大 AI Agent** — Claude Code、Codex、Cursor Agent、Qoder CLI、Gemini CLI、OpenCode、iFlow CLI，按需选用，也可以同时使用
- **9 大聊天平台** — 飞书、钉钉、Slack、Telegram、Discord、企业微信、LINE、QQ、QQ 官方机器人，大部分无需公网 IP
- **多机器人中继** — 在群聊中绑定多个机器人，让它们相互协作。问 Claude，再听 Gemini 的见解 — 同一个对话搞定
- **聊天即控制** — 切换模型 `/model`、切换推理强度 `/reasoning`、切换权限 `/mode`、管理会话，全部通过斜杠命令完成
- **Agent 记忆** — 在聊天中直接读写 Agent 指令文件 `/memory`，无需回到终端
- **定时任务** — 自然语言创建 cron 任务，"每天早上6点帮我总结 GitHub trending" 即刻生效
- **语音 & 图片** — 发语音或截图，cc-connect 自动转文字和多模态转发
- **多项目管理** — 一个进程同时管理多个项目，各自独立的 Agent + 平台组合

<p align="center">
  <img src="docs/images/screenshot/cc-connect-lark.JPG" alt="飞书" width="32%" />
  <img src="docs/images/screenshot/cc-connect-telegram.JPG" alt="Telegram" width="32%" />
  <img src="docs/images/screenshot/cc-connect-wechat.JPG" alt="微信" width="32%" />
</p>
<p align="center">
  <em>左：飞书 &nbsp;|&nbsp; Telegram &nbsp;|&nbsp; 右：个人微信（通过企业微信关联）</em>
</p>

## 快速开始

### 通过 AI Agent 安装配置（推荐）

把下面这段话发给 Claude Code 或其他 AI Agent，它会帮你完成整个安装和配置过程：

```
请参考 https://raw.githubusercontent.com/chenhg5/cc-connect/refs/heads/main/INSTALL.md 帮我安装和配置 cc-connect
```

### 手动安装

**通过 npm：**

```bash
# 稳定版
npm install -g cc-connect

# Beta 版（功能更新，可能不稳定）
npm install -g cc-connect@beta
```

**从 [GitHub Releases](https://github.com/chenhg5/cc-connect/releases) 下载：**

```bash
# Linux amd64 - 稳定版
curl -L -o cc-connect https://github.com/chenhg5/cc-connect/releases/latest/download/cc-connect-linux-amd64
chmod +x cc-connect
sudo mv cc-connect /usr/local/bin/

# Beta 版（从 pre-release 下载）
curl -L -o cc-connect https://github.com/chenhg5/cc-connect/releases/download/v1.x.x-beta/cc-connect-linux-amd64
```

**从源码编译（需要 Go 1.22+）：**

```bash
git clone https://github.com/chenhg5/cc-connect.git
cd cc-connect
make build
```

### 配置

```bash
mkdir -p ~/.cc-connect
cp config.example.toml ~/.cc-connect/config.toml
vim ~/.cc-connect/config.toml
```

最简配置：

```toml
[[projects]]
name = "my-project"

[projects.agent]
type = "claudecode"

[projects.agent.options]
work_dir = "/path/to/your/project"

[[projects.platforms]]
type = "telegram"

[projects.platforms.options]
token = "123456:ABC-xxx"
```

### 运行

```bash
./cc-connect
```

### 升级

```bash
# npm
npm install -g cc-connect           # 稳定版

# 二进制自更新
cc-connect update                   # 稳定版
cc-connect update --pre             # Beta 版（含 pre-release）
```

## 支持状态

| 组件 | 类型 | 状态 |
|------|------|------|
| Agent | Claude Code | ✅ 已支持 |
| Agent | Codex (OpenAI) | ✅ 已支持 |
| Agent | Cursor Agent | ✅ 已支持 |
| Agent | Gemini CLI (Google) | ✅ 已支持 |
| Agent | Qoder CLI | ✅ 已支持 |
| Agent | OpenCode (Crush) | ✅ 已支持 |
| Agent | iFlow CLI | ✅ 已支持 |
| Agent | Goose (Block) | 🔜 计划中 |
| Agent | Aider | 🔜 计划中 |
| Platform | 飞书 (Lark) | ✅ WebSocket 长连接 — 无需公网 IP |
| Platform | 钉钉 (DingTalk) | ✅ Stream 模式 — 无需公网 IP |
| Platform | Telegram | ✅ Long Polling — 无需公网 IP |
| Platform | Slack | ✅ Socket Mode — 无需公网 IP |
| Platform | Discord | ✅ Gateway — 无需公网 IP |
| Platform | LINE | ✅ Webhook — 需要公网 URL |
| Platform | 企业微信 (WeChat Work) | ✅ WebSocket / Webhook |
| Platform | QQ (NapCat/OneBot) | ✅ WebSocket — Beta |
| Platform | QQ 官方机器人 (QQ Bot) | ✅ WebSocket — 无需公网 IP |

## 平台接入指南

| 平台 | 指南 | 连接方式 | 需要公网 IP? |
|------|------|---------|-------------|
| 飞书 (Lark) | [docs/feishu.md](docs/feishu.md) | WebSocket | 不需要 |
| 钉钉 | [docs/dingtalk.md](docs/dingtalk.md) | Stream | 不需要 |
| Telegram | [docs/telegram.md](docs/telegram.md) | Long Polling | 不需要 |
| Slack | [docs/slack.md](docs/slack.md) | Socket Mode | 不需要 |
| Discord | [docs/discord.md](docs/discord.md) | Gateway | 不需要 |
| 企业微信 | [docs/wecom.md](docs/wecom.md) | WebSocket / Webhook | 不需要 (WS) / 需要 (Webhook) |
| QQ / QQ 机器人 | [docs/qq.md](docs/qq.md) | WebSocket | 不需要 |

## 常用命令

### 会话管理

```
/new [名称]            创建新会话
/list                  列出会话
/switch <id>           切换会话
/current               查看当前会话
```

### 权限模式

```
/mode             查看可用模式
/mode yolo        # 自动批准所有工具
/mode default     # 每次工具调用前询问
```

### Provider 管理

```
/provider list              列出 Provider
/provider switch <名称>     运行时切换 API Provider
```

### 定时任务

```
/cron add 0 6 * * * 帮我总结 GitHub trending
```

📖 **完整文档：** [docs/usage.zh-CN.md](docs/usage.zh-CN.md)

## 文档

- [使用指南](docs/usage.zh-CN.md) — 完整功能文档
- [INSTALL.md](INSTALL.md) — AI Agent 友好的安装指南
- [config.example.toml](config.example.toml) — 配置模板

## 社区

- [Discord](https://discord.gg/kHpwgaM4kq)
- [Telegram](https://t.me/+odGNDhCjbjdmMmZl)
- 微信用户群

<img src="https://quick.go-admin.cn/ai/article/cc-connect_wechat_group.JPG" alt="用户群" width="100px" />

## 贡献者

<a href="https://github.com/chenhg5/cc-connect/graphs/contributors">
  <img src="https://contrib.rocks/image?repo=chenhg5/cc-connect" />
</a>

## Star History

<a href="https://www.star-history.com/#chenhg5/cc-connect&Date">
 <picture>
   <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/svg?repos=chenhg5/cc-connect&type=Date&theme=dark" />
   <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/svg?repos=chenhg5/cc-connect&type=Date" />
   <img alt="Star History Chart" src="https://api.star-history.com/svg?repos=chenhg5/cc-connect&type=Date" />
 </picture>
</a>

## License

MIT
