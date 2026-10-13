# 执行卡与交互稿的视觉对齐

2026-10-07。用户反馈 v0.14.5 实际卡片与交互稿差异明显，尤其是视觉样式。本次修改直接作用于飞书原生执行卡渲染器。

参考交互稿：[v0.14.5](https://bytedance.larkoffice.com/page/Jnjbmp8l2d5nxGaEmKgcc3vnnSg)。源文件为 `docs/prototypes/team-interaction/execution-cards-v0.14.5.html`；Chrome 本地渲染截图 `/tmp/cc-prototype-reference.png` 仅用于对照原型，不代表飞书客户端实际截图。

## 已调整

- 去掉大面积彩色标题栏，改为白底正文和右上角小字号状态；保留失败、停止、等待补充和等待授权的语义。
- 公开说明使用正文层级；命令采用左侧引用线、彩色状态符号、蓝色工具名称、等宽截断命令，完成状态与耗时使用灰色小字。相邻命令间距 4px，命令整行可点，不增加独立详情按钮。
- 详情页将返回、前后命令和刷新排成横向操作栏，长标签可换行；完整命令与执行结果在同一代码区域连续呈现，超限仍按容量分页。
- 补充和授权区域置于原执行卡顶部的浅橙色区域，选项及授权按钮横向排列；输入框与提交按钮保持在同一区域。
- 同一卡中的说明和命令分组、按容量续卡、阅读快照、私聊通知、结果单独交付沿用现有规则。

## 同时发现并修正的原生表单错误

真实 CardKit 校验拒绝了补充表单：原 `decision_<token>` 元素 ID 长度为 35，超过 20 字符限制。改为从原请求 token 派生的稳定 20 字符 ID；审批 token 和处理人校验不变。导航时提交草稿、真正提交答案、命令详情入口均有回归覆盖。

控件改为嵌套列布局后，草稿处理递归遍历列与元素；导航只保存草稿，真正提交仍绑定原请求。补充期间的命令入口采用可提交表单的文字按钮，防止嵌套布局丢失用户输入。

## 验证

- `go test ./...`、显式 CUJ、相关 core/feishu race 检查、`go build ./...` 和运行二进制构建通过。
- 全量验证中发现旧 CUJ-T3 使用群入口直接派发新轮任务，可能落入旧报告上下文。测试改为使用新指令实际路由的上下文；不改变运行时的旧轮次拒绝规则。修正后连续 30 次通过，race 下连续 10 次通过。修正前失败日志 `/tmp/cc-card-visual-cuj-t3-repeat.log` 保留。
- 摘要、详情、授权、补充四种最终 payload 均通过飞书 `POST /open-apis/cardkit/v1/cards` 服务端校验。仅创建未发送的卡片实体，没有向真实群发送测试审批或处理真实授权。
- 覆盖 30,000 字节、200 组件边界及分页完整性；布局包装与表单组件计入实际容量。
- 测试与校验记录：`/tmp/cc-card-visual-all-final.log`、`/tmp/cc-card-visual-cuj-final.log`、`/tmp/cc-card-visual-race.log`、`/tmp/cc-card-visual-cuj-race-final.log`、`/tmp/cc-card-visual-build.log`、`/tmp/cc-card-visual-platform-validation-final.json`。

官方结构依据：[卡片结构](https://open.feishu.cn/document/feishu-cards/card-json-v2-structure)、[富文本](https://open.feishu.cn/document/feishu-cards/card-json-v2-components/content-components/rich-text)、[分栏](https://open.feishu.cn/document/feishu-cards/card-json-v2-components/containers/column-set)、[按钮](https://open.feishu.cn/document/feishu-cards/card-json-v2-components/interactive-components/button)。服务端接收仅验证结构，不等于桌面/手机客户端的视觉和点击验收。

## 上线与边界

- 确认无执行中、排队或等待审批任务，且 28 张原生执行卡均已结束后，于 2026-10-07 13:56 +08 更新团队服务。
- `cc-connect-team.service` PID 2135020；4 个平台 ready、4 个 WebSocket 已连接、启动错误 0；控制 socket HTTP 200。
- 配置和项目数据与停机备份一致。备份目录 `/home/bytedance/.cc-connect-team/backups/card-visual-20261007-135559/`，包含旧二进制、配置、服务定义及停机数据快照。
- 安装二进制 SHA-256：`8eb182fa0f63d8c25652a21dc2b1e7021a6aaf01c7e872d9037f27e0694cad06`。
- 新创建或重新渲染的卡片使用新样式；不会批量改写聊天中所有旧卡。
- 私聊通知精确定位原审批卡仍待客户端复现，此版本没有宣称修复该跳转。原型中的浏览器字体、阴影和悬停效果不作为飞书客户端逐像素一致的承诺。
- 部署与健康记录：`/tmp/cc-card-visual-deployment.json`、`/tmp/cc-card-visual-health.json`。

## 14:04 截图反馈：初始卡留白

初始卡尚未收到正文，却使用完整卡片宽度、上下 12px 内边距和右对齐状态，造成状态与引用分离。修正为左对齐状态，空卡使用原生 compact 宽度、上下 4px 内边距；正文到达后恢复普通宽度并更新同一消息，常规正文上下内边距缩至 8px。

回归 `TestExecutionEmptyStartUsesCompactStatusThenGrowsInPlace` 在修复前失败、修复后通过，验证空态与首段正文使用同一卡。全量测试、相关 feishu race 检查和构建通过；空态、摘要、详情、授权、补充五种结构均经 CardKit 服务端接收。记录在 `/tmp/cc-card-blank-{red,focused,all,race,build}.log` 和 `/tmp/cc-card-blank-validation.json`。候选二进制 `/tmp/cc-connect-team-card-blank-fix`。

一次性 `cc-connect-card-compact-deploy.service` 于 14:15 +08 完成备份和上线，部署任务随后退出并自动回收。后续核对纠正：Popovich 在 14:15:00 接收 T023 结果开始审查，14:15:11 的重启中断了这次审查，且未自动接续。原先“未中断业务任务”的判断不成立；平台 ready、WebSocket 连接与控制 socket 可用只能证明服务在线。上线时 PID 2157258，配置和项目数据未变。备份 `/home/bytedance/.cc-connect-team/backups/card-blank-20261007-141510/`；部署和健康记录 `/tmp/cc-card-blank-deployment.json`、`/tmp/cc-card-blank-health.json`。二进制 SHA-256：`d8e3a5a7fe5897ded87655ac4f4535ff6a4fd1165c91bd5ee2783a06195d252c`。
