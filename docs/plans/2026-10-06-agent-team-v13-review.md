# v0.13 交互稿评审记录

2026-10-06。用户要求先看交互稿再实现。本次仅修改方案、原型和飞书设计文档，尚未修改或部署运行代码。

- 可点击稿：https://bytedance.larkoffice.com/docx/SFZtddQ2ronWJ8xGRg5c7vRZnQe#doxcnRwzNpruvTrbI9EHwVjCE5K
- 方案正文：https://bytedance.larkoffice.com/docx/SFZtddQ2ronWJ8xGRg5c7vRZnQe#doxcnM6UlgdtB0mZO4MbAemQPgb
- 文档 revision 47，回读验证包含 1 个可点击 HTML、1 个源码附件、8 张截图。下载的 HTML 与本地验证版本逐字节相同。
- 原型：docs/prototypes/team-interaction/dashboard-v0.13.html；index.html 同步此稿；interaction-v0.12.html 保留历史。
- 核心变更：结束当前轮工作，同群直接接受新命令；保留成员上下文及交付历史。项目面板明确返回，提供刷新、暂停/恢复、结束本轮、历史及待处理请求入口。
- 源码观察：core/team_workflow.go 的 handleProjectWorkflow 没有 dashboard 别名；board 调 refreshWorkflow 更新持久 BoardMessageID，渲染内容相同时可能无操作。尚未用真实客户端事件复现具体用户故障。
- 浏览器验证：/tmp/cc-team-ui-review-tools/verify-v0.13.cjs，输出 /tmp/cc-team-v13-prototype-check.log。覆盖新旧命令、刷新失败重试、完整命令、结束预览取消/确认、状态变化失效、结束等待、新轮、上下文提示、旧审批失效、历史、串行待办、暂停恢复、桌面/390px手机、输入转义及 JS 异常。通过；不是运行端联调。
- 运行源码与二进制与本轮前哈希一致；cc-connect-team.service PID 1709971 保持 active/running（v0.12）。本次未暂停、恢复或结束真实任务。

后续须先接收用户对交互稿的反馈，再实施主方案第 13.1～13.3 节；v0.12 永久结束语义已被用户纠正，不应沿用。工作轮次与任务返工 attempt 要分开记录；同一项目×成员的持续会话不能因轮次变化而重置。关闭旧运行和请求后需确认实际退出，再允许新轮占用；旧结果、卡片、消息不能触发新轮。新增人类指令可以开启新轮，读取面板和 bot 迟到消息不能开启新轮。
