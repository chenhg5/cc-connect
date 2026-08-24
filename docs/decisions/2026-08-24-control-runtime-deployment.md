# Control、Server 与远程 Runtime 的部署所有权

## 问题

Linux 公网 Web 需要统一认证、持久化和部署入口，但 Codex App 项目、thread、`CODEX_HOME` 与深链只存在于用户的 macOS 设备。让业务进程公开 Web 或在 Linux 复制 Codex 状态会形成双权威；让 systemd 同时管理 control 和 server 会使更新、回滚、日志和数据库恢复没有唯一生命周期所有者。

## 决定

交付四类独立制品：`cc-connect-control`、`cc-connect-server`、Linux `cc-connect-deploy-host` 和 macOS `cc-connect-runtime`。原生安装由 systemd 管理 control；容器安装由 systemd 只管理 deploy-host，deploy-host 管理 control 容器，control 通过私有 Unix Socket 监管 server。Runtime 使用出站 TLS/WebSocket 和 Ed25519 challenge-response 连接 control，并在本机复用 Codex 原生后端。服务器只保存不透明全局 `workspaceRef` 和最近目录 catalog，不复制 Codex 对话正文。

control 是认证、设备、server、Release、执行槽、日志和跨重启 activation 的唯一生命周期所有者。工作区聊天仍由 `WorkspaceChatService` 独占 actor、Turn、交互和 realtime 状态，并只向 control 暴露部署前只读活动快照。

systemd 模式下 control 拥有签名 Release 的在线更新、回滚和跨重启 activation。Docker 模式下 control 仍拥有部署业务事务，deploy-host 独占 control 容器切换与 watchdog 回滚。control 不挂载 Docker Socket，只能通过带 peer credential 校验的 Unix Socket 请求固定 Release/tag 操作；Web 更新和回滚保持可用。两种模式不能同时指向同一持久目录。

control 与 deploy-host 使用唯一的容器宿主协议指纹；不匹配只返回 `update_required`。候选确认同时校验正在运行的 control 编译版本、激活目标和宿主持久状态，不能由旧镜像确认新目标。

Runtime 连接代际以 `control.db` 的最后 checkpoint 为下界，control 重启后不得复用旧 generation。每次连接拥有独立 context；断线会取消并等待该代 RPC 与原生订阅，旧响应不得写入新连接。设备撤销由 Broker 同步关闭活动连接。设备连接事件进入 `control.db.audit_events`，作为 Web 查询和实时日志的唯一持久来源。

Release manifest 固定仓库 `shusfun/cc-connect`、workflow、tag、commit、八个目标制品及协议/数据库元数据。control、deploy-host 和 Runtime 均验证 GitHub OIDC/Sigstore identity、manifest 和 SHA-256；deploy-host 还独立验证固定 GHCR 镜像签名，不提供未签名 fallback。

## 被考虑的替代方案

- Linux 直接运行本地 Codex App Server：无法取得 macOS Codex App 的真实项目、认证和 thread 状态，并会产生第二权威。
- VPN 或反向穿透到 macOS：服务器流量和公网入口已充足，且入站网络会扩大设备攻击面；出站 TLS 长连接足以覆盖需求。
- systemd 分别管理 control 和 server：部署事务无法原子协调业务活动、日志、候选健康与数据库恢复。
- 在容器内运行 systemd 或把 Docker Socket 交给 control：会产生第二个宿主生命周期所有者并扩大容器权限；容器模式改由 Compose 原子替换 control 容器。
- control 在原进程内覆盖二进制：无法可靠处理 control 自更新的启动失败窗口。

## 兼容与迁移

项目为 `v0.1.0`。旧 management token、业务进程公开 TCP、`cc-connect web`、CLI update、`cc-connect daemon`、服务器 `template_project` 和本地 App Server 路径均删除，不保留双协议或兼容解析。

`control.db` 使用显式事务迁移保存永久控制状态。`workspace_chat.db` 继续使用版本不匹配时精确重建的破坏性策略，Codex 原生 thread 不受影响。

## 事务与生命周期

systemd 模式下 DeploymentManager 先锁定 Release 和机器级执行槽，再验签、检查活动 Turn/交互/realtime、检查磁盘、暂存在线 Runtime、备份 `control.db` 并写 activation record。进入 server stop 后运行不可取消；随后切换 `current` 并激活 Runtime，交给 systemd 启动候选 control。

旧 control 停止后的第一次 `ExecStopPost` 只消费 handoff 标记。候选 control 启动后先恢复 Runtime 连接并发送 `runtime/update/confirm`；Runtime 在确认前保留本地 activation 看门狗。候选 control 健康后提交 run 并删除 activation；候选无法执行、Runtime 未确认或健康检查失败时，第二次 `ExecStopPost` 使用稳定 helper 恢复旧槽、数据库和已切换 Runtime。回滚复用同一事务，只允许上一成功槽。

Docker 模式使用独立版本的 `container-activation.json`，不复用 systemd release slot 或 activation schema。control 检查活动操作、暂存 Runtime、备份 `control.db` 后请求 deploy-host 激活已准备的 digest；deploy-host 持久化 previous/pending 状态，再替换 control 容器。候选 control 验证 server、Runtime、数据库与宿主 pending 后确认。Compose 启动失败或确认超时会停止候选、恢复数据库备份与上一 digest；数据库无法恢复时保持容器停止并明确失败。

## 后果与验证

多台 Mac 的项目和 thread 按设备隔离，离线设备保持只读 catalog 且不自动重发 Turn。两种安装模式都由 Web 发起更新并由 control 阻断活动原生操作。验证覆盖签名与摘要、路径逃逸、数据库备份、handoff 两阶段恢复、Runtime 私有槽激活、宿主执行器 peer credential、固定命令边界、watchdog 回滚、执行槽、持久日志游标、候选确认，以及非 root 容器、loopback 暴露和 bind 持久目录契约。
