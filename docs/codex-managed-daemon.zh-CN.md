# Codex 托管 daemon 使用指南

[English](codex-managed-daemon.md)

本文说明如何接入已有 Codex managed daemon 及管理共享会话。完整配置示例见 [config.example.toml](../config.example.toml)。

## 1. Codex 托管 daemon 共享会话

Codex 可以通过 App Server 接入一个已经运行的 managed daemon。cc-connect 作为客户端连接 daemon，共享已有 thread 的输出、审批、问答和运行状态；不会自动启动、停止、重启或更新 daemon。

### 配置示例

```toml
[projects.agent.options]
backend = "app_server"
app_server_transport = "managed_daemon"
daemon_attach_only = true
daemon_socket = "/path/to/app-server-control.sock"
daemon_reconnect_attempts = 3
daemon_enable_steer = true
daemon_busy_message_mode = "queue"
daemon_enable_interrupt = true
daemon_enable_approvals = true
daemon_enable_questions = true
daemon_enable_async_questions = true
daemon_enable_delete = true
```

### 配置项

| 配置项 | 默认值 | 说明 |
|---|---:|---|
| `backend` | `"exec"` | 使用 `"app_server"` 启用 App Server 后端。 |
| `app_server_transport` | `"stdio"` | App Server 传输方式；托管 daemon 使用 `"managed_daemon"`。 |
| `daemon_attach_only` | `false` | 为 `true` 时必须先通过 `/attach` 或 `/switch` 选择已有 thread，不能自动创建新 thread。 |
| `daemon_socket` | 未设置 | daemon 控制 socket。未设置时通过 Codex CLI 进行只读发现。 |
| `daemon_reconnect_attempts` | `3` | 断线后的重试次数；`0` 完全关闭自动重连，但仍需要首次连接成功。 |
| `daemon_enable_steer` | `true` | 是否允许通过 `/steer` 向运行中的 turn 注入文本。 |
| `daemon_busy_message_mode` | `"queue"` | 忙碌时普通文本的处理方式：`queue` 等当前 turn 结束，`steer` 插入当前 turn。 |
| `daemon_enable_interrupt` | `true` | 是否启用停止当前 turn 的能力。 |
| `daemon_enable_approvals` | `true` | 是否由 cc-connect 接收并处理工具审批请求。关闭后交给其他 daemon 客户端处理。 |
| `daemon_enable_questions` | `true` | 是否接收阻塞式 `requestUserInput` 问答。 |
| `daemon_enable_async_questions` | `true` | 是否接收可通过 steer 回答的异步提示。 |
| `daemon_enable_delete` | `true` | 是否允许 `/delete` 通过 daemon 删除 thread。 |

托管模式不接受 `env` 和 `app_server_url`；凭据、Provider 和进程环境需要在 daemon 中配置。接入已有 thread 时，该 thread 自身的 model、Provider、审批策略和 sandbox 设置会保留；`model`、`mode`、`reasoning_effort`、`provider` 只用于新建 thread。

### 旧 stdio 配置兼容

原有配置无需迁移，仍可使用：

```toml
[projects.agent.options]
backend = "app_server"
app_server_url = "stdio://"
```

`app_server_transport` 是新增的可选字段。省略时默认沿用原有 stdio 路径，`app_server_url` 仍按原逻辑处理；省略 `app_server_url` 时也默认使用 stdio。省略 `backend` 时仍使用 exec 后端。

只有显式设置 `backend = "app_server"` 和 `app_server_transport = "managed_daemon"` 才会启用托管 daemon。`daemon_*` 配置项仅用于此模式，不能混入旧 stdio 配置。

## 2. 托管会话聊天命令

以下命令主要用于 `managed_daemon` 模式。参数中的 `<id>` 可以是 thread ID 或当前 `/list` 显示的序号。

| 命令 | 作用 |
|---|---|
| `/attach <id\|序号>` | 接入已有 Codex thread，并开始接收其输出。 |
| `/detach` | 断开当前 thread 的观察和控制连接，thread 本身继续运行。 |
| `/steer <文本>` | 向当前正在运行的 turn 注入新的文本指示。 |
| `/terminals` | 列出当前 thread 创建的后台终端。 |
| `/terminals list` | 同 `/terminals`，显式列出后台终端。 |
| `/terminals stop <id>` | 停止指定后台终端。 |
| `/terminals stop all` | 停止当前 thread 的全部后台终端。 |
| `/decision <action-token> <decision>` | 使用审批提示中的操作 token 提交决定，例如 `allow`、`allow_session`、`allow_similar`、`network_allow` 或 `cancel`；仅接受当前请求提供的选项。 |
| `/answer <action-token> <问题索引> <答案>` | 使用问答提示中的操作 token 回复指定问题；问题索引从 `0` 开始。 |
| `/skip <action-token>` | 使用问答提示中的操作 token 跳过当前待处理的问题。 |
| `/async-answer <action-token> text <答案>` | 使用异步问题提示中的操作 token 回复自由文本；也可直接点击选项按钮。 |

