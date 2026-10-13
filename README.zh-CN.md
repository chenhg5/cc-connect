# cc-connect

[English](README.md) · [团队使用指南](docs/agent-team.md) · [配置示例](config.example.toml) · [上游项目](https://github.com/chenhg5/cc-connect)

cc-connect 把本地编程 Agent 接入消息平台。你在飞书、Telegram、Discord 等渠道发送需求，Agent 在本机工作目录执行，回复、文件、问题以及支持的工具审批回到原对话。

当前分支新增 **Agent Team**：可复用的角色 Bot、每个业务项目独立的飞书群、持续的成员会话，以及明确的派工、交付和验收。原有单 Bot 配置和命令继续可用。

## 选择使用方式

| 方式 | 适用场景 | 入口 |
| --- | --- | --- |
| 单 Bot | 一个编程 Agent 接入一个或多个消息渠道 | 普通 `[[projects]]` 配置 |
| Agent Team | 管理者在项目群里协调多个成员 | `team init`，随后使用 `/team` 和 `/project` |

**Team（团队）**是可复用的成员配置；**Project（业务项目）**包含目标、代码目录、参与 Bot 和独立群聊；**Workspace（工作目录）**是 Agent 实际使用的目录。为兼容原有配置，TOML 中的 `[[projects]]` 仍定义 Bot 运行实例，`[[teams]]` 按名称引用它们。

角色与执行后端分别配置。管理者、规划者、开发者、测试者和审查者都可以使用 Codex、Claude Code 或通过 ACP 接入的 Trae。

## 构建当前分支

启动前，先安装并登录需要使用的 Agent CLI。源码构建要求 Go 1.25 或更新版本；构建 Web UI 还需要 Node.js 和 npm。

```sh
# 在仓库根目录执行：
make build-team
# 产物：dist/cc-connect-team

# 可选：构建 Web UI，再重新构建二进制以嵌入页面资源。
make web
make build-team
```

`build-team` 包含已注册的 Agent、消息平台和 Web 后端，`agent_team` 编译标记用于增加能力。普通构建仍可使用 `make build`；按需裁剪插件见 [AGENTS.md](AGENTS.md#selective-compilation)。

使用源码构建的二进制获得当前分支的改动；上游发布的二进制和 npm 包是独立的分发产物。

## 启动 Agent Team

### 创建管理者

在本机终端执行，指定一个已有的工作目录：

```sh
./dist/cc-connect-team team init --agent codex --work-dir "$HOME/Workspace"
```

初始化支持 `codex`、`claudecode`、`trae`。按提示完成扫码注册，连续创建管理者 Bot、绑定所有者、保存配置并启动服务。飞书应用发布、可用范围和租户权限仍需满足平台要求。

默认配置保存到 `~/.cc-connect-team/config.toml`。`--no-start` 只保存配置；`--config` 可指定新的配置文件。后续启动使用：

```sh
./dist/cc-connect-team --config "$HOME/.cc-connect-team/config.toml"
```

服务需要持续运行在存放代码和 Agent 凭据的机器上。Bot 注册和用户 OAuth 授权应在本机终端或私聊中进行。

### 添加成员并创建项目

与管理者**私聊**，发送 `/help` 或 `/team bot create`，选择角色和后端并完成注册。Bot 显示名读取飞书名称；改名后内部 ID 保持不变。

发送 `/project create`，填写项目名称、目录、目标和成员。创建成功后返回新群链接，群内包含管理者、创建者及所选 Bot。也支持完整文本命令：

```text
/team bot create developer-a developer codex
/team bot create reviewer-a reviewer claudecode
/project create shop /absolute/path/to/shop developer-a,reviewer-a 实现并验证订单接口
```

进入项目群描述需求。开启 `auto_coordinate = true` 时，创建者没有 @ 任何人的消息由管理者处理。**其他人必须真实 @ 对应 Bot**，普通聊天、纯文本 `@名字` 和 @所有人都不会触发回复；直接 @ 后仍检查原有访问权限。

### 跟踪任务与决策

| 命令 | 用途 |
| --- | --- |
| `/project` 或 `/project settings` | 查看目标、代码目录和 Bot 设置 |
| `/project board` | 创建或刷新项目总览 |
| `/project tasks` | 查看任务清单和状态 |
| `/project task T001` | 查看任务与交付摘要 |
| `/project task T001 logs` | 下载记录的工具日志 |
| `/project pause` / `/project resume` | 停止项目执行 / 恢复接收新工作 |
| `/project finish` | 确认结束项目，保留历史和产物 |

管理者派工、检查交付并显式验收。模型回合结束表示成员提交了结果，不代表任务已验收或测试通过。有依赖的任务须等前置任务验收后派发；计划事项在管理者实际派工前保持待办状态。

本地提交已有修改、创建 worktree 等范围明确的操作，默认由一位合适的执行者完成，再进行一次验收。成员已提供完整答案时复用原稿，管理者按需补充修正或新决策。后端权限和仓库检查要求继续生效。

## 会话、卡片与权限

- **项目持续上下文**：同一项目的同一 Bot 跨任务和公开话题继续同一模型会话；不同项目隔离，共用目录也不会共用模型历史。`/new` 显式创建新的成员会话；修改项目目录或目标须先结束运行中的工作，再启用新的上下文版本。
- **自动命名**：项目会话使用 `项目名 · Bot 显示名`。支持的 Codex App Server 和 Trae 会话在创建或恢复时同步原生标题；命名失败记录日志，不阻塞执行。旧的非当前会话继续保留。
- **执行信息展示**：过程卡显示公开进展与工具操作，当前动作放在底部；配置后结论单独展示并渲染 Markdown。没有内容的回合不留下空过程卡，普通回复不自动 @ 发起人，命令摘要不提供详情跳转。
- **原生问答和审批**：问题和支持的工具审批回传到同一 Agent 会话；过期或不匹配的回调不能回答新请求。实际权限行为由后端及当前模式决定。

Team 初始化默认使用交互审批：Codex 配置 `backend = "app_server"`、`mode = "suggest"`；Claude Code 使用 `mode = "default"`；Trae 以显式默认权限启动 ACP。Codex `exec` 不提供这里所需的审批回传通道。模型、模式和推理选项以各后端提供的能力为准。

上下文隔离不等于文件或账号权限隔离。多个 Bot 修改同一仓库时需要安排分工或使用独立 worktree。Codex 的 `full-auto` 保留沙箱，运行时可写目录和网络访问需要显式配置。恢复项目不会自动重放中断的任务。

卡片配置、多目录、个人标签、迁移与协调命令见[团队使用指南](docs/agent-team.md)及[团队配置示例](config.agent-team.example.toml)。

## 接入单个 Bot

Team 配置是可选的。以下是最小飞书/Codex 配置：

```toml
language = "zh"

[[projects]]
name = "my-code"
admin_from = "${FEISHU_OWNER_OPEN_ID}"

[projects.agent]
type = "codex"
[projects.agent.options]
work_dir = "/absolute/path/to/repository"
backend = "app_server"
mode = "suggest"

[[projects.platforms]]
type = "feishu"
[projects.platforms.options]
app_id = "${FEISHU_APP_ID}"
app_secret = "${FEISHU_APP_SECRET}"
allow_from = "${FEISHU_OWNER_OPEN_ID}"
```

提供环境变量、替换工作目录，将内容保存为 `config.toml`，然后启动：

```sh
./dist/cc-connect-team --config ./config.toml
```

按[飞书接入指南](docs/feishu.md)配置应用权限和事件。`/help` 展示可用命令，`/whoami` 返回平台用户 ID。`/new`、`/list`、`/switch`、`/history`、`/model`、`/mode`、`/reasoning`、`/memory`、`/cron`、`/workspace` 沿用后端能力判断和访问检查。

普通配置默认在空闲 30 分钟后轮换会话；在 `[[projects]]` 下设置 `reset_on_idle_mins = 0` 可关闭。Team 项目会话不会因普通空闲而重置。

## 其他平台与后端

插件注册机制还支持 Gemini CLI、Cursor、OpenCode、Copilot、Kimi、Pi 及兼容 ACP 的 Agent 等后端，参数见 [config.example.toml](config.example.toml)。

| 平台 | 接入指南 |
| --- | --- |
| 飞书 / Lark | [feishu.md](docs/feishu.md) |
| Telegram | [telegram.md](docs/telegram.md) |
| Discord | [discord.md](docs/discord.md) |
| Slack | [slack.md](docs/slack.md) |
| 钉钉 | [dingtalk.md](docs/dingtalk.md) |
| 企业微信 / 微信个人号 | [wecom.md](docs/wecom.md)、[weixin.md](docs/weixin.md) |
| QQ / QQ Bot | [qq.md](docs/qq.md) |
| Matrix | [matrix.md](docs/matrix.md) |
| WPS 协作 | [wps-xiezuo.md](docs/wps-xiezuo.md) |

各渠道能力不同。飞书专属的 Team 注册、项目建群和原生卡片能力，不能直接推定为所有平台都支持。

## 开发与文档

```sh
go build ./...
go test ./...
make test-team
go test ./core -run TestCUJ
go vet ./...
```

核心逻辑通过可选能力接口接入后端和平台；涉及用户流程的改动应包含有效回归测试。开发规则见 [AGENTS.md](AGENTS.md) 和 [CONTRIBUTING.md](CONTRIBUTING.md)。

- [使用说明与命令](docs/usage.md)
- [Agent Team 使用指南](docs/agent-team.md)
- [完整运行配置示例](config.example.toml)
- [Team 配置示例](config.agent-team.example.toml)
- [设计历史与本地原型](docs/prototypes/team-interaction/README.md)

本仓库基于 [chenhg5/cc-connect](https://github.com/chenhg5/cc-connect)。原型文件用于设计参考；具体交互是否已实现，以运行代码和测试为准。
