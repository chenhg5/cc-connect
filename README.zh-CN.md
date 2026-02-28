# cc-connect

[English](./README.md) | 中文

将本地 AI 编程助手（Claude Code / Cursor / Gemini CLI / Codex）连接到飞书、钉钉、Slack 等即时通讯平台，实现双向对话。

## 架构

```
┌──────────────┐     ┌────────────┐     ┌──────────────┐
│   飞书/钉钉   │◄───►│   Engine    │◄───►│  Claude Code │
│   Slack/...  │     │  (路由中心)  │     │  Cursor/...  │
└──────────────┘     └────────────┘     └──────────────┘
    Platform              Core               Agent
```

- **Platform**：消息平台适配器，负责接收/发送消息（WebSocket / Stream）
- **Agent**：AI 助手适配器，负责调用 AI 工具并获取响应
- **Engine**：核心路由引擎，将平台消息转发给 Agent，再将响应回传平台

所有组件通过接口解耦，支持即插即用扩展。

## 支持状态

| 组件 | 类型 | 状态 |
|------|------|------|
| Agent | Claude Code | ✅ 已支持 |
| Agent | Cursor Agent | 🔜 计划中 |
| Agent | Gemini CLI | 🔜 计划中 |
| Agent | Codex | 🔜 计划中 |
| Platform | 飞书 (Lark) | ✅ 已支持 |
| Platform | 钉钉 (DingTalk) | ✅ 已支持 |
| Platform | Slack | 🔜 计划中 |
| Platform | Telegram | 🔜 计划中 |

## 快速开始

### 前置条件

- Go 1.22+
- [Claude Code CLI](https://docs.anthropic.com/en/docs/claude-code) 已安装并配置

### 安装

```bash
git clone https://github.com/chenhg5/cc-connect.git
cd cc-connect
make build
```

### 配置

```bash
cp config.example.toml config.toml
vim config.toml
```

### 运行

```bash
# 使用默认配置文件 (config.toml)
./cc-connect

# 指定配置文件
./cc-connect -config /path/to/config.toml
```

## 配置说明

```toml
[agent]
type = "claudecode"

  [agent.options]
  work_dir = "/path/to/your/project"

[[platforms]]
type = "feishu"

  [platforms.options]
  app_id = "cli_xxxx"
  app_secret = "xxxx"
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

// 实现 Name(), Start(), Reply(), Stop() 方法
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
│   ├── message.go           # 统一消息类型
│   ├── session.go           # 会话管理
│   └── engine.go            # 路由引擎
├── platform/                # 平台适配器
│   ├── feishu/              # 飞书（WebSocket 长连接）
│   └── dingtalk/            # 钉钉（Stream 模式）
├── agent/                   # AI 助手适配器
│   └── claudecode/          # Claude Code CLI
├── config/                  # 配置加载
├── config.example.toml      # 配置模板
├── Makefile
└── README.md
```

## License

MIT
