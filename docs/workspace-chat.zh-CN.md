# 统一工作区对话

统一工作区对话把 Web 主聊天和企业微信单聊连接到同一组 Codex App 项目、原生 thread、设置、Turn 和结构化交互。Codex thread 是对话历史的权威来源；cc-connect 只在 `data_dir/workspace_chat.db` 保存协调状态。

项目当前为 `v0.1.0`。工作区聊天只有一套现行 REST 契约、一套 WebSocket 契约、一个事件封装和一个数据库 schema。被替换的路由、字段、事件、解析器和数据库结构不保留兼容别名。`/platform-sessions` 是平台 `session_key` 会话的独立产品域，不复用或包装工作区聊天协议。

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

`template_project` 必须存在，Agent 必须为 Codex，backend 必须为 `app_server`。`transports` 至少包含 `web` 或 `wecom`；包含 `web` 时必须启用管理服务。配置不满足条件时 cc-connect 会明确启动失败。模板 Agent 持有一个供全部工作区共用的长驻 App Server 连接，并且只使用 `stdio://` 传输；`app_server_url` 和 WebSocket 传输别名会被拒绝。模板的 `work_dir` 不是客户端可选择的目录。

初始化时 cc-connect 启用 App Server experimental API，并分别探测原生设置、协作模式、分页历史和 realtime。能力不可用时返回真实原因，不切换到已删除的 RPC 或事件协议。

## 项目与会话

项目侧栏只读展示有效 `CODEX_HOME/.codex-global-state.json` 中的顺序、项目和根目录。多根项目会展开成多个工作区；目录不存在或无效时保留真实错误并禁用选择。

客户端只能提交服务端签发的不透明 `workspaceRef`。每次操作都会重新解析该引用，并校验原生 thread 的规范 cwd 属于所选根目录。浏览器 JSON 不能提交 `cwd`、服务器路径、sandbox roots、developer instructions 或未经验证的本地附件路径。

工作区内只支持以下会话操作：

- 列出和切换原生 thread；
- 为新会话创建草稿；
- 物化后复制 `codex://threads/{threadId}`。

这里不能创建、编辑或删除项目，也不能 fork、命名/重命名、pin、archive、delete 或 search thread。

## 草稿物化

空 `thread/start` 在首个 Turn 前没有可恢复 rollout，因此“新建”不会立即创建原生 thread，而是在一个事务中创建 SQLite 草稿并选中它。Web 路由为 `/chat/{workspaceRef}/draft/{draftRef}`；新建不接受名称。

首条普通消息到达后，会话 actor 依次执行 `thread/start` 和 `turn/start`。App Server 明确接受 Turn 后，同一个数据库事务绑定真实 thread、更新 selection 与提交状态，再发布 `thread_materialized`。Web 随后把 URL 替换为 `/chat/{workspaceRef}/{threadId}`。在此之前不能复制深链或开启 realtime，界面会显示“首个 Turn 后可用”。

待提交正文只保留到 App Server 明确接受，随后清除。重启后，原生结果尚未确认的 submission 或设置更新会标记为 `needs_retry`，cc-connect 不会自动重发。

## 原生操控

已物化会话支持原生模型、普通 effort、Default/Plan、Plan effort、权限配置、service tier、personality、reasoning summary、状态、usage、活动 Turn、审批、MCP elicitation 和结构化提问。

模型、effort、service tier 和 permission profile 只能选择 App Server catalog 声明的值；Default/Plan 只能选择 collaboration mode mask 声明的模式。personality 与 reasoning summary 使用当前 App Server schema 的枚举，realtime voice 来自 App Server voice catalog；客户端不能自造 ID。公开 patch 中 `effort` 只表示 Default 的普通 effort，`plan_effort` 只表示独立的 Plan effort。`thread/settings/updated` 是设置成功的唯一确认信号。Plan 下修改模型或 `plan_effort` 时，同一请求同时更新顶层设置与 collaboration mode settings；切回 Default 时恢复普通 effort。

