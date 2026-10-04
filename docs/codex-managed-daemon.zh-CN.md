# Codex 托管 daemon 使用指南

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
| `daemon_reconnect_attempts` | `3` | 断线后的自动重连次数；设置为 `0` 关闭自动重连。 |
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

## 3. 注意事项

1. `/attach`、`/steer`、审批和问答命令需要当前 Agent 支持对应的托管会话能力；不支持时会返回能力不可用。
2. `daemon_attach_only = true` 适合只管理已有 Codex 工作；需要让 cc-connect 创建新 thread 时应设为 `false`。
3. `daemon_busy_message_mode = "steer"` 只适用于可 steer 的普通文本；待处理审批/问答会优先消费对应答案，附件不能通过 steer 注入。
4. `daemon_socket`、Provider 和凭据应与实际运行 daemon 的环境保持一致。