已有会话命令在托管 thread 上继续可用：

```text
/list
/switch <id>
/history [n]
/stop
/delete <id>
```

其中 `/stop` 停止当前 turn；它与 `/terminals stop` 不同，后者只停止后台终端。`/delete` 仅用于删除不活跃 thread，并受 `daemon_enable_delete` 控制。

审批或问答命令使用请求提示生成的操作 token，避免回复已经由其他客户端处理的旧请求。文字审批也可以直接回复提示中列出的决定；`cancel` 表示取消，不会转换为拒绝。

多工作区中的共享轮次（包括外部终端发起的轮次）及人工审批等待会计入工作区活动，任务结束或断开观察连接后释放活动计数，避免空闲回收中断进行中的任务。

`/history [n]` 从最新 turn 开始分页读取，转换为有效消息后返回最近 `n` 条，并保持时间顺序；不足时继续翻页。不限制数量时也逐页读取。单个超大页面仍可能超过 WebSocket 的 32 MiB 消息上限。

## 3. 共享会话的推理展示

共享 thread 发出的推理事件也可能显示在连接的聊天中，包括由 CLI 或 IDE 发起的轮次。CC-Connect 沿用 `thinking_messages` 展示设置（默认 `true`），托管模式不改变默认值。

要在全部 CC-Connect 聊天中隐藏推理，配置：

```toml
[display]
thinking_messages = false
```

只针对某个项目关闭时，将以下表放在该项目的 `[[projects]]` 条目内：

```toml
[projects.display]
thinking_messages = false
```

也可通过 `/quiet` 隐藏推理和工具进度，同时保留助手回复。这些设置只影响 CC-Connect 展示，不会关闭模型推理，也不会改变其他 daemon 客户端收到的事件。启用推理展示时，应考虑连接的聊天中有哪些读者。

## 4. daemon 生命周期与长期运行

以下命令已按 Codex CLI 0.161.0 核对；其他版本请先查看 `codex app-server daemon --help`。通用 App Server 协议见 [OpenAI 官方文档](https://learn.chatgpt.com/docs/app-server)。

启动 CC-Connect 前，使用与交互式 Codex 客户端相同的系统账号和 `CODEX_HOME`，分别执行：

```sh
codex app-server daemon start
codex app-server daemon version
```

`start` 在需要时启动后台 daemon；`version` 以 JSON 返回状态、版本和 `socketPath`，也是 CC-Connect 的只读发现命令。可以将报告的 socket 路径填入 `daemon_socket`，或省略该项自动发现。符合条件的交互式 Codex 会话可能已经启动后台服务，此时先用 `version` 检查。

CC-Connect 作为服务运行时，应在服务账号及其环境下检查 daemon 发现结果，不能只看终端中的结果。若 daemon 使用自定义 `CODEX_HOME`，请在服务环境或 Agent 的 `codex_home` 选项中设置相同值。显式指定 `daemon_socket` 会跳过自动发现，但服务账号仍需有权限访问该 socket。

daemon 退出后，CC-Connect 最多重试 `daemon_reconnect_attempts` 次连接，不会重新启动 daemon。设置为 `0` 时断线后关闭观察连接，不重试。需单独恢复 daemon，再重新 attach。`/detach` 或停止 CC-Connect 都不会停止 daemon。

需要主动停止 daemon 时执行 `codex app-server daemon stop`。`restart` 和 `update` 可能中断其他客户端的共享任务，应安排在没有活跃轮次时操作。凭据和 Provider 应在 daemon 启动前配置；客户端共享 daemon 启动时继承的环境。

## 5. 注意事项

1. `/attach`、`/steer`、审批和问答命令需要当前 Agent 支持对应的托管会话能力；不支持时会返回能力不可用。
2. `daemon_attach_only = true` 适合只管理已有 Codex 工作；需要让 cc-connect 创建新 thread 时应设为 `false`。
3. `daemon_busy_message_mode = "steer"` 只适用于可 steer 的普通文本；待处理审批/问答会优先消费对应答案，附件不能通过 steer 注入。
4. `daemon_socket`、Provider 和凭据应与实际运行 daemon 的环境保持一致。

5. CC-Connect 拒绝连接继承的 `CODEX_THREAD_ID` 所标识的 thread。这是启动当前进程的 Codex thread，拒绝它是为避免自我控制循环；其他正在运行的 thread 仍可接入。
