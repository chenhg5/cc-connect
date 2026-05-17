# Reasonix Agent 接入指南

<p align="center">
  <img src="https://img.shields.io/badge/Agent-Reasonix-0f766e?style=for-the-badge" alt="Reasonix agent"/>
  <img src="https://img.shields.io/badge/Transport-ACP%20stdio-2563eb?style=for-the-badge" alt="ACP stdio"/>
  <img src="https://img.shields.io/badge/Model-DeepSeek-b91c1c?style=for-the-badge" alt="DeepSeek"/>
  <img src="https://img.shields.io/badge/Public%20IP-%E4%B8%8D%E9%9C%80%E8%A6%81-16a34a?style=for-the-badge" alt="无需公网 IP"/>
</p>

Reasonix 是面向 DeepSeek 的代码 Agent。cc-connect 通过 Agent Client
Protocol (ACP) 的 stdio 模式把它作为一级 Agent 运行：

```text
Telegram / 飞书 / 微信个人号 / ... -> cc-connect -> reasonix acp -> DeepSeek
```

这个接入发生在 Agent 层，所以平台层不用单独适配 Reasonix。只要某个平台已经能接入
cc-connect，同一个 Reasonix 项目就可以复用到这个平台上。

## 当前状态

| 项目 | 状态 |
|------|------|
| Agent 类型 | `reasonix` |
| 通信方式 | ACP over stdio (`reasonix acp`) |
| 会话恢复 | Reasonix 宣告 ACP session loading 时支持 |
| 权限请求 | 通过 cc-connect 聊天权限流处理 |
| Provider 切换 | 暂未接入 cc-connect providers；请在 Reasonix 侧配置 |
| 公网 IP | Agent 不需要；是否需要取决于平台 |

## 已验证渠道

以下渠道已用本地 cc-connect + Reasonix ACP adapter 做过端到端验证：

| 平台 | 连接方式 | 结果 |
|------|----------|------|
| Telegram | Long polling | 收到消息、创建 Reasonix session、完成回复 |
| 微信个人号 | ilink / OpenClaw 同类长轮询 | 扫码登录、token 复用、收到消息、完成回复 |
| 飞书 / Lark | WebSocket 长连接 | 识别机器人、收到 `im.message.receive_v1`、恢复 session、完成回复 |

平台接入方式不变，继续使用现有文档：
[Telegram](telegram.md)、[微信个人号](weixin.md)、[飞书 / Lark](feishu.md)。

## 安装 Reasonix

```bash
npm install -g reasonix
reasonix --version
reasonix setup
reasonix doctor
```

`reasonix setup` 用于配置 Reasonix 自己的本地账号 / API Key / 默认项。
如果 Reasonix 能读取自己的本地配置，cc-connect 不需要知道 DeepSeek key。

## 最小配置

```toml
[[projects]]
name = "reasonix-telegram"

[projects.agent]
type = "reasonix"

[projects.agent.options]
work_dir = "/absolute/path/to/your/project"

[[projects.platforms]]
type = "telegram"

[projects.platforms.options]
token = "${TELEGRAM_BOT_TOKEN}"
allow_from = "123456789"
```

`reasonix` 专用 Agent 会自动补以下默认值：

| 选项 | 默认值 |
|------|--------|
| `command` | `reasonix` |
| `args` | `["acp"]` |
| `display_name` | `Reasonix` |

## 高级配置

当 launchd / systemd 这类服务管理器没有继承交互式 shell 的 `PATH` 时，建议把
`command` 写成绝对路径。

```toml
[[projects]]
name = "reasonix-feishu"

[projects.agent]
type = "reasonix"

[projects.agent.options]
work_dir = "/absolute/path/to/your/project"
command = "/opt/homebrew/bin/reasonix"
args = ["acp", "--yolo", "--preset", "flash"]
mode = "yolo"

[[projects.platforms]]
type = "feishu"

[projects.platforms.options]
app_id = "${FEISHU_APP_ID}"
app_secret = "${FEISHU_APP_SECRET}"
enable_feishu_card = false
allow_from = "ou_xxx"
allow_chat = "oc_xxx"
```

Reasonix 还支持 `--model`、`--budget`、`--transcript`、`--mcp`、`--dir`
等 ACP 参数。注意保留 `acp` 作为第一个参数。

## 密钥处理

推荐以下方式之一：

1. 用 `reasonix setup` 把 DeepSeek 凭证保存在 Reasonix 自己的配置里。
2. 通过服务环境变量注入 `DEEPSEEK_API_KEY`。
3. 在本地配置文件中使用 cc-connect 的环境变量替换：

```toml
[projects.agent.options.env]
DEEPSEEK_API_KEY = "${DEEPSEEK_API_KEY}"
```

不要把 API key、bot token、飞书 secret、微信 token 或生成的 session store 提交到仓库。

## 运行建议

- 先向平台机器人发送 `/whoami`，拿到 ID 后再收紧 `allow_from`。
- 用 `/mode` 查看 ACP session 宣告的权限模式。
- 用 `/new` 开新 Reasonix 会话，用 `/list` / `/switch` 管理历史会话。
- 如果终端里能跑、launchd/systemd 下不能跑，优先检查 `command` 绝对路径和服务用户是否能读取 Reasonix 凭证。

## 排障

| 现象 | 检查项 |
|------|--------|
| `acp: command "reasonix" not found in PATH` | 运行 `which reasonix`，然后设置 `command = "/absolute/path/to/reasonix"` |
| session 启动但 Reasonix 无法调用 DeepSeek | 用运行 cc-connect 的同一个系统用户执行 `reasonix doctor` |
| 飞书返回 `app do not have bot` | 在飞书开放平台启用机器人能力，发布应用，然后重启 cc-connect |
| 飞书收不到消息 | 使用 WebSocket 长连接，并订阅 `im.message.receive_v1` |
| 微信扫码成功但收不到消息 | 检查 `allow_from`，重启 cc-connect，然后向机器人再发一条消息以建立 context token |
| 聊天里出现权限请求 | 根据工具内容回复 `allow`、`deny` 或 `allow all` |
