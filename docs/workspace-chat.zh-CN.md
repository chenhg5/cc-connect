# 统一工作区对话

统一工作区对话把 Web 主聊天和企业微信单聊连接到同一组 Codex App 项目与原生 Codex thread。Codex thread 是对话内容的权威来源；`data_dir/workspace_chat.db` 只保存客户端选择、编号菜单快照和 Turn 投递状态。

## 配置

```toml
[management]
enabled = true
port = 9820
token = "your-management-token"

[workspace_chat]
enabled = true
template_project = "codex-template"
transports = ["web", "wecom"]

[[projects]]
name = "codex-template"
work_dir = "/path/to/a/default/project"

[projects.agent]
type = "codex"

[projects.agent.options]
backend = "app_server"
```

`template_project` 必须存在，Agent 必须为 Codex，backend 必须为 `app_server`。`transports` 至少包含 `web` 或 `wecom`；包含 `web` 时必须启用管理服务。配置不满足条件时 cc-connect 会直接启动失败，不会选择第一个 Codex 项目或猜测目录。

## Web

`/chat` 左侧栏读取 `CODEX_HOME/.codex-global-state.json` 中的 Codex App 项目。多根项目会展开为多个目录；不存在或不是目录的路径仍会显示真实原因，但不能选择。

选择目录时恢复该目录最近更新的 thread；没有 thread 时立即创建。URL 使用 `/chat/{workspaceRef}/{threadId}`，刷新时优先恢复 URL，访问裸 `/chat` 时读取 SQLite 中 `web:admin` 的最近选择。旧 `session_key` 聊天位于 `/platform-sessions`，不会导入或写入新模型。

历史来自 `thread/read(includeTurns=true)`，按 Turn 显示消息、推理摘要、计划、命令、文件修改、MCP/动态工具、搜索、错误和未知 item 原始详情。

## 企业微信

企业微信使用 WebSocket 长连接，不需要公网回调或穿透。单聊选择保存在 `wecom:user:{userId}`；群聊不进入工作区聊天。命令和编号快照见[企业微信接入指南](wecom.md#统一工作区对话)。图片和文件继续通过企业微信现有附件能力进入同一个 Turn。

## 持久化与并发

SQLite 使用显式 schema migration、WAL 和单连接事务。重启时，`queued` 或 `in_progress` Turn 会显式变为 `interrupted`，客户端的工作区/thread 选择和编号菜单保留。

每个 thread 只有一个 FIFO worker。Web 与企业微信同时发送时按入队顺序执行；回复发送给发起平台，同时 Web 订阅者收到该 thread 的实时事件。取消、审批、服务关闭和活动状态发布均由 `WorkspaceChatService` 统一协调。
