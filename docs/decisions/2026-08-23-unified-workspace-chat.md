# 统一工作区对话的权威状态与生命周期

## 问题

cc-connect 原有主聊天以平台 `session_key` 和截断 History 为中心，无法直接操控 Codex App 原生 thread。上一版工作区聊天虽然引入了项目侧栏和原生 thread，但仍通过每工作区 Agent 克隆、旧 Engine 文本事件和一组最小 REST/WS 消息运行，无法表达原生设置、结构化交互、完整事件和 realtime。空 `thread/start` 在首个 Turn 前也没有可恢复 rollout，不能作为持久化的新会话。

## 决定

主 Web 聊天使用“只读工作区 -> 草稿或 Codex 原生 thread -> Turn”模型，旧平台 Session 只保留在独立的 `/platform-sessions`。Codex App 状态文件是工作区目录和排序的权威来源；客户端只提交服务端签发的 `workspaceRef`，服务端每次操作都重新验证 thread 的规范 cwd。

每台配对的 macOS Runtime 持有一个长驻 App Server 客户端，并为该设备的所有工作区复用物理连接。Linux server 通过远程 NativeConversation 能力访问它。`WorkspaceChatService` 是会话 actor、活动 Turn、设置、结构化交互、realtime、投递、订阅和关闭的唯一生命周期所有者。App Server 的 thread、item、Turn 和 settings 终态是运行状态的权威来源，SQLite 不复制原生对话正文。

cc-connect 对外只提供一套工作区聊天协议。App Server stable/experimental 方法通过同一连接逐项探测，能力缺失时明确返回不可用，不保留旧 RPC、旧事件或第二套下游协议。完整历史只使用 `thread/turns/list` 和 `thread/items/list`；`thread/read(includeTurns=false)` 只承担精确 metadata 与 cwd 归属校验。

项目只读展示和切换；会话只提供列表、切换、新建草稿和复制深链。模型、effort、Default/Plan、权限、tier、personality、reasoning summary、审批、结构化提问、steer、取消、状态、usage、完整历史和事件属于当前会话操控范围。fork、命名/重命名、pin、archive、delete、search、sections、goal、compact、review、终端与桌面全局管理明确排除。

## 被考虑的替代方案

- 保留旧 REST/WS 并新增 v2：会形成两套生产者和消费者，当前 `v0.1.0` 没有兼容承诺，因此直接替换。
- 用空 `thread/start` 立即创建会话：App Server 退出后该 thread 无 rollout 可恢复，因此新建先持久化 cc-connect 草稿，首个 Turn 才物化。
- 每个工作区克隆 Codex Agent：会产生多个 App Server 进程和分裂的通知、审批及设置状态，因此每台 Runtime 只持有一个客户端。
- 把完整 History 复制进 SQLite：会形成双权威，并在 App Server 扩展未知 item 时丢失信息。
- 接受客户端 `cwd` 或服务器路径：无法证明目录来自当前 Codex App 项目，存在跨目录访问风险。

## 兼容与数据库

项目处于 `v0.1.0`，工作区聊天的旧路由、字段、事件、解析器、测试和文档在同一变更中删除，不提供别名、双读、双写或版本协商。`/platform-sessions` 是独立产品域，不是工作区协议兼容层。

`data_dir/workspace_chat.db` 通过 `PRAGMA user_version = 3` 标识当前唯一 schema。版本不匹配时关闭连接，精确删除数据库及 SQLite sidecar 后重新创建，不备份或导入旧状态。数据库无法读取或完整性检查失败时明确启动失败，不借版本升级静默删除损坏数据。Codex 原生 thread 位于 `CODEX_HOME`，不受该数据库重建影响。

## 事务与生命周期

新建只提交草稿和 selection；首个普通 Turn 由同一 actor 顺序执行 `thread/start` 和 `turn/start`。App Server 接受后，actor 使用服务拥有的有界提交 context，在一个数据库事务中绑定真实 thread、更新 selection 和提交投递状态，再发布 `thread_materialized`；客户端此时取消请求不会撤销已被原生后端接受的本地终态。原生 mutation 的连接结果不确定时清除可重放正文并标记 `needs_retry`，草稿物化窗口不确定时另标记 `materialization_uncertain`，两者都禁止自动重发。草稿物化前不提供深链或 realtime。

设置修改由 actor 串行发送，收到 `thread/settings/updated` 后才发布成功。审批和结构化提问以原始 JSON-RPC ID、thread/turn/item 及连接 generation 路由；旧连接响应不会进入新连接。事件先进入按 thread 排序的序列流，再发送给订阅者；缓冲缺口或客户端提交同 epoch 的未来 cursor 都触发显式 resync，不静默丢弃。每个 WebSocket 连接至多拥有一个 realtime 目标，切换会话必须先成功 stop，失败时保留所有权供关闭路径再次清理。

服务关闭时先停止 realtime、取消活动 Turn、终结 pending interaction 和投递状态，再注销 thread 路由并关闭唯一 App Server 连接，最后关闭 repository。

## 后果与验证

Web 刷新和企业微信重启后应恢复准确草稿或原生 thread；同一会话操作串行，不同会话并发；Web 与企业微信观察同一原生设置和 Turn。验收必须覆盖数据库精确重建、草稿物化崩溃窗口、单连接路由、分页历史、设置确认、stale steer/cancel、全部交互请求、事件 resync、WebRTC 清理，以及伪造 workspaceRef 和跨目录 thread 拒绝。
