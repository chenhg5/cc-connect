# 飞书团队交互 v0.10 实现记录

2026-10-06 18:44 +08。落实“返工也用群消息交接”。在 v0.9 工作区增量实现，没有创建 Git 提交。

## 行为

- 管理者对既有任务发起返工时，保留任务编号，发送新的群消息并真实 @ 原成员，标题标注返工轮次。
- 成员在新返工消息的话题下执行；本轮结果另发群消息 @ 管理者。管理者在这条新结果下核对，验收后才发布后续任务。
- 每轮历史保存派工消息、会话、指令、结果消息及验收证据。新执行上下文携带此前要求和结果；旧过程卡与结果不被覆盖。
- 原请求人或项目负责人可在群里真实 @ 成员，带唯一任务编号直接返工。该入站消息本身作为执行入口；任务状态关联新轮次，不额外发送重复派工。任务忙碌或依赖未就绪时拒绝新轮次。
- 同一直接返工消息去重；错误成员、不同任务混合或无权用户不能直接改变任务。旧话题的迟到结果不能作为无归属报告交付或完成新轮次。
- 已结束的任务话题收到返工要求时，提示改用群入口。运行中的问答和补充仍按当前会话处理；审批回调保留原请求校验。
- 新返工消息发送失败或未取得消息 ID 时不启动成员，不回退到旧话题。历史记录与失败状态可查。

## 验证

最终代码通过：

```text
go test ./...
go test ./core/ -run TestCUJ
go test -race ./core ./platform/feishu -run 'Test(CUJ_T1|GroupRework|Workflow_Rework|ReworkFollowup|NativeTask|ResultHandoff)' -count=1
go build ./...
go build -o /tmp/cc-connect-team-v10 ./cmd/cc-connect
git diff --check
```

CUJ 使用真实 Engine、SessionManager 以及模拟进程和发送边界，覆盖首次指令、群中提出返工、管理者发送新返工消息、用户回答后本轮继续，以及用户直接在群里向成员发起下一轮返工。断言群消息、原始结果、过程话题位置、历史输入、重复投递隔离及管理者最终答复的接收人。

单元回归补充：错误归属及身份拒绝，旧话题迟到报告隔离，投递失败不在旧话题执行；飞书适配测试检查真实提及结构和新群消息 API。

浏览器原型验证通过：群消息返工入口、独立本轮过程话题、历次结果保留、管理者在新结果下处理、依赖未验收不派发、问答审批、移动端宽度。日志：`/tmp/cc-team-v10-*.log`；脚本：`/tmp/cc-team-ui-review-tools/verify-v0.10.cjs`。

## 设计稿

飞书文档 revision 38 已替换当前方案、可点击原型、HTML 附件及五张截图。历史版本章节保留，文首明确以 v0.10 为准。

入口：https://bytedance.larkoffice.com/docx/SFZtddQ2ronWJ8xGRg5c7vRZnQe#doxcnrfox68yvCwUDOCsV3Zj8rg

本地原型：`docs/prototypes/team-interaction/index.html`。截图：`docs/prototypes/team-interaction/previews-v0.10/`。

## 本机部署

团队服务 `cc-connect-team.service` 已更新并重启。部署前无正在执行的任务和子进程；配置内容及项目暂停状态保持不变。

- 二进制 SHA-256：`91eaf1d03ffb827a52a85da508b2748355c153189d286a96c14c4d39a71c1529`
- v0.9 回退备份：`/home/bytedance/.cc-connect-team/backups/interaction-v10-20261006-184427/`
- 部署及健康记录：`/tmp/cc-team-v10-deployment.json`、`/tmp/cc-team-v10-health.json`
- 启动检查：服务 active，4 个平台 ready、4 个 WebSocket 连接，启动错误 0。

## 验收边界

本次未向真实群发送验收任务。真实飞书桌面端与手机端的提及通知、原生话题打开、完整附件和问答审批回调仍待验收；原型和模拟接口测试不能代替客户端验证。项目继续保持暂停。

v0.9 及 v0.8 已记录的其他能力边界继续适用，包括 Trae ACP 原生结构化问答未接通、可选私聊催办未实现、异常退出后旧审批卡外观刷新待完善。新轮次携带结构化历史，不承诺恢复前一轮完整模型内部会话。
