# 飞书团队交互 v0.9 实现记录

2026-10-06 18:30 +08。本次在 v0.8 基础上落实“群消息交接结果，话题展示接收方本轮处理”。未创建 Git 提交。

## 本次行为

- 用户指令是管理者首次处理的话题入口；派工消息是成员执行的话题入口。
- 成员完成后，在群主聊天发送新的结果消息，真实 @ 管理者，正文保留模型本轮原始最终输出。执行话题里的过程卡不再重复附加最终输出。
- 管理者收到成员结果后，在这条结果消息的话题下核对和处理；后续派工或最终答复仍是新的群消息。最终答复真实 @ 原始用户。
- 原任务返工继续在原派工话题执行；返工结果另发群消息，并成为管理者新一轮处理的话题入口。
- 跨结果消息保留原始请求人、指令、任务及结果版本关联。前序验收、依赖控制、群内问答和审批沿用 v0.8 规则。
- 结果投递成功并保存消息关联后，才通知管理者处理；失败不伪装成交接成功。重复投递复用消息标识，过时任务轮次不得发送新结果。
- 超出群消息安全预算的结果以完整文本文件交付，同群发送 @ 消息作为入口；不截断原文。
- 展示公开说明、工具执行与结果，不展示未公开的内部推理。

## 验证

以下命令已通过：

```text
go test ./...
go test ./core/ -run TestCUJ
go test -race ./core ./platform/feishu -run 'Test(ResultHandoff|CUJ_T1|Workflow_Rework|Native|Decision)' -count=1
go build ./...
go build -o /tmp/cc-connect-team-v09 ./cmd/cc-connect
git diff --check
```

新增或更新的测试覆盖：原文及真实提及、群消息与话题位置、管理者处理入口随结果切换、原始请求人继承、发送失败重试、重复结果去重、旧轮次拒绝、超长结果完整文件，以及原话题返工和群内回答后的继续执行。

原型通过浏览器检查：收起话题后各次群结果仍可见；展开成员结果后显示管理者处理过程；返工保留旧执行记录；问答、审批、过期和移动端宽度无回归。原型使用演示数据；Engine 和 HTTP 测试模拟外部进程及发送接口。

测试日志：`/tmp/cc-team-v09-*.log`。浏览器验证脚本：`/tmp/cc-team-ui-review-tools/verify-v0.9.cjs`。

## 设计稿

飞书文档已更新至 revision 35，替换当前方案、可点击 HTML 原型、HTML 附件及四张截图。

新版入口：https://bytedance.larkoffice.com/docx/SFZtddQ2ronWJ8xGRg5c7vRZnQe#doxcnmxQyXpNjBUJ5L1nVBrqD3R

本地原型：`docs/prototypes/team-interaction/index.html`；截图：`docs/prototypes/team-interaction/previews-v0.9/`。

## 本机部署

团队服务 `cc-connect-team.service` 已替换二进制并重启。部署前没有执行中的子进程；配置内容及项目暂停状态保持不变。

- 二进制 SHA-256：`fc1ac2657119e2bbfc5d76555efc40630716a3daf33feeb4878e57584dd3fb50`
- v0.8 回退备份：`/home/bytedance/.cc-connect-team/backups/interaction-v09-20261006-183016/`
- 部署记录：`/tmp/cc-team-v09-deployment.json`
- 健康检查：`/tmp/cc-team-v09-health.json`；服务 active，4 个平台 ready，4 个 WebSocket 连接，启动错误 0。

## 仍需真实客户端验收

尚未在真实飞书群执行本次多角色验收任务。桌面端和手机端的提及通知、群结果打开后的话题归属、附件访问以及问答审批回调，需要在恢复项目后验证。保持现有项目暂停，避免部署时自行开始任务。

v0.8 中记录的其他能力边界继续适用，尤其是 Trae ACP 原生结构化问答尚未接通、私聊催办未实现及异常重启后的旧审批卡外观刷新待完善。自动化通过不等于客户端体验已验收。