每个 `workspaceRef + conversationRef` 由一个 `WorkspaceChatService` actor 持有。同会话操作串行，不同会话可以并发。空闲 thread 接受 `turn_start`；活动 thread 只接受携带预期 Turn ID 的显式 `turn_steer` 或携带活动 Turn ID 的 `turn_interrupt`。审批与结构化交互保留原始 JSON-RPC request ID，并且只接受 App Server 声明的决定。secret 不写入数据库或日志。

完整历史只使用分页的 `thread/turns/list` 和 `thread/items/list`。`thread/read(includeTurns=false)` 仅用于精确读取 metadata 并校验 thread ID 与规范 cwd，绝不读取历史。Web 将权威分页快照与实时事件合并，展示已知消息、推理摘要、计划、命令、文件修改、MCP/动态工具、搜索、错误，并为未知 item 保留通用原始详情。系统没有固定 200 条历史请求，也不保留第二套历史协议。

## Web 协议与实时语音

唯一 REST 资源和 WebSocket 消息见[管理 API](management-api.zh-CN.md#统一工作区对话资源)。访问裸 `/chat` 时从 SQLite 恢复 `web:admin` 的准确选择，URL 中的选择优先。thread snapshot 包含 metadata、已物化设置、状态、usage、活动 Turn、待处理交互、capabilities 和服务端生成的深链。

所有 WebSocket 事件使用同一个 envelope，包含 `type`、`epoch`、`sequence`、工作区/会话标识、可选 thread/Turn/request 标识、`payload`、`error` 和 `occurred_at`。原生通知使用 `type = "native_event"`，payload 保留 App Server `method` 与原始 payload。回放有界；epoch 或 sequence 出现缺口时发送 `resync_required` 和新 snapshot，不会静默丢失状态。

Web realtime 使用 WebRTC SDP，仅在 thread 已物化且探测到 realtime 能力时可用。切换会话、WebSocket 断开、组件卸载或服务停止时都会 stop realtime 并释放媒体资源。企业微信不提供 realtime 语音。

## 企业微信

企业微信工作区聊天只使用 WebSocket 长连接，不需要 VPN、内网穿透或公网回调。单聊选择以 `wecom:user:{userId}` 保存；群聊会明确拒绝。文本、图片和文件使用经过服务端验证的平台输入链路。智能机器人语音回调只包含 `voice.content` 转写文本，不包含音频 URL、AES key、格式或原始字节，因此不会构造音频附件。活动 Turn 下收到普通消息时会提示使用 `/steer`，不会暗中改变消息语义。

项目、thread、设置与交互的编号菜单都会持久化快照。后续选择命令只消费该次快照，并重新校验工作区、thread、catalog 选项或待处理请求。完整命令表见[企业微信接入指南](wecom.md#统一工作区对话)。

## 数据库生命周期

`data_dir/workspace_chat.db` 使用当前 `PRAGMA user_version` 作为精确 schema 标识，不存在 migration runner 或旧表读取器。

- 数据库不存在时，cc-connect 原子创建并校验当前 schema。
- 健康数据库的 `user_version` 不匹配时，cc-connect 先关闭连接，只删除 `workspace_chat.db`、`workspace_chat.db-wal` 和 `workspace_chat.db-shm`，再创建当前 schema。
- 不备份或导入旧 selection、菜单、草稿、submission、interaction、delivery record 或 setting intent。
- SQLite 无法读取、任一 integrity check 失败，或当前版本 schema 畸形时，启动明确失败并保留原文件；损坏绝不会伪装成升级后重建。

Codex 原生 thread 位于 `CODEX_HOME`，不受协调数据库重建影响。

## 明确排除

工作区聊天不实现 fork、会话命名/重命名、pin、archive、delete、search、sections、goal、compact、review、终端、Codex App 项目编辑或桌面全局管理。这些是产品边界，不是隐藏或旧协议能力。
