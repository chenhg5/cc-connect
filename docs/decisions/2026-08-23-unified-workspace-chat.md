# 统一工作区对话的权威状态与生命周期

## 问题

cc-connect 原有主聊天以平台 `session_key` 和截断 History 为中心，无法直接切换 Codex App 项目及原生 thread。Web 与企业微信各自建立会话会导致内容、审批、取消和刷新恢复互相漂移；允许客户端提交 `cwd` 还会扩大服务器目录访问边界。

## 决定

主 Web 聊天使用“工作区 → Codex 原生 thread → Turn”模型，旧平台 Session 只保留在 `/platform-sessions`。Codex App 状态文件是工作区目录与排序的权威来源，客户端只使用服务端签发的 `workspaceRef`；Codex App Server 的 `thread/read(includeTurns=true)` 是对话内容的权威来源。

`data_dir/workspace_chat.db` 使用显式版本迁移，保存客户端选择、企业微信编号菜单快照和 Turn 投递状态，不复制对话正文。Web 和企业微信通过同一个 `WorkspaceChatService` 访问这些能力。

每个 Codex Agent 持有一个长驻 App Server 物理连接。原生管理请求和逻辑 AgentSession 共用该连接；通知、审批、Turn 状态按 `threadId` 分派。物理连接断开时全部逻辑 session 明确失败，下一次操作重建连接，不静默切换到另一后端。

## 被考虑的替代方案

- 保留独立 Codex Web 页面：会产生第二套会话选择、执行循环和持久化，无法让企业微信与 Web 共享同一 thread。
- 把完整 History 复制进 SQLite：会与 Codex thread 形成双权威，并在未知 item 或 Codex schema 扩展时丢失信息。
- 继续为每个 Turn 启动 App Server：审批和通知天然绑定不同进程，无法满足多 thread 并发隔离与统一关闭。
- 接受客户端 `cwd`：无法证明目录来自当前 Codex App 项目，存在伪造和跨目录 thread 访问风险。

## 兼容与迁移

旧 SessionManager JSON 不导入新模型，也不再由工作区聊天写入。旧页面和接口继续服务“平台会话”，入口迁到 `/platform-sessions`。新数据库只通过显式 migration 演进；重启时未完成 Turn 标记为 `interrupted`，不使用运行时 fallback 掩盖未迁移状态。

## 事务与生命周期

`WorkspaceChatService` 是 thread FIFO、活动 Turn、审批、取消、订阅和关闭的唯一生命周期所有者。Turn 在 SQLite 成功写入 `queued` 后才发布入队事件；worker 开始时提交 `in_progress`，执行结束后提交终态再发布终态事件。服务关闭取消活动 job、停止对应 AgentSession、排空队列并等待 worker，最后关闭 repository。

App Server 物理连接由 Codex Agent 拥有；逻辑 thread session 关闭只注销自身，Agent 关闭才统一终止物理连接及所有逻辑 session。

## 后果与验证

Web 刷新和企业微信重启后可恢复准确目录/thread；同一 thread 的跨客户端消息严格 FIFO，不同 thread 可以并发；伪造引用和跨目录 thread 被拒绝。单元和 CUJ 测试覆盖 SQLite 重启恢复、Codex 状态 schema、App Server 分页/复用/审批隔离/重连、企业微信编号命令、管理 REST/WS、取消和统一关闭。
