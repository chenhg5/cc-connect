# 飞书团队交互 v0.11 实现记录

2026-10-06。在 v0.10 工作区增量实现“同一成员持续上下文，任务串行”。没有创建 Git 提交。

## 行为

- 模型会话按项目、成员和工作目录上下文版本绑定。管理者收到多项结果、成员接到新任务或返工，继续使用自己的模型会话；派工根消息和过程话题仍属于本轮交接。
- 同一成员共用执行锁和运行队列。等待用户补充或审批仍占用会话，不允许第二项任务同时执行；不同成员的会话和执行锁相互独立。
- 管理者可用 `team plan` 持久保存未来事项，记录任务编号、成员、指令、产物和依赖。计划不发送群消息、不创建执行话题、不占用成员；可以修改或取消。同一请求重试去重，不同来源的请求不合并。
- 管理者检查成员结果并验收后，更新后续输入，再用 `team dispatch --task-id` 明确派发。忙碌成员、未验收的依赖及过期依赖版本仍由运行时拦截。首次释放计划保持第 1 轮，返工才增加轮次。
- 重启保留计划和会话映射；验收、重启以及项目恢复不会自动批量派发。待办选择和发布由管理者负责，不把调度责任交给执行成员。
- 原生命令中的稳定会话标识在执行时解析回当前交接来源。排队的新指令不能改变当前任务的原请求人或过程位置；旧话题的普通文字、旧审批回调不能回答当前话题的问题。
- 项目成员会话不因普通空闲超时自动重置。显式新建、切换会话或更换项目目录继续按既有机制处理。

## 升级与恢复

旧版按话题保存的会话首次升级时，选取该成员本项目最近使用的会话继续，其他历史记录保留。保存原生后端会话 ID，用于进程重启后的恢复。不会拼接多个后端原生会话，也不承诺后端失效会话能够恢复或无限保留全部上下文。

## 验证

以下验证均通过，日志位于 `/tmp/cc-team-v11-*.log`：

```text
go test ./...
go test ./core/ -run TestCUJ
go test -race ./core ./platform/feishu -run 'Test(CUJ_T[12]|SharedConversation|TeamSession|PlannedTasks|PlanEdit|SharedSession|TeamModel|GroupRework|Workflow_Rework|NativeTask|ResultHandoff)' -count=1
go test -race ./core -run 'Test(IdenticalPlansFromSeparateRequests|PlannedTasks|PlanEdit)' -count=1
go build ./...
go build -o /tmp/cc-connect-team-v11 ./cmd/cc-connect
git diff --check
```

新增 CUJ T2 使用真实 Engine、SessionManager 和模拟外部进程/平台：派发 A → 用户问题 → 保存 B 计划 → 用户回答 → A 交付但未验收时拒绝 B → 验收 A → 派发 B。断言 A、B 的过程话题不同，而开发者只启动一个原生会话、执行两个回合；管理者跨结果消息和后续用户指令同样复用会话并正确切换展示位置。

单元回归覆盖会话迁移及持久化、共享锁、新建和切换会话、项目/成员隔离、来源隔离、待办去重、循环依赖、取消、暂停恢复及空闲策略。

浏览器验证脚本 `/tmp/cc-team-ui-review-tools/verify-v0.11.cjs` 检查新串行情景、审批等待时不能推进、验收后才显示 B、上下文持续说明，以及既有返工、完整命令展开、问答审批和移动端布局。原型验证通过。

## 设计稿

飞书文档 revision 41 已替换当前方案、可点击原型、HTML 附件及八张截图。回查确认线上 HTML 与本地原型完全一致，历史章节保留。

入口：https://bytedance.larkoffice.com/docx/SFZtddQ2ronWJ8xGRg5c7vRZnQe#doxcneoiKMRwBt5zirakt2ROQrc

本地原型：`docs/prototypes/team-interaction/index.html`。截图：`docs/prototypes/team-interaction/previews-v0.11/`。

## 本机部署

2026-10-06 19:36 +08，已更新并重启 `cc-connect-team.service`。更新前没有运行任务和子进程；备份二进制、配置和 data 目录中的持久文件（不复制运行时 Unix socket），包含会话映射与任务状态。

- 二进制 SHA-256：`32deb6a1af7d944d2898361c7979b164ded7e7d1709666fd1bb83e415b7343ed`
- v0.10 回退备份：`/home/bytedance/.cc-connect-team/backups/interaction-v11-20261006-193631/`
- 部署与健康记录：`/tmp/cc-team-v11-deployment.json`、`/tmp/cc-team-v11-health.json`
- 服务 active，4 个平台 ready、4 个 WebSocket 连接，启动错误 0。
- 配置及项目暂停状态保持不变，安装二进制与验证构建一致。

## 验收边界

本次未向真实群发送验收任务。真实飞书桌面端与手机端的提及通知、原生话题打开、问答审批回调仍需验收；模拟接口测试和 HTML 原型不能代替客户端验证。

此前已记录的其他能力边界继续适用，包括 Trae ACP 原生结构化问答尚未接通、可选私聊催办未实现、异常退出后旧审批卡外观刷新待完善。
