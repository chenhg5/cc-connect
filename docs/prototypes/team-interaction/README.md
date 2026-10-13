# 多 Bot 协作交互原型索引

最新执行卡：[v0.14.5 可点击交互稿](https://bytedance.larkoffice.com/page/Jnjbmp8l2d5nxGaEmKgcc3vnnSg)；本地文件 `execution-cards-v0.14.5.html`。

[飞书方案文档](https://bytedance.larkoffice.com/docx/SFZtddQ2ronWJ8xGRg5c7vRZnQe) · [完整设计说明](../../plans/2026-10-07-execution-card-v14-prototype.md)。这些文件是各阶段的设计记录和本地模拟，具体运行能力以[团队使用指南](../../agent-team.md)、代码和测试为准；原型不直接调用生产服务。

v0.13 项目面板基线仍由 `dashboard-v0.13.html` / `index.html` 保留。下面为较早稿件记录。

---

# 飞书话题协作原型 v0.8

当前约定：管理者负责依赖判断与派工；成员返回模型原始最终输出。已派发任务需要返工时，管理者或用户进入原任务话题真实 @ 原执行成员，追加本轮要求和执行记录；旧结果保留，管理者核对本轮结果后再决定后续工作。问题与权限请求仍在群主聊天处理并回传原任务。

`index.html` 保留六阶段协作演示，提供三个独立入口：原话题返工、需要你补充、需要你审批。返工场景可操作“模拟成员提交返工结果”“模拟管理者确认并派发审查”，保持原 T001 及话题不变，不创建重复派工。问答支持原选项、自由文本、允许本次、拒绝、查看执行过程、请求到期。所有消息与操作为本地模拟，不发送真实 @ 或执行命令。

v0.8 阶段发布源：`visual-v0.8.xml`、`native-thread-flow-v0.8.xml`；截图：`previews-v0.8/`。v0.7 及更早材料保留为历史；浏览器验证不替代原生飞书与后端联调。

飞书评审稿：https://bytedance.larkoffice.com/docx/SFZtddQ2ronWJ8xGRg5c7vRZnQe

新版原型直达：https://bytedance.larkoffice.com/docx/SFZtddQ2ronWJ8xGRg5c7vRZnQe#doxcnO76rB6560SQI1rR29t8NAc

验证：原话题返工、保留旧派工与执行结果、追加本轮原始结果、管理者确认后才派发后续任务，以及原有问答／审批场景、手机布局和场景重置，22 项浏览器检查通过，未发现脚本错误。已替换飞书文档内嵌 HTML、HTML 附件、七张截图及当前方案；重新读取的内嵌 HTML 与本地 SHA256 一致，正文规则已回读验证（文档 revision 32）。
